package protocol

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func loadStatusFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return b
}

func TestParseStatusFrameVendorExample(t *testing.T) {
	frame := loadStatusFixture(t, "status_response_example.bin")
	resp, err := ParseStatusFrame(frame)
	if err != nil {
		t.Fatalf("ParseStatusFrame: %v", err)
	}
	if resp.PAIdentifier != "20K" {
		t.Fatalf("PAIdentifier = %q, want 20K", resp.PAIdentifier)
	}
	if resp.OperatingCode != "S" || resp.TXRXCode != "R" {
		t.Fatalf("unexpected operating/txrx codes: %+v", resp)
	}
	if resp.BandCode != "00" {
		t.Fatalf("BandCode = %q, want 00", resp.BandCode)
	}
	if resp.TXAntenna != "1" || resp.ATUStatusCode != "a" || resp.RXAntenna != "0r" {
		t.Fatalf("unexpected antenna fields: %+v", resp)
	}
	if resp.OutputPowerRaw != "0000" || resp.SWRATURaw != "0.00" || resp.TempUpperRaw != "033" {
		t.Fatalf("unexpected numeric fields: %+v", resp)
	}
	if resp.WarningCode != "N" || resp.AlarmCode != "N" {
		t.Fatalf("unexpected warning/alarm codes: %+v", resp)
	}
}

func TestStatusFromResponseMapsDocumentedFields(t *testing.T) {
	frame := loadStatusFixture(t, "status_response_example.bin")
	status, err := StatusFromFrame(frame, "serial")
	if err != nil {
		t.Fatalf("StatusFromFrame: %v", err)
	}
	if status.ModelName != "EXPERT 2K-FA" {
		t.Fatalf("ModelName = %q, want EXPERT 2K-FA", status.ModelName)
	}
	if status.OperatingState != "standby" || status.Mode != "standby" {
		t.Fatalf("unexpected operating state: %+v", status)
	}
	if status.TX == nil || *status.TX {
		t.Fatalf("TX = %v, want false", status.TX)
	}
	if status.Input != "1" || status.Antenna != "1" || status.OutputLevel != "LOW" {
		t.Fatalf("unexpected mapped fields: %+v", status)
	}
	if status.PowerWatts == nil || *status.PowerWatts != 0 {
		t.Fatalf("PowerWatts = %v, want 0", status.PowerWatts)
	}
	if status.SWR == nil || *status.SWR != 0 {
		t.Fatalf("SWR = %v, want 0", status.SWR)
	}
	if status.TemperatureC == nil || *status.TemperatureC != 33 {
		t.Fatalf("TemperatureC = %v, want 33", status.TemperatureC)
	}
	if status.Band != "" || status.BandCode != "00" || status.BandText != "160m" {
		t.Fatalf("band fields = %q/%q/%q, want empty band, raw 00 code, and decoded 160m", status.Band, status.BandCode, status.BandText)
	}
	if status.Source != "serial" || status.Confidence != "protocol-native" || status.Provenance != "status-poll" {
		t.Fatalf("unexpected metadata: %+v", status)
	}
	if status.WarningCode != "" || status.AlarmCode != "" {
		t.Fatalf("unexpected raw warning/alarm codes: %+v", status)
	}
	if status.ATUStatusCode != "a" {
		t.Fatalf("ATUStatusCode = %q, want a", status.ATUStatusCode)
	}
	if status.AntennaSWR == nil || *status.AntennaSWR != 0 || status.AntennaSWRDisplay != "0.00" {
		t.Fatalf("unexpected antenna swr fields: %+v", status)
	}
	if status.PASupplyVoltage == nil || *status.PASupplyVoltage != 0 || status.PASupplyVoltageDisplay != "0.0" {
		t.Fatalf("unexpected PA voltage fields: %+v", status)
	}
	if status.PACurrent == nil || *status.PACurrent != 0 || status.PACurrentDisplay != "0.0" {
		t.Fatalf("unexpected PA current fields: %+v", status)
	}
	if status.TemperatureLowerC == nil || *status.TemperatureLowerC != 0 || status.TemperatureLowerDisplay != "0 C" {
		t.Fatalf("unexpected lower temperature fields: %+v", status)
	}
	if status.TemperatureCombinerC == nil || *status.TemperatureCombinerC != 0 || status.TemperatureCombinerDisplay != "0 C" {
		t.Fatalf("unexpected combiner temperature fields: %+v", status)
	}
	if len(status.Notes) != 0 {
		t.Fatalf("notes = %+v, want empty after promoted documented fields", status.Notes)
	}
	if len(status.ActiveAlarms) != 0 || len(status.Warnings) != 0 || len(status.WarningsText) != 0 || len(status.AlarmsText) != 0 {
		t.Fatalf("unexpected warnings/alarms: %+v", status)
	}
}

