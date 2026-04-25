package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/config"
)

type configTelemetry struct {
	Source string `json:"source"`
}

func htmlHasID(body, id string) bool {
	return regexp.MustCompile(`\bid="` + regexp.QuoteMeta(id) + `"`).MatchString(body)
}

func htmlHasOption(body, value, label string) bool {
	pattern := `<option\b[^>]*\bvalue="` + regexp.QuoteMeta(value) + `"[^>]*>\s*` + regexp.QuoteMeta(label) + `\s*</option>`
	return regexp.MustCompile(pattern).MatchString(body)
}

func htmlHasIDWithClass(body, id, class string) bool {
	pattern := `<[^>]+\bid="` + regexp.QuoteMeta(id) + `"[^>]+\bclass="[^"]*\b` + regexp.QuoteMeta(class) + `\b[^"]*"|` +
		`<[^>]+\bclass="[^"]*\b` + regexp.QuoteMeta(class) + `\b[^"]*"[^>]+\bid="` + regexp.QuoteMeta(id) + `"`
	return regexp.MustCompile(pattern).MatchString(body)
}

func htmlHasClasses(body string, classes ...string) bool {
	classAttr := regexp.MustCompile(`class="([^"]*)"`)
	for _, match := range classAttr.FindAllStringSubmatch(body, -1) {
		fields := strings.Fields(match[1])
		foundAll := true
		for _, class := range classes {
			found := false
			for _, field := range fields {
				if field == class {
					found = true
					break
				}
			}
			if !found {
				foundAll = false
				break
			}
		}
		if foundAll {
			return true
		}
	}
	return false
}

