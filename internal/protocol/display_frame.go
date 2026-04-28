package protocol

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

// RadioDisplayPrefix is the legacy 8-byte prefix used by the stream decoder
// and older display-frame tests. For 371-byte GetLCD responses, byte 7 is also
// the first byte of the inverted flag word; use GetLCDResponsePrefix when
// validating the fully understood GetLCD layout.
var RadioDisplayPrefix = []byte{0xAA, 0xAA, 0xAA, 0x6A, 0x01, 0x95, 0xFE, 0x01}

// GetLCDResponsePrefix is the fixed 7-byte prefix before the inverted 2-byte
// flag word in 371-byte GetLCD responses.
var GetLCDResponsePrefix = []byte{0xAA, 0xAA, 0xAA, 0x6A, 0x01, 0x95, 0xFE}

func IsRadioDisplayFrame(frame []byte) bool {
	if IsGetLCDResponseFrame(frame) {
		return true
	}
	if len(frame) < len(RadioDisplayPrefix) {
		return false
	}
	for i := range RadioDisplayPrefix {
		if frame[i] != RadioDisplayPrefix[i] {
			return false
		}
	}
	return true
}

// DecodeDisplayChar maps a raw serial display byte to the ROM index stored
// in display.State.Chars. The rendering path passes this value directly to
// font.ROM.Glyph as the ROM index.
//
// The SPE Expert 1.3K protocol byte IS the ROM index — no translation needed
// for the printable text range. The extracted font ROM at VA 0x46b568 in
// SPE-style LCD font table stores glyphs at the same indices the protocol uses:
//
//   - 0x00       → 0x60 (blank; protocol blank maps to ROM blank slot)
//   - 0x01–0x5f  → direct pass-through (ROM index = protocol byte)
//   - 0x60       → 0x60 (blank slot in ROM; NewState uses 0x60 as sentinel)
//   - 0x61–0x7e  → direct pass-through (blank slots in ROM, not used by display)
//   - 0x80–0xDF  → pass through (full custom-glyph range: borders, meters,
//     sidebar ornaments, degree sign — all present in the ROM)
//   - anything else → 0x60 (blank)
//
// Note: an earlier version of this function applied a +0x20 shift to the
// 0x01–0x5F range, storing ASCII codes instead of ROM indices. That caused
// every character to render from the wrong glyph (e.g. protocol 0x25 → stored
// as 0x45 → ROM[0x45] = lowercase 'e' shape instead of ROM[0x25] = uppercase
// 'E'). The shift has been removed.
func DecodeDisplayChar(v byte) byte {
	switch {
	case v == 0x00:
		return 0x60
	case v >= 0x01 && v <= 0x7e:
		// Direct pass-through: protocol byte = ROM index.
		return v
	case v >= 0x80 && v <= 0xdf:
		// Full custom-glyph range confirmed present in extracted font ROM.
		// 0xE0–0xFF are blank in the ROM and decode to 0x60 below.
		return v
	default:
		return 0x60
	}
}

func DecodeTextChar(v byte) rune {
	switch {
	case v == 0x00:
		return ' '
	case v >= 0x01 && v <= 0x5f:
		return rune(v + 0x20)
	case v >= 32 && v <= 126:
		return rune(v)
	default:
		return ' '
	}
}

func GuessDisplayStart(frame []byte) int {
	candidates := []int{8, 16, 24, 32, 40, 48, 56, 64, 72, 80}
	bestOffset := candidates[0]
	bestScore := -1 << 30
	for _, candidate := range candidates {
		if candidate >= len(frame) {
			break
		}
		bodyLength := min(320, len(frame)-candidate)
		text := make([]rune, bodyLength)
		score := 0
		for i, b := range frame[candidate : candidate+bodyLength] {
			decoded := DecodeTextChar(b)
			text[i] = decoded
			switch {
			case decoded == ' ':
				score += 1
			case (decoded >= '0' && decoded <= '9') || (decoded >= 'A' && decoded <= 'Z') || (decoded >= 'a' && decoded <= 'z'):
				score += 3
			case strings.ContainsRune(".-:/[]()>", decoded):
				score += 2
			}
		}

		rows := bodyLength / display.Cols
		if bodyLength%display.Cols != 0 {
			rows++
		}
		firstNonEmpty := rows
		leadingSpaces := 0
		for row := 0; row < rows; row++ {
			rowStart := row * display.Cols
			rowEnd := min(rowStart+display.Cols, len(text))
			rowText := string(text[rowStart:rowEnd])
			if strings.TrimSpace(rowText) != "" {
				firstNonEmpty = row
				leadingSpaces = len(rowText) - len(strings.TrimLeft(rowText, " "))
				break
			}
		}
		if firstNonEmpty < rows {
			score -= firstNonEmpty * 20
			if leadingSpaces <= 16 {
				score += leadingSpaces
			} else {
				score += 4
			}
		}

		if candidate > 8 && len(text) > 0 && text[0] != ' ' {
			score -= 8
		}

		if score > bestScore {
			bestScore = score
			bestOffset = candidate
		}
	}
	return bestOffset
}

