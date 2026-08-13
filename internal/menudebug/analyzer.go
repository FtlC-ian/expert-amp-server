package menudebug

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

type ScreenKind string

const (
	ScreenUnknown ScreenKind = "unknown"
	ScreenSetup   ScreenKind = "setup-menu"
	ScreenFan     ScreenKind = "fan-menu"
	ScreenBank    ScreenKind = "bank-menu"
	ScreenStoring ScreenKind = "storing"
	ScreenHome    ScreenKind = "home"
)

type ScreenObservation struct {
	SchemaVersion string     `json:"schemaVersion"`
	Kind          ScreenKind `json:"kind"`
	Capability    string     `json:"capability,omitempty"`
	Rows          []string   `json:"rows"`
	SelectedText  string     `json:"selectedText,omitempty"`
	Values        []string   `json:"values,omitempty"`
	SelectedValue string     `json:"selectedValue,omitempty"`
	ActiveValue   string     `json:"activeValue,omitempty"`
	SaveVisible   bool       `json:"saveVisible"`
	Fingerprint   string     `json:"fingerprint"`
	CandidateOnly bool       `json:"candidateOnly"`
	SetupTopology string     `json:"-"`
}

const (
	SetupTopologyFirstSeries   = "expert-first-series-setup-v1"
	SetupTopologySecondSeries  = "expert-second-series-setup-v1"
	SetupTopologyThirdSeries2K = "expert-2k-fa-third-series-setup-v1"
)

var bankValuePattern = regexp.MustCompile(`(?i)\bBNK\s+([A-Z0-9]+)\b`)

func Analyze(state display.State) ScreenObservation {
	rows := decodeRows(state)
	selected := selectedText(state, rows)
	joined := strings.ToUpper(strings.Join(rows[:], "\n"))
	result := ScreenObservation{
		SchemaVersion: ReportSchemaVersion,
		Kind:          ScreenUnknown,
		Rows:          append([]string(nil), rows[:]...),
		SelectedText:  selected,
		SaveVisible:   containsWord(joined, "SAVE"),
		CandidateOnly: true,
	}

	switch {
	case strings.Contains(joined, "STORING DATA!") && strings.Contains(joined, "SAVE SETTINGS AND EXIT"):
		result.Kind = ScreenStoring
	case strings.Contains(joined, "SETUP OPTIONS"):
		result.Kind = ScreenSetup
		result.SetupTopology = setupTopology(rows)
		if strings.Contains(joined, "FAN") || strings.Contains(joined, "TEMP") {
			result.Capability = "fan"
		}
		if strings.Contains(joined, "BANK") {
			if result.Capability == "" {
				result.Capability = "bank"
			} else {
				result.Capability = "fan,bank"
			}
		}
	case strings.Contains(joined, "STORAGE MANAGEMENT") && (strings.Contains(joined, "MEMORY BANK") || strings.Contains(joined, "SAVE SETTINGS AND EXIT")):
		result.Kind = ScreenBank
		result.Capability = "bank"
		result.Values = bankValues(rows)
		result.SelectedValue = selectedBankValue(selected)
		result.ActiveValue = activeBankValue(state, rows)
	case isFanScreen(joined):
		result.Kind = ScreenFan
		result.Capability = "fan"
		result.Values = knownFanValues(joined)
		result.SelectedValue = selectedFanValue(state, rows, selected, joined)
		result.ActiveValue = activeFanValue(state, rows)
	case isHomeHeader(rows[6]):
		result.Kind = ScreenHome
		result.ActiveValue = homeBankValue(rows)
	}

	result.Fingerprint = fingerprintState(state, result)
	return result
}