func TestSettingsEndpointFirstRun(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body struct {
		Success bool            `json:"success"`
		Data    config.Snapshot `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !body.Success || !body.Data.NeedsSetup {
		t.Fatalf("unexpected body: %+v", body)
	}
	if body.Data.Settings.PollIntervalMs != config.DefaultPollIntervalMs {
		t.Fatalf("PollIntervalMs = %d, want %d", body.Data.Settings.PollIntervalMs, config.DefaultPollIntervalMs)
	}
}

func TestSettingsEndpointPersistsSerialPort(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	payload := []byte(`{"serialPort":"/dev/ttyUSB0","listenAddress":":8088","pollIntervalMs":250,"displayPollingEnabled":true,"statusPollingEnabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body struct {
		Success bool            `json:"success"`
		Data    config.Snapshot `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !body.Success {
		t.Fatalf("unexpected failure body")
	}
	if body.Data.NeedsSetup {
		t.Fatalf("NeedsSetup = true, want false")
	}
	if body.Data.Settings.SerialPort != "/dev/ttyUSB0" {
		t.Fatalf("SerialPort = %q", body.Data.Settings.SerialPort)
	}
}

func TestSettingsEndpointPreservesPollingFlagsWhenOmitted(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := mgr.Update(config.Settings{
		SerialPort:            "/dev/ttyUSB0",
		ListenAddress:         ":8088",
		PollIntervalMs:        250,
		DisplayPollingEnabled: false,
		StatusPollingEnabled:  false,
	}); err != nil {
		t.Fatalf("seed Update: %v", err)
	}

	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})
	payload := []byte(`{"serialPort":"/dev/ttyUSB1","listenAddress":":8088","pollIntervalMs":500}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	snap := mgr.Get()
	if snap.Settings.DisplayPollingEnabled || snap.Settings.StatusPollingEnabled {
		t.Fatalf("polling flags reset unexpectedly: %+v", snap.Settings)
	}
}

func TestSettingsEndpointReportsRestartNeededForListenAddressChange(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	payload := []byte(`{"serialPort":"/dev/ttyUSB0","listenAddress":":8090","pollIntervalMs":250}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !body.Success {
		t.Fatalf("unexpected failure body")
	}
	if body.Message != "settings saved, restart the server for the new listen address and runtime changes to take effect" {
		t.Fatalf("Message = %q", body.Message)
	}
}

func TestSettingsEndpointReportsRestartNeededForPollingChange(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	payload := []byte(`{"serialPort":"/dev/ttyUSB0","listenAddress":":8088","pollIntervalMs":500,"displayPollingEnabled":false,"statusPollingEnabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !body.Success {
		t.Fatalf("unexpected failure body")
	}
	if body.Message != "settings saved, restart the server for runtime changes to take effect" {
		t.Fatalf("Message = %q", body.Message)
	}
}

func TestWatchUIUsesRuntimeRenderByDefault(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	body := res.Body.String()
	if strings.Contains(body, `src="/api/v1/display/render.png?scale=4"`) {
		t.Fatalf("watch page should not hardcode an initial runtime render src anymore: %q", body)
	}
	if strings.Contains(body, `src="/api/v1/display/render.png?source=protocol&kind=home"`) {
		t.Fatalf("watch page still defaults to fixture render override: %q", body)
	}
	// With dual-layout UI the page should avoid a markup-time image fetch and let
	// JS decide when to refresh the display render.
	if !strings.Contains(body, "function refreshDisplayRender(force = false)") {
		t.Fatalf("watch page missing refreshDisplayRender helper: %q", body)
	}
	if !strings.Contains(body, "/api/v1/display/render.png?scale=${scale}&tick=${tick}`;") {
		t.Fatalf("watch page refresh loop does not contain scaled runtime render path: %q", body)
	}
	if !strings.Contains(body, "/api/v1/display/ws") || !strings.Contains(body, "connectDisplayWebsocket()") || !strings.Contains(body, "snapshot advances") {
		t.Fatalf("watch page missing websocket-driven display refresh wiring: %q", body)
	}
	if !strings.Contains(body, "Display scale") || !htmlHasOption(body, "4", "4×") {
		t.Fatalf("watch page missing display scale control with 4x option: %q", body)
	}
	// Layout selector must be present with panel as the default option.
	if !strings.Contains(body, "Front Panel (default)") {
		t.Fatalf("watch page missing panel layout option: %q", body)
	}
	if !strings.Contains(body, `/api/v1/display/render.png?source=protocol&amp;kind=home`) {
		t.Fatalf("watch page missing explicit fixture render override: %q", body)
	}
}

func TestWatchUIHasPanelLayoutAndOperatorAlternate(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	body := res.Body.String()

	// Panel and operator layouts must be in the HTML.
	if !htmlHasID(body, "layout-panel") {
		t.Fatalf("watch page missing panel layout section: %q", body)
	}
	if strings.Contains(body, `id="layout-simple"`) {
		t.Fatalf("watch page should not include simple layout section anymore: %q", body)
	}

	// Panel layout is shown by default; operator is hidden.
	if htmlHasIDWithClass(body, "layout-panel", "hidden") {
		t.Fatalf("panel layout should not be hidden by default")
	}

	// Layout selector dropdown must offer panel plus the operator layout.
	if !strings.Contains(body, `value="panel"`) {
		t.Fatalf("watch page missing panel option in layout selector: %q", body)
	}
	if strings.Contains(body, `value="simple"`) {
		t.Fatalf("watch page should not include simple option in layout selector: %q", body)
	}
	if !htmlHasOption(body, "operator", "Operator") {
		t.Fatalf("watch page missing operator option in layout selector: %q", body)
	}
	if !htmlHasIDWithClass(body, "layout-operator", "hidden") {
		t.Fatalf("watch page missing hidden operator layout root: %q", body)
	}
	if !htmlHasID(body, "watch-layout-panel-template") || !htmlHasID(body, "watch-layout-operator-template") {
		t.Fatalf("watch page missing template-backed layout definitions: %q", body)
	}
	if strings.Contains(body, `id="watch-layout-simple-template"`) {
		t.Fatalf("watch page should not include simple template anymore: %q", body)
	}
	if !htmlHasIDWithClass(body, "operator-display-tier", "operator-display-tier") {
		t.Fatalf("watch page missing operator display tier with expected id/class: %q", body)
	}
	if !htmlHasIDWithClass(body, "op-tune-state", "operator-control-state") || !strings.Contains(body, "Ready") {
		t.Fatalf("watch page missing operator tune state readout: %q", body)
	}
	if !htmlHasClasses(body, "operator-card-title", "operator-help-only") || !strings.Contains(body, "Operate and routine controls") {
		t.Fatalf("watch page missing operator help-only routine controls heading: %q", body)
	}
	if !htmlHasClasses(body, "operator-control-button", "operator-control-nav") {
		t.Fatalf("watch page missing operator nav control button class combination: %q", body)
	}
	for _, alias := range []string{"API alias: up", "API alias: down"} {
		if !strings.Contains(body, alias) {
			t.Fatalf("watch page missing nav alias hint %q: %q", alias, body)
		}
	}

	for _, needle := range []string{
		`operator-controls-grid`,
		`operator-display-tier`,
		`operator-nav-tier`,
		`sendButton('tune')`,
		`sendButton('antenna')`,
		`sendButton('input')`,
		`sendButton('cat')`,
		`Preview warnings`,
		`Warning`,
		`Alarm`,
		`Power on`,
		`sendWake()`,
		`Power off`,
		`TUNE`,
		`Band +`,
		`Band -`,
		`DSP`,
		`id="op-temp-main"`,
		`id="op-temp-main-fill"`,
		`id="op-volts"`,
		`id="op-amps"`,
		`id="op-atu-swr"`,
		`id="op-temp-lower"`,
		`id="op-temp-combiner"`,
		`id="op-status-summary"`,
		`display-hidden`,
		`id="operator-fault-region"`,
		`id="op-warning-lane"`,
		`id="op-warning-list"`,
		`id="op-alarm-lane"`,
		`id="op-alarm-list"`,
		`id="operator-show-display"`,
		`id="operator-help-mode"`,
		`id="operator-show-menu-controls"`,
		`id="operator-fault-preview-toggle"`,
		`id="operator-display-tier"`,
		`id="operator-layout"`,
		`expertAmpOperatorHelpMode`,
		`expertAmpOperatorShowMenuControls`,
		`expertAmpOperatorFaultPreview`,
		`DEFAULT_OPERATOR_HELP_MODE = false`,
		`DEFAULT_OPERATOR_SHOW_MENU_CONTROLS = true`,
		`function currentOperatorHelpMode()`,
		`function currentOperatorShowMenuControls()`,
		`function applyOperatorHelpMode(helpMode)`,
		`function applyOperatorMenuControlsVisibility(showMenuControls)`,
		`function applyOperatorDisplayVisibility(showDisplay)`,
		`function currentOperatorFaultPreview()`,
		`function nextOperatorFaultPreview(state)`,
		`function previewOperatorStatus(status)`,
		`function updateOperatorFaultPreviewToggle()`,
		`menu-hidden`,
		`operator-help-only`,
		`operator-help-button-label`,
		`op-operate-button`,
		`op-operate-action`,
		`operator-control-button--operate`,
		`operator-operate-active`,
		`operator-operate-standby`,
		`operator-operate-tx-live`,
		`operator-operate-tx-standby`,
		`op-antenna-action`,
		`op-input-action`,
		`op-power-action`,
		`Change operate / standby state`,
		`Switch to next antenna path`,
		`Switch to next station input`,
		`Cycle to next power level preset`,
		`Start tune cycle`,
		`sendButton('display')`,
		`ATU Off`,
		`BANK `,
		`◄▲`,
		`▼►`,
		`id="screen-operator"`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("watch page missing operator element %q: %q", needle, body)
		}
	}

	// Panel layout must include the three-column body marker.
	if !strings.Contains(body, "panel-1k3-body") {
		t.Fatalf("watch page missing 1.3K panel body element: %q", body)
	}

	// Panel layout LEDs must be present.
	for _, ledID := range []string{"led-tx", "led-on", "led-op", "led-set", "led-tune", "led-al"} {
		if !strings.Contains(body, ledID) {
			t.Fatalf("watch page missing LED element %q", ledID)
		}
	}
	for _, needle := range []string{"/api/v1/status/ws", "function updateStatusIndicators(status)", "function pollStatus()"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("watch page missing live status wiring %q", needle)
		}
	}

	// Document-backed wired buttons must be in the panel layout (sendButton calls).
	for _, btn := range []string{"sendButton('display')", "sendButton('set')", "sendButton('left')", "sendButton('right')", "sendButton('up')", "sendButton('down')", "sendButton('input')", "sendButton('antenna')", "sendButton('band-')", "sendButton('band+')", "sendButton('l-')", "sendButton('l+')", "sendButton('c-')", "sendButton('c+')", "sendButton('tune')", "sendButton('off')", "sendButton('power')", "sendButton('operate')", "sendButton('cat')"} {
		if !strings.Contains(body, btn) {
			t.Fatalf("watch page missing button call %q in panel layout", btn)
		}
	}
	if !strings.Contains(body, "sendWake()") {
		t.Fatalf("watch page missing wake call in panel layout")
	}

	// Default layout JS constant must be 'panel'.
	if !strings.Contains(body, `DEFAULT_LAYOUT = 'panel'`) {
		t.Fatalf("watch page DEFAULT_LAYOUT is not 'panel': %q", body)
	}
	for _, needle := range []string{"const WATCH_LAYOUTS = {", "function normalizeLayout(layout)", "function renderWatchLayouts()"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("watch page missing layout plumbing %q", needle)
		}
	}
}