// displayBodyOffset is the fixed byte offset at which the 8×40 display body
// starts in every radio display frame.
//
// Frame layout (confirmed from real captured frames):
//
//	[0..7]   8-byte prefix: AA AA AA 6A 01 95 FE 01 (RadioDisplayPrefix)
//	[8]      1-byte frame-type discriminator: e.g. 0xF8 = home, 0xD8 = menu/panel
//	[9..328] 320-byte display body: 8 rows × 40 cols, one byte per cell
//	[329..368] optional 40-byte packed attribute bitplane: 40 bytes, one per column.
//	          Each byte is a column bitmask: bit r (LSB = row 0) marks row r of
//	          that column as highlighted. Layout is column-major: attr[col] >> row & 1.
//	[... ]    trailing checksum/terminator bytes vary by frame family
//
// The display body therefore starts at byte 9, not byte 8. Using 8 instead
// placed the frame-type discriminator into col 0 of row 0, blanking the
// top-left corner cell (or rendering a stray glyph for frame types whose
// discriminator byte falls in 0x80–0xDF) and shifting every subsequent cell
// one position to the right within its row.
const displayBodyOffset = 9 // prefix(8) + frame-type-discriminator(1)

const (
	legacyLCDDataOffset = displayBodyOffset
	getLCDPrefixLen     = 3
	getLCDLengthOffset  = 3
	getLCDHeaderLen     = getLCDPrefixLen + 2 + 2
	getLCDFlagLen       = 2
	getLCDPayloadLen    = getLCDFlagLen + display.Rows*display.Cols + display.Cols
	getLCDTotalLen      = getLCDHeaderLen + getLCDPayloadLen + 2
)

// IsGetLCDResponseFrame reports whether frame starts with the length/checksum
// shape of a response to the 0x80 GetLCD request. Captured standalone responses
// are 371 bytes. Live stream frames may be longer because bytes from a following
// status-poll response can arrive before the next display prefix; in that case
// the first 371 bytes are still the GetLCD response and the trailing bytes are
// ignored by display parsing and flag checksum validation.
func IsGetLCDResponseFrame(frame []byte) bool {
	if len(frame) < getLCDTotalLen {
		return false
	}
	if len(frame) < getLCDHeaderLen {
		return false
	}
	for i := range GetLCDResponsePrefix {
		if frame[i] != GetLCDResponsePrefix[i] {
			return false
		}
	}
	return int(binary.LittleEndian.Uint16(frame[getLCDLengthOffset:])) == getLCDPayloadLen
}

func LCDDataOffset(frame []byte) int {
	if IsGetLCDResponseFrame(frame) {
		return getLCDHeaderLen + getLCDFlagLen
	}
	return legacyLCDDataOffset
}

func LCDFlagsFromFrame(frame []byte) (*LCDFlags, bool) {
	if !IsGetLCDResponseFrame(frame) {
		return nil, false
	}
	raw := binary.LittleEndian.Uint16(frame[getLCDHeaderLen : getLCDHeaderLen+getLCDFlagLen])
	flags := &LCDFlags{
		RawInverted:     raw,
		Decoded:         ^raw,
		ChecksumPresent: len(frame) >= getLCDTotalLen,
	}
	if flags.ChecksumPresent {
		sum := 0
		for _, b := range frame[getLCDHeaderLen : getLCDHeaderLen+getLCDPayloadLen] {
			sum += int(b)
		}
		got := binary.LittleEndian.Uint16(frame[getLCDHeaderLen+getLCDPayloadLen:])
		flags.ChecksumValid = uint16(sum) == got
	}
	flags.LEDs = DecodeLCDLEDs(flags.Decoded, flags.ChecksumValid)
	return flags, true
}

