package fanpolicy

import (
	"strings"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

func displayRows(state display.State) [display.Rows]string {
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

func selectedText(state display.State) string {
	rows := displayRows(state)
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
			if text := strings.TrimSpace(rows[row][start:end]); text != "" {
				selected = append(selected, text)
			}
			start = end
		}
	}
	return strings.Join(selected, "\n")
}

type highlightedSelection struct {
	row   int
	start int
	end   int
	text  string
}

func singleHighlightedSelection(state display.State) (highlightedSelection, bool) {
	rows := displayRows(state)
	var found []highlightedSelection
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
			if text := strings.TrimSpace(rows[row][start:end]); text != "" {
				found = append(found, highlightedSelection{row: row, start: start, end: end, text: text})
			}
			start = end
		}
	}
	if len(found) != 1 {
		return highlightedSelection{}, false
	}
	return found[0], true
}

func matchesHome(state display.State) bool {
	return matchesStandbyHome(state) || matchesOperateHome(state)
}

func matchesStandbyHome(state display.State) bool {
	rows := displayRows(state)
	if selectedText(state) != "" ||
		strings.TrimSpace(rows[6]) != "IN  BAND ANT BNK  CAT   OUT   SWR   TEMP" {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(rows[1]), "EXPERT ") &&
		strings.TrimSpace(rows[4]) == "Standby" &&
		strings.TrimSpace(rows[2]) == "Solid State" &&
		strings.TrimSpace(rows[3]) == "Fully Automatic"
}

func matchesOperateHome(state display.State) bool {
	rows := displayRows(state)
	if selectedText(state) != "" ||
		strings.TrimSpace(rows[6]) != "IN  BAND ANT BNK  CAT   OUT   SWR   TEMP" {
		return false
	}
	return strings.TrimSpace(rows[0]) == "0   125  250  375  500" &&
		strings.HasPrefix(strings.TrimSpace(rows[1]), "PA OUT") &&
		strings.HasSuffix(strings.TrimSpace(rows[1]), "W pep") &&
		strings.TrimSpace(rows[2]) == "0  12.5  25  37.5  50" &&
		strings.HasPrefix(strings.TrimSpace(rows[3]), "I PA") &&
		strings.HasSuffix(strings.TrimSpace(rows[3]), "A") &&
		strings.TrimSpace(rows[4]) == "" &&
		strings.TrimSpace(rows[5]) == ""
}

func setupSelectionKey(state display.State) (string, bool) {
	rows := displayRows(state)
	if !strings.HasPrefix(strings.TrimSpace(rows[0]), "SETUP OPTIONS vs. INPUT ") ||
		strings.TrimSpace(rows[3]) != "MANUAL TUNE   TEMP/FANS      BANK" {
		return "", false
	}
	selection, ok := singleHighlightedSelection(state)
	if !ok {
		return "", false
	}
	type expectedSelection struct {
		row, start, end int
		values          []string
		key             string
	}
	expected := []expectedSelection{
		{1, 0, 13, []string{"ANTENNA"}, "setup:ANTENNA"},
		{2, 0, 13, []string{"CAT"}, "setup:CAT"},
		{3, 0, 13, []string{"MANUAL TUNE"}, "setup:MANUAL TUNE"},
		{4, 0, 13, []string{"DISPLAY"}, "setup:DISPLAY"},
		{1, 14, 28, []string{"BEEP    On", "BEEP    Off"}, "setup:BEEP"},
		{2, 14, 28, []string{"START   Stby", "START   Oper"}, "setup:START"},
		{3, 14, 28, []string{"TEMP/FANS"}, "setup:TEMP/FANS"},
	}
	for _, candidate := range expected {
		if selection.row != candidate.row || selection.start != candidate.start || selection.end != candidate.end {
			continue
		}
		for _, value := range candidate.values {
			if selection.text == value {
				return candidate.key, true
			}
		}
		return "", false
	}
	return "", false
}

func matchesSetupSelection(state display.State, key string) bool {
	actual, ok := setupSelectionKey(state)
	return ok && actual == key
}

func matchesSubmenuSelection(state display.State, selected string) bool {
	rows := displayRows(state)
	return strings.TrimSpace(rows[0]) == "TEMPERATURE AND FANS" &&
		strings.HasPrefix(strings.TrimSpace(rows[2]), "TEMPERATURE SCALE") &&
		strings.HasPrefix(strings.TrimSpace(rows[3]), "FAN MANAGEMENT") &&
		strings.TrimSpace(rows[4]) == "SAVE" &&
		selectedText(state) == selected
}

func submenuPolicy(state display.State, selected string) (string, bool) {
	if !matchesSubmenuSelection(state, selected) {
		return PolicyUnknown, false
	}
	row := strings.TrimSpace(displayRows(state)[3])
	switch {
	case strings.HasSuffix(row, "NORMAL"):
		return PolicyNormal, true
	case strings.HasSuffix(row, "CONTEST"):
		return PolicyHigh, true
	default:
		return PolicyUnknown, false
	}
}

func matchesStoring(state display.State) bool {
	rows := displayRows(state)
	return strings.TrimSpace(rows[0]) == "TEMPERATURE AND FANS" &&
		strings.TrimSpace(rows[2]) == "STORING DATA!" &&
		strings.TrimSpace(rows[6]) == "SAVE SETTINGS AND EXIT"
}

func semanticDisplayKey(state display.State) string {
	if matchesHome(state) {
		return "home"
	}
	if key, ok := setupSelectionKey(state); ok {
		return key
	}
	for _, selected := range []string{"TEMPERATURE SCALE", "FAN MANAGEMENT", "SAVE"} {
		if policy, ok := submenuPolicy(state, selected); ok {
			return "submenu:" + selected + ":" + policy
		}
	}
	if matchesStoring(state) {
		return "submenu:STORING"
	}
	return ""
}

func firstNonBlankRow(state display.State) string {
	for _, row := range displayRows(state) {
		if text := strings.TrimSpace(row); text != "" {
			return text
		}
	}
	return ""
}