func TestRuntimeEndpointIncludesPollingShape(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := mgr.Update(config.Settings{SerialPort: "/dev/ttyUSB0", ListenAddress: ":8088", PollIntervalMs: 250, DisplayPollingEnabled: false, StatusPollingEnabled: true}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			PollIntervalMs        int    `json:"pollIntervalMs"`
			DisplayPollingEnabled bool   `json:"displayPollingEnabled"`
			StatusPollingEnabled  bool   `json:"statusPollingEnabled"`
			UpdatedAt             string `json:"updatedAt"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !body.Success {
		t.Fatalf("unexpected failure body: %+v", body)
	}
	if body.Data.PollIntervalMs != 250 {
		t.Fatalf("PollIntervalMs = %d, want 250", body.Data.PollIntervalMs)
	}
	if !body.Data.StatusPollingEnabled || body.Data.DisplayPollingEnabled {
		t.Fatalf("unexpected polling flags: %+v", body.Data)
	}
	if body.Data.UpdatedAt == "" {
		t.Fatalf("UpdatedAt empty")
	}
}

func TestSettingsEndpointRejectsInvalidSerialPort(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	payload := []byte("{\"serialPort\":\"/dev/ttyUSB0\\n/dev/ttyUSB1\",\"listenAddress\":\":8088\",\"pollIntervalMs\":250}")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

// TestWatchUIHasSafeButtonControls verifies that the watch page exposes the
// documented wired subset and keeps only still-undocumented controls blocked.
func TestWatchUIHasSafeButtonControls(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	body := res.Body.String()

	// The documented wired actions should appear as active sendButton targets in the page.
	for _, action := range []string{"left", "right", "set", "display", "input", "antenna", "band-", "band+", "l-", "l+", "c-", "c+", "tune", "off", "power", "operate", "cat"} {
		if !strings.Contains(body, "sendButton('"+action+"')") {
			t.Errorf("watch page missing sendButton onclick for %q", action)
		}
	}

	// up and down are API-level aliases for the combined nav keys; they must appear
	// somewhere in the page (label text, JS comment, or btnIds) so the mapping is
	// visible, even though they do not get separate onclick buttons.
	for _, alias := range []string{"up", "down"} {
		if !strings.Contains(body, alias) {
			t.Errorf("watch page missing any reference to nav alias %q", alias)
		}
	}

	// blocked actions must NOT appear as sendButton calls
	for _, action := range []string{"back", "standby"} {
		if strings.Contains(body, "sendButton('"+action+"')") {
			t.Errorf("watch page exposes blocked action %q as sendButton call", action)
		}
	}
	if strings.Contains(body, "sendButton('on')") {
		t.Error("watch page exposes fake on button action")
	}

	// blocked actions must be mentioned as unavailable (text note, not active buttons)
	for _, action := range []string{"Back", "On", "Standby"} {
		if !strings.Contains(body, action) {
			t.Errorf("watch page does not mention blocked action %q at all", action)
		}
	}
}

// TestWatchUIHasTelemetryPolling verifies that the watch page polls the
// snapshot endpoint and uses the canonical telemetry field names that the
// /api/v1/runtime/snapshot response provides.
func TestWatchUIHasTelemetryPolling(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	body := res.Body.String()

	// page must still fetch the snapshot for metadata/fallback behavior
	if !strings.Contains(body, "/api/v1/runtime/snapshot") {
		t.Error("watch page does not poll /api/v1/runtime/snapshot")
	}
	if !strings.Contains(body, "/api/v1/display/ws") {
		t.Error("watch page missing display websocket endpoint")
	}
	if !strings.Contains(body, "Status polling enabled") {
		t.Error("watch page missing updated status polling wording")
	}
	if !strings.Contains(body, "Protocol status refresh enabled") {
		t.Error("watch page missing updated protocol status refresh wording")
	}
	if !strings.Contains(body, "/api/v1/status") {
		t.Error("watch page/API reference missing /api/v1/status")
	}
	if !strings.Contains(body, "/api/v1/docs") {
		t.Error("watch page/API reference missing /api/v1/docs")
	}

	// canonical status/display fields used by the live watch UI must appear in the page JS.
	for _, field := range []string{"modelName", "operatingState", "band", "antenna", "antennaBank", "outputLevel", "powerWatts", "swr", "temperatureC", "mode", "tx"} {
		if !strings.Contains(body, field) {
			t.Errorf("watch page missing status/display field %q", field)
		}
	}
}

// TestWatchUIBlockedActionsDeclaredHonestly verifies that blocked actions are
// called out as unavailable and that the page carries a caution/explanation note
// about hardware confirmation still being needed for the exposed documented subset.
func TestWatchUIBlockedActionsDeclaredHonestly(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	body := res.Body.String()

	// must carry a human-readable note that blocked actions are unavailable
	if !strings.Contains(body, "Not available") {
		t.Error("watch page missing 'Not available' note for blocked actions")
	}
	for _, action := range []string{"Back", "On", "Standby"} {
		if !strings.Contains(body, action) {
			t.Errorf("watch page missing blocked action note for %q", action)
		}
	}
	// must carry the hardware-confirmation caution for the exposed documented subset
	if !strings.Contains(body, "document-backed") || !strings.Contains(body, "physical confirmation") || !strings.Contains(body, "DTR/RTS") {
		t.Error("watch page missing hardware-confirmation caution note")
	}
}

// TestSettingsEndpointPersistsAdvancedSerialFields verifies that the POST
// /api/v1/settings handler accepts and persists the advanced serial fields.
func TestSettingsEndpointPersistsAdvancedSerialFields(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	payload, _ := json.Marshal(map[string]interface{}{
		"serialPort":               "/dev/ttyUSB0",
		"listenAddress":            ":8088",
		"pollIntervalMs":           250,
		"displayPollingEnabled":    true,
		"statusPollingEnabled":     true,
		"serialBaudRate":           9600,
		"serialReadTimeoutMs":      500,
		"statusPollCommandEnabled": false,
		"statusPollIntervalMs":     750,
		"serialAssertDTR":          false,
		"serialAssertRTS":          false,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}

	var body struct{ Data config.Snapshot }
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	s := body.Data.Settings
	if s.SerialBaudRate != 9600 {
		t.Errorf("SerialBaudRate = %d, want 9600", s.SerialBaudRate)
	}
	if s.SerialReadTimeoutMs != 500 {
		t.Errorf("SerialReadTimeoutMs = %d, want 500", s.SerialReadTimeoutMs)
	}
	if s.StatusPollCommandEnabled {
		t.Errorf("StatusPollCommandEnabled = true, want false")
	}
	if s.StatusPollIntervalMs != 750 {
		t.Errorf("StatusPollIntervalMs = %d, want 750", s.StatusPollIntervalMs)
	}
	if s.SerialAssertDTR {
		t.Errorf("SerialAssertDTR = true, want false")
	}
	if s.SerialAssertRTS {
		t.Errorf("SerialAssertRTS = true, want false")
	}
}

// TestSettingsPageHasAllConfigFields verifies that the settings page HTML
// contains all the config-backed fields so they're actually editable in the UI.
func TestSettingsPageHasAllConfigFields(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	body := res.Body.String()

	// Every config-backed field the settings page wires to the API must be present.
	for _, id := range []string{
		"s-serial-port",
		"s-listen-address",
		"s-poll-interval",
		"s-display-polling",
		"s-status-polling",
		"s-panel-model-label",
		"s-baud",
		"s-read-timeout",
		"s-status-poll-command-enabled",
		"s-status-poll-interval",
		"s-assert-dtr",
		"s-assert-rts",
	} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("settings page missing field id=%q", id)
		}
	}

	// Browser-local-only fields must be present and labeled as local.
	for _, id := range []string{"s-layout", "s-scale", "s-lcd-polarity"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("settings page missing browser-local field id=%q", id)
		}
	}
	if !strings.Contains(body, "browser only") {
		t.Error("settings page does not label browser-only fields as browser only")
	}
	if !strings.Contains(body, `id="restart-server"`) {
		t.Error("settings page missing restart-server button")
	}
	if !strings.Contains(body, "/api/v1/runtime/restart") {
		t.Error("settings page missing runtime restart API wiring")
	}
	if !strings.Contains(body, "/api/v1/docs") {
		t.Error("settings page missing local API docs link")
	}
	if !strings.Contains(body, "systemd") || !strings.Contains(body, "launchd") {
		t.Error("settings page missing honest restart supervisor caveat")
	}
	if !strings.Contains(body, "Inverted (white-on-black)") || !strings.Contains(body, "LCD Native (black-on-white)") {
		t.Error("settings page missing corrected LCD polarity labels")
	}

	// Settings page must be in a dedicated tab container.
	if !strings.Contains(body, `id="view-settings"`) {
		t.Error("settings page missing view-settings tab container")
	}
	if !strings.Contains(body, `id="tab-settings"`) {
		t.Error("settings page missing tab-settings button")
	}
}

// TestSettingsEndpointStatusPollingNoRestartNeeded verifies that toggling
// statusPollingEnabled alone does not trigger a restart-needed message, since
// the poller respects it as a live toggle.
func TestRuntimeRestartEndpointRequestsCleanExit(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, restart := newServer(mgr, 250*time.Millisecond, func() {})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/restart", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !body.Success {
		t.Fatalf("unexpected failure body: %+v", body)
	}
	if !strings.Contains(body.Message, "process will exit cleanly") {
		t.Fatalf("Message = %q, want clean-exit restart note", body.Message)
	}
	for i := 0; i < 20 && !restart.requested(); i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if !restart.requested() {
		t.Fatal("restart was not requested")
	}
}

func TestSettingsEndpointAcceptsLegacySerialPollAliases(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})
	payload, _ := json.Marshal(map[string]interface{}{
		"serialPort":            "/dev/ttyUSB0",
		"listenAddress":         ":8088",
		"pollIntervalMs":        250,
		"displayPollingEnabled": true,
		"statusPollingEnabled":  true,
		"serialPollEnabled":     false,
		"serialPollIntervalMs":  900,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	s := mgr.Get().Settings
	if s.StatusPollCommandEnabled {
		t.Fatalf("StatusPollCommandEnabled = true, want false")
	}
	if s.StatusPollIntervalMs != 900 {
		t.Fatalf("StatusPollIntervalMs = %d, want 900", s.StatusPollIntervalMs)
	}
}

func TestSettingsEndpointStatusPollingNoRestartNeeded(t *testing.T) {
	mgr, err := config.NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// Seed defaults so only statusPollingEnabled changes.
	if _, err := mgr.Update(config.Settings{
		SerialPort:            "/dev/ttyUSB0",
		ListenAddress:         ":8088",
		PollIntervalMs:        250,
		DisplayPollingEnabled: true,
		StatusPollingEnabled:  true,
	}); err != nil {
		t.Fatalf("seed Update: %v", err)
	}

	h, _, _, _, _ := newServer(mgr, 250*time.Millisecond, func() {})
	payload, _ := json.Marshal(map[string]interface{}{
		"serialPort":            "/dev/ttyUSB0",
		"listenAddress":         ":8088",
		"pollIntervalMs":        250,
		"displayPollingEnabled": true,
		"statusPollingEnabled":  false, // toggled off
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !body.Success {
		t.Fatalf("unexpected failure: %+v", body)
	}
	if body.Message != "settings saved" {
		t.Fatalf("Message = %q, want \"settings saved\" (statusPollingEnabled is a live toggle, no restart needed)", body.Message)
	}
}