func StateFromFrame(frame []byte) (display.State, error) {
	if !IsRadioDisplayFrame(frame) {
		return display.State{}, errors.New("not a radio display frame")
	}
	state := display.NewState()
	start := LCDDataOffset(frame)
	bodyLength := min(display.Rows*display.Cols, len(frame)-start)
	for i := 0; i < bodyLength; i++ {
		row := i / display.Cols
		col := i % display.Cols
		if row >= display.Rows {
			break
		}
		state.Chars[row][col] = DecodeDisplayChar(frame[start+i])
	}

	// The 40-byte attribute trailer is column-major: one byte per column.
	// attr[col] is a bitmask where bit r (LSB = row 0) marks cell (row=r, col) as
	// highlighted. Earlier code treated this as a flat row-major bitstream
	// (bit i -> row=i//40, col=i%40), which scattered highlights across wrong cells.
	attrStart := start + display.Rows*display.Cols
	attrAvail := min(display.Cols, len(frame)-attrStart)
	for col := 0; col < attrAvail; col++ {
		byte_ := frame[attrStart+col]
		for row := 0; row < display.Rows; row++ {
			if byte_&(1<<uint(row)) != 0 {
				state.SetAttr(row, col, 0x01)
			}
		}
	}
	return state, nil
}

func ScreenText(frame []byte) (string, error) {
	if !IsRadioDisplayFrame(frame) {
		return "", errors.New("not a radio display frame")
	}
	start := LCDDataOffset(frame)
	bodyLength := min(display.Rows*display.Cols, len(frame)-start)
	rows := make([]string, 0, display.Rows)
	for row := 0; row < display.Rows; row++ {
		rowStart := row * display.Cols
		if rowStart >= bodyLength {
			break
		}
		buf := make([]rune, display.Cols)
		for col := 0; col < display.Cols; col++ {
			buf[col] = ' '
		}
		for col := 0; col < display.Cols && rowStart+col < bodyLength; col++ {
			buf[col] = DecodeTextChar(frame[start+rowStart+col])
		}
		line := strings.TrimRight(string(buf), " ")
		rows = append(rows, line)
	}
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	return strings.Join(rows, "\n"), nil
}

func LoadFixtureState(path string) (display.State, FrameMeta, error) {
	frame, err := osReadFile(path)
	if err != nil {
		return display.State{}, FrameMeta{}, err
	}
	state, err := StateFromFrame(frame)
	if err != nil {
		return display.State{}, FrameMeta{}, err
	}
	text, _ := ScreenText(frame)
	flags, _ := LCDFlagsFromFrame(frame)
	return state, FrameMeta{
		Source:      path,
		Length:      len(frame),
		StartOffset: LCDDataOffset(frame),
		ScreenText:  text,
		LCDFlags:    flags,
	}, nil
}

const (
	LCDFlagTX      uint16 = 0x0800
	LCDFlagOperate uint16 = 0x1000
	LCDFlagSet     uint16 = 0x2000
	LCDFlagTune    uint16 = 0x4000

	KnownLCDLEDMask = LCDFlagTX | LCDFlagOperate | LCDFlagSet | LCDFlagTune
)

type LCDFlags struct {
	RawInverted     uint16
	Decoded         uint16
	ChecksumPresent bool
	ChecksumValid   bool
	LEDs            *LCDLEDs
}

type LCDLEDs struct {
	TX      bool
	Operate bool
	Set     bool
	Tune    bool
}

func (f LCDFlags) Validated() bool {
	return f.ChecksumPresent && f.ChecksumValid
}

func DecodeLCDLEDs(decoded uint16, checksumValid bool) *LCDLEDs {
	if !checksumValid {
		return nil
	}
	return &LCDLEDs{
		TX:      decoded&LCDFlagTX != 0,
		Operate: decoded&LCDFlagOperate != 0,
		Set:     decoded&LCDFlagSet != 0,
		Tune:    decoded&LCDFlagTune != 0,
	}
}

type FrameMeta struct {
	Source      string
	Length      int
	StartOffset int
	ScreenText  string
	LCDFlags    *LCDFlags
}

// ReadFixtureBytes reads the raw bytes from a fixture file. Exported for
// test helpers that need the raw frame data.
func ReadFixtureBytes(path string) ([]byte, error) {
	return osReadFile(path)
}

var osReadFile = os.ReadFile

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
