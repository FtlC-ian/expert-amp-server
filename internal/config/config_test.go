package config

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
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
	if snap.Settings.AmplifierTemperatureUnit != "C" {
		t.Fatalf("AmplifierTemperatureUnit = %q, want C", snap.Settings.AmplifierTemperatureUnit)
	}
	if !snap.Settings.StatusPollCommandEnabled || snap.Settings.StatusPollIntervalMs != 125 {
		t.Fatalf("status poll command defaults unexpected: %+v", snap.Settings)
	}
	if snap.Settings.AutomaticFanPolicyEnabled || snap.Settings.FanHighTemperatureC != 50 || snap.Settings.FanNormalTemperatureC != 42 {
		t.Fatalf("automatic fan policy defaults unexpected: %+v", snap.Settings)
	}
	if snap.Settings.MenuDebugEnabled {
		t.Fatal("MenuDebugEnabled = true, want disabled by default")
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
	if got.AutomaticFanPolicyEnabled || got.FanHighTemperatureC != 50 || got.FanNormalTemperatureC != 42 {
		t.Fatalf("saved automatic fan policy defaults unexpected: %+v", got)
	}
	if got.MenuDebugEnabled {
		t.Fatal("saved MenuDebugEnabled = true, want false")
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode saved settings map: %v", err)
	}
	if raw, ok := saved["menuDebugEnabled"]; !ok || string(raw) != "false" {
		t.Fatalf("saved menuDebugEnabled = %s, present=%v; want explicit false", raw, ok)
	}
}

