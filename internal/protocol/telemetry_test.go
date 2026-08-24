package protocol

import (
	"testing"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

func TestTelemetryFromDisplayStateHomeFixture(t *testing.T) {
	frame := loadFixture(t, "real_home_status_frame.bin")
	state, err := StateFromFrame(frame)
	if err != nil {
		t.Fatalf("StateFromFrame: %v", err)
	}

	telem := TelemetryFromDisplayState(state, "fixture:home")
	if telem.ModelName != "EXPERT 1.3K-FA" {
		t.Fatalf("modelName = %q, want EXPERT 1.3K-FA", telem.ModelName)
	}
	if telem.OperatingState != "standby" || telem.Mode != "standby" {
		t.Fatalf("operatingState/mode = %q/%q, want standby/standby", telem.OperatingState, telem.Mode)
	}
	if telem.TX == nil || *telem.TX {
		t.Fatalf("tx = %v, want false pointer", telem.TX)
	}
	if telem.Input != "2" || telem.Band != "40m" || telem.Antenna != "4b" || telem.AntennaBank != "A" {
		t.Fatalf("unexpected front-panel fields: %+v", telem)
	}
	if telem.CATInterface != "ICOM" || telem.OutputLevel != "LOW" {
		t.Fatalf("unexpected cat/output fields: %+v", telem)
	}
	if telem.SWR != nil {
		t.Fatalf("swr numeric = %v, want nil for --.--", telem.SWR)
	}
	if telem.SWRDisplay != "--.--" {
		t.Fatalf("swrDisplay = %q, want --.--", telem.SWRDisplay)
	}
	if telem.TemperatureC == nil || *telem.TemperatureC != 22 {
		t.Fatalf("temperatureC = %v, want 22", telem.TemperatureC)
	}
	if telem.TemperatureDisplay != "22 C" {
		t.Fatalf("temperatureDisplay = %q, want 22 C", telem.TemperatureDisplay)
	}
	if telem.Frequency != "" {
		t.Fatalf("frequency = %q, want empty", telem.Frequency)
	}
	if telem.PowerWatts != nil {
		t.Fatalf("powerWatts = %v, want nil", telem.PowerWatts)
	}
	if telem.Confidence != "display-derived" || telem.Provenance != "display-frame" || telem.Source != "fixture:home" {
		t.Fatalf("unexpected metadata: %+v", telem)
	}
	if len(telem.Notes) < 2 {
		t.Fatalf("notes = %v, want unknown-field notes", telem.Notes)
	}
}

func TestTelemetryFromDisplayStateConvertsExplicitFahrenheit(t *testing.T) {
	state := display.NewState()
	state.SetRow(1, "EXPERT 2K-FA")
	state.SetRow(6, "TEMP")
	state.SetRow(7, "86 F")

	telem := TelemetryFromDisplayState(state, "serial")
	if telem.TemperatureUnit != "F" || telem.TemperatureDisplay != "86 F" {
		t.Fatalf("unexpected raw-unit fields: %+v", telem)
	}
	if telem.TemperatureC == nil || *telem.TemperatureC != 30 {
		t.Fatalf("TemperatureC = %v, want 30", telem.TemperatureC)
	}
}

func TestTelemetryFromDisplayStatePreservesFixedColumnMultiWordValues(t *testing.T) {
	state := display.NewState()
	state.SetRow(1, "EXPERT 2K-FA")
	state.SetRow(6, " IN   BAND  ANT  CAT   OUT   SWR   TEMP")
	state.SetRow(7, "  2   80 m   2  FLEX   MID  --.--  90 F")

	telem := TelemetryFromDisplayState(state, "fixture:2k-fa")
	if telem.Input != "2" || telem.Band != "80 m" || telem.Antenna != "2" {
		t.Fatalf("unexpected input/band/antenna fields: %+v", telem)
	}
	if telem.CATInterface != "FLEX" || telem.OutputLevel != "MID" {
		t.Fatalf("unexpected cat/output fields: %+v", telem)
	}
	if telem.SWRDisplay != "--.--" || telem.SWR != nil {
		t.Fatalf("unexpected SWR fields: %+v", telem)
	}
	if telem.TemperatureDisplay != "90 F" || telem.TemperatureUnit != "F" {
		t.Fatalf("unexpected raw temperature fields: %+v", telem)
	}
	if telem.TemperatureC == nil || *telem.TemperatureC < 32.22 || *telem.TemperatureC > 32.23 {
		t.Fatalf("TemperatureC = %v, want about 32.22", telem.TemperatureC)
	}
}

func TestParseTemperatureAcceptsDegreeSymbol(t *testing.T) {
	got, unit, ok := parseTemperature("86 °F")
	if !ok || unit != "F" || got != 30 {
		t.Fatalf("parseTemperature = %v, %q, %v; want 30, F, true", got, unit, ok)
	}
}

func TestTelemetryFromDisplayStateOperateScreenLeavesModelNameEmptyWhenRowIsNotAModel(t *testing.T) {
	state := display.NewState()
	copy(state.Chars[1][:], []byte("PA OUT 0W pep"))
	copy(state.Chars[4][:], []byte("Operate"))

	telem := TelemetryFromDisplayState(state, "serial")
	if telem.ModelName != "" {
		t.Fatalf("modelName = %q, want empty", telem.ModelName)
	}
	if telem.OperatingState != "operate" || telem.Mode != "operate" {
		t.Fatalf("operatingState/mode = %q/%q, want operate/operate", telem.OperatingState, telem.Mode)
	}
	if telem.TX != nil {
		t.Fatalf("tx = %v, want nil for operate display fallback", telem.TX)
	}
	if telem.Source != "serial" {
		t.Fatalf("source = %q, want serial", telem.Source)
	}
}

func TestTelemetryFromDisplayStateBlankStateLeavesUnknownsEmpty(t *testing.T) {
	telem := TelemetryFromDisplayState(display.NewState(), "serial")
	if telem.ModelName != "" || telem.Band != "" || telem.OperatingState != "" {
		t.Fatalf("expected blank telemetry, got %+v", telem)
	}
	if telem.Source != "serial" {
		t.Fatalf("source = %q, want serial", telem.Source)
	}
}
