package server

import (
	"strings"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
)

func displayTextFromSnapshot(snapshot runtime.Snapshot, modelName string) api.DisplayText {
	rows := make([]string, display.Rows)
	spans := make([]api.DisplayTextSpan, 0)
	selected := make([]string, 0)

	for row := 0; row < display.Rows; row++ {
		decoded := make([]rune, display.Cols)
		for col := 0; col < display.Cols; col++ {
			decoded[col] = decodeStateTextChar(snapshot.State.Chars[row][col], snapshot.State.Attrs[row][col])
		}
		rows[row] = string(decoded)

		for start := 0; start < display.Cols; {
			if !isHighlighted(snapshot.State.Attrs[row][start]) {
				start++
				continue
			}
			end := start + 1
			for end < display.Cols && isHighlighted(snapshot.State.Attrs[row][end]) {
				end++
			}
			text := string(decoded[start:end])
			spans = append(spans, api.DisplayTextSpan{
				Row:         row,
				StartColumn: start,
				EndColumn:   end,
				Text:        text,
			})
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				selected = append(selected, trimmed)
			}
			start = end
		}
	}

	return api.DisplayText{
		Rows:             rows,
		HighlightedSpans: spans,
		SelectedText:     strings.Join(selected, "\n"),
		Sequence:         snapshot.Sequence,
		UpdatedAt:        snapshot.UpdatedAt,
		Source:           snapshot.Source,
		ModelName:        modelName,
		ScreenText:       snapshot.Frame.ScreenText,
	}
}

func decodeStateTextChar(value byte, attr byte) rune {
	// Match the renderer's alternate-bank lookup before interpreting the
	// effective ROM slot as text. Byte subtraction intentionally wraps just as
	// font.ROM.Glyph does for malformed/unknown combinations.
	if attr&0x80 != 0 {
		value -= 0x20
	}
	// Normalized display state stores shifted printable text in 0x01-0x5e.
	// Slot 0x5f is blank in the bundled ROM, not the DEL control character.
	// ROM slots 0x60-0x7f render blank, while 0x80-0xdf are custom glyphs that
	// intentionally remain semantically undecoded.
	if value >= 0x01 && value <= 0x5e {
		return rune(value + 0x20)
	}
	return ' '
}

func isHighlighted(attr byte) bool {
	// Attribute bit 7 selects an alternate glyph bank; only low bits mean
	// reverse-video/highlight in the decoded display state.
	return attr&0x7f != 0
}