func TestManagerPersistsAndValidatesAmplifierTemperatureUnit(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	settings := mgr.Get().Settings
	settings.AmplifierTemperatureUnit = "f"
	snap, err := mgr.Update(settings)
	if err != nil {
		t.Fatalf("Update(F): %v", err)
	}
	if snap.Settings.AmplifierTemperatureUnit != "F" {
		t.Fatalf("AmplifierTemperatureUnit = %q, want F", snap.Settings.AmplifierTemperatureUnit)
	}
	settings = snap.Settings
	settings.AmplifierTemperatureUnit = "kelvin"
	if _, err := mgr.Update(settings); err == nil || !strings.Contains(err.Error(), "temperature unit") {
		t.Fatalf("Update(kelvin) error = %v, want unit validation error", err)
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
	if snap.Settings.AutomaticFanPolicyEnabled || snap.Settings.FanHighTemperatureC != 50 || snap.Settings.FanNormalTemperatureC != 42 {
		t.Fatalf("automatic fan policy defaults unexpected after normalize: %+v", snap.Settings)
	}
}

func TestSerialControlLineFalseValuesSurviveSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expert-amp-server.json")
	if err := os.WriteFile(path, []byte(`{
		"serialPort": "/dev/ttyUSB0",
		"serialAssertDTR": false,
		"serialAssertRTS": false
	}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if got := mgr.Get().Settings; got.SerialAssertDTR || got.SerialAssertRTS {
		t.Fatalf("explicit false values normalized away: %+v", got)
	}

	settings := mgr.Get().Settings
	settings.PanelModelLabel = "Test"
	if _, err := mgr.Update(settings); err != nil {
		t.Fatalf("Update unrelated setting: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal persisted config: %v", err)
	}
	for _, key := range []string{"serialAssertDTR", "serialAssertRTS"} {
		raw, ok := persisted[key]
		if !ok || string(raw) != "false" {
			t.Fatalf("persisted %s = %s, present=%v; want explicit false", key, raw, ok)
		}
	}

	reloaded, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("reload NewManager: %v", err)
	}
	if got := reloaded.Get().Settings; got.SerialAssertDTR || got.SerialAssertRTS {
		t.Fatalf("explicit false values did not survive reload: %+v", got)
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
		SerialPort:                  "/dev/ttyUSB1",
		ListenAddress:               ":8090",
		PollingMode:                 string(PollingModeBoth),
		PollIntervalMs:              500,
		DisplayPollingEnabled:       true,
		StatusPollingEnabled:        true,
		PanelModelLabel:             "  N0CALL  ",
		InputLabels:                 map[string]string{"1": " IC-7300 ", "2": "ANAN G2"},
		AntennaLabels:               map[string]string{"1": "Dipole", "6": "Dummy Load"},
		SafetyMonitoringEnabled:     true,
		OvertemperatureStandbyArmed: true,
		TemperatureWarningC:         70,
		TemperatureTripC:            80,
		TemperatureResetC:           65,
		SWRWarning:                  2,
		SWRTrip:                     3,
		AutomaticFanPolicyEnabled:   true,
		FanHighTemperatureC:         65,
		FanNormalTemperatureC:       55,
		FanDisplayProfile:           FanDisplayProfileFirstSeries,
		MenuDebugEnabled:            true,
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
	if !snap.Settings.DisplayPollingEnabled {
		t.Fatalf("DisplayPollingEnabled = false, want true")
	}
	if !snap.Settings.StatusPollingEnabled || !snap.Settings.StatusPollCommandEnabled {
		t.Fatalf("status legacy fields not synced: %+v", snap.Settings)
	}
	if snap.Settings.PanelModelLabel != "N0CALL" {
		t.Fatalf("PanelModelLabel = %q", snap.Settings.PanelModelLabel)
	}
	if snap.Settings.InputLabels["1"] != "IC-7300" || snap.Settings.InputLabels["2"] != "ANAN G2" {
		t.Fatalf("InputLabels not normalized: %+v", snap.Settings.InputLabels)
	}
	if snap.Settings.AntennaLabels["1"] != "Dipole" || snap.Settings.AntennaLabels["6"] != "Dummy Load" {
		t.Fatalf("AntennaLabels not normalized: %+v", snap.Settings.AntennaLabels)
	}
	if !snap.Settings.SafetyMonitoringEnabled || !snap.Settings.OvertemperatureStandbyArmed || snap.Settings.TemperatureWarningC != 70 || snap.Settings.TemperatureTripC != 80 || snap.Settings.TemperatureResetC != 65 || snap.Settings.SWRWarning != 2 || snap.Settings.SWRTrip != 3 {
		t.Fatalf("safety monitoring settings not persisted: %+v", snap.Settings)
	}
	if !snap.Settings.AutomaticFanPolicyEnabled || snap.Settings.FanHighTemperatureC != 65 || snap.Settings.FanNormalTemperatureC != 55 || snap.Settings.FanDisplayProfile != FanDisplayProfileFirstSeries {
		t.Fatalf("automatic fan policy settings not persisted: %+v", snap.Settings)
	}
	if !snap.Settings.MenuDebugEnabled {
		t.Fatal("MenuDebugEnabled was not persisted")
	}
	reloaded, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	if !reloaded.Get().Settings.MenuDebugEnabled {
		t.Fatal("MenuDebugEnabled was not restored after reload")
	}
}

func TestFanPolicyRuntimeStatePersistsWithoutBecomingEditableSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expert-amp-server.json")
	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	state := FanPolicyRuntimeState{
		ManualOverride:      "high-cooling",
		ManualOverrideUntil: "2026-07-31T12:00:00Z",
		LastVerifiedPolicy:  "high-cooling",
		LastVerifiedAt:      "2026-07-30T17:00:00Z",
		LastVerifiedSource:  "manual-override",
	}
	if err := mgr.UpdateFanPolicyState(state); err != nil {
		t.Fatalf("UpdateFanPolicyState: %v", err)
	}
	if _, err := mgr.Update(Settings{
		PollingMode:            string(PollingModeBoth),
		FanHighTemperatureC:    50,
		FanNormalTemperatureC:  42,
		FanDisplayProfile:      FanDisplayProfileFirstSeries,
		VerifyFanModeOnStartup: true,
	}); err != nil {
		t.Fatalf("Update settings: %v", err)
	}

	reloaded, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("reload NewManager: %v", err)
	}
	if got := reloaded.FanPolicyState(); got != state {
		t.Fatalf("reloaded fan state = %+v, want %+v", got, state)
	}
	snapshotJSON, err := json.Marshal(reloaded.Get())
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	if strings.Contains(string(snapshotJSON), "fanPolicyState") || strings.Contains(string(snapshotJSON), "lastVerifiedPolicy") {
		t.Fatalf("server-owned state leaked into editable settings snapshot: %s", snapshotJSON)
	}
	if !reloaded.Get().Settings.VerifyFanModeOnStartup {
		t.Fatal("verify-on-startup setting was not persisted")
	}
}

func TestUpdateWriteFailureDoesNotMutateCurrentSettings(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	before := mgr.Get()
	mgr.path = t.TempDir() // os.WriteFile cannot replace a directory.
	if _, err := mgr.Update(Settings{
		PollingMode:               string(PollingModeBoth),
		AutomaticFanPolicyEnabled: true,
		FanHighTemperatureC:       80,
		FanNormalTemperatureC:     75,
		FanDisplayProfile:         FanDisplayProfileFirstSeries,
	}); err == nil {
		t.Fatal("Update error = nil, want persistence failure")
	}
	after := mgr.Get()
	if after.Settings.AutomaticFanPolicyEnabled != before.Settings.AutomaticFanPolicyEnabled ||
		after.Settings.FanDisplayProfile != before.Settings.FanDisplayProfile {
		t.Fatalf("failed write mutated current settings: before=%+v after=%+v", before.Settings, after.Settings)
	}
}

func TestUpdateNormalizesZeroFanThresholdsToDefaults(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	snap, err := mgr.Update(Settings{
		AutomaticFanPolicyEnabled: true,
		PollingMode:               string(PollingModeBoth),
		FanDisplayProfile:         FanDisplayProfileFirstSeries,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if snap.Settings.FanHighTemperatureC != 50 || snap.Settings.FanNormalTemperatureC != 42 {
		t.Fatalf("automatic fan thresholds were not normalized to defaults: %+v", snap.Settings)
	}
}

func TestUpdateValidatesAutomaticFanPolicyThresholds(t *testing.T) {
	tests := []struct {
		name   string
		high   float64
		normal float64
	}{
		{name: "normal equals high", high: 80, normal: 80},
		{name: "normal above high", high: 75, normal: 80},
		{name: "negative high", high: -1, normal: 75},
		{name: "negative normal", high: 80, normal: -1},
		{name: "hysteresis below minimum", high: 80, normal: 75.1},
		{name: "nan high", high: math.NaN(), normal: 75},
		{name: "infinite normal", high: 80, normal: math.Inf(1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			_, err = mgr.Update(Settings{
				AutomaticFanPolicyEnabled: true,
				PollingMode:               string(PollingModeBoth),
				FanHighTemperatureC:       tc.high,
				FanNormalTemperatureC:     tc.normal,
				FanDisplayProfile:         FanDisplayProfileFirstSeries,
			})
			if err == nil {
				t.Fatal("Update error = nil, want automatic fan policy validation error")
			}
		})
	}
}

func TestUpdateRequiresBothPollingForFanControl(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{name: "status only", mode: string(PollingModeStatus)},
		{name: "display only", mode: string(PollingModeDisplay)},
		{name: "polling off", mode: string(PollingModeOff)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			_, err = mgr.Update(Settings{
				PollingMode:               tc.mode,
				AutomaticFanPolicyEnabled: true,
				FanHighTemperatureC:       80,
				FanNormalTemperatureC:     75,
			})
			if err == nil {
				t.Fatal("Update error = nil, want fan policy safety-gate validation error")
			}
		})
	}
}

func TestUpdateRequiresBothPollingForStartupFanVerification(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := mgr.Update(Settings{
		PollingMode:            string(PollingModeStatus),
		FanHighTemperatureC:    50,
		FanNormalTemperatureC:  42,
		FanDisplayProfile:      FanDisplayProfileFirstSeries,
		VerifyFanModeOnStartup: true,
	}); err == nil {
		t.Fatal("Update error = nil, want startup verification polling validation error")
	}
}

func TestLoadExistingConfigRejectsEnabledFanPolicyWithoutBothPolling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expert-amp-server.json")
	if err := os.WriteFile(path, []byte(`{
		"pollingMode": "status",
		"automaticFanPolicyEnabled": true,
		"fanHighTemperatureC": 80,
		"fanNormalTemperatureC": 75
	}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewManager(path, ":8088"); err == nil {
		t.Fatal("NewManager error = nil, want invalid fan polling error")
	}
}

func TestUpdatePersistsFanBoostDuration(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	snap, err := mgr.Update(Settings{
		PollingMode:             string(PollingModeBoth),
		FanHighTemperatureC:     50,
		FanNormalTemperatureC:   42,
		FanBoostDurationMinutes: 30,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if snap.Settings.FanBoostDurationMinutes != 30 {
		t.Fatalf("FanBoostDurationMinutes = %d, want 30", snap.Settings.FanBoostDurationMinutes)
	}
}

func TestUpdateRejectsInvalidFanBoostDuration(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	for _, duration := range []int{-1, 10081} {
		_, err := mgr.Update(Settings{
			PollingMode:             string(PollingModeBoth),
			FanHighTemperatureC:     50,
			FanNormalTemperatureC:   42,
			FanBoostDurationMinutes: duration,
		})
		if err == nil {
			t.Fatalf("duration %d accepted, want validation error", duration)
		}
	}
}

func TestLoadExistingConfigRejectsInvalidAutomaticFanPolicyThresholds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expert-amp-server.json")
	if err := os.WriteFile(path, []byte(`{
		"automaticFanPolicyEnabled": true,
		"fanHighTemperatureC": 75,
		"fanNormalTemperatureC": 80
	}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewManager(path, ":8088"); err == nil {
		t.Fatal("NewManager error = nil, want invalid automatic fan policy threshold error")
	}
}

func TestLoadExistingConfigMigratesInvalidDisabledFanThresholds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expert-amp-server.json")
	if err := os.WriteFile(path, []byte(`{
		"automaticFanPolicyEnabled": false,
		"fanHighTemperatureC": 75,
		"fanNormalTemperatureC": 80
	}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	got := mgr.Get().Settings
	if got.FanHighTemperatureC != DefaultFanHighTemperatureC || got.FanNormalTemperatureC != DefaultFanNormalTemperatureC {
		t.Fatalf("disabled legacy thresholds were not migrated: %+v", got)
	}
}

func TestUpdateRejectsFanThresholdThatCannotPreemptArmedStandby(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	_, err = mgr.Update(Settings{
		SafetyMonitoringEnabled:     true,
		OvertemperatureStandbyArmed: true,
		TemperatureWarningC:         70,
		TemperatureTripC:            75,
		TemperatureResetC:           65,
		PollingMode:                 string(PollingModeBoth),
		AutomaticFanPolicyEnabled:   true,
		FanHighTemperatureC:         80,
		FanNormalTemperatureC:       75,
		FanDisplayProfile:           FanDisplayProfileFirstSeries,
	})
	if err == nil {
		t.Fatal("Update error = nil, want fan/standby ordering validation error")
	}
}

func TestUpdateValidatesSafetyMonitoringThresholds(t *testing.T) {
	tests := []struct {
		name     string
		settings Settings
	}{
		{
			name: "enabled without thresholds",
			settings: Settings{
				SafetyMonitoringEnabled: true,
			},
		},
		{
			name: "partial temperature pair",
			settings: Settings{
				TemperatureWarningC: 70,
			},
		},
		{
			name: "temperature warning above trip",
			settings: Settings{
				TemperatureWarningC: 90,
				TemperatureTripC:    80,
			},
		},
		{
			name: "partial swr pair",
			settings: Settings{
				SWRWarning: 2,
			},
		},
		{
			name: "armed without monitoring",
			settings: Settings{
				OvertemperatureStandbyArmed: true,
				TemperatureWarningC:         70,
				TemperatureTripC:            80,
			},
		},
		{
			name: "armed without temperature pair",
			settings: Settings{
				SafetyMonitoringEnabled:     true,
				OvertemperatureStandbyArmed: true,
				SWRWarning:                  2,
				SWRTrip:                     3,
			},
		},
		{
			name: "reset at trip",
			settings: Settings{
				TemperatureWarningC: 70,
				TemperatureTripC:    80,
				TemperatureResetC:   80,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr, err := NewManager(filepath.Join(t.TempDir(), "expert-amp-server.json"), ":8088")
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			if _, err := mgr.Update(tc.settings); err == nil {
				t.Fatalf("Update(%+v) error = nil, want validation error", tc.settings)
			}
		})
	}
}

func TestLoadExistingConfigDefaultsSafetyMonitoringOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expert-amp-server.json")
	if err := os.WriteFile(path, []byte(`{"serialPort":"/dev/ttyUSB0"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	settings := mgr.Get().Settings
	if settings.SafetyMonitoringEnabled || settings.OvertemperatureStandbyArmed || settings.TemperatureWarningC != 0 || settings.TemperatureTripC != 0 || settings.TemperatureResetC != 0 || settings.SWRWarning != 0 || settings.SWRTrip != 0 {
		t.Fatalf("unexpected safety defaults: %+v", settings)
	}
}

func TestLoadDoesNotMigrateLegacyPowerOffArmToStandby(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expert-amp-server.json")
	if err := os.WriteFile(path, []byte(`{
		"serialPort": "/dev/ttyUSB0",
		"safetyMonitoringEnabled": true,
		"overtemperatureShutdownArmed": true,
		"temperatureWarningC": 70,
		"temperatureTripC": 75
	}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mgr, err := NewManager(path, ":8088")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if mgr.Get().Settings.OvertemperatureStandbyArmed {
		t.Fatal("legacy power-off arm unexpectedly enabled standby automation")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := persisted["overtemperatureShutdownArmed"]; ok {
		t.Fatalf("legacy power-off arm was not removed during normalization: %s", data)
	}
}

func TestLoadExistingConfigRejectsInvalidSafetyThresholds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "expert-amp-server.json")
	if err := os.WriteFile(path, []byte(`{
		"safetyMonitoringEnabled": true,
		"temperatureWarningC": 80,
		"temperatureTripC": 70
	}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewManager(path, ":8088"); err == nil {
		t.Fatal("NewManager error = nil, want invalid safety threshold error")
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