func TestParseStatusFrameLiveCapture(t *testing.T) {
	frameHex := "aaaaaa432c31334b2c532c522c412c322c30352c34622c30722c4c2c303030302c20302e30302c20302e30302c20302e302c20302e302c2032352c3030302c3030302c4e2c4e2c3b0d2c0d0a"
	frame, err := hex.DecodeString(frameHex)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	resp, err := ParseStatusFrame(frame)
	if err != nil {
		t.Fatalf("ParseStatusFrame(live capture): %v", err)
	}
	if resp.PAIdentifier != "13K" || resp.MemoryBank != "A" || resp.Input != "2" || resp.BandCode != "05" {
		t.Fatalf("unexpected parsed identifiers: %+v", resp)
	}
	if resp.TXAntenna != "4" || resp.ATUStatusCode != "b" || resp.RXAntenna != "0r" {
		t.Fatalf("unexpected antenna/atu fields: %+v", resp)
	}
	if resp.OutputLevelCode != "L" || resp.OutputPowerRaw != "0000" || resp.SWRATURaw != "0.00" || resp.SWRANTRaw != "0.00" {
		t.Fatalf("unexpected power/swr fields: %+v", resp)
	}
	if resp.VPARaw != "0.0" || resp.IPARaw != "0.0" || resp.TempUpperRaw != "25" || resp.TempLowerRaw != "000" || resp.TempCombRaw != "000" {
		t.Fatalf("unexpected voltage/current/temp fields: %+v", resp)
	}
	if resp.WarningCode != "N" || resp.AlarmCode != "N" {
		t.Fatalf("unexpected warning/alarm codes: %+v", resp)
	}
}

func TestStatusFromResponseMapsLiveCaptureFields(t *testing.T) {
	frameHex := "aaaaaa432c31334b2c532c522c412c322c30352c34622c30722c4c2c303030302c20302e30302c20302e30302c20302e302c20302e302c2032352c3030302c3030302c4e2c4e2c3b0d2c0d0a"
	frame, err := hex.DecodeString(frameHex)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	status, err := StatusFromFrame(frame, "serial")
	if err != nil {
		t.Fatalf("StatusFromFrame(live capture): %v", err)
	}
	if status.Provenance != "status-poll" || status.ModelName != "EXPERT 1.3K-FA" || status.BandCode != "05" || status.BandText != "20m" {
		t.Fatalf("unexpected mapped live status: %+v", status)
	}
	if status.AntennaBank != "A" || status.Input != "2" || status.Antenna != "4" || status.OutputLevel != "LOW" {
		t.Fatalf("unexpected mapped fields: %+v", status)
	}
	if status.TX == nil || *status.TX {
		t.Fatalf("TX = %v, want false", status.TX)
	}
	if status.ATUStatusCode != "b" {
		t.Fatalf("ATUStatusCode = %q, want b", status.ATUStatusCode)
	}
	if status.AntennaSWR == nil || *status.AntennaSWR != 0 || status.AntennaSWRDisplay != "0.00" {
		t.Fatalf("unexpected antenna swr fields: %+v", status)
	}
	if status.PASupplyVoltage == nil || *status.PASupplyVoltage != 0 || status.PASupplyVoltageDisplay != "0.0" {
		t.Fatalf("unexpected PA voltage fields: %+v", status)
	}
	if status.PACurrent == nil || *status.PACurrent != 0 || status.PACurrentDisplay != "0.0" {
		t.Fatalf("unexpected PA current fields: %+v", status)
	}
	if status.TemperatureLowerC == nil || *status.TemperatureLowerC != 0 || status.TemperatureLowerDisplay != "0 C" {
		t.Fatalf("unexpected lower temperature fields: %+v", status)
	}
	if status.TemperatureCombinerC == nil || *status.TemperatureCombinerC != 0 || status.TemperatureCombinerDisplay != "0 C" {
		t.Fatalf("unexpected combiner temperature fields: %+v", status)
	}
	if len(status.Notes) != 0 {
		t.Fatalf("notes = %+v, want empty after promoted documented fields", status.Notes)
	}
}

func TestStatusStreamDecoderFindsStatusFrameInChunk(t *testing.T) {
	frame := loadStatusFixture(t, "status_response_example.bin")
	decoder := NewStatusStreamDecoder()
	chunk := append([]byte{0x00, 0x01}, frame...)
	chunk = append(chunk, 0x02)
	frames := decoder.Push(chunk)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	if len(frames[0]) != len(frame) {
		t.Fatalf("frame len = %d, want %d", len(frames[0]), len(frame))
	}
}

func TestStatusStreamDecoderFindsLiveCapturedStatusFrameInChunk(t *testing.T) {
	frameHex := "aaaaaa432c31334b2c532c522c412c322c30352c34622c30722c4c2c303030302c20302e30302c20302e30302c20302e302c20302e302c2032352c3030302c3030302c4e2c4e2c3b0d2c0d0a"
	frame, err := hex.DecodeString(frameHex)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	decoder := NewStatusStreamDecoder()
	chunk := append([]byte{0x00, 0x01}, frame...)
	chunk = append(chunk, 0x02)
	frames := decoder.Push(chunk)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	if len(frames[0]) != len(frame) {
		t.Fatalf("frame len = %d, want %d", len(frames[0]), len(frame))
	}
}

