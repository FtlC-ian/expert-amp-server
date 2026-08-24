package protocol

import (
	"strconv"
	"strings"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/tempunit"
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

	for label, value := range displayStatusFields(rows[6], rows[7]) {
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
			if parsed, unit, ok := parseTemperature(value); ok {
				telem.TemperatureC = &parsed
				telem.TemperatureUnit = unit
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

type displayLabelSpan struct {
	label string
	start int
	end   int
}

func displayStatusFields(labelRow, valueRow string) map[string]string {
	labels := displayLabelSpans(labelRow)
	values := []rune(valueRow)
	fields := make(map[string]string, len(labels))
	for i, label := range labels {
		start := 0
		if i > 0 {
			start = labelBoundary(labels[i-1], label)
		}
		end := len(values)
		if i+1 < len(labels) {
			end = labelBoundary(label, labels[i+1])
		}
		if start > len(values) {
			start = len(values)
		}
		if end > len(values) {
			end = len(values)
		}
		fields[label.label] = strings.TrimSpace(string(values[start:end]))
	}
	return fields
}

func displayLabelSpans(row string) []displayLabelSpan {
	runes := []rune(row)
	spans := make([]displayLabelSpan, 0, len(strings.Fields(row)))
	for col := 0; col < len(runes); {
		if runes[col] == ' ' {
			col++
			continue
		}
		start := col
		for col < len(runes) && runes[col] != ' ' {
			col++
		}
		spans = append(spans, displayLabelSpan{
			label: strings.ToUpper(string(runes[start:col])),
			start: start,
			end:   col,
		})
	}
	return spans
}

func labelBoundary(left, right displayLabelSpan) int {
	// Values are centered beneath their labels, so split the inter-label gap at
	// its midpoint instead of treating either label's start as the boundary.
	return (left.end + right.start + 1) / 2
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

func parseTemperature(v string) (float64, string, bool) {
	clean := strings.TrimSpace(v)
	if clean == "" || strings.Contains(clean, "--") {
		return 0, "", false
	}
	unit := tempunit.Celsius
	upper := strings.ToUpper(clean)
	switch {
	case strings.HasSuffix(upper, "F"):
		unit = tempunit.Fahrenheit
		clean = strings.TrimSpace(strings.TrimSuffix(clean[:len(clean)-1], "°"))
	case strings.HasSuffix(upper, "C"):
		clean = strings.TrimSpace(strings.TrimSuffix(clean[:len(clean)-1], "°"))
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, "", false
	}
	return tempunit.ToCelsius(f, unit), unit, true
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