func setupTopology(rows [display.Rows]string) string {
	normalized := func(row int) string { return strings.Join(strings.Fields(rows[row]), " ") }
	if knownSetupInputHeader(normalized(0)) &&
		normalized(1) == "CONFIG DISPLAY ALARMS LOG" &&
		(normalized(2) == "ANTENNA BEEP Off TUN ANT" || normalized(2) == "ANTENNA BEEP On TUN ANT") &&
		(normalized(3) == "CAT START Stby RX ANT" || normalized(3) == "CAT START Oper RX ANT") &&
		normalized(4) == "MANUAL TUNE TEMP/FANS EXIT" {
		return SetupTopologySecondSeries
	}
	if knownSetupInputHeader(normalized(0)) &&
		normalized(1) == "CONFIG DISPLAY ALARMS LOG" &&
		(normalized(2) == "ANTENNA BEEP Off TUN ANT" || normalized(2) == "ANTENNA BEEP On TUN ANT") &&
		(normalized(3) == "CAT START Stby RX ANT" || normalized(3) == "CAT START Oper RX ANT" || normalized(3) == "CAT START Oprt RX ANT") &&
		thirdSeriesTemperatureFanRow(normalized(4)) &&
		normalized(5) == "EXIT" {
		return SetupTopologyThirdSeries2K
	}
	if strings.HasPrefix(normalized(0), "SETUP OPTIONS vs. INPUT ") &&
		normalized(3) == "MANUAL TUNE TEMP/FANS BANK" &&
		strings.HasPrefix(normalized(1), "ANTENNA BEEP ") && strings.HasSuffix(normalized(1), "TUN ANT") &&
		strings.HasPrefix(normalized(2), "CAT START ") && strings.HasSuffix(normalized(2), "RX ANT") &&
		normalized(4) == "DISPLAY ALARMS LOG EXIT" {
		return SetupTopologyFirstSeries
	}
	return ""
}

func knownSetupInputHeader(row string) bool {
	return row == "SETUP OPTIONS vs. INPUT 1" || row == "SETUP OPTIONS vs. INPUT 2"
}

func thirdSeriesTemperatureFanRow(row string) bool {
	fields := strings.Fields(row)
	if len(fields) != 6 || fields[0] != "MANUAL" || fields[1] != "TUNE" || fields[2] != "TEMP." || fields[4] != "FAN" || fields[5] != "NOISE" {
		return false
	}
	return fields[3] == "C" || fields[3] == "F" || fields[3] == "°C" || fields[3] == "°F"
}

