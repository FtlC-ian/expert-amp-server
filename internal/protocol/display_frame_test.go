package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "fixtures", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestGuessDisplayStart(t *testing.T) {
	// GuessDisplayStart is a text-scoring heuristic used only for diagnostics.
	// It is NOT used by StateFromFrame, which uses LCDDataOffset instead. These
	// tests just document what the heuristic returns so regressions are caught.
	//
	// Note: the heuristic returns 56 for real_home_status_frame.bin because its
	// row 0 is all custom border glyphs (0x80–0xDF) which score as spaces and
	// trigger the firstNonEmpty penalty. That is a heuristic flaw, but it no
	// longer causes a rendering bug since StateFromFrame uses the fixed offset.
	cases := []struct {
		name string
		want int
	}{
		// Home frame: heuristic incorrectly scores offset 56 higher than 9 because
		// the all-border-glyph row 0 looks "empty" to the text scorer. StateFromFrame
		// bypasses this heuristic and uses LCDDataOffset.
		{name: "real_home_status_frame.bin", want: 56},
		{name: "real_menu_frame.bin", want: 8},
		{name: "real_panel_frame.bin", want: 8},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GuessDisplayStart(loadFixture(t, tc.name))
			if got != tc.want {
				t.Fatalf("GuessDisplayStart(%s) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// TestStateFromFrameUsesFixedOffset verifies that StateFromFrame always decodes
// from LCDDataOffset, not from the heuristic GuessDisplayStart.
//
// Frame layout: [8-byte prefix][1-byte frame-type discriminator][320-byte body]
// The display body starts at byte 9. Using 8 instead caused the frame-type
// discriminator byte to be decoded as the first display cell, blanking the
// top-left corner (home: discriminator=0xF8 → blank) or inserting a stray
// ornament glyph (menu/panel: discriminator=0xD8 → custom glyph), shifting
// every subsequent cell one position to the right within its row.
//
// Fix: StateFromFrame uses LCDDataOffset (=9 for current 371-byte GetLCD captures
// and legacy short captures).
func TestGetLCDResponseLayoutParsesFlagsDataAndChecksum(t *testing.T) {
	cases := []struct {
		name        string
		rawInverted uint16
		decoded     uint16
		checksumOK  bool
	}{
		{name: "real_home_status_frame.bin", rawInverted: 0xf801, decoded: 0x07fe, checksumOK: true},
		{name: "sample_display_frame.bin", rawInverted: 0x3301, decoded: 0xccfe, checksumOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := loadFixture(t, tc.name)
			assertGetLCDFrameDiagnostics(t, frame, tc.name, tc.rawInverted, tc.decoded, tc.checksumOK)
		})
	}
}

func TestGetLCDResponseDiagnosticsTolerateTrailingStatusBytes(t *testing.T) {
	frame := loadFixture(t, "real_home_status_frame.bin")
	// Live serial chunks can contain the 371-byte GetLCD response followed by a
	// 76-byte status response before the next display prefix. Display parsing and
	// flag checks must use only the leading GetLCD response and ignore the tail.
	frame = append(append([]byte(nil), frame...), make([]byte, 76)...)

	assertGetLCDFrameDiagnostics(t, frame, "real_home_status_frame.bin+status-tail", 0xf801, 0x07fe, true)
	text, err := ScreenText(frame)
	if err != nil {
		t.Fatalf("ScreenText: %v", err)
	}
	if !strings.Contains(text, "EXPERT 1.3K-FA") || !strings.Contains(text, "Standby") {
		t.Fatalf("ScreenText missing expected display text after trailing bytes:\n%s", text)
	}
}

func assertGetLCDFrameDiagnostics(t *testing.T, frame []byte, name string, rawInverted, decoded uint16, checksumOK bool) {
	t.Helper()
	if !IsGetLCDResponseFrame(frame) {
		t.Fatalf("IsGetLCDResponseFrame(%s) = false, want true", name)
	}
	if got := LCDDataOffset(frame); got != 9 {
		t.Fatalf("LCDDataOffset(%s) = %d, want 9", name, got)
	}
	flags, ok := LCDFlagsFromFrame(frame)
	if !ok || flags == nil {
		t.Fatalf("LCDFlagsFromFrame(%s) missing", name)
	}
	if flags.RawInverted != rawInverted || flags.Decoded != decoded {
		t.Fatalf("flags = raw 0x%04x decoded 0x%04x, want raw 0x%04x decoded 0x%04x", flags.RawInverted, flags.Decoded, rawInverted, decoded)
	}
	if !flags.ChecksumPresent {
		t.Fatal("ChecksumPresent = false, want true")
	}
	if flags.ChecksumValid != checksumOK {
		t.Fatalf("ChecksumValid = %v, want %v", flags.ChecksumValid, checksumOK)
	}
	if checksumOK && flags.LEDs == nil {
		t.Fatalf("LEDs missing for checksum-valid %s", name)
	}
	if !checksumOK && flags.LEDs != nil {
		t.Fatalf("LEDs = %+v for checksum-invalid %s, want nil", flags.LEDs, name)
	}
}

func TestLCDFlagLEDDecodeUsesChecksumValidDecodedBits(t *testing.T) {
	if got := DecodeLCDLEDs(0x07fe, true); got == nil || got.TX || got.Operate || got.Set || got.Tune {
		t.Fatalf("standby LEDs = %+v, want all false", got)
	}
	if got := DecodeLCDLEDs(0x17fe, true); got == nil || !got.Operate || got.TX || got.Set || got.Tune {
		t.Fatalf("operate LEDs = %+v, want operate only", got)
	}
	if got := DecodeLCDLEDs(0x27fe, true); got == nil || !got.Set || got.TX || got.Operate || got.Tune {
		t.Fatalf("set LEDs = %+v, want set only", got)
	}
	if got := DecodeLCDLEDs(0x47fe, true); got == nil || !got.Tune || got.TX || got.Operate || got.Set {
		t.Fatalf("tune LEDs = %+v, want tune only", got)
	}
	if got := DecodeLCDLEDs(0x7ffe, false); got != nil {
		t.Fatalf("checksum-invalid LEDs = %+v, want nil", got)
	}
}

func TestLegacyShortDisplayFramesDoNotExposeLCDFlags(t *testing.T) {
	for _, name := range []string{"real_menu_frame.bin", "real_panel_frame.bin"} {
		t.Run(name, func(t *testing.T) {
			frame := loadFixture(t, name)
			if IsGetLCDResponseFrame(frame) {
				t.Fatalf("IsGetLCDResponseFrame(%s) = true, want false", name)
			}
			if got := LCDDataOffset(frame); got != displayBodyOffset {
				t.Fatalf("LCDDataOffset(%s) = %d, want %d", name, got, displayBodyOffset)
			}
			if flags, ok := LCDFlagsFromFrame(frame); ok || flags != nil {
				t.Fatalf("LCDFlagsFromFrame(%s) = %+v, %v; want nil, false", name, flags, ok)
			}
		})
	}
}

func TestStateFromFrameUsesFixedOffset(t *testing.T) {
	cases := []struct {
		name string
		// row0HasCustom is true if row 0 at offset 9 contains custom glyphs (0x80–0xDF),
		// which confirms the border row is being decoded (not skipped to offset 56).
		row0HasCustom bool
		// key text expected to appear on specific rows at offset 9
		row1ContainsText string // empty means skip
	}{
		// Home frame: row 0 = top border (all custom glyphs), row 1 = sidebar + "EXPERT"
		{name: "real_home_status_frame.bin", row0HasCustom: true, row1ContainsText: "EXPERT"},
		// Menu frame: row 0 = top border + text, still starts at offset 9
		{name: "real_menu_frame.bin", row0HasCustom: true},
		// Panel frame: same
		{name: "real_panel_frame.bin", row0HasCustom: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := loadFixture(t, tc.name)
			state, err := StateFromFrame(frame)
			if err != nil {
				t.Fatalf("StateFromFrame: %v", err)
			}
			if tc.row0HasCustom {
				foundCustom := false
				for col := 0; col < 40; col++ {
					if state.Chars[0][col] >= 0x80 && state.Chars[0][col] <= 0xdf {
						foundCustom = true
						break
					}
				}
				if !foundCustom {
					t.Errorf("row 0 contains no custom glyphs: offset is likely wrong (expected border row at offset 8)")
				}
			}
			if tc.row1ContainsText != "" {
				// Use ScreenText to decode row 1: state.Chars holds ROM indices
				// (protocol bytes), not ASCII values, so direct byte comparison
				// is wrong after the decode-mapping fix.
				screenText, _ := ScreenText(loadFixture(t, tc.name))
				rows := strings.Split(screenText, "\n")
				row1 := ""
				if len(rows) > 1 {
					row1 = rows[1]
				}
				if !strings.Contains(row1, tc.row1ContainsText) {
					t.Errorf("row 1 = %q, want it to contain %q (offset likely wrong)", row1, tc.row1ContainsText)
				}
			}
		})
	}
}

// TestStateFromFrameLoadsPackedAttrBitplaneWhenPresent verifies column-major
// attribute decoding. The 40-byte attr trailer uses one byte per column; bit r
// (LSB = row 0) marks cell (row=r, col) as highlighted.
//
// Earlier code treated the trailer as a flat row-major bitstream which scattered
// highlights to wrong positions (e.g. selecting ANTENNA on row 1 caused visible
// inversion on scattered columns including the last 'N' only — not the full word).
func TestStateFromFrameLoadsPackedAttrBitplaneWhenPresent(t *testing.T) {
	// Frame: prefix + discriminator + 320-byte body + 40-byte col-major attr trailer.
	frame := make([]byte, displayBodyOffset+display.Rows*display.Cols+display.Cols)
	copy(frame[:len(RadioDisplayPrefix)], RadioDisplayPrefix)
	frame[len(RadioDisplayPrefix)] = 0xD8
	attrStart := displayBodyOffset + display.Rows*display.Cols

	// Mark cell (row=1, col=5) and cell (row=4, col=17) as highlighted.
	// Column-major encoding: attr[col] |= 1 << row.
	frame[attrStart+5] |= 1 << 1  // col=5, row=1
	frame[attrStart+17] |= 1 << 4 // col=17, row=4

	state, err := StateFromFrame(frame)
	if err != nil {
		t.Fatalf("StateFromFrame: %v", err)
	}
	if state.Attrs[1][5] != 0x01 || state.Attrs[4][17] != 0x01 {
		t.Fatalf("packed attrs not loaded: got[1][5]=0x%02x got[4][17]=0x%02x", state.Attrs[1][5], state.Attrs[4][17])
	}
	// Verify no smearing: adjacent rows at same column must be clear.
	if state.Attrs[0][5] != 0x00 || state.Attrs[2][5] != 0x00 {
		t.Fatalf("highlight smeared into other rows: row0=0x%02x row2=0x%02x", state.Attrs[0][5], state.Attrs[2][5])
	}
	// Verify no smearing across adjacent columns at same row.
	if state.Attrs[1][4] != 0x00 || state.Attrs[1][6] != 0x00 {
		t.Fatalf("highlight smeared into adjacent cols: col4=0x%02x col6=0x%02x", state.Attrs[1][4], state.Attrs[1][6])
	}
}

// TestDecodeDisplayCharROMMapping verifies DecodeDisplayChar against the
// bundled SPE-style LCD font table.
//
// The protocol byte IS the ROM index. No +0x20 shift is applied.
// An earlier version applied v+0x20 to the 0x01–0x5F range, which caused every
// character to index the wrong glyph (e.g. 0x25 → stored 0x45 → ROM[0x45] =
// lowercase 'e' instead of ROM[0x25] = uppercase 'E').
//
//   - 0x00       → 0x60 (blank)
//   - 0x01–0x7E  → direct pass-through (ROM index = protocol byte)
//   - 0x80–0xDF  → pass through (custom glyphs: borders, meters, ornaments)
//   - 0xE0–0xFF  → 0x60 (blank in ROM)
func TestDecodeDisplayCharROMMapping(t *testing.T) {
	cases := []struct {
		name string
		in   byte
		want byte
	}{
		// Blank
		{name: "blank", in: 0x00, want: 0x60},
		// Protocol bytes in 0x01–0x5F: pass through directly (ROM index = protocol byte)
		{name: "digit 1 (0x11)", in: 0x11, want: 0x11},
		{name: "uppercase K (0x2b)", in: 0x2b, want: 0x2b},
		{name: "period (0x0e)", in: 0x0e, want: 0x0e},
		{name: "hyphen (0x0d)", in: 0x0d, want: 0x0d},
		// 0x41 is in range 0x01–0x7E: pass through
		{name: "protocol 0x41 passthrough", in: 0x41, want: 0x41},
		// 0x60 is the blank sentinel slot
		{name: "blank sentinel 0x60", in: 0x60, want: 0x60},
		// Custom glyph range 0x80–0xDF: all pass through
		{name: "custom 0x80 framed block", in: 0x80, want: 0x80},
		{name: "custom 0x8d divider", in: 0x8d, want: 0x8d},
		{name: "custom 0x8e lower tee", in: 0x8e, want: 0x8e},
		{name: "custom 0x8f slim vert", in: 0x8f, want: 0x8f},
		{name: "custom 0xa0 bottom bar", in: 0xa0, want: 0xa0},
		{name: "custom 0xa1 right wall", in: 0xa1, want: 0xa1},
		{name: "sidebar ornament 0xb0", in: 0xb0, want: 0xb0},
		{name: "ROM slot 0xc2", in: 0xc2, want: 0xc2},
		// 0xE0–0xFF: blank in ROM
		{name: "above-ROM 0xe0 -> blank", in: 0xe0, want: 0x60},
		{name: "above-ROM 0xff -> blank", in: 0xff, want: 0x60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecodeDisplayChar(tc.in)
			if got != tc.want {
				t.Fatalf("DecodeDisplayChar(0x%02x) = 0x%02x, want 0x%02x", tc.in, got, tc.want)
			}
		})
	}
}

// TestStateFromFrameNoRenderBlocks verifies that StateFromFrame never stores
// 0x80 (framed-block glyph) for cells whose raw frame byte was NOT 0x80.
// This pins down the sentinel-overwrite bug that was fixed in the blank-sentinel
// commit: the 0x80–0xDF pass-through range means cells get exactly their ROM
// glyph code, not a clobbered sentinel.
func TestStateFromFrameNoRenderBlocks(t *testing.T) {
	frame := loadFixture(t, "real_home_status_frame.bin")
	state, err := StateFromFrame(frame)
	if err != nil {
		t.Fatalf("StateFromFrame: %v", err)
	}
	// Scan every cell. If any cell is 0x80 but the raw frame byte at that
	// position was NOT actually 0x80, that's the bug.
	// Use displayBodyOffset (fixed 9) to map cell positions back to frame bytes.
	blockCount := 0
	for row := 0; row < 8; row++ {
		for col := 0; col < 40; col++ {
			cell := state.Chars[row][col]
			if cell == 0x80 {
				rawIdx := displayBodyOffset + row*40 + col
				if rawIdx < len(frame) && frame[rawIdx] != 0x80 {
					t.Errorf("row %d col %d: raw=0x%02x decoded to 0x80 (framed block sentinel) — should map to blank 0x60", row, col, frame[rawIdx])
					blockCount++
				}
			}
		}
	}
	if blockCount > 0 {
		t.Fatalf("%d spurious framed-block cells found (render would show visible blocks for non-block bytes)", blockCount)
	}
}

func TestScreenTextContainsExpectedLabels(t *testing.T) {
	cases := []struct {
		name     string
		expected []string
	}{
		{
			name:     "real_home_status_frame.bin",
			expected: []string{"EXPERT 1.3K-FA", "Solid State", "Fully Automatic", "Standby"},
		},
		{
			name:     "real_menu_frame.bin",
			expected: []string{"SETUP OPTIONS vs. INPUT 2", "ANTENNA", "MANUAL TUNE", "SET ANTENNAS vs. BANDS"},
		},
		{
			name:     "real_panel_frame.bin",
			expected: []string{"SET CAT INTERFACE ON INPUT 2", "YAESU", "KENWOOD", "SET INTERFACE TYPE"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, err := ScreenText(loadFixture(t, tc.name))
			if err != nil {
				t.Fatalf("ScreenText(%s): %v", tc.name, err)
			}
			for _, fragment := range tc.expected {
				if !strings.Contains(text, fragment) {
					t.Fatalf("ScreenText(%s) missing %q\nfull text:\n%s", tc.name, fragment, text)
				}
			}
		})
	}
}

// TestStateCharsHoldProtocolBytes is a regression test for the +0x20 decode
// mapping bug. An earlier version of DecodeDisplayChar applied v+0x20 to the
// 0x01–0x5F range, storing ASCII values instead of ROM indices. This caused
// every glyph lookup to index the wrong row in the bundled LCD font table —
// e.g. protocol 0x25 ('E') was stored as 0x45, and ROM[0x45] is a lowercase
// 'e' shape, not uppercase 'E'.
//
// After the fix, state.Chars holds the protocol byte directly as the ROM index.
// This test checks that specific known bytes in the home fixture ("EXPERT" row)
// are stored as protocol bytes, not ASCII-shifted values.
func TestStateCharsHoldProtocolBytes(t *testing.T) {
	frame := loadFixture(t, "real_home_status_frame.bin")
	state, err := StateFromFrame(frame)
	if err != nil {
		t.Fatalf("StateFromFrame: %v", err)
	}

	// Row 1 of real_home_status_frame.bin contains the label row.
	// At offset 9 (displayBodyOffset) + 40 (row 1 start) = 49 in the frame.
	// The raw bytes at that row include 0x25 (protocol E), 0x38 (protocol X), etc.
	// After the fix, state.Chars[1][col] should equal the raw frame byte,
	// NOT the raw frame byte + 0x20.
	for col := 0; col < 40; col++ {
		rawIdx := displayBodyOffset + 40 + col
		if rawIdx >= len(frame) {
			break
		}
		raw := frame[rawIdx]
		got := state.Chars[1][col]
		expected := DecodeDisplayChar(raw)
		if got != expected {
			t.Errorf("row 1 col %d: raw=0x%02x state.Chars=0x%02x want DecodeDisplayChar result=0x%02x",
				col, raw, got, expected)
		}
		// Extra check: if raw is in the text range 0x01–0x5F,
		// state.Chars must equal raw (not raw+0x20).
		if raw >= 0x01 && raw <= 0x5f && got == raw+0x20 {
			t.Errorf("row 1 col %d: raw=0x%02x stored as 0x%02x (ASCII-shifted) — old +0x20 bug regression",
				col, raw, got)
		}
	}
}

// TestAttrColumnMajorMenuHighlight is a regression test for the col-major highlight bug.
//
// Symptom: selecting ANTENNA (row 1, cols 1-7 of the setup menu) caused scattered
// vertical highlight columns visible on screen instead of a horizontal inverted bar
// over the menu entry. Selecting CAT (row 2) would shift the scatter left by one.
//
// Root cause: the 40-byte attr trailer uses column-major packing: attr[col] is a
// bitmask where bit r (LSB=row 0) marks cell (row=r, col). The old code treated the
// 40 bytes as a flat row-major bitstream (bit i -> row=i//40, col=i%40), misrouting
// every bit to the wrong cell.
//
// This test encodes a menu selection the correct column-major way and verifies that
// StateFromFrame places highlights only on the intended row, without smearing.
func TestAttrColumnMajorMenuHighlight(t *testing.T) {
	// Build a synthetic frame: prefix + discriminator + 320-byte body + 40-byte attrs.
	frame := make([]byte, displayBodyOffset+display.Rows*display.Cols+display.Cols)
	copy(frame[:len(RadioDisplayPrefix)], RadioDisplayPrefix)
	frame[len(RadioDisplayPrefix)] = 0xD8 // menu discriminator

	attrStart := displayBodyOffset + display.Rows*display.Cols

	// Simulate ANTENNA selected: row 1, cols 1-7 highlighted.
	// Column-major: attr[col] |= 1 << 1 for each highlighted column.
	antennaCols := []int{1, 2, 3, 4, 5, 6, 7}
	for _, col := range antennaCols {
		frame[attrStart+col] |= 1 << 1 // row 1
	}

	state, err := StateFromFrame(frame)
	if err != nil {
		t.Fatalf("StateFromFrame: %v", err)
	}

	// All cols 1-7 on row 1 must be highlighted.
	for _, col := range antennaCols {
		if state.Attrs[1][col] != 0x01 {
			t.Errorf("ANTENNA col %d: Attrs[1][%d]=0x%02x, want 0x01", col, col, state.Attrs[1][col])
		}
	}

	// Row 0 and row 2 must be clear for those columns (no smearing).
	for _, col := range antennaCols {
		if state.Attrs[0][col] != 0x00 {
			t.Errorf("smear into row 0, col %d: Attrs[0][%d]=0x%02x", col, col, state.Attrs[0][col])
		}
		if state.Attrs[2][col] != 0x00 {
			t.Errorf("smear into row 2, col %d: Attrs[2][%d]=0x%02x", col, col, state.Attrs[2][col])
		}
	}

	// The old row-major bug would place a bit from attr[5] bit-1 at flat=46 -> (row=1, col=6).
	// Under the new col-major code attr[5]|=1<<1 puts a highlight at (row=1, col=5) — correct.
	// Verify col 0 (never set) is clear.
	if state.Attrs[1][0] != 0x00 {
		t.Errorf("col 0 row 1 should not be highlighted: got 0x%02x", state.Attrs[1][0])
	}

	// Now simulate CAT selected: row 2, cols 1-3 highlighted.
	frame2 := make([]byte, displayBodyOffset+display.Rows*display.Cols+display.Cols)
	copy(frame2[:len(RadioDisplayPrefix)], RadioDisplayPrefix)
	frame2[len(RadioDisplayPrefix)] = 0xD8
	attrStart2 := displayBodyOffset + display.Rows*display.Cols
	catCols := []int{1, 2, 3}
	for _, col := range catCols {
		frame2[attrStart2+col] |= 1 << 2 // row 2
	}

	state2, err := StateFromFrame(frame2)
	if err != nil {
		t.Fatalf("StateFromFrame (CAT): %v", err)
	}
	for _, col := range catCols {
		if state2.Attrs[2][col] != 0x01 {
			t.Errorf("CAT col %d: Attrs[2][%d]=0x%02x, want 0x01", col, col, state2.Attrs[2][col])
		}
	}
	// Row 1 must be clear (CAT is row 2, not ANTENNA row 1).
	for _, col := range catCols {
		if state2.Attrs[1][col] != 0x00 {
			t.Errorf("CAT: smear into row 1, col %d: Attrs[1][%d]=0x%02x", col, col, state2.Attrs[1][col])
		}
	}
}
