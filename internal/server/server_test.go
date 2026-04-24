package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/config"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/font"
	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
	"github.com/gorilla/websocket"
)

type stubButtonTransport struct {
	result api.ActionResult
	err    error
	action api.ButtonAction
}

type stubWakeTransport struct {
	result api.ActionResult
	err    error
}

type mockStatusOpener struct {
	port serial.Port
}

func (m *mockStatusOpener) Open(string, int) (serial.Port, error) {
	return m.port, nil
}

type mockStatusPort struct {
	chunks [][]byte
	idx    int
}

func (m *mockStatusPort) Read(buf []byte) (int, error) {
	if m.idx >= len(m.chunks) {
		time.Sleep(10 * time.Millisecond)
		return 0, io.EOF
	}
	chunk := m.chunks[m.idx]
	m.idx++
	copy(buf, chunk)
	return len(chunk), nil
}

func (m *mockStatusPort) Write(buf []byte) (int, error) { return len(buf), nil }
func (m *mockStatusPort) Close() error                  { return nil }
func (m *mockStatusPort) SetReadTimeout(time.Duration) error {
	return nil
}
func (m *mockStatusPort) SetDTR(bool) error { return nil }
func (m *mockStatusPort) SetRTS(bool) error { return nil }

func (s *stubButtonTransport) SendButton(_ context.Context, action api.ButtonAction) (api.ActionResult, error) {
	s.action = action
	if s.result.Name == "" {
		s.result.Name = action.Name
	}
	return s.result, s.err
}

func (s *stubWakeTransport) SendWake(context.Context) (api.ActionResult, error) {
	if s.result.Name == "" {
		s.result.Name = "wake"
	}
	return s.result, s.err
}

func newTestHandler(store *runtime.Store, fixtures runtime.FixtureCatalog) http.Handler {
	return newTestHandlerWithTransport(store, fixtures, nil)
}

func newTestHandlerWithTransport(store *runtime.Store, fixtures runtime.FixtureCatalog, buttonTransport transport.ButtonTransport) http.Handler {
	return newTestHandlerWithOptions(store, fixtures, nil, buttonTransport, nil)
}

func newTestHandlerWithOptions(store *runtime.Store, fixtures runtime.FixtureCatalog, serialSource *runtime.SerialSource, buttonTransport transport.ButtonTransport, wakeTransport transport.WakeTransport) http.Handler {
	var statusState *runtime.StatusState
	if serialSource != nil {
		statusState = serialSource.StatusState()
	}
	if statusState == nil {
		statusState = runtime.NewStatusState(api.Status{})
	}
	return NewHandler(Options{
		IndexHTML:       []byte("ok"),
		DocsHTML:        []byte("<html>docs</html>"),
		OpenAPIJSON:     []byte(`{"openapi":"3.0.3"}`),
		ROM:             font.Builtin(),
		Store:           store,
		StatusState:     statusState,
		SerialSource:    serialSource,
		DemoState:       display.DemoState(),
		AltState:        display.DemoStateAlt(),
		Fixtures:        fixtures,
		ButtonTransport: buttonTransport,
		WakeTransport:   wakeTransport,
	})
}

