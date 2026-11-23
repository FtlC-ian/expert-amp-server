package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/config"
	"github.com/FtlC-ian/expert-amp-server/internal/fanpolicy"
	"github.com/FtlC-ian/expert-amp-server/internal/monitoring"
	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
)

func TestDisplayTemperatureWarningUsesExistingThresholdsAndFreshness(t *testing.T) {
	settings := config.Settings{TemperatureWarningC: 55, TemperatureTripC: 65, TemperatureResetC: 40}
	for _, tc := range []struct {
		name       string
		temp       float64
		recent     bool
		configured bool
		want       string
	}{
		{"normal", 54, true, true, monitoring.StateNormal},
		{"warning boundary", 55, true, true, monitoring.StateWarning},
		{"trip boundary", 65, true, true, monitoring.StateTrip},
		{"stale", 80, false, true, monitoring.StateUnavailable},
		{"unconfigured", 80, true, false, monitoring.StateDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := api.Status{Telemetry: api.Telemetry{TemperatureC: &tc.temp, Provenance: runtime.ProvenancePassthroughTap}, RecentContact: tc.recent}
			cfg := settings
			if !tc.configured {
				cfg = config.Settings{}
			}
			got := displayTemperatureWarning(s, cfg)
			if got.State != tc.want || got.Provenance != runtime.ProvenancePassthroughTap {
				t.Fatalf("warning = %+v", got)
			}
			if !tc.recent && (got.TemperatureC != nil || got.Reason == "") {
				t.Fatalf("stale warning presents a current value: %+v", got)
			}
		})
	}
	upper, lower, combiner := 50.0, 70.0, 60.0
	got := displayTemperatureWarning(api.Status{Telemetry: api.Telemetry{TemperatureC: &upper, TemperatureLowerC: &lower, TemperatureCombinerC: &combiner}, RecentContact: true}, settings)
	if got.State != monitoring.StateTrip || got.Sensor != "lower" || got.TemperatureC == nil || *got.TemperatureC != lower {
		t.Fatalf("hottest displayed sensor not used: %+v", got)
	}
	missing := displayTemperatureWarning(api.Status{RecentContact: true}, settings)
	if missing.State != monitoring.StateUnavailable || missing.TemperatureC != nil {
		t.Fatalf("missing reading = %+v", missing)
	}
}

func TestHotTappedFrameWarnsInStatusAndWebsocketWithoutActuation(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "config.json"), ":8088")
	if err != nil {
		t.Fatal(err)
	}
	s := mgr.Get().Settings
	s.SafetyMonitoringEnabled, s.OvertemperatureStandbyArmed = true, true
	s.TemperatureWarningC, s.TemperatureTripC, s.TemperatureResetC = 55, 65, 45
	if _, err := mgr.Update(s); err != nil {
		t.Fatal(err)
	}
	buttons := &stubButtonTransport{}
	safety := monitoring.NewController(buttons)
	fan := fanpolicy.NewController(buttons)
	src := runtime.NewSerialSource(runtime.SerialSourceConfig{Port: "/dev/ttyTEST0", BaudRate: 115200, MinFrameLen: 64, MaxBuffer: 8192}, leaseTestOpener{}, runtime.Update{})
	src.ConfigureSafetyController(safety, func() monitoring.ControlSettings {
		return monitoring.ControlSettings{Enabled: true, Armed: true, Thresholds: monitoring.Thresholds{TemperatureWarningC: 55, TemperatureTripC: 65, TemperatureResetC: 45}}
	})
	src.ConfigureFanPolicyController(fan, func() fanpolicy.Settings {
		return fanpolicy.Settings{Enabled: true, HighTemperatureC: 60, NormalTemperatureC: 40}
	})
	handle, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	frame, err := os.ReadFile("../protocol/testdata/status_response_example.bin")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(string(frame[4:71]), ",")
	fields[1], fields[14], fields[15], fields[16] = "O", "080", "080", "080"
	copy(frame[4:71], strings.Join(fields, ","))
	sum := 0
	for _, b := range frame[4:71] {
		sum += int(b)
	}
	frame[71], frame[72] = byte(sum%256), byte(sum/256)
	handle.ObserveFromAmp(frame)
	handler := NewHandler(Options{Config: mgr, StatusState: src.StatusState(), SafetyController: safety, FanPolicy: fan})
	check := func(status api.Status) {
		t.Helper()
		w := status.DisplayTemperatureWarning
		if w == nil || w.State != monitoring.StateTrip || w.Provenance != runtime.ProvenancePassthroughTap || w.TemperatureC == nil || *w.TemperatureC != 80 {
			t.Fatalf("hot tap warning = %+v; status = %+v", w, status)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	var reply struct {
		Data api.Status `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	check(reply.Data)
	server := httptest.NewServer(handler)
	defer server.Close()
	ws := dialWS(t, server.URL, "/api/v1/status/ws")
	defer ws.Close()
	check(readStatusWSMessage(t, ws))
	if result := safety.Apply(monitoring.Result{}, true); result.Latched || result.Action.State != monitoring.ActionIdle {
		t.Fatalf("display warning spent safety latch: %+v", result)
	}
	if buttons.calls != 0 || fan.Current().Pending || fan.Current().DesiredPolicy == fanpolicy.PolicyHigh {
		t.Fatalf("display warning actuated: calls=%d fan=%+v", buttons.calls, fan.Current())
	}
	// Replace the display evidence with a known-expired arrival, without sleeps.
	stale := runtime.NewStore(runtime.Snapshot{Telemetry: api.Telemetry{TemperatureC: reply.Data.TemperatureC, Provenance: runtime.ProvenancePassthroughTap}, DisplayReceivedAt: time.Now().Add(-2 * runtime.RecentContactWindow)})
	expired := selectedStatus(Options{Config: mgr, Store: stale})
	if expired.DisplayTemperatureWarning.State != monitoring.StateUnavailable || expired.DisplayTemperatureWarning.TemperatureC != nil {
		t.Fatalf("expired display evidence = %+v", expired.DisplayTemperatureWarning)
	}
	// Fresh display contact cannot renew the retained protocol temperature.
	oldProtocol := runtime.NewStatusState(api.Status{Telemetry: api.Telemetry{TemperatureC: reply.Data.TemperatureC, Provenance: "status-poll"}})
	freshDisplay := runtime.NewStore(runtime.Snapshot{DisplayReceivedAt: time.Now()})
	old := selectedStatus(Options{Config: mgr, Store: freshDisplay, StatusState: oldProtocol})
	if !old.RecentContact || old.DisplayTemperatureWarning.State != monitoring.StateUnavailable || old.DisplayTemperatureWarning.TemperatureC != nil {
		t.Fatalf("display contact renewed old protocol temperature: %+v", old)
	}
}
