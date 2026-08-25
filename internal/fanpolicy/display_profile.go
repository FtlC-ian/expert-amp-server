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
		(normalizedDisplayRow(rows[6]) != "IN BAND ANT BNK CAT OUT SWR TEMP" &&
			normalizedDisplayRow(rows[6]) != "IN BAND ANT CAT OUT SWR TEMP") {
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

func firstSeriesSetupSelectionKey(state display.State) (string, bool) {
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

func normalizedDisplayRow(row string) string {
	return strings.Join(strings.Fields(row), " ")
}

func knownSecondSeriesInputHeader(row string) bool {
	return row == "SETUP OPTIONS vs. INPUT 1" || row == "SETUP OPTIONS vs. INPUT 2"
}

func secondSeriesSetupSelectionKey(state display.State) (string, bool) {
	rows := displayRows(state)
	if !knownSecondSeriesInputHeader(normalizedDisplayRow(rows[0])) ||
		normalizedDisplayRow(rows[1]) != "CONFIG DISPLAY ALARMS LOG" ||
		normalizedDisplayRow(rows[2]) != "ANTENNA BEEP Off TUN ANT" && normalizedDisplayRow(rows[2]) != "ANTENNA BEEP On TUN ANT" ||
		normalizedDisplayRow(rows[3]) != "CAT START Stby RX ANT" && normalizedDisplayRow(rows[3]) != "CAT START Oper RX ANT" ||
		normalizedDisplayRow(rows[4]) != "MANUAL TUNE TEMP/FANS EXIT" ||
		normalizedDisplayRow(rows[5]) != "" ||
		!strings.Contains(normalizedDisplayRow(rows[7]), ":SELECT") ||
		!strings.Contains(normalizedDisplayRow(rows[7]), "[SET]:") {
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
		{1, 0, 13, []string{"CONFIG"}, "setup:CONFIG"},
		{2, 0, 13, []string{"ANTENNA"}, "setup:ANTENNA"},
		{3, 0, 13, []string{"CAT"}, "setup:CAT"},
		{4, 0, 13, []string{"MANUAL TUNE"}, "setup:MANUAL TUNE"},
		{1, 14, 28, []string{"DISPLAY"}, "setup:DISPLAY"},
		{2, 14, 28, []string{"BEEP    On", "BEEP    Off"}, "setup:BEEP"},
		{3, 14, 28, []string{"START   Stby", "START   Oper"}, "setup:START"},
		{4, 14, 28, []string{"TEMP/FANS"}, "setup:TEMP/FANS"},
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

func thirdSeriesSetupSelectionKey(state display.State) (string, bool) {
	rows := displayRows(state)
	if !knownSecondSeriesInputHeader(normalizedDisplayRow(rows[0])) ||
		normalizedDisplayRow(rows[1]) != "CONFIG DISPLAY ALARMS LOG" ||
		normalizedDisplayRow(rows[2]) != "ANTENNA BEEP On TUN ANT" ||
		normalizedDisplayRow(rows[3]) != "CAT START Oprt RX ANT" ||
		normalizedDisplayRow(rows[4]) != "MANUAL TUNE TEMP. F FAN NOISE" ||
		normalizedDisplayRow(rows[5]) != "EXIT" ||
		(normalizedDisplayRow(rows[7]) != "[ ][ ]:SELECT [SET]:CONFIRM" && normalizedDisplayRow(rows[7]) != "[ ][ ]:SELECT [SET]:CHANGE") {
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
		legend          string
	}
	expected := []expectedSelection{
		{1, 0, 13, []string{"CONFIG"}, "setup:CONFIG", "[ ][ ]:SELECT [SET]:CONFIRM"},
		{2, 0, 13, []string{"ANTENNA"}, "setup:ANTENNA", "[ ][ ]:SELECT [SET]:CONFIRM"},
		{3, 0, 13, []string{"CAT"}, "setup:CAT", "[ ][ ]:SELECT [SET]:CONFIRM"},
		{4, 0, 13, []string{"MANUAL TUNE"}, "setup:MANUAL TUNE", "[ ][ ]:SELECT [SET]:CONFIRM"},
		{1, 14, 28, []string{"DISPLAY"}, "setup:DISPLAY", "[ ][ ]:SELECT [SET]:CONFIRM"},
		{2, 14, 28, []string{"BEEP    On"}, "setup:BEEP", "[ ][ ]:SELECT [SET]:CHANGE"},
		{3, 14, 28, []string{"START   Oprt"}, "setup:START", "[ ][ ]:SELECT [SET]:CHANGE"},
		{4, 14, 28, []string{"TEMP.    F"}, "setup:TEMP.", "[ ][ ]:SELECT [SET]:CHANGE"},
		{1, 29, 40, []string{"ALARMS LOG"}, "setup:ALARMS LOG", "[ ][ ]:SELECT [SET]:CONFIRM"},
		{2, 29, 40, []string{"TUN ANT"}, "setup:TUN ANT", "[ ][ ]:SELECT [SET]:CONFIRM"},
		{3, 29, 40, []string{"RX  ANT", "RX ANT"}, "setup:RX ANT", "[ ][ ]:SELECT [SET]:CONFIRM"},
		{4, 29, 40, []string{"FAN NOISE"}, "setup:FAN NOISE", "[ ][ ]:SELECT [SET]:CONFIRM"},
	}
	for _, candidate := range expected {
		if selection.row != candidate.row || selection.start != candidate.start || selection.end != candidate.end {
			continue
		}
		if normalizedDisplayRow(rows[7]) != candidate.legend {
			return "", false
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

func setupSelectionKey(state display.State) (string, bool) {
	if key, ok := firstSeriesSetupSelectionKey(state); ok {
		return key, true
	}
	if key, ok := secondSeriesSetupSelectionKey(state); ok {
		return key, true
	}
	return thirdSeriesSetupSelectionKey(state)
}

func setupSelectionKeyForProfile(state display.State, profileID string) (string, bool) {
	switch profileID {
	case FirstSeriesDisplayProfile:
		return firstSeriesSetupSelectionKey(state)
	case SecondSeriesDisplayProfile:
		return secondSeriesSetupSelectionKey(state)
	case ThirdSeriesDisplayProfile:
		return thirdSeriesSetupSelectionKey(state)
	default:
		return "", false
	}
}

func matchesProfileSetupSelection(state display.State, profileID, key string) bool {
	actual, ok := setupSelectionKeyForProfile(state, profileID)
	return ok && actual == key
}

type fanDisplayProfile struct {
	id                              string
	model                           string
	setupKeys                       []string
	supportedModes                  []string
	verified                        bool
	startupSetupDisplayExitVerified bool
	allowedFirmwares                []string
	standbyOnly                     bool
}

var fanDisplayProfiles = []fanDisplayProfile{
	{id: FirstSeriesDisplayProfile, model: "EXPERT 1.3K-FA", setupKeys: []string{"setup:ANTENNA", "setup:CAT", "setup:MANUAL TUNE", "setup:DISPLAY", "setup:BEEP", "setup:START", "setup:TEMP/FANS"}, supportedModes: []string{"normal", "contest"}, verified: true, startupSetupDisplayExitVerified: true},
	{id: SecondSeriesDisplayProfile, model: "EXPERT 1.5K-FA", setupKeys: []string{"setup:CONFIG", "setup:ANTENNA", "setup:CAT", "setup:MANUAL TUNE", "setup:DISPLAY", "setup:BEEP", "setup:START", "setup:TEMP/FANS"}, supportedModes: []string{"normal", "contest"}, verified: true, startupSetupDisplayExitVerified: true},
	{id: ThirdSeriesDisplayProfile, model: "EXPERT 2K-FA", setupKeys: []string{"setup:CONFIG", "setup:ANTENNA", "setup:CAT", "setup:MANUAL TUNE", "setup:DISPLAY", "setup:BEEP", "setup:START", "setup:TEMP.", "setup:ALARMS LOG", "setup:TUN ANT", "setup:RX ANT", "setup:FAN NOISE"}, supportedModes: []string{"normal", "contest"}, verified: true, allowedFirmwares: []string{ThirdSeriesEvidenceFirmware, ThirdSeriesCurrentFirmware}, standbyOnly: true},
}

func fanDisplayProfileByID(id string) (fanDisplayProfile, bool) {
	for _, profile := range fanDisplayProfiles {
		if profile.id == id {
			return profile, true
		}
	}
	return fanDisplayProfile{}, false
}

func fanDisplayProfileForModel(model, firmware string) (fanDisplayProfile, bool) {
	for _, profile := range fanDisplayProfiles {
		if strings.EqualFold(strings.TrimSpace(model), profile.model) && firmwareAllowed(profile.allowedFirmwares, firmware) {
			return profile, true
		}
	}
	return fanDisplayProfile{}, false
}

func firmwareAllowed(allowlist []string, firmware string) bool {
	if len(allowlist) == 0 {
		return true
	}
	actual := strings.TrimSpace(firmware)
	for _, allowed := range allowlist {
		if actual == allowed {
			return true
		}
	}
	return false
}

func verifiedFanDisplayProfileForModel(model, firmware string) (fanDisplayProfile, bool) {
	profile, ok := fanDisplayProfileForModel(model, firmware)
	return profile, ok && profile.verified
}

func supportedFanModesForModel(model, firmware string) []string {
	profile, ok := verifiedFanDisplayProfileForModel(model, firmware)
	if !ok {
		return []string{}
	}
	return append([]string(nil), profile.supportedModes...)
}

func identifyFanDisplayProfile(state display.State, model, firmware string) (fanDisplayProfile, string, bool) {
	profile, ok := verifiedFanDisplayProfileForModel(model, firmware)
	if !ok || len(profile.setupKeys) == 0 {
		return fanDisplayProfile{}, "", false
	}
	key, ok := setupSelectionKeyForProfile(state, profile.id)
	if ok && key == profile.setupKeys[0] {
		return profile, key, true
	}
	return fanDisplayProfile{}, "", false
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

// NormalContestFanScreen returns the selected waypoint and policy only when
// the display exactly matches the reviewed NORMAL/CONTEST fan-menu topology,
// including its single highlighted selection geometry. Model-specific setup
// navigation remains outside this matcher.
func NormalContestFanScreen(state display.State) (selected, policy string, ok bool) {
	for _, candidate := range []string{"TEMPERATURE SCALE", "FAN MANAGEMENT", "SAVE"} {
		if value, matches := submenuPolicy(state, candidate); matches {
			return candidate, value, true
		}
	}
	return "", PolicyUnknown, false
}

// ThirdSeriesFanScreen recognizes only the exact 2K-FA NORMAL/QUIET radio
// page. Hardware QUIET maps to logical normal cooling; hardware NORMAL maps
// to logical high cooling. The raw 0xAE marker, not decoded text, is the
// authority for the active hardware value.
func ThirdSeriesFanScreen(state display.State) (selected, activePolicy string, ok bool) {
	rows := displayRows(state)
	if normalizedDisplayRow(rows[0]) != "POWER-SUPPLY FAN" ||
		normalizedDisplayRow(rows[2]) != "[ ] QUIET MODE (SSB ONLY)" ||
		normalizedDisplayRow(rows[3]) != "[ ] NORMAL MODE (ALL MODES) SAVE" ||
		normalizedDisplayRow(rows[7]) != "[ ][ ]:SELECT [SET]:CONFIRM" {
		return "", PolicyUnknown, false
	}
	quietActive := state.Chars[2][4] == 0xae
	normalActive := state.Chars[3][4] == 0xae
	if quietActive == normalActive {
		return "", PolicyUnknown, false
	}
	selection, selectionOK := singleHighlightedSelection(state)
	if !selectionOK {
		return "", PolicyUnknown, false
	}
	switch {
	case selection.row == 2 && selection.start == 2 && selection.end == 31 && strings.Contains(selection.text, "QUIET"):
		selected = "quiet"
	case selection.row == 3 && selection.start == 2 && selection.end == 31 && strings.Contains(selection.text, "NORMAL"):
		selected = "hardware-normal"
	case selection.row == 3 && selection.start == 32 && selection.end == 38 && selection.text == "SAVE":
		selected = "save"
	default:
		return "", PolicyUnknown, false
	}
	if quietActive {
		return selected, PolicyNormal, true
	}
	return selected, PolicyHigh, true
}

func thirdSeriesSelectionForPolicy(policy string) string {
	if policy == PolicyNormal {
		return "quiet"
	}
	if policy == PolicyHigh {
		return "hardware-normal"
	}
	return ""
}

func nextThirdSeriesSelection(selected string) string {
	switch selected {
	case "quiet":
		return "hardware-normal"
	case "hardware-normal":
		return "save"
	case "save":
		return "quiet"
	default:
		return ""
	}
}

func matchesThirdSeriesStoring(state display.State) bool {
	rows := displayRows(state)
	return normalizedDisplayRow(rows[0]) == "POWER-SUPPLY FAN" &&
		normalizedDisplayRow(rows[2]) == "STORING DATA!" &&
		normalizedDisplayRow(rows[6]) == "SAVE SETTINGS AND EXIT" &&
		normalizedDisplayRow(rows[7]) == "[ ][ ]:SELECT [SET]:CONFIRM"
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
	if selected, policy, ok := ThirdSeriesFanScreen(state); ok {
		return "third-fan:" + selected + ":" + policy
	}
	for _, selected := range []string{"TEMPERATURE SCALE", "FAN MANAGEMENT", "SAVE"} {
		if policy, ok := submenuPolicy(state, selected); ok {
			return "submenu:" + selected + ":" + policy
		}
	}
	if matchesStoring(state) {
		return "submenu:STORING"
	}
	if matchesThirdSeriesStoring(state) {
		return "third-fan:STORING"
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