func TestStatusStreamDecoderRetains75ByteLivePrefixUntilFinalByteArrives(t *testing.T) {
	frameHex := "aaaaaa432c31334b2c532c522c412c322c30352c34622c30722c4c2c303030302c20302e30302c20302e30302c20302e302c20302e302c2032352c3030302c3030302c4e2c4e2c3b0d2c0d0a"
	frame, err := hex.DecodeString(frameHex)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	decoder := NewStatusStreamDecoder()
	frames := decoder.Push(frame[:75])
	if len(frames) != 0 {
		t.Fatalf("frames after first chunk = %d, want 0", len(frames))
	}
	frames = decoder.Push(frame[75:])
	if len(frames) != 1 {
		t.Fatalf("frames after second chunk = %d, want 1", len(frames))
	}
	if got := hex.EncodeToString(frames[0]); got != frameHex {
		t.Fatalf("decoded frame = %s, want %s", got, frameHex)
	}
}

func TestParseStatusFrameRejects76ByteFrameWithExtraByteBeforeCRLF(t *testing.T) {
	frameHex := "aaaaaa432c31334b2c532c522c412c322c30352c34622c30722c4c2c303030302c20302e30302c20302e30302c20302e302c20302e302c2032352c3030302c3030302c4e2c4e2c3b0d58220d0a"
	frame, err := hex.DecodeString(frameHex)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	if _, err := ParseStatusFrame(frame); err == nil {
		t.Fatal("ParseStatusFrame unexpectedly accepted invalid 76-byte terminator")
	}
}

func TestStatusFromResponseFormatsNonZeroNumericFields(t *testing.T) {
	status := StatusFromResponse(StatusResponse{
		PAIdentifier:    "13K",
		OperatingCode:   "O",
		TXRXCode:        "T",
		OutputLevelCode: "H",
		OutputPowerRaw:  "1234",
		SWRATURaw:       "1.37",
		SWRANTRaw:       "1.42",
		VPARaw:          "48.6",
		IPARaw:          "12.5",
		TempUpperRaw:    "41",
		TempLowerRaw:    "37.0",
		TempCombRaw:     "39",
		WarningCode:     "N",
		AlarmCode:       "N",
		ATUStatusCode:   "b",
	}, "serial")

	if status.PowerWatts == nil || *status.PowerWatts != 1234 {
		t.Fatalf("PowerWatts = %v, want 1234", status.PowerWatts)
	}
	if status.SWR == nil || *status.SWR != 1.37 || status.SWRDisplay != "1.37" {
		t.Fatalf("unexpected swr fields: %+v", status)
	}
	if status.AntennaSWR == nil || *status.AntennaSWR != 1.42 || status.AntennaSWRDisplay != "1.42" {
		t.Fatalf("unexpected antenna swr fields: %+v", status)
	}
	if status.PASupplyVoltage == nil || *status.PASupplyVoltage != 48.6 || status.PASupplyVoltageDisplay != "48.6" {
		t.Fatalf("unexpected PA voltage fields: %+v", status)
	}
	if status.PACurrent == nil || *status.PACurrent != 12.5 || status.PACurrentDisplay != "12.5" {
		t.Fatalf("unexpected PA current fields: %+v", status)
	}
	if status.TemperatureC == nil || *status.TemperatureC != 41 || status.TemperatureDisplay != "41 C" {
		t.Fatalf("unexpected upper temperature fields: %+v", status)
	}
	if status.TemperatureLowerC == nil || *status.TemperatureLowerC != 37 || status.TemperatureLowerDisplay != "37 C" {
		t.Fatalf("unexpected lower temperature fields: %+v", status)
	}
	if status.TemperatureCombinerC == nil || *status.TemperatureCombinerC != 39 || status.TemperatureCombinerDisplay != "39 C" {
		t.Fatalf("unexpected combiner temperature fields: %+v", status)
	}
}

func TestNormalizeStatusFieldsOnlyAcceptsObservedEdgeEmptyCase(t *testing.T) {
	observed := []string{"", "13K", "S", "R", "A", "2", "05", "4b", "0r", "L", "0000", " 0.00", " 0.00", " 0.0", " 0.0", " 25", "000", "000", "N", "N", ""}
	normalized := normalizeStatusFields(observed)
	if len(normalized) != 19 || normalized[0] != "13K" || normalized[len(normalized)-1] != "N" {
		t.Fatalf("normalizeStatusFields(observed) = %#v", normalized)
	}

	notObserved := []string{"", "13K", "S", "R", "A", "2", "05", "4b", "0r", "L", "0000", " 0.00", " 0.00", " 0.0", " 0.0", " 25", "000", "000", "N", "N", "x"}
	normalized = normalizeStatusFields(notObserved)
	if len(normalized) != len(notObserved) {
		t.Fatalf("normalizeStatusFields modified unexpected shape: %#v", normalized)
	}
}

func TestStatusModelNameFromIdentifierIncludes15K(t *testing.T) {
	if got := modelNameFromIdentifier("15K"); got != "EXPERT 1.5K-FA" {
		t.Fatalf("modelNameFromIdentifier(15K) = %q, want EXPERT 1.5K-FA", got)
	}
}
