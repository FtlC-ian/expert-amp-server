package menudebug

import (
	"testing"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

func TestAnalyzeBankMenuDiscoversVariableBankCounts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		banks    []string
		selected int
	}{
		{name: "two banks from live 1.3K layout", banks: []string{"A", "B"}, selected: 0},
		{name: "four banks on another model", banks: []string{"A", "B", "C", "D"}, selected: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := bankMenuState(tc.banks, tc.selected)
			got := Analyze(state)
			if got.Kind != ScreenBank || got.Capability != "bank" {
				t.Fatalf("classification = %q/%q", got.Kind, got.Capability)
			}
			if len(got.Values) != len(tc.banks) {
				t.Fatalf("values = %v, want %v", got.Values, tc.banks)
			}
			if got.SelectedValue != tc.banks[tc.selected] || got.ActiveValue != tc.banks[tc.selected] {
				t.Fatalf("selected = %q, want %q", got.SelectedValue, tc.banks[tc.selected])
			}
			if !got.SaveVisible || got.Fingerprint == "" || !got.CandidateOnly {
				t.Fatalf("unsafe/incomplete observation: %+v", got)
			}
		})
	}
}

func TestAnalyzeFanMenuIsCaseInsensitive(t *testing.T) {
	state := display.NewState()
	state.SetRow(0, "Temperature and Fans")
	state.SetRow(2, "Temperature Scale       C")
	state.SetRow(3, "Fan Management    Contest")
	state.SetRow(4, "Save")
	for col := 0; col < len("Fan Management    Contest"); col++ {
		state.SetAttr(3, col, 1)
	}

	got := Analyze(state)
	if got.Kind != ScreenFan || got.Capability != "fan" || got.SelectedValue != "contest" {
		t.Fatalf("unexpected fan observation: %+v", got)
	}
	if !got.SaveVisible {
		t.Fatal("SAVE was not detected")
	}
}

func TestAnalyzeSetupFindsFanAndBankCandidatesWithoutAuthorizing(t *testing.T) {
	state := display.NewState()
	state.SetRow(0, "SETUP OPTIONS vs. INPUT 1")
	state.SetRow(3, "MANUAL TUNE   TEMP/FANS      BANK")

	got := Analyze(state)
	if got.Kind != ScreenSetup || got.Capability != "fan,bank" || !got.CandidateOnly {
		t.Fatalf("unexpected setup observation: %+v", got)
	}
}

func TestAnalyzeRecognizesFirstSeriesStoringReceipt(t *testing.T) {
	state := display.NewState()
	state.SetRow(0, "TEMPERATURE AND FANS")
	state.SetRow(2, "STORING DATA!")
	state.SetRow(6, "SAVE SETTINGS AND EXIT")
	if got := Analyze(state); got.Kind != ScreenStoring {
		t.Fatalf("kind = %q, want %q", got.Kind, ScreenStoring)
	}
}

func bankMenuState(banks []string, selected int) display.State {
	state := display.NewState()
	state.SetRow(0, "           STORAGE MANAGEMENT")
	for i, bank := range banks {
		row := i + 2
		state.SetRow(row, "        [ ] BNK "+bank)
		if i == selected {
			state.Chars[row][9] = 0xae
			for col := 7; col < 19; col++ {
				state.SetAttr(row, col, 1)
			}
		}
	}
	state.SetRow(6, "    SET MEMORY BANK FOR ANTENNAS/ATU")
	state.SetRow(7, " [  ][  ]:SELECT           [SET]:CHANGE")
	state.SetRow(3, stringWithSave(state, 3))
	return state
}

func TestAnalyzeHomeExtractsActiveBankFromAlignedLCDField(t *testing.T) {
	state := display.NewState()
	state.SetRow(6, "IN  BAND ANT BNK  CAT   OUT   SWR   TEMP")
	state.SetRow(7, " 2   10m  2   B  KENWD  LOW  --.--  26 C")
	got := Analyze(state)
	if got.Kind != ScreenHome || got.ActiveValue != "B" {
		t.Fatalf("home observation = %+v", got)
	}
}

func TestAnalyzeBankSaveConfirmationRetainsActiveBank(t *testing.T) {
	state := bankMenuState([]string{"A", "B"}, 1)
	for row := 0; row < display.Rows; row++ {
		for col := 0; col < display.Cols; col++ {
			state.SetAttr(row, col, 0)
		}
	}
	state.SetRow(6, "         SAVE SETTINGS AND EXIT")
	state.Chars[3][9] = 0xae
	for col := 28; col < 32; col++ {
		state.SetAttr(3, col, 1)
	}
	got := Analyze(state)
	if got.Kind != ScreenBank || got.SelectedText != "SAVE" || got.ActiveValue != "B" || !got.SaveVisible {
		t.Fatalf("save confirmation observation = %+v", got)
	}
}

func stringWithSave(state display.State, row int) string {
	text := "        [ ] BNK B"
	if row == 3 {
		text += "           SAVE"
	}
	return text
}