func isHomeHeader(row string) bool {
	fields := strings.Fields(strings.ToUpper(row))
	for _, expected := range [][]string{
		{"IN", "BAND", "ANT", "BNK", "CAT", "OUT", "SWR", "TEMP"},
		{"IN", "BAND", "ANT", "CAT", "OUT", "SWR", "TEMP"},
	} {
		if len(fields) != len(expected) {
			continue
		}
		matched := true
		for i := range expected {
			if fields[i] != expected[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func isFanScreen(joined string) bool {
	if strings.Contains(joined, "FAN") && (strings.Contains(joined, "TEMPERATURE") || strings.Contains(joined, "TEMP/")) {
		return true
	}
	if strings.Contains(joined, "POWER-SUPPLY FAN") &&
		strings.Contains(joined, "QUIET") &&
		strings.Contains(joined, "NORMAL") &&
		containsWord(joined, "SAVE") {
		return true
	}
	return containsWord(joined, "FAN") &&
		containsWord(joined, "SAVE") &&
		strings.Contains(joined, ":SELECT") &&
		strings.Contains(joined, "[SET]:")
}

func decodeRows(state display.State) [display.Rows]string {
	var rows [display.Rows]string
	for row := 0; row < display.Rows; row++ {
		decoded := make([]rune, display.Cols)
		for col := 0; col < display.Cols; col++ {
			value := state.Chars[row][col]
			if state.Attrs[row][col]&0x80 != 0 {
				value -= 0x20
			}
			if value >= 0x01 && value <= 0x5e {
				decoded[col] = rune(value + 0x20)
			} else {
				decoded[col] = ' '
			}
		}
		rows[row] = string(decoded)
	}
	return rows
}

func selectedText(state display.State, rows [display.Rows]string) string {
	var selected []string
	for row := 0; row < display.Rows; row++ {
		for start := 0; start < display.Cols; {
			if state.Attrs[row][start]&0x7f == 0 {
				start++
				continue
			}
			end := start + 1
			for end < display.Cols && state.Attrs[row][end]&0x7f != 0 {
				end++
			}
			if value := strings.TrimSpace(rows[row][start:end]); value != "" {
				selected = append(selected, value)
			}
			start = end
		}
	}
	return strings.Join(selected, "\n")
}

func bankValues(rows [display.Rows]string) []string {
	seen := map[string]struct{}{}
	for _, row := range rows {
		for _, match := range bankValuePattern.FindAllStringSubmatch(row, -1) {
			seen[strings.ToUpper(match[1])] = struct{}{}
		}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func selectedBankValue(selected string) string {
	match := bankValuePattern.FindStringSubmatch(selected)
	if len(match) != 2 {
		return ""
	}
	return strings.ToUpper(match[1])
}

// activeBankValue decodes the model's non-ASCII marker between '[' and ']'.
// A blank marker is the protocol blank sentinel; any other raw glyph marks the
// currently active bank. Multiple markers are rejected as ambiguous.
func activeBankValue(state display.State, rows [display.Rows]string) string {
	active := ""
	for row, text := range rows {
		for _, match := range bankValuePattern.FindAllStringSubmatchIndex(text, -1) {
			if len(match) != 4 || match[0] < 3 {
				continue
			}
			markerCol := match[0] - 3
			if markerCol <= 0 || markerCol+1 >= display.Cols || text[markerCol-1] != '[' || text[markerCol+1] != ']' || state.Chars[row][markerCol] == 0x60 {
				continue
			}
			value := strings.ToUpper(text[match[2]:match[3]])
			if active != "" && active != value {
				return ""
			}
			active = value
		}
	}
	return active
}

func homeBankValue(rows [display.Rows]string) string {
	header := strings.ToUpper(rows[6])
	start := strings.Index(header, "BNK")
	if start < 0 || start >= len(rows[7]) {
		return ""
	}
	end := start + 4
	if end > len(rows[7]) {
		end = len(rows[7])
	}
	fields := strings.Fields(rows[7][start:end])
	if len(fields) != 1 || !regexp.MustCompile(`^[A-Z0-9]+$`).MatchString(strings.ToUpper(fields[0])) {
		return ""
	}
	return strings.ToUpper(fields[0])
}

func knownFanValues(joined string) []string {
	var values []string
	for _, value := range []string{"NORMAL", "CONTEST", "QUIET"} {
		if containsWord(joined, value) {
			values = append(values, strings.ToLower(value))
		}
	}
	return values
}

func selectedFanValue(state display.State, rows [display.Rows]string, selected, joined string) string {
	// Some reviewed screens highlight only the setting label while placing its
	// current value later on the same row. Restrict the fallback to highlighted
	// rows so an unselected NORMAL option cannot mask a selected QUIET option.
	selectedContext := selected
	for row := 0; row < display.Rows; row++ {
		highlighted := false
		for col := 0; col < display.Cols; col++ {
			if state.Attrs[row][col]&0x7f != 0 {
				highlighted = true
				break
			}
		}
		if highlighted {
			selectedContext += "\n" + rows[row]
		}
	}
	upper := strings.ToUpper(selectedContext)
	for _, value := range []string{"NORMAL", "CONTEST", "QUIET"} {
		if containsWord(upper, value) {
			return strings.ToLower(value)
		}
	}
	// Legacy screens show one global current value even when SAVE is selected.
	// Accept that only when the entire page contains exactly one known value.
	values := knownFanValues(joined)
	if len(values) == 1 {
		return values[0]
	}
	return ""
}

// activeFanValue reads the non-ASCII check marker used by the Third Series
// 2K-FA radio group. Text decoding deliberately leaves the marker blank, so
// the raw grid is the only reliable source of the active value.
func activeFanValue(state display.State, rows [display.Rows]string) string {
	active := ""
	for row, text := range rows {
		upper := strings.ToUpper(text)
		for _, candidate := range []string{"QUIET", "NORMAL", "CONTEST"} {
			start := strings.Index(upper, candidate)
			if start < 4 || upper[start-4] != '[' || upper[start-2] != ']' || state.Chars[row][start-3] != 0xae {
				continue
			}
			value := strings.ToLower(candidate)
			if active != "" && active != value {
				return ""
			}
			active = value
		}
	}
	return active
}

func containsWord(text, word string) bool {
	return regexp.MustCompile(`(?i)(^|[^A-Z0-9])` + regexp.QuoteMeta(word) + `([^A-Z0-9]|$)`).MatchString(text)
}

func fingerprintState(state display.State, observation ScreenObservation) string {
	var exact bytes.Buffer
	for row := 0; row < display.Rows; row++ {
		exact.Write(state.Chars[row][:])
		exact.Write(state.Attrs[row][:])
	}
	exact.WriteString("\nKIND=" + string(observation.Kind))
	exact.WriteString("\nCAPABILITY=" + observation.Capability)
	exact.WriteString("\nSELECTED=" + observation.SelectedText)
	exact.WriteString("\nVALUE=" + observation.SelectedValue)
	exact.WriteString("\nVALUES=" + strings.Join(observation.Values, ","))
	if observation.SaveVisible {
		exact.WriteString("\nSAVE=1")
	}
	sum := sha256.Sum256(exact.Bytes())
	return hex.EncodeToString(sum[:])
}
