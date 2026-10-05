package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/config"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/fanpolicy"
	"github.com/FtlC-ian/expert-amp-server/internal/font"
	"github.com/FtlC-ian/expert-amp-server/internal/menudebug"
	"github.com/FtlC-ian/expert-amp-server/internal/monitoring"
	"github.com/FtlC-ian/expert-amp-server/internal/rawpassthrough"
	"github.com/FtlC-ian/expert-amp-server/internal/render"
	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"buildDate,omitempty"`
	Channel   string `json:"channel,omitempty"`
}

type Options struct {
	IndexHTML          []byte
	DocsHTML           []byte
	OpenAPIJSON        []byte
	ROM                *font.ROM
	Store              *runtime.Store
	StatusState        *runtime.StatusState
	SerialSource       *runtime.SerialSource // nil when running in fixture mode
	DemoState          display.State
	AltState           display.State
	Fixtures           runtime.FixtureCatalog
	Config             *config.Manager
	SafetyController   *monitoring.Controller
	FanPolicy          *fanpolicy.Controller
	MenuDebug          *menudebug.Controller
	MenuDebugTransport transport.ButtonTransport
	MenuDebugUploader  MenuDebugUploader
	ButtonTransport    transport.ButtonTransport
	WakeTransport      transport.WakeTransport
	RawPassthrough     *rawpassthrough.Controller // nil when raw passthrough is disabled
	RestartServer      func(context.Context) error
	Version            VersionInfo
}

type displayStateResponse struct {
	State     display.State `json:"state"`
	FrameKind string        `json:"frameKind,omitempty"`
	Source    string        `json:"source,omitempty"`
	Sequence  uint64        `json:"sequence,omitempty"`
	UpdatedAt time.Time     `json:"updatedAt,omitempty"`
}

type runtimeStatusResponse struct {
	PollingMode           string `json:"pollingMode"`
	PollIntervalMs        int    `json:"pollIntervalMs"`
	DisplayPollingEnabled bool   `json:"displayPollingEnabled"`
	StatusPollingEnabled  bool   `json:"statusPollingEnabled"`
	UpdatedAt             string `json:"updatedAt"`
}

type alarmsResponse struct {
	Active        []string          `json:"active"`
	Warnings      []string          `json:"warnings"`
	WarningCode   string            `json:"warningCode,omitempty"`
	AlarmCode     string            `json:"alarmCode,omitempty"`
	Source        string            `json:"source,omitempty"`
	RecentContact bool              `json:"recentContact"`
	LastContactAt string            `json:"lastContactAt,omitempty"`
	Monitor       monitoring.Result `json:"monitor"`
	Stub          bool              `json:"stub"`
}

type settingsRequest struct {
	SerialPort                  *string            `json:"serialPort"`
	ListenAddress               *string            `json:"listenAddress"`
	PollingMode                 string             `json:"pollingMode"`
	PollIntervalMs              *int               `json:"pollIntervalMs"`
	DisplayPollingEnabled       *bool              `json:"displayPollingEnabled"`
	StatusPollingEnabled        *bool              `json:"statusPollingEnabled"`
	AmplifierTemperatureUnit    *string            `json:"amplifierTemperatureUnit,omitempty"`
	PanelModelLabel             *string            `json:"panelModelLabel,omitempty"`
	InputLabels                 *map[string]string `json:"inputLabels,omitempty"`
	AntennaLabels               *map[string]string `json:"antennaLabels,omitempty"`
	SafetyMonitoringEnabled     *bool              `json:"safetyMonitoringEnabled,omitempty"`
	OvertemperatureStandbyArmed *bool              `json:"overtemperatureStandbyArmed,omitempty"`
	TemperatureWarningC         *float64           `json:"temperatureWarningC,omitempty"`
	TemperatureTripC            *float64           `json:"temperatureTripC,omitempty"`
	TemperatureResetC           *float64           `json:"temperatureResetC,omitempty"`
	SWRWarning                  *float64           `json:"swrWarning,omitempty"`
	SWRTrip                     *float64           `json:"swrTrip,omitempty"`
	AutomaticFanPolicyEnabled   *bool              `json:"automaticFanPolicyEnabled,omitempty"`
	FanHighTemperatureC         *float64           `json:"fanHighTemperatureC,omitempty"`
	FanNormalTemperatureC       *float64           `json:"fanNormalTemperatureC,omitempty"`
	FanDisplayProfile           *string            `json:"fanDisplayProfile,omitempty"`
	FanPolicyFirmwareVersion    *string            `json:"fanPolicyFirmwareVersion,omitempty"`
	VerifyFanModeOnStartup      *bool              `json:"verifyFanModeOnStartup,omitempty"`
	FanBoostDurationMinutes     *int               `json:"fanBoostDurationMinutes,omitempty"`
	MenuDebugEnabled            *bool              `json:"menuDebugEnabled,omitempty"`

	SerialBaudRate           *int  `json:"serialBaudRate,omitempty"`
	SerialReadTimeoutMs      *int  `json:"serialReadTimeoutMs,omitempty"`
	StatusPollCommandEnabled *bool `json:"statusPollCommandEnabled,omitempty"`
	StatusPollIntervalMs     *int  `json:"statusPollIntervalMs,omitempty"`
	SerialPollEnabled        *bool `json:"serialPollEnabled,omitempty"`
	SerialPollIntervalMs     *int  `json:"serialPollIntervalMs,omitempty"`
	SerialAssertDTR          *bool `json:"serialAssertDTR,omitempty"`
	SerialAssertRTS          *bool `json:"serialAssertRTS,omitempty"`

	RawPassthroughEnabled       *bool   `json:"rawPassthroughEnabled,omitempty"`
	RawPassthroughListenAddress *string `json:"rawPassthroughListenAddress,omitempty"`
}

type fanPolicyOverrideRequest struct {
	Mode            string `json:"mode"`
	DurationMinutes *int   `json:"durationMinutes,omitempty"`
}

func selectedStatus(opts Options) api.Status {
	return opts.StatusState.Resolve(currentSnapshot(opts.Store))
}

func selectedVersion(opts Options) VersionInfo {
	version := opts.Version
	if version.Version == "" {
		version.Version = "dev"
	}
	if version.Channel == "" {
		version.Channel = "dev"
	}
	return version
}

func NewHandler(opts Options) http.Handler {
	mux := http.NewServeMux()
	registerMenuDebugRoutes(mux, opts)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(opts.IndexHTML)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("X-Expert-Amp-Version", selectedVersion(opts).Version)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: selectedVersion(opts)})
	})

	mux.HandleFunc("/api/v1/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		if len(opts.OpenAPIJSON) == 0 {
			writeAPIError(w, http.StatusServiceUnavailable, "openapi document unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(opts.OpenAPIJSON)
	})

	mux.HandleFunc("/api/v1/docs", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		if len(opts.DocsHTML) == 0 {
			writeAPIError(w, http.StatusServiceUnavailable, "docs ui unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(opts.DocsHTML)
	})

	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		state, _, err := selectedState(r, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(state)
	})

	mux.HandleFunc("/diff", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(display.Compare(opts.DemoState, opts.AltState))
	})

	mux.HandleFunc("/api/v1/display/state", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		state, kind, err := selectedState(r, opts)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		snapshot := currentSnapshot(opts.Store)
		resp := displayStateResponse{State: state, FrameKind: kind}
		if kind == "" {
			resp.FrameKind = snapshot.FrameKind
		}
		if resp.FrameKind == snapshot.FrameKind || kind == "" {
			resp.Source = snapshot.Source
			resp.Sequence = snapshot.Sequence
			resp.UpdatedAt = snapshot.UpdatedAt
		} else {
			resp.Source = "fixture:" + resp.FrameKind
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: resp})
	})

	mux.HandleFunc("/api/v1/display/text", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		snapshot := currentSnapshot(opts.Store)
		modelName := snapshot.Telemetry.ModelName
		if opts.StatusState != nil {
			modelName = opts.StatusState.Resolve(snapshot).ModelName
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: displayTextFromSnapshot(snapshot, modelName)})
	})

	mux.HandleFunc("/api/v1/display/frame", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		kind, ok, err := selectedFixtureKind(r, opts.Fixtures)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		if ok {
			if frame, ok := opts.Fixtures.Frame(kind); ok {
				writeAPI(w, http.StatusOK, api.Response{Success: true, Data: frame})
				return
			}
		}
		snapshot := currentSnapshot(opts.Store)
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: snapshot.Frame})
	})

	mux.HandleFunc("/api/v1/telemetry", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		snapshot := currentSnapshot(opts.Store)
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: snapshot.Telemetry})
	})

	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: selectedStatus(opts)})
	})
	mux.HandleFunc("/api/v1/status/ws", handleStatusWebsocket(opts))
	mux.HandleFunc("/api/v1/display/ws", handleDisplayWebsocket(opts))

	mux.HandleFunc("/api/v1/alarms", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		status := selectedStatus(opts)
		settings := config.Settings{}
		if opts.Config != nil {
			settings = opts.Config.Get().Settings
		}
		monitor := monitoring.Evaluate(status, settings.SafetyMonitoringEnabled, monitoring.Thresholds{
			TemperatureWarningC: settings.TemperatureWarningC,
			TemperatureTripC:    settings.TemperatureTripC,
			TemperatureResetC:   settings.TemperatureResetC,
			SWRWarning:          settings.SWRWarning,
			SWRTrip:             settings.SWRTrip,
		})
		monitor = opts.SafetyController.Apply(monitor, settings.OvertemperatureStandbyArmed)
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: alarmsResponse{
			Active:        nonNilStrings(status.ActiveAlarms),
			Warnings:      nonNilStrings(status.Warnings),
			WarningCode:   status.WarningCode,
			AlarmCode:     status.AlarmCode,
			Source:        status.Source,
			RecentContact: status.RecentContact,
			LastContactAt: status.LastContactAt,
			Monitor:       monitor,
			Stub:          false,
		}})
	})

	mux.HandleFunc("/api/v1/fan-policy", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		if opts.FanPolicy == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "fan-policy controller unavailable")
			return
		}
		settings := config.Settings{}
		if opts.Config != nil {
			settings = opts.Config.Get().Settings
		}
		status := opts.StatusState.CurrentProtocolNativeWithContact()
		result := opts.FanPolicy.View(status, fanPolicySettings(settings))
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: result})
	})

	mux.HandleFunc("/api/v1/fan-policy/override", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodPost) {
			return
		}
		if opts.FanPolicy == nil || opts.Config == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "fan-policy control unavailable")
			return
		}
		var req fanPolicyOverrideRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		policy, ok := fanPolicyOverride(req.Mode)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "mode must be automatic, normal, or contest")
			return
		}
		var duration time.Duration
		if req.DurationMinutes != nil {
			if *req.DurationMinutes < 1 || *req.DurationMinutes > 10080 {
				writeAPIError(w, http.StatusBadRequest, "durationMinutes must be between 1 and 10080")
				return
			}
			if policy == "" {
				writeAPIError(w, http.StatusBadRequest, "durationMinutes is only valid for normal or contest overrides")
				return
			}
			duration = time.Duration(*req.DurationMinutes) * time.Minute
		}
		settings := opts.Config.Get().Settings
		status := opts.StatusState.CurrentProtocolNativeWithContact()
		if err := opts.FanPolicy.SetManualOverride(policy, duration); err != nil {
			writeAPI(w, http.StatusConflict, api.Response{
				Success: false,
				Error:   err.Error(),
				Data:    opts.FanPolicy.View(status, fanPolicySettings(settings)),
			})
			return
		}
		result := opts.FanPolicy.UpdateSettings(status, fanPolicySettings(settings))
		writeAPI(w, http.StatusAccepted, api.Response{
			Success: true,
			Message: "manual fan override accepted; the verified transaction will run when RX safety evidence is available",
			Data:    result,
		})
	})

	mux.HandleFunc("/api/v1/fan-policy/verify", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodPost) {
			return
		}
		if opts.FanPolicy == nil || opts.Config == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "fan-policy control unavailable")
			return
		}
		settings := opts.Config.Get().Settings
		status := opts.StatusState.CurrentProtocolNativeWithContact()
		if err := opts.FanPolicy.RequestVerification("api"); err != nil {
			writeAPI(w, http.StatusConflict, api.Response{
				Success: false,
				Error:   err.Error(),
				Data:    opts.FanPolicy.View(status, fanPolicySettings(settings)),
			})
			return
		}
		result := opts.FanPolicy.UpdateSettings(status, fanPolicySettings(settings))
		writeAPI(w, http.StatusAccepted, api.Response{
			Success: true,
			Message: "fan-mode verification accepted; the verified transaction will run when RX safety evidence is available",
			Data:    result,
		})
	})

	mux.HandleFunc("/api/v1/fan-policy/recover", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodPost) {
			return
		}
		if opts.FanPolicy == nil || opts.Config == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "fan-policy control unavailable")
			return
		}
		settings := opts.Config.Get().Settings
		status := opts.StatusState.CurrentProtocolNativeWithContact()
		if err := opts.FanPolicy.Recover(status, fanPolicySettings(settings)); err != nil {
			writeAPI(w, http.StatusConflict, api.Response{
				Success: false,
				Error:   err.Error(),
				Data:    opts.FanPolicy.View(status, fanPolicySettings(settings)),
			})
			return
		}
		result := opts.FanPolicy.UpdateSettings(status, fanPolicySettings(settings))
		writeAPI(w, http.StatusOK, api.Response{
			Success: true,
			Message: "failed fan-policy transaction cleared; manual override removed and OPERATE remains unchanged",
			Data:    result,
		})
	})

	mux.HandleFunc("/api/v1/runtime/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: currentSnapshot(opts.Store)})
	})

	mux.HandleFunc("/api/v1/runtime/ingest", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		if opts.SerialSource == nil {
			writeAPI(w, http.StatusOK, api.Response{Success: true, Data: runtime.IngestDiagnostics{Connected: false, SerialPort: "", LastError: "fixture mode, no serial source"}})
			return
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: opts.SerialSource.Diagnostics()})
	})

	// Read-only. Raw passthrough is enabled through settings like every other
	// serial transport option, not by an action endpoint.
	mux.HandleFunc("/api/v1/raw-passthrough", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		if opts.RawPassthrough == nil {
			writeAPI(w, http.StatusOK, api.Response{Success: true, Data: rawpassthrough.Status{
				Enabled:                    false,
				AutomaticControlsAvailable: true,
				Note:                       rawPassthroughUnavailableNote(opts.Config),
			}})
			return
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: opts.RawPassthrough.Status()})
	})

	mux.HandleFunc("/api/v1/serial-ports", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		ports, err := serial.EnumeratePorts()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: ports})
	})

	mux.HandleFunc("/api/v1/runtime", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: currentRuntimeStatus(opts.Config, opts.Store)})
	})

	mux.HandleFunc("/api/v1/runtime/restart", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodPost) {
			return
		}
		if opts.RestartServer == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "server restart unavailable in this runtime")
			return
		}
		if err := opts.RestartServer(r.Context()); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Message: "server restart requested; the process will exit cleanly and should come back if a supervisor restarts it"})
	})

	mux.HandleFunc("/api/v1/actions/button", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodPost) {
			return
		}
		handleButtonActionAPI(w, r, opts.ButtonTransport)
	})

	mux.HandleFunc("/api/v1/actions/wake", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodPost) {
			return
		}
		handleWakeActionAPI(w, r, opts.WakeTransport)
	})

	if opts.Config != nil {
		// One settings update at a time. Each reads the current settings and
		// merges a partial request onto them, so two in flight together would
		// both merge onto the same starting point and the second would silently
		// undo the first. See handleSettingsUpdateAPI for why this cannot be
		// left to the passthrough setup boundary.
		var settingsMu sync.Mutex
		mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				writeAPI(w, http.StatusOK, api.Response{Success: true, Data: opts.Config.Get()})
			case http.MethodPost:
				handleSettingsUpdateAPI(w, r, opts.Config, opts.RawPassthrough, &settingsMu, func(settings config.Settings) {
					status := opts.StatusState.CurrentProtocolNativeWithContact()
					opts.FanPolicy.UpdateSettings(status, fanPolicySettings(settings))
				})
			default:
				writeMethodNotAllowedAPI(w, []string{http.MethodGet, http.MethodPost})
			}
		})
	}

	mux.HandleFunc("/api/telemetry", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		snapshot := currentSnapshot(opts.Store)
		_ = json.NewEncoder(w).Encode(snapshot.Telemetry)
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(selectedStatus(opts))
	})

	mux.HandleFunc("/api/frame", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		kind, ok, err := selectedFixtureKind(r, opts.Fixtures)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if ok {
			if frame, ok := opts.Fixtures.Frame(kind); ok {
				_ = json.NewEncoder(w).Encode(frame)
				return
			}
		}
		snapshot := currentSnapshot(opts.Store)
		_ = json.NewEncoder(w).Encode(snapshot.Frame)
	})

	mux.HandleFunc("/api/runtime/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(currentSnapshot(opts.Store))
	})

	mux.HandleFunc("/api/runtime/ingest", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if opts.SerialSource == nil {
			_ = json.NewEncoder(w).Encode(runtime.IngestDiagnostics{Connected: false, SerialPort: "", LastError: "fixture mode, no serial source"})
			return
		}
		_ = json.NewEncoder(w).Encode(opts.SerialSource.Diagnostics())
	})

	mux.HandleFunc("/api/runtime", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(currentRuntimeStatus(opts.Config, opts.Store))
	})

	mux.HandleFunc("/api/actions/button", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodPost) {
			return
		}
		handleButtonActionAPI(w, r, opts.ButtonTransport)
	})

	mux.HandleFunc("/render.png", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "image/png")
		state, _, err := selectedState(r, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		img := render.Image(state, opts.ROM, color.Gray{Y: 0xff}, color.Gray{Y: 0x00})
		img = render.ScaleGray(img, renderScale(r, 4))
		if err := png.Encode(w, img); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	mux.HandleFunc("/render.svg", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		state, _, err := selectedState(r, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(render.SVG(state, opts.ROM, "#d6f5d6", "#102010", 2)))
	})

	mux.HandleFunc("/api/v1/display/render.png", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "image/png")
		state, _, err := selectedState(r, opts)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		img := render.Image(state, opts.ROM, color.Gray{Y: 0xff}, color.Gray{Y: 0x00})
		img = render.ScaleGray(img, renderScale(r, 4))
		if err := png.Encode(w, img); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
		}
	})

	mux.HandleFunc("/api/v1/display/render.svg", func(w http.ResponseWriter, r *http.Request) {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		state, _, err := selectedState(r, opts)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		_, _ = w.Write([]byte(render.SVG(state, opts.ROM, "#d6f5d6", "#102010", 2)))
	})

	return mux
}

func allowMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func allowMethodAPI(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	writeMethodNotAllowedAPI(w, []string{method})
	return false
}

func writeMethodNotAllowedAPI(w http.ResponseWriter, methods []string) {
	for _, method := range methods {
		w.Header().Add("Allow", method)
	}
	writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeAPI(w http.ResponseWriter, status int, response api.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeAPI(w, status, api.Response{Success: false, Error: message})
}

func handleButtonActionAPI(w http.ResponseWriter, r *http.Request, buttonTransport transport.ButtonTransport) {
	var action api.ButtonAction
	if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	action = action.Normalized()
	if buttonTransport == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "button transport unavailable")
		return
	}
	result, err := buttonTransport.SendButton(context.Background(), action)
	if err != nil {
		status := transport.ButtonStatusCode(err)
		if result.Name == "" {
			result.Name = action.Name
		}
		writeAPI(w, status, api.Response{Success: false, Error: err.Error(), Data: result})
		return
	}
	writeAPI(w, http.StatusOK, api.Response{Success: true, Message: "button sent", Data: result})
}

func handleWakeActionAPI(w http.ResponseWriter, r *http.Request, wakeTransport transport.WakeTransport) {
	if wakeTransport == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "wake transport unavailable")
		return
	}
	result, err := wakeTransport.SendWake(r.Context())
	if err != nil {
		writeAPI(w, transport.ButtonStatusCode(err), api.Response{Success: false, Error: err.Error(), Data: result})
		return
	}
	writeAPI(w, http.StatusOK, api.Response{Success: true, Message: "wake sent", Data: result})
}

// handleSettingsUpdateAPI applies a settings update as one serialized
// transaction: read what the settings are now, merge the request onto that,
// validate the result, decide whether it arms anything, and commit -- with no
// point in the middle where another writer or a connecting client can change
// the answer underneath it.
//
// Reading the current settings before the request body is what made that
// impossible. Decoding a body is network I/O of unbounded duration, so a slow
// or stalled client stretched the gap between the read and the commit
// arbitrarily: another update could disarm a control and a passthrough client
// could take the port, and this request would still merge onto the snapshot it
// took at the start. It cost three separate things -- the armed-control
// transition disappeared, because both the stale value and the merged one read
// armed and controlsNewlyArmedBy compares the two; every field the other writer
// changed was silently overwritten; and settingsMessage computed what needs a
// restart against settings that were already several updates old.
//
// So the body is decoded first and outside every lock, and nothing is read from
// the manager until the boundary is held. Holding a lock across the body read
// would trade the stale snapshot for a stalled client that can block session
// setup for as long as it likes, which is the worse of the two.
//
// settingsMu serializes this against other settings updates. It cannot be left
// to the passthrough boundary: that is a no-op when passthrough is not
// configured, which is every default install.
func handleSettingsUpdateAPI(w http.ResponseWriter, r *http.Request, mgr *config.Manager, passthrough *rawpassthrough.Controller, settingsMu *sync.Mutex, settingsApplied func(config.Settings)) {
	req, err := decodeSettingsRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if (req.FanHighTemperatureC != nil && *req.FanHighTemperatureC <= 0) ||
		(req.FanNormalTemperatureC != nil && *req.FanNormalTemperatureC <= 0) {
		writeAPIError(w, http.StatusBadRequest, "automatic fan policy thresholds must be greater than zero")
		return
	}

	settingsMu.Lock()
	defer settingsMu.Unlock()

	var (
		current config.Settings
		next    config.Snapshot
	)
	err = passthrough.HoldForSettingsTransaction(func(leased bool) error {
		current = mgr.Get().Settings

		// Decide everything below on the stored form of the merged request,
		// never on the request's own spelling of it. Update normalizes on the
		// way to disk -- it trims and lowercases pollingMode, and derives it
		// from the legacy booleans when it is blank -- so a candidate judged as
		// written can be committed as something else, and a check that compares
		// against the canonical value accepts "OFF" and then saves "off": the
		// exact state it exists to refuse. current is already stored, so both
		// sides of every comparison here are now in the same form.
		//
		// Normalization is idempotent, so committing this value stores it
		// unchanged: what was validated is what lands on disk.
		nextSettings := mgr.Normalized(mergeSettingsRequest(current, req))

		if err := validatePassthroughPrerequisites(nextSettings); err != nil {
			return err
		}

		// An automatic control that is armed while a raw client holds the port
		// is a lie: it reports itself armed, and every serial write it would
		// make is refused for the life of the lease. Refuse the arming instead.
		// Disarming is never blocked, and neither is any other setting.
		if arming := controlsNewlyArmedBy(current, nextSettings); len(arming) > 0 && leased {
			return rawpassthrough.ErrArmingDuringLease(arming)
		}

		committed, err := mgr.Update(nextSettings)
		if err != nil {
			return err
		}
		next = committed
		if settingsApplied != nil {
			settingsApplied(next.Settings)
		}
		return nil
	})
	if err != nil {
		writeAPIError(w, settingsUpdateStatus(err), err.Error())
		return
	}

	writeAPI(w, http.StatusOK, api.Response{Success: true, Message: settingsMessage(current, next.Settings), Data: next})
}

// settingsUpdateStatus maps a failed settings transaction to its status. A
// conflict with live state carries its own -- 409, and the request succeeds
// unchanged once that state clears -- while everything else is the request
// itself being wrong.
func settingsUpdateStatus(err error) int {
	var coded interface{ HTTPStatus() int }
	if errors.As(err, &coded) {
		return coded.HTTPStatus()
	}
	return http.StatusBadRequest
}

// validatePassthroughPrerequisites refuses a settings combination that asks for
// raw passthrough and removes what it runs on.
//
// It must be given normalized settings -- see config.Manager.Normalized. The
// value it compares is the one that reaches disk, and the request's spelling of
// it is not that value.
//
// Passthrough leases the serial source, and no serial source is built while
// pollingMode is off, so the two settings contradict each other: the saved
// configuration says passthrough is enabled and the server reports it
// unavailable, with nothing in between to say why. Refusing the update is what
// stops that state being created in the first place.
//
// It is deliberately here rather than in config's own validation, which
// LoadOrCreate shares: a rejection there would also refuse to start on a
// configuration that is already on disk, and a server that will not start is a
// server whose overtemperature standby is not running. Turning an operator's
// existing config into a boot failure is too much for a conflict they can fix
// with one request, so an already-saved combination still starts and is
// explained by rawPassthroughUnavailableNote instead.
func validatePassthroughPrerequisites(settings config.Settings) error {
	if !settings.RawPassthroughEnabled {
		return nil
	}
	if settings.PollingMode == string(config.PollingModeOff) {
		return errors.New("raw passthrough requires polling: set pollingMode to display, status or both, or disable rawPassthroughEnabled")
	}
	return nil
}

// rawPassthroughUnavailableNote explains why no passthrough controller exists.
//
// "Disabled" is the wrong word for a configuration that asks for passthrough,
// names a serial port, and still gets nothing: passthrough needs a serial
// source, and no serial source is built while pollingMode is off, so the two
// settings contradict each other. Saying which one won is the whole point of
// this note.
//
// POST /api/v1/settings now refuses to create that combination, so the only way
// to reach the pollingMode branch below is a configuration that was already on
// disk -- hand-edited, or saved before the update path rejected it. Those still
// start, deliberately, rather than turning an operator's existing config into a
// boot failure; see validatePassthroughPrerequisites.
func rawPassthroughUnavailableNote(mgr *config.Manager) string {
	const disabled = "raw serial passthrough is disabled; the server owns the serial port"
	if mgr == nil {
		return disabled
	}
	settings := mgr.Get().Settings
	if !settings.RawPassthroughEnabled {
		return disabled
	}
	if settings.SerialPort == "" {
		return "raw serial passthrough is enabled but no serial port is configured, so there is nothing to lease"
	}
	if settings.PollingMode == string(config.PollingModeOff) {
		return "raw serial passthrough is enabled but pollingMode is off, which stops the serial source it leases from being created. " +
			"Set pollingMode to display, status or both and restart the server, or leave passthrough disabled."
	}
	return "raw serial passthrough is enabled but unavailable; restart the server for the change to take effect"
}

// armedAutomaticControls names the server-side automatic controls that these
// settings leave armed.
//
// It has to agree with the ArmedAutomaticControls callback the raw passthrough
// controller is built with in cmd/server: the two answer the same question from
// opposite ends -- that one refuses a client while these are armed, this one
// refuses arming while a client holds the port -- and a disagreement between
// them would reopen the gap both exist to close.
func armedAutomaticControls(settings config.Settings) []string {
	var armed []string
	if settings.AutomaticFanPolicyEnabled {
		armed = append(armed, "automatic fan control")
	}
	if settings.SafetyMonitoringEnabled && settings.OvertemperatureStandbyArmed {
		armed = append(armed, "overtemperature standby")
	}
	return armed
}

// controlsNewlyArmedBy names the controls an update would arm that are not armed
// already. Re-sending a control that is already armed arms nothing, so it is not
// a conflict; only a disarmed-to-armed transition is.
func controlsNewlyArmedBy(current, next config.Settings) []string {
	before := make(map[string]struct{})
	for _, name := range armedAutomaticControls(current) {
		before[name] = struct{}{}
	}
	var added []string
	for _, name := range armedAutomaticControls(next) {
		if _, armed := before[name]; !armed {
			added = append(added, name)
		}
	}
	return added
}

func decodeSettingsRequest(r *http.Request) (settingsRequest, error) {
	var req settingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return settingsRequest{}, err
	}
	return req, nil
}

func mergeSettingsRequest(current config.Settings, req settingsRequest) config.Settings {
	displayPollingEnabled := pickBool(current.DisplayPollingEnabled, req.DisplayPollingEnabled)
	statusPollingEnabled := pickBool(current.StatusPollingEnabled, req.StatusPollingEnabled)
	statusPollCommandEnabled := pickBool(current.StatusPollCommandEnabled, firstBool(req.StatusPollCommandEnabled, req.SerialPollEnabled))
	return config.Settings{
		SerialPort:                  pickOptionalString(current.SerialPort, req.SerialPort),
		ListenAddress:               pickOptionalString(current.ListenAddress, req.ListenAddress),
		PollingMode:                 mergedPollingMode(current.PollingMode, req, displayPollingEnabled, statusPollingEnabled, statusPollCommandEnabled),
		PollIntervalMs:              pickPositiveInt(current.PollIntervalMs, req.PollIntervalMs),
		DisplayPollingEnabled:       displayPollingEnabled,
		StatusPollingEnabled:        statusPollingEnabled,
		AmplifierTemperatureUnit:    pickOptionalString(current.AmplifierTemperatureUnit, req.AmplifierTemperatureUnit),
		PanelModelLabel:             pickOptionalString(current.PanelModelLabel, req.PanelModelLabel),
		InputLabels:                 pickOptionalMap(current.InputLabels, req.InputLabels),
		AntennaLabels:               pickOptionalMap(current.AntennaLabels, req.AntennaLabels),
		SafetyMonitoringEnabled:     pickBool(current.SafetyMonitoringEnabled, req.SafetyMonitoringEnabled),
		OvertemperatureStandbyArmed: pickBool(current.OvertemperatureStandbyArmed, req.OvertemperatureStandbyArmed),
		TemperatureWarningC:         pickFloat(current.TemperatureWarningC, req.TemperatureWarningC),
		TemperatureTripC:            pickFloat(current.TemperatureTripC, req.TemperatureTripC),
		TemperatureResetC:           pickFloat(current.TemperatureResetC, req.TemperatureResetC),
		SWRWarning:                  pickFloat(current.SWRWarning, req.SWRWarning),
		SWRTrip:                     pickFloat(current.SWRTrip, req.SWRTrip),
		AutomaticFanPolicyEnabled:   pickBool(current.AutomaticFanPolicyEnabled, req.AutomaticFanPolicyEnabled),
		FanHighTemperatureC:         pickFloat(current.FanHighTemperatureC, req.FanHighTemperatureC),
		FanNormalTemperatureC:       pickFloat(current.FanNormalTemperatureC, req.FanNormalTemperatureC),
		FanDisplayProfile:           pickOptionalString(current.FanDisplayProfile, req.FanDisplayProfile),
		FanPolicyFirmwareVersion:    pickOptionalString(current.FanPolicyFirmwareVersion, req.FanPolicyFirmwareVersion),
		VerifyFanModeOnStartup:      pickBool(current.VerifyFanModeOnStartup, req.VerifyFanModeOnStartup),
		FanBoostDurationMinutes:     pickInt(current.FanBoostDurationMinutes, req.FanBoostDurationMinutes),
		MenuDebugEnabled:            pickBool(current.MenuDebugEnabled, req.MenuDebugEnabled),
		SerialBaudRate:              pickPositiveInt(current.SerialBaudRate, req.SerialBaudRate),
		SerialReadTimeoutMs:         pickPositiveInt(current.SerialReadTimeoutMs, req.SerialReadTimeoutMs),
		StatusPollCommandEnabled:    statusPollCommandEnabled,
		StatusPollIntervalMs:        pickPositiveInt(current.StatusPollIntervalMs, firstInt(req.StatusPollIntervalMs, req.SerialPollIntervalMs)),
		SerialAssertDTR:             pickBool(current.SerialAssertDTR, req.SerialAssertDTR),
		SerialAssertRTS:             pickBool(current.SerialAssertRTS, req.SerialAssertRTS),
		RawPassthroughEnabled:       pickBool(current.RawPassthroughEnabled, req.RawPassthroughEnabled),
		RawPassthroughListenAddress: pickOptionalString(current.RawPassthroughListenAddress, req.RawPassthroughListenAddress),
	}
}

func fanPolicySettings(settings config.Settings) fanpolicy.Settings {
	return fanpolicy.Settings{
		Enabled:            settings.AutomaticFanPolicyEnabled,
		HighTemperatureC:   settings.FanHighTemperatureC,
		NormalTemperatureC: settings.FanNormalTemperatureC,
		DisplayProfile:     settings.FanDisplayProfile,
		FirmwareVersion:    settings.FanPolicyFirmwareVersion,
		SafetyStandbyArmed: settings.SafetyMonitoringEnabled && settings.OvertemperatureStandbyArmed,
		SafetyStandbyTripC: settings.TemperatureTripC,
	}
}

func fanPolicyOverride(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "automatic":
		return "", true
	case "normal":
		return fanpolicy.PolicyNormal, true
	case "contest":
		return fanpolicy.PolicyHigh, true
	default:
		return "", false
	}
}

func pickOptionalString(current string, next *string) string {
	if next != nil {
		return *next
	}
	return current
}

func pickOptionalMap(current map[string]string, next *map[string]string) map[string]string {
	if next != nil {
		return *next
	}
	return current
}

func mergedPollingMode(current string, req settingsRequest, display, status, statusCommand bool) string {
	if req.PollingMode != "" {
		return req.PollingMode
	}
	if req.DisplayPollingEnabled != nil || req.StatusPollingEnabled != nil || req.StatusPollCommandEnabled != nil || req.SerialPollEnabled != nil {
		switch {
		case display && status && statusCommand:
			return string(config.PollingModeBoth)
		case display && !status:
			return string(config.PollingModeDisplay)
		case !display && status && statusCommand:
			return string(config.PollingModeStatus)
		default:
			return string(config.PollingModeOff)
		}
	}
	return current
}

func pickString(current, next string) string {
	if next != "" {
		return next
	}
	return current
}

func pickBool(current bool, next *bool) bool {
	if next != nil {
		return *next
	}
	return current
}

func pickFloat(current float64, next *float64) float64 {
	if next != nil {
		return *next
	}
	return current
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}

func pickPositiveInt(current int, next *int) int {
	if next != nil && *next > 0 {
		return *next
	}
	return current
}

func pickInt(current int, next *int) int {
	if next != nil {
		return *next
	}
	return current
}

func firstBool(primary, legacy *bool) *bool {
	if primary != nil {
		return primary
	}
	return legacy
}

func firstInt(primary, legacy *int) *int {
	if primary != nil {
		return primary
	}
	return legacy
}

func currentSnapshot(store *runtime.Store) runtime.Snapshot {
	if store == nil {
		return runtime.Snapshot{}
	}
	return store.Current()
}

func currentRuntimeStatus(cfg *config.Manager, store *runtime.Store) runtimeStatusResponse {
	snapshot := currentSnapshot(store)
	return runtimeStatusResponse{
		PollingMode:           currentPollingMode(cfg),
		PollIntervalMs:        currentPollInterval(cfg),
		DisplayPollingEnabled: currentDisplayPollingEnabled(cfg),
		StatusPollingEnabled:  currentStatusPollingEnabled(cfg),
		UpdatedAt:             snapshot.UpdatedAt.Format(time.RFC3339),
	}
}

func currentPollingMode(cfg *config.Manager) string {
	if cfg == nil {
		return string(config.PollingModeBoth)
	}
	return cfg.Get().Settings.PollingMode
}

func currentPollInterval(cfg *config.Manager) int {
	if cfg == nil {
		return config.DefaultPollIntervalMs
	}
	return cfg.Get().Settings.PollIntervalMs
}

func currentDisplayPollingEnabled(cfg *config.Manager) bool {
	if cfg == nil {
		return true
	}
	return cfg.Get().Settings.DisplayPollingEnabled
}

func currentStatusPollingEnabled(cfg *config.Manager) bool {
	if cfg == nil {
		return true
	}
	return cfg.Get().Settings.StatusPollingEnabled
}

func settingsMessage(current, next config.Settings) string {
	// Listen address and unified serial polling cadence/mode changes require a
	// restart to take effect because they shape serial-source creation.
	//
	// Raw passthrough belongs on that list for the same reason: the controller
	// is built once from the startup snapshot, so toggling it or moving its
	// listener changes what is persisted and nothing about what is running.
	// Reporting a bare "settings saved" for those two read as though the change
	// had taken effect.
	restartNeeded := current.ListenAddress != next.ListenAddress ||
		current.PollIntervalMs != next.PollIntervalMs ||
		current.PollingMode != next.PollingMode ||
		current.RawPassthroughEnabled != next.RawPassthroughEnabled ||
		current.RawPassthroughListenAddress != next.RawPassthroughListenAddress
	if current.ListenAddress != next.ListenAddress {
		return "settings saved, restart the server for the new listen address and runtime changes to take effect"
	}
	if restartNeeded {
		return "settings saved, restart the server for runtime changes to take effect"
	}
	return "settings saved"
}

func selectedState(r *http.Request, opts Options) (display.State, string, error) {
	if kind, ok, err := selectedFixtureKind(r, opts.Fixtures); err != nil {
		return display.State{}, "", err
	} else if ok {
		if state, ok := opts.Fixtures.State(kind); ok {
			return state, kind, nil
		}
	}
	snapshot := currentSnapshot(opts.Store)
	return snapshot.State, snapshot.FrameKind, nil
}

func renderScale(r *http.Request, def int) int {
	q := r.URL.Query().Get("scale")
	if q == "" {
		return def
	}
	var scale int
	if _, err := fmt.Sscanf(q, "%d", &scale); err != nil {
		return def
	}
	if scale < 1 {
		return 1
	}
	if scale > 8 {
		return 8
	}
	return scale
}

func selectedFixtureKind(r *http.Request, fixtures runtime.FixtureCatalog) (string, bool, error) {
	kind := r.URL.Query().Get("kind")
	if kind == "" && r.URL.Query().Get("source") == "protocol" {
		kind = "home"
	}
	if kind == "" {
		return "", false, nil
	}
	if _, ok := fixtures.State(kind); ok {
		return kind, true, nil
	}
	if _, ok := fixtures.Frame(kind); ok {
		return kind, true, nil
	}
	return "", false, invalidFixtureKindError(kind)
}

type invalidFixtureKindError string

func (e invalidFixtureKindError) Error() string {
	return "unknown fixture kind: " + string(e)
}