func TestSnapshotEndpointReturnsCurrentRuntimeSnapshot(t *testing.T) {
	state := display.DemoState()
	store := runtime.NewStore(runtime.Snapshot{
		State:     state,
		Telemetry: api.Telemetry{Band: "20m", Source: "fixture:home", TX: boolPtr(false)},
		Frame:     api.FrameInfo{Source: "fixtures/home.bin", Length: 371},
		FrameKind: "home",
		Source:    "fixture:home",
		Sequence:  3,
	})

	handler := newTestHandler(store, runtime.FixtureCatalog{
		States: map[string]display.State{"home": state},
		Frames: map[string]api.FrameInfo{"home": {Source: "fixtures/home.bin", Length: 371}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/snapshot", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got runtime.Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Sequence != 3 {
		t.Fatalf("sequence = %d, want 3", got.Sequence)
	}
	if got.Telemetry.Band != "20m" {
		t.Fatalf("band = %q, want 20m", got.Telemetry.Band)
	}
}

func TestV1SnapshotEndpointReturnsEnvelope(t *testing.T) {
	state := display.DemoState()
	store := runtime.NewStore(runtime.Snapshot{
		State:     state,
		Telemetry: api.Telemetry{Band: "20m", Source: "fixture:home", TX: boolPtr(false)},
		Frame:     api.FrameInfo{Source: "fixtures/home.bin", Length: 371},
		FrameKind: "home",
		Source:    "fixture:home",
		Sequence:  3,
	})

	handler := newTestHandler(store, runtime.FixtureCatalog{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/snapshot", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool             `json:"success"`
		Data    runtime.Snapshot `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || body.Data.Sequence != 3 {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestStateEndpointDefaultsToRuntimeSnapshot(t *testing.T) {
	snapshotState := display.DemoStateAlt()
	store := runtime.NewStore(runtime.Snapshot{State: snapshotState, FrameKind: "home", Sequence: 1})

	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/state", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got display.State
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got != snapshotState {
		t.Fatal("state endpoint did not return runtime snapshot state")
	}
}

func TestV1DisplayStateEndpointReturnsEnvelope(t *testing.T) {
	snapshotState := display.DemoStateAlt()
	store := runtime.NewStore(runtime.Snapshot{State: snapshotState, FrameKind: "home", Sequence: 7, Source: "fixture:home"})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/display/state", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool                 `json:"success"`
		Data    displayStateResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || body.Data.Sequence != 7 || body.Data.FrameKind != "home" || body.Data.State != snapshotState {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestStateEndpointSupportsFixtureSelectionWithoutTouchingRuntime(t *testing.T) {
	fixtureState := display.DemoState()
	runtimeState := display.DemoStateAlt()
	store := runtime.NewStore(runtime.Snapshot{State: runtimeState, FrameKind: "home", Sequence: 5})

	handler := newTestHandler(store, runtime.FixtureCatalog{
		States: map[string]display.State{"panel": fixtureState},
		Frames: map[string]api.FrameInfo{"panel": {Source: "fixtures/panel.bin"}},
	})

	req := httptest.NewRequest(http.MethodGet, "/state?kind=panel", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var got display.State
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got != fixtureState {
		t.Fatal("fixture state was not returned")
	}
	if store.Current().Sequence != 5 {
		t.Fatal("runtime snapshot should not be mutated by fixture selection")
	}
}

func TestV1DisplayFrameEndpointSupportsFixtureSelection(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Frame: api.FrameInfo{Source: "runtime", Length: 1}, FrameKind: "home", Sequence: 2})
	handler := newTestHandler(store, runtime.FixtureCatalog{
		Frames: map[string]api.FrameInfo{"panel": {Source: "fixtures/panel.bin", Length: 371}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/display/frame?kind=panel", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool          `json:"success"`
		Data    api.FrameInfo `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || body.Data.Source != "fixtures/panel.bin" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestV1TelemetryEndpointReturnsEnvelope(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Telemetry: api.Telemetry{Band: "20m", Source: "fixture:home", TX: boolPtr(false)}})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/telemetry", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var body struct {
		Success bool          `json:"success"`
		Data    api.Telemetry `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || body.Data.Band != "20m" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestV1StatusEndpointReturnsEnvelope(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Telemetry: api.Telemetry{
		Band:               "20m",
		OperatingState:     "standby",
		Antenna:            "4b",
		AntennaBank:        "A",
		TemperatureDisplay: "22 C",
		Source:             "serial",
		Confidence:         "display-derived",
		Provenance:         "display-frame",
		TX:                 boolPtr(false),
	}})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool       `json:"success"`
		Data    api.Status `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success {
		t.Fatalf("unexpected body: %+v", body)
	}
	if body.Data.Band != "20m" || body.Data.OperatingState != "standby" || body.Data.Source != "serial" {
		t.Fatalf("unexpected status payload: %+v", body.Data)
	}
	if body.Data.ActiveAlarms != nil {
		t.Fatalf("activeAlarms = %v, want nil when unknown", body.Data.ActiveAlarms)
	}
}

func TestV1StatusEndpointPrefersProtocolNativeStatusAfterStatusPoll(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Telemetry: api.Telemetry{
		Band:           "20m",
		Source:         "serial",
		Confidence:     "display-derived",
		Provenance:     "display-frame",
		OperatingState: "standby",
	}})
	statusFrame, err := os.ReadFile("../protocol/testdata/status_response_example.bin")
	if err != nil {
		t.Fatalf("ReadFile status fixture: %v", err)
	}
	serialSource := runtime.NewSerialSource(runtime.SerialSourceConfig{
		Port:        "/dev/ttyTEST0",
		ReadTimeout: 10 * time.Millisecond,
		ReadSize:    512,
		MinFrameLen: 64,
		MaxBuffer:   8192,
	}, &mockStatusOpener{port: &mockStatusPort{chunks: [][]byte{statusFrame}}}, runtime.Update{})

	ctx, cancel := context.WithCancel(context.Background())
	serialSource.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()

	handler := newTestHandlerWithOptions(store, runtime.FixtureCatalog{}, serialSource, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool       `json:"success"`
		Data    api.Status `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Provenance != "status-poll" || body.Data.ModelName != "EXPERT 2K-FA" || body.Data.BandCode != "00" || body.Data.BandText != "160m" {
		t.Fatalf("unexpected protocol-native status payload: %+v", body.Data)
	}
}

func TestV1StatusEndpointUsesFresherDisplayOnlyFieldsOverStaleProtocolSnapshot(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	statusState := runtime.NewStatusState(api.Status{})
	statusState.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		OperatingState: "standby",
		Mode:           "standby",
		OutputLevel:    "LOW",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}})
	time.Sleep(10 * time.Millisecond)
	store.Apply(runtime.Update{Telemetry: api.Telemetry{
		OperatingState: "operate",
		Mode:           "operate",
		OutputLevel:    "HIGH",
		Source:         "serial",
		Confidence:     "display-derived",
		Provenance:     "display-frame",
	}})

	handler := NewHandler(Options{
		IndexHTML:   []byte("ok"),
		DocsHTML:    []byte("<html>docs</html>"),
		OpenAPIJSON: []byte(`{"openapi":"3.0.3"}`),
		ROM:         font.Builtin(),
		Store:       store,
		StatusState: statusState,
		DemoState:   display.DemoState(),
		AltState:    display.DemoStateAlt(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool       `json:"success"`
		Data    api.Status `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Provenance != "status-poll" {
		t.Fatalf("provenance = %q, want status-poll", body.Data.Provenance)
	}
	if body.Data.OperatingState != "operate" || body.Data.Mode != "operate" {
		t.Fatalf("expected status endpoint to favor fresher display-only fields, got %+v", body.Data)
	}
	if body.Data.OutputLevel != "LOW" {
		t.Fatalf("outputLevel = %q, want protocol-native LOW", body.Data.OutputLevel)
	}
}

func TestV1StatusEndpointIgnoresSerialFallbackWithoutStatusPollProvenance(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Telemetry: api.Telemetry{
		Band:           "20m",
		Source:         "serial",
		Confidence:     "display-derived",
		Provenance:     "display-frame",
		OperatingState: "standby",
	}})
	serialSource := runtime.NewSerialSource(runtime.SerialSourceConfig{Port: "/dev/null"}, nil, runtime.Update{Telemetry: api.Telemetry{
		Band:           "6m",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "display-frame",
		OperatingState: "operate",
		ModelName:      "EXPERT 2K-FA",
	}})

	handler := newTestHandlerWithOptions(store, runtime.FixtureCatalog{}, serialSource, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var body struct {
		Success bool       `json:"success"`
		Data    api.Status `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Provenance != "display-frame" || body.Data.Band != "20m" {
		t.Fatalf("unexpected fallback status payload: %+v", body.Data)
	}
}

func TestSettingsUpdateMergePrefersCurrentValuesAndLegacyAliases(t *testing.T) {
	current := config.Settings{
		SerialPort:               "/dev/ttyUSB0",
		ListenAddress:            ":8088",
		PollIntervalMs:           250,
		DisplayPollingEnabled:    true,
		StatusPollingEnabled:     true,
		SerialBaudRate:           115200,
		SerialReadTimeoutMs:      250,
		StatusPollCommandEnabled: true,
		StatusPollIntervalMs:     500,
		SerialAssertDTR:          true,
		SerialAssertRTS:          true,
	}
	falseVal := false
	interval := 900
	merged := mergeSettingsRequest(current, settingsRequest{
		SerialPort:            "/dev/ttyUSB1",
		ListenAddress:         ":9090",
		PollIntervalMs:        500,
		DisplayPollingEnabled: &falseVal,
		SerialPollEnabled:     &falseVal,
		SerialPollIntervalMs:  &interval,
	})
	if merged.DisplayPollingEnabled {
		t.Fatal("DisplayPollingEnabled = true, want false")
	}
	if merged.StatusPollCommandEnabled {
		t.Fatal("StatusPollCommandEnabled = true, want false from legacy alias")
	}
	if merged.StatusPollIntervalMs != 900 {
		t.Fatalf("legacy alias merge mismatch: %+v", merged)
	}
	if merged.StatusPollingEnabled != current.StatusPollingEnabled || merged.SerialBaudRate != current.SerialBaudRate || merged.SerialReadTimeoutMs != current.SerialReadTimeoutMs {
		t.Fatalf("unrelated current fields were not preserved: %+v", merged)
	}
}

func TestLegacyStatusEndpointReturnsBareStatusJSON(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Telemetry: api.Telemetry{Band: "40m", Source: "fixture:home"}})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got api.Status
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Band != "40m" || got.Source != "fixture:home" {
		t.Fatalf("unexpected status payload: %+v", got)
	}
}

func TestV1StatusWebsocketSendsInitialSnapshotAndUpdates(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Telemetry: api.Telemetry{
		Band:           "20m",
		OperatingState: "standby",
		Source:         "serial",
		Provenance:     "display-frame",
		TX:             boolPtr(false),
	}})
	handler := newTestHandler(store, runtime.FixtureCatalog{})
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL, "/api/v1/status/ws")
	defer conn.Close()

	first := readStatusWSMessage(t, conn)
	if first.Band != "20m" || first.OperatingState != "standby" || first.Source != "serial" {
		t.Fatalf("unexpected initial websocket payload: %+v", first)
	}

	store.Apply(runtime.Update{Telemetry: api.Telemetry{
		Band:           "6m",
		OperatingState: "operate",
		Source:         "serial",
		Provenance:     "display-frame",
		TX:             boolPtr(true),
	}})

	second := readStatusWSMessage(t, conn)
	if second.Band != "6m" || second.OperatingState != "operate" || second.TX == nil || !*second.TX {
		t.Fatalf("unexpected updated websocket payload: %+v", second)
	}
}

func TestV1StatusWebsocketUsesSharedProtocolNativeState(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Telemetry: api.Telemetry{
		Band:           "20m",
		OperatingState: "standby",
		Source:         "serial",
		Confidence:     "display-derived",
		Provenance:     "display-frame",
	}})
	statusState := runtime.NewStatusState(api.Status{})
	handler := NewHandler(Options{
		IndexHTML:   []byte("ok"),
		DocsHTML:    []byte("<html>docs</html>"),
		OpenAPIJSON: []byte(`{"openapi":"3.0.3"}`),
		ROM:         font.Builtin(),
		Store:       store,
		StatusState: statusState,
		DemoState:   display.DemoState(),
		AltState:    display.DemoStateAlt(),
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL, "/api/v1/status/ws")
	defer conn.Close()

	first := readStatusWSMessage(t, conn)
	if first.Provenance != "display-frame" || first.Band != "20m" {
		t.Fatalf("unexpected initial fallback payload: %+v", first)
	}

	statusState.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		ModelName:      "EXPERT 2K-FA",
		OperatingState: "standby",
		Mode:           "standby",
		OutputLevel:    "LOW",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}, BandCode: "00", BandText: "160m"})

	store.Apply(runtime.Update{Telemetry: api.Telemetry{
		Band:           "20m",
		OperatingState: "operate",
		Mode:           "operate",
		OutputLevel:    "HIGH",
		Source:         "serial",
		Confidence:     "display-derived",
		Provenance:     "display-frame",
	}})

	second := readStatusWSMessage(t, conn)
	if second.Provenance != "status-poll" || second.ModelName != "EXPERT 2K-FA" || second.BandCode != "00" || second.BandText != "160m" {
		t.Fatalf("unexpected shared-state websocket payload: %+v", second)
	}
	if second.OperatingState != "operate" || second.Mode != "operate" {
		t.Fatalf("expected websocket payload to favor fresher display-only state, got %+v", second)
	}
	if second.OutputLevel != "LOW" {
		t.Fatalf("outputLevel = %q, want protocol-native LOW", second.OutputLevel)
	}
}

func TestV1StatusWebsocketIgnoresLegacyPaceQueryParameter(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Telemetry: api.Telemetry{Band: "20m", Provenance: "display-frame"}})
	handler := newTestHandler(store, runtime.FixtureCatalog{})
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL, "/api/v1/status/ws?pace=turbo")
	defer conn.Close()

	first := readStatusWSMessage(t, conn)
	if first.Band != "20m" || first.Provenance != "display-frame" {
		t.Fatalf("unexpected websocket payload: %+v", first)
	}
}

func TestV1DisplayWebsocketSendsInitialSnapshotAndUpdates(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Source: "fixture:home", FrameKind: "home", Sequence: 3, UpdatedAt: time.Now().UTC()})
	handler := newTestHandler(store, runtime.FixtureCatalog{})
	server := httptest.NewServer(handler)
	defer server.Close()

	conn := dialWS(t, server.URL, "/api/v1/display/ws")
	defer conn.Close()

	var first struct {
		Sequence  uint64    `json:"sequence"`
		Source    string    `json:"source"`
		FrameKind string    `json:"frameKind"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	readWSJSON(t, conn, &first)
	if first.Sequence != 3 || first.Source != "fixture:home" || first.FrameKind != "home" || first.UpdatedAt.IsZero() {
		t.Fatalf("unexpected initial display event: %+v", first)
	}

	updatedAt := time.Now().UTC()
	store.Apply(runtime.Update{State: display.DemoStateAlt(), Telemetry: api.Telemetry{Source: "serial"}, Frame: api.FrameInfo{Source: "serial"}, FrameKind: "serial", Source: "serial"})

	var second struct {
		Sequence  uint64    `json:"sequence"`
		Source    string    `json:"source"`
		FrameKind string    `json:"frameKind"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	readWSJSON(t, conn, &second)
	if second.Sequence <= first.Sequence || second.Source != "serial" || second.FrameKind != "serial" {
		t.Fatalf("unexpected updated display event: %+v", second)
	}
	if second.UpdatedAt.Before(updatedAt.Add(-2 * time.Second)) {
		t.Fatalf("updatedAt looks stale: %+v", second)
	}
}

func TestV1AlarmsEndpointReturnsHonestStub(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{Source: "fixture:home"})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alarms", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var body struct {
		Success bool           `json:"success"`
		Data    alarmsResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || !body.Data.Stub || len(body.Data.Active) != 0 {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestRenderEndpointDefaultsToRuntimeSnapshot(t *testing.T) {
	runtimeState := display.DemoStateAlt()
	fixtureState := display.DemoState()
	store := runtime.NewStore(runtime.Snapshot{State: runtimeState, FrameKind: "home", Sequence: 2})

	handler := newTestHandler(store, runtime.FixtureCatalog{
		States: map[string]display.State{"home": fixtureState},
	})

	runtimeReq := httptest.NewRequest(http.MethodGet, "/render.png", nil)
	runtimeRec := httptest.NewRecorder()
	handler.ServeHTTP(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK {
		t.Fatalf("runtime render status = %d, want %d", runtimeRec.Code, http.StatusOK)
	}

	fixtureReq := httptest.NewRequest(http.MethodGet, "/render.png?kind=home", nil)
	fixtureRec := httptest.NewRecorder()
	handler.ServeHTTP(fixtureRec, fixtureReq)
	if fixtureRec.Code != http.StatusOK {
		t.Fatalf("fixture render status = %d, want %d", fixtureRec.Code, http.StatusOK)
	}

	if bytes.Equal(runtimeRec.Body.Bytes(), fixtureRec.Body.Bytes()) {
		t.Fatal("default render unexpectedly matched fixture render; watch UI is not proving runtime path")
	}
}

func TestStateEndpointRejectsUnknownFixtureKind(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{State: display.DemoStateAlt(), FrameKind: "home", Sequence: 1})
	handler := newTestHandler(store, runtime.FixtureCatalog{
		States: map[string]display.State{"home": display.DemoState()},
		Frames: map[string]api.FrameInfo{"home": {Source: "fixtures/home.bin"}},
	})

	req := httptest.NewRequest(http.MethodGet, "/state?kind=nope", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if body := rec.Body.String(); body == "" || !bytes.Contains(rec.Body.Bytes(), []byte("unknown fixture kind: nope")) {
		t.Fatalf("body = %q, want unknown fixture kind error", body)
	}
}

func TestV1DisplayStateRejectsUnknownFixtureKindWithEnvelope(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{State: display.DemoStateAlt(), FrameKind: "home", Sequence: 1})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/display/state?kind=nope", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var body struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Success || body.Error != "unknown fixture kind: nope" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestV1ButtonActionUsesEnvelope(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	transport := &stubButtonTransport{result: api.ActionResult{Name: "set", Sent: true, Queued: false, Transport: "serial", FrameHex: "555555011111"}}
	handler := newTestHandlerWithTransport(store, runtime.FixtureCatalog{}, transport)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/button", bytes.NewBufferString(`{"name":" Set "}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool             `json:"success"`
		Message string           `json:"message"`
		Data    api.ActionResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || body.Message != "button sent" || !body.Data.Sent || body.Data.Name != "set" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if transport.action.Name != "set" {
		t.Fatalf("transport action = %+v, want normalized set", transport.action)
	}
}

func TestV1ButtonActionRejectsUnsafeButtonWithEnvelope(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	transport := &stubButtonTransport{result: api.ActionResult{Transport: "serial"}, err: transport.InvalidButtonActionError("back")}
	handler := newTestHandlerWithTransport(store, runtime.FixtureCatalog{}, transport)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/button", bytes.NewBufferString(`{"name":"back"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var body struct {
		Success bool             `json:"success"`
		Error   string           `json:"error"`
		Data    api.ActionResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Success || body.Error != "unsupported button action: back" || body.Data.Name != "back" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestLegacyButtonActionCompatibilityRouteUsesSameHandler(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	transport := &stubButtonTransport{result: api.ActionResult{Name: "left", Sent: true, Queued: false, Transport: "serial"}}
	handler := newTestHandlerWithTransport(store, runtime.FixtureCatalog{}, transport)

	req := httptest.NewRequest(http.MethodPost, "/api/actions/button", bytes.NewBufferString(`{"name":"left"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestV1ButtonActionReturnsTransportUnavailableWhenMissing(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	handler := newTestHandlerWithTransport(store, runtime.FixtureCatalog{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/button", bytes.NewBufferString(`{"name":"set"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	var body struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Success || body.Error != "button transport unavailable" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestV1ButtonActionReturnsInternalErrorDetails(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	transport := &stubButtonTransport{result: api.ActionResult{Transport: "serial"}, err: errors.New("write button frame: serial offline")}
	handler := newTestHandlerWithTransport(store, runtime.FixtureCatalog{}, transport)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/button", bytes.NewBufferString(`{"name":"set"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestV1DisplayStateMethodNotAllowedUsesEnvelope(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/display/state", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	var body struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Success || body.Error != "method not allowed" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestHealthzIncludesVersionHeader(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	handler := NewHandler(Options{
		IndexHTML:   []byte("ok"),
		OpenAPIJSON: []byte(`{"openapi":"3.0.3"}`),
		ROM:         font.Builtin(),
		Store:       store,
		StatusState: runtime.NewStatusState(api.Status{}),
		DemoState:   display.DemoState(),
		AltState:    display.DemoStateAlt(),
		Fixtures:    runtime.FixtureCatalog{},
		Version:     VersionInfo{Version: "1.2.3", Commit: "abcdef", BuildDate: "2026-04-24T17:00:00Z", Channel: "stable"},
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Expert-Amp-Version"); got != "1.2.3" {
		t.Fatalf("X-Expert-Amp-Version = %q, want 1.2.3", got)
	}
	if got := rec.Body.String(); got != "ok\n" {
		t.Fatalf("body = %q, want ok", got)
	}
}

func TestVersionEndpointUsesEnvelope(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	handler := NewHandler(Options{
		IndexHTML:   []byte("ok"),
		OpenAPIJSON: []byte(`{"openapi":"3.0.3"}`),
		ROM:         font.Builtin(),
		Store:       store,
		StatusState: runtime.NewStatusState(api.Status{}),
		DemoState:   display.DemoState(),
		AltState:    display.DemoStateAlt(),
		Fixtures:    runtime.FixtureCatalog{},
		Version:     VersionInfo{Version: "1.2.3", Commit: "abcdef", BuildDate: "2026-04-24T17:00:00Z", Channel: "stable"},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool        `json:"success"`
		Data    VersionInfo `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || body.Data.Version != "1.2.3" || body.Data.Commit != "abcdef" || body.Data.Channel != "stable" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestOpenAPIDocumentEndpointServesJSON(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"openapi":"3.0.3"`) {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestDocsEndpointServesHTML(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, "docs") {
		t.Fatalf("unexpected body: %q", body)
	}
}

func dialWS(t *testing.T, serverURL, path string) *websocket.Conn {
	t.Helper()
	base, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("Parse server URL: %v", err)
	}
	rel, err := url.Parse(path)
	if err != nil {
		t.Fatalf("Parse websocket path: %v", err)
	}
	wsURL := base.ResolveReference(rel)
	wsURL.Scheme = strings.Replace(wsURL.Scheme, "http", "ws", 1)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	if err != nil {
		t.Fatalf("Dial websocket: %v", err)
	}
	return conn
}

func readStatusWSMessage(t *testing.T, conn *websocket.Conn) api.Status {
	t.Helper()
	var status api.Status
	readWSJSON(t, conn, &status)
	return status
}

func readWSJSON(t *testing.T, conn *websocket.Conn, dst any) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(6 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		t.Fatalf("Unmarshal websocket payload: %v payload=%s", err, string(payload))
	}
}

func boolPtr(v bool) *bool { return &v }

func TestV1RuntimeRestartReturnsEnvelopeAndInvokesCallback(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	var called bool
	handler := NewHandler(Options{
		IndexHTML:   []byte("ok"),
		OpenAPIJSON: []byte(`{"openapi":"3.0.3"}`),
		ROM:         font.Builtin(),
		Store:       store,
		StatusState: runtime.NewStatusState(api.Status{}),
		DemoState:   display.DemoState(),
		AltState:    display.DemoStateAlt(),
		Fixtures:    runtime.FixtureCatalog{},
		RestartServer: func(ctx context.Context) error {
			called = true
			return nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/restart", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || body.Message == "" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if !called {
		t.Fatal("RestartServer callback was not invoked")
	}
}

func TestV1RuntimeRestartReturnsUnavailableWhenCallbackMissing(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	handler := NewHandler(Options{
		IndexHTML:   []byte("ok"),
		OpenAPIJSON: []byte(`{"openapi":"3.0.3"}`),
		ROM:         font.Builtin(),
		Store:       store,
		StatusState: runtime.NewStatusState(api.Status{}),
		DemoState:   display.DemoState(),
		AltState:    display.DemoStateAlt(),
		Fixtures:    runtime.FixtureCatalog{},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/restart", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	var body struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Success || body.Error != "server restart unavailable in this runtime" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestV1WakeActionUsesEnvelope(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	wake := &stubWakeTransport{result: api.ActionResult{Name: "wake", Sent: true, Queued: false, Transport: "serial-live-wake"}}
	handler := newTestHandlerWithOptions(store, runtime.FixtureCatalog{}, nil, nil, wake)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/wake", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Success bool             `json:"success"`
		Message string           `json:"message"`
		Data    api.ActionResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || body.Message != "wake sent" || !body.Data.Sent || body.Data.Name != "wake" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestV1WakeActionReturnsUnavailableWhenMissing(t *testing.T) {
	store := runtime.NewStore(runtime.Snapshot{})
	handler := newTestHandlerWithOptions(store, runtime.FixtureCatalog{}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/wake", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
