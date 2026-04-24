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
