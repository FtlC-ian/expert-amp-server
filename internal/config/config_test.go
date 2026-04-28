package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNewManagerCreatesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "expert-amp-server.json")

	mgr, err := NewManager(path, ":9000")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	snap := mgr.Get()
	if !snap.NeedsSetup {
		t.Fatalf("NeedsSetup = false, want true")
	}
	if snap.Settings.ListenAddress != ":9000" {
		t.Fatalf("ListenAddress = %q, want %q", snap.Settings.ListenAddress, ":9000")
	}
	if snap.Settings.PollingMode != string(PollingModeBoth) {
		t.Fatalf("PollingMode = %q, want both", snap.Settings.PollingMode)
	}
	if snap.Settings.PollIntervalMs != DefaultPollIntervalMs {
		t.Fatalf("PollIntervalMs = %d, want %d", snap.Settings.PollIntervalMs, DefaultPollIntervalMs)
	}
	if !snap.Settings.DisplayPollingEnabled || !snap.Settings.StatusPollingEnabled {
		t.Fatalf("polling defaults not enabled: %+v", snap.Settings)
	}
	if !snap.Settings.StatusPollCommandEnabled || snap.Settings.StatusPollIntervalMs != 125 {
		t.Fatalf("status poll command defaults unexpected: %+v", snap.Settings)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got Settings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.PollIntervalMs != DefaultPollIntervalMs {
		t.Fatalf("saved PollIntervalMs = %d, want %d", got.PollIntervalMs, DefaultPollIntervalMs)
	}
}

func TestLoadOrCreateNormalizesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "expert-amp-server.json")
	if err := os.WriteFile(path, []byte(`{"serialPort":"/dev/ttyUSB0"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	snap := mgr.Get()
	if snap.NeedsSetup {
		t.Fatalf("NeedsSetup = true, want false")
	}
	if snap.Settings.SerialPort != "/dev/ttyUSB0" {
		t.Fatalf("SerialPort = %q", snap.Settings.SerialPort)
	}
	if snap.Settings.PollIntervalMs != DefaultPollIntervalMs {
		t.Fatalf("PollIntervalMs = %d, want %d", snap.Settings.PollIntervalMs, DefaultPollIntervalMs)
	}
	if !snap.Settings.DisplayPollingEnabled || !snap.Settings.StatusPollingEnabled {
		t.Fatalf("polling defaults not enabled after normalize: %+v", snap.Settings)
	}
	if !snap.Settings.StatusPollCommandEnabled || snap.Settings.StatusPollIntervalMs != 125 {
		t.Fatalf("status poll command defaults unexpected after normalize: %+v", snap.Settings)
	}
}

func TestLoadOrCreateNormalizesLegacySerialPollFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "expert-amp-server.json")
	legacy := `{
		"serialPort":"/dev/ttyUSB0",
		"serialPollEnabled":false,
		"serialPollIntervalMs":750
	}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	snap := mgr.Get()
	if snap.Settings.SerialPort != "/dev/ttyUSB0" {
		t.Fatalf("SerialPort = %q", snap.Settings.SerialPort)
	}
	if snap.Settings.StatusPollCommandEnabled {
		t.Fatalf("StatusPollCommandEnabled = true, want false")
	}
	if snap.Settings.StatusPollIntervalMs != 750 {
		t.Fatalf("StatusPollIntervalMs = %d, want 750", snap.Settings.StatusPollIntervalMs)
	}
	if snap.Settings.PollingMode != string(PollingModeOff) {
		t.Fatalf("PollingMode = %q, want off", snap.Settings.PollingMode)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := got["serialPollEnabled"]; ok {
		t.Fatalf("legacy serialPollEnabled should not persist after normalization: %+v", got)
	}
	if _, ok := got["statusPollCommandFrameHex"]; ok {
		t.Fatalf("statusPollCommandFrameHex should not persist after normalization: %+v", got)
	}
	if _, ok := got["serialPollFrameHex"]; ok {
		t.Fatalf("serialPollFrameHex should not persist after normalization: %+v", got)
	}
}

func TestUpdatePersistsSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "expert-amp-server.json")
	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	snap, err := mgr.Update(Settings{
		SerialPort:            "/dev/ttyUSB1",
		ListenAddress:         ":8090",
		PollingMode:           string(PollingModeStatus),
		PollIntervalMs:        500,
		DisplayPollingEnabled: false,
		StatusPollingEnabled:  true,
		PanelModelLabel:       "  AF5SH  ",
		InputLabels:           map[string]string{"1": " IC-7300 ", "2": "ANAN G2"},
		AntennaLabels:         map[string]string{"1": "Dipole", "6": "Dummy Load"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if snap.NeedsSetup {
		t.Fatalf("NeedsSetup = true, want false")
	}
	if snap.Settings.ListenAddress != ":8090" || snap.Settings.PollIntervalMs != 500 {
		t.Fatalf("unexpected settings: %+v", snap.Settings)
	}
	if snap.Settings.DisplayPollingEnabled {
		t.Fatalf("DisplayPollingEnabled = true, want false")
	}
	if !snap.Settings.StatusPollingEnabled || !snap.Settings.StatusPollCommandEnabled {
		t.Fatalf("status legacy fields not synced: %+v", snap.Settings)
	}
	if snap.Settings.PanelModelLabel != "AF5SH" {
		t.Fatalf("PanelModelLabel = %q", snap.Settings.PanelModelLabel)
	}
	if snap.Settings.InputLabels["1"] != "IC-7300" || snap.Settings.InputLabels["2"] != "ANAN G2" {
		t.Fatalf("InputLabels not normalized: %+v", snap.Settings.InputLabels)
	}
	if snap.Settings.AntennaLabels["1"] != "Dipole" || snap.Settings.AntennaLabels["6"] != "Dummy Load" {
		t.Fatalf("AntennaLabels not normalized: %+v", snap.Settings.AntennaLabels)
	}
}

func TestUpdateRejectsInvalidStationLabels(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = mgr.Update(Settings{
		PollingMode:     string(PollingModeBoth),
		InputLabels:     map[string]string{"3": "Too far"},
		AntennaLabels:   map[string]string{"1": "Dipole"},
		PanelModelLabel: "ok",
	})
	if err == nil {
		t.Fatal("Update error = nil, want invalid input label key error")
	}
}

func TestUpdatePersistsPollingModeAndSyncsLegacyFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "expert-amp-server.json")
	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = mgr.Update(Settings{
		SerialPort:     "/dev/ttyUSB0",
		ListenAddress:  ":8088",
		PollingMode:    string(PollingModeDisplay),
		PollIntervalMs: 125,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var saved Settings
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if saved.PollingMode != string(PollingModeDisplay) {
		t.Fatalf("PollingMode = %q, want display", saved.PollingMode)
	}
	if !saved.DisplayPollingEnabled || saved.StatusPollingEnabled || saved.StatusPollCommandEnabled {
		t.Fatalf("legacy polling fields not synced for display mode: %+v", saved)
	}
}

func TestLoadOrCreateDerivesPollingModeFromLegacyFields(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"both", `{"displayPollingEnabled":true,"statusPollingEnabled":true,"statusPollCommandEnabled":true}`, "both"},
		{"display", `{"displayPollingEnabled":true,"statusPollingEnabled":false,"statusPollCommandEnabled":true}`, "display"},
		{"status", `{"displayPollingEnabled":false,"statusPollingEnabled":true,"statusPollCommandEnabled":true}`, "status"},
		{"off", `{"displayPollingEnabled":false,"statusPollingEnabled":true,"statusPollCommandEnabled":false}`, "off"},
		{"off when status command disabled despite display and status", `{"displayPollingEnabled":true,"statusPollingEnabled":true,"statusPollCommandEnabled":false}`, "off"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "expert-amp-server.json")
			if err := os.WriteFile(path, []byte(tc.json), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			mgr, err := NewManager(path, ":8088")
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			if got := mgr.Get().Settings.PollingMode; got != tc.want {
				t.Fatalf("PollingMode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLegacyStatusIntervalSeedsUnifiedPollInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "expert-amp-server.json")
	legacy := `{"displayPollingEnabled":false,"statusPollingEnabled":true,"statusPollCommandEnabled":true,"statusPollIntervalMs":750}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	settings := mgr.Get().Settings
	if settings.PollingMode != string(PollingModeStatus) || settings.PollIntervalMs != 750 {
		t.Fatalf("unexpected migrated polling settings: %+v", settings)
	}
}

func TestUpdateRejectsObviouslyBadSerialPort(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = mgr.Update(Settings{SerialPort: "/dev/ttyUSB0\nrm -rf /", PollingMode: string(PollingModeBoth), DisplayPollingEnabled: true, StatusPollingEnabled: true})
	if err == nil {
		t.Fatal("Update error = nil, want validation error")
	}
}
