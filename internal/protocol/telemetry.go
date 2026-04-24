package protocol

import (
	"strconv"
	"strings"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

func TelemetryFromDisplayState(state display.State, source string) api.Telemetry {
	rows := stateRows(state)
	telem := api.Telemetry{
		Source:     source,
		Confidence: "display-derived",
		Provenance: "display-frame",
	}

	if model := displayDerivedModelName(rows[1]); model != "" {
		telem.ModelName = model
	}

	if operating := strings.TrimSpace(rows[4]); operating != "" {
		telem.OperatingState = strings.ToLower(operating)
		telem.Mode = telem.OperatingState
		if telem.OperatingState == "standby" {
			telem.TX = boolPtr(false)
		}
	}

	labels := strings.Fields(rows[6])
	values := strings.Fields(rows[7])
	for i, label := range labels {
		label = strings.ToUpper(strings.TrimSpace(label))
		if i >= len(values) {
			break
		}
		value := strings.TrimSpace(values[i])
		if label == "TEMP" && i+1 < len(values) {
			value = strings.TrimSpace(values[i] + " " + values[i+1])
		}
		if value == "" {
			continue
		}
		switch label {
		case "IN":
			telem.Input = value
		case "BAND":
			telem.Band = value
		case "ANT":
			telem.Antenna = value
		case "BNK":
			telem.AntennaBank = value
		case "CAT":
			telem.CATInterface = value
		case "OUT":
			telem.OutputLevel = value
		case "SWR":
			telem.SWRDisplay = value
			if parsed, ok := parseFloatDisplay(value); ok {
				telem.SWR = &parsed
			}
		case "TEMP":
			telem.TemperatureDisplay = value
			if parsed, ok := parseTemperatureC(value); ok {
				telem.TemperatureC = &parsed
			}
		}
	}

	if telem.Frequency == "" {
		telem.Notes = append(telem.Notes, "frequency not exposed in current captured home display frame")
	}
	if telem.PowerWatts == nil {
		telem.Notes = append(telem.Notes, "powerWatts not exposed in current captured home display frame")
	}
	return telem
}

func stateRows(state display.State) []string {
	rows := make([]string, display.Rows)
	for row := 0; row < display.Rows; row++ {
		buf := make([]rune, display.Cols)
		for col := 0; col < display.Cols; col++ {
			buf[col] = textCharFromStateByte(state.Chars[row][col])
		}
		rows[row] = strings.TrimRight(string(buf), " ")
	}
	return rows
}

func parseFloatDisplay(v string) (float64, bool) {
	if strings.TrimSpace(v) == "" || strings.Contains(v, "--") {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func parseTemperatureC(v string) (float64, bool) {
	clean := strings.TrimSpace(strings.TrimSuffix(v, "C"))
	clean = strings.TrimSpace(clean)
	if clean == "" || strings.Contains(clean, "--") {
		return 0, false
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func displayDerivedModelName(row string) string {
	row = strings.TrimSpace(row)
	if row == "" {
		return ""
	}
	upper := strings.ToUpper(row)
	if !strings.HasPrefix(upper, "EXPERT ") {
		return ""
	}
	if !strings.Contains(upper, "K") {
		return ""
	}
	return row
}

func textCharFromStateByte(v byte) rune {
	if v == 0x60 {
		return ' '
	}
	return DecodeTextChar(v)
}

func boolPtr(v bool) *bool { return &v }
