package server

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"net/http"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/config"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/font"
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
	IndexHTML       []byte
	DocsHTML        []byte
	OpenAPIJSON     []byte
	ROM             *font.ROM
	Store           *runtime.Store
	StatusState     *runtime.StatusState
	SerialSource    *runtime.SerialSource // nil when running in fixture mode
	DemoState       display.State
	AltState        display.State
	Fixtures        runtime.FixtureCatalog
	Config          *config.Manager
	ButtonTransport transport.ButtonTransport
	WakeTransport   transport.WakeTransport
	RestartServer   func(context.Context) error
	Version         VersionInfo
}

type displayStateResponse struct {
	State     display.State `json:"state"`
	FrameKind string        `json:"frameKind,omitempty"`
	Source    string        `json:"source,omitempty"`
	Sequence  uint64        `json:"sequence,omitempty"`
	UpdatedAt time.Time     `json:"updatedAt,omitempty"`
}

type runtimeStatusResponse struct {
	PollIntervalMs        int    `json:"pollIntervalMs"`
	DisplayPollingEnabled bool   `json:"displayPollingEnabled"`
	StatusPollingEnabled  bool   `json:"statusPollingEnabled"`
	UpdatedAt             string `json:"updatedAt"`
}

type alarmsResponse struct {
	Active []string `json:"active"`
	Source string   `json:"source,omitempty"`
	Stub   bool     `json:"stub"`
}

type settingsRequest struct {
	SerialPort            string            `json:"serialPort"`
	ListenAddress         string            `json:"listenAddress"`
	PollIntervalMs        int               `json:"pollIntervalMs"`
	DisplayPollingEnabled *bool             `json:"displayPollingEnabled"`
	StatusPollingEnabled  *bool             `json:"statusPollingEnabled"`
	PanelModelLabel       string            `json:"panelModelLabel,omitempty"`
	InputLabels           map[string]string `json:"inputLabels,omitempty"`
	AntennaLabels         map[string]string `json:"antennaLabels,omitempty"`

	SerialBaudRate           *int  `json:"serialBaudRate,omitempty"`
	SerialReadTimeoutMs      *int  `json:"serialReadTimeoutMs,omitempty"`
	StatusPollCommandEnabled *bool `json:"statusPollCommandEnabled,omitempty"`
	StatusPollIntervalMs     *int  `json:"statusPollIntervalMs,omitempty"`
	SerialPollEnabled        *bool `json:"serialPollEnabled,omitempty"`
	SerialPollIntervalMs     *int  `json:"serialPollIntervalMs,omitempty"`
	SerialAssertDTR          *bool `json:"serialAssertDTR,omitempty"`
	SerialAssertRTS          *bool `json:"serialAssertRTS,omitempty"`
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
		snapshot := currentSnapshot(opts.Store)
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: alarmsResponse{Active: []string{}, Source: snapshot.Source, Stub: true}})
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
		mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				writeAPI(w, http.StatusOK, api.Response{Success: true, Data: opts.Config.Get()})
			case http.MethodPost:
				handleSettingsUpdateAPI(w, r, opts.Config)
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

func handleSettingsUpdateAPI(w http.ResponseWriter, r *http.Request, mgr *config.Manager) {
	current := mgr.Get()
	req, err := decodeSettingsRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	nextSettings := mergeSettingsRequest(current.Settings, req)
	next, err := mgr.Update(nextSettings)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, api.Response{Success: true, Message: settingsMessage(current.Settings, next.Settings), Data: next})
}

func decodeSettingsRequest(r *http.Request) (settingsRequest, error) {
	var req settingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return settingsRequest{}, err
	}
	return req, nil
}

func mergeSettingsRequest(current config.Settings, req settingsRequest) config.Settings {
	return config.Settings{
		SerialPort:               req.SerialPort,
		ListenAddress:            req.ListenAddress,
		PollIntervalMs:           req.PollIntervalMs,
		DisplayPollingEnabled:    pickBool(current.DisplayPollingEnabled, req.DisplayPollingEnabled),
		StatusPollingEnabled:     pickBool(current.StatusPollingEnabled, req.StatusPollingEnabled),
		PanelModelLabel:          req.PanelModelLabel,
		InputLabels:              req.InputLabels,
		AntennaLabels:            req.AntennaLabels,
		SerialBaudRate:           pickPositiveInt(current.SerialBaudRate, req.SerialBaudRate),
		SerialReadTimeoutMs:      pickPositiveInt(current.SerialReadTimeoutMs, req.SerialReadTimeoutMs),
		StatusPollCommandEnabled: pickBool(current.StatusPollCommandEnabled, firstBool(req.StatusPollCommandEnabled, req.SerialPollEnabled)),
		StatusPollIntervalMs:     pickPositiveInt(current.StatusPollIntervalMs, firstInt(req.StatusPollIntervalMs, req.SerialPollIntervalMs)),
		SerialAssertDTR:          pickBool(current.SerialAssertDTR, req.SerialAssertDTR),
		SerialAssertRTS:          pickBool(current.SerialAssertRTS, req.SerialAssertRTS),
	}
}

func pickBool(current bool, next *bool) bool {
	if next != nil {
		return *next
	}
	return current
}

func pickPositiveInt(current int, next *int) int {
	if next != nil && *next > 0 {
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
		PollIntervalMs:        currentPollInterval(cfg),
		DisplayPollingEnabled: currentDisplayPollingEnabled(cfg),
		StatusPollingEnabled:  currentStatusPollingEnabled(cfg),
		UpdatedAt:             snapshot.UpdatedAt.Format(time.RFC3339),
	}
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
	// Listen address and backend display-frame refresh cadence changes require a
	// restart to take effect because they shape serial-source creation.
	// statusPollingEnabled remains a live toggle.
	restartNeeded := current.ListenAddress != next.ListenAddress ||
		current.PollIntervalMs != next.PollIntervalMs ||
		current.DisplayPollingEnabled != next.DisplayPollingEnabled
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
