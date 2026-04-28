package font

import (
	"testing"
)

// TestBuiltinROMLoadsSPEGlyphs verifies that Builtin() returns the bundled
// SPE-style LCD font table. We spot-check a few glyphs that differ from the
// old placeholder font and are known-correct from hardware captures.
func TestBuiltinROMLoadsSPEGlyphs(t *testing.T) {
	rom := Builtin()

	cases := []struct {
		name string
		code byte
		// row0 is the first scanline byte; MSB=leftmost pixel.
		row0 byte
		// row7 is the last scanline byte.
		row7 byte
	}{
		// 0x21 = 'A' in SPE shifted-ASCII. ROM row0=0x0e (....###.), row7=0x00.
		// The public-domain substitute had a different shape for 'A'.
		{name: "A (0x21)", code: 0x21, row0: 0x0e, row7: 0x00},
		// 0x60 = blank (display space). All zero in the ROM.
		{name: "blank (0x60)", code: 0x60, row0: 0x00, row7: 0x00},
		// 0x8D = horizontal divider bar. Row 4 = 0x3f; row 0 = 0x00.
		{name: "horiz bar (0x8D)", code: 0x8D, row0: 0x00, row7: 0x00},
		// 0xB0 = top-left corner ornament. Row0=0x3f (..######), row7=0x20 (..#.....).
		{name: "top-left corner (0xB0)", code: 0xB0, row0: 0x3f, row7: 0x20},
		// 0xA2 = top-right corner. Row0=0x3f, row7=0x01 (.......#).
		{name: "top-right corner (0xA2)", code: 0xA2, row0: 0x3f, row7: 0x01},
		// 0xAA = degree/superscript dots. Row0=0x0c (....##..), row7=0x00.
		{name: "degree dots (0xAA)", code: 0xAA, row0: 0x0c, row7: 0x00},
		// 0xE0–0xFF are blank in ROM.
		{name: "above-ROM (0xE0)", code: 0xE0, row0: 0x00, row7: 0x00},
		{name: "above-ROM (0xFF)", code: 0xFF, row0: 0x00, row7: 0x00},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := rom.Glyphs[tc.code]
			if g[0] != tc.row0 {
				t.Errorf("Glyphs[0x%02X][0] = 0x%02x, want 0x%02x", tc.code, g[0], tc.row0)
			}
			if g[7] != tc.row7 {
				t.Errorf("Glyphs[0x%02X][7] = 0x%02x, want 0x%02x", tc.code, g[7], tc.row7)
			}
		})
	}
}

// TestGlyphAttrInvert verifies that attr low bits cause pixel inversion.
func TestGlyphAttrInvert(t *testing.T) {
	rom := Builtin()
	plain := rom.Glyph(0x21, 0x00)
	inverted := rom.Glyph(0x21, 0x01)
	for y := 0; y < GlyphBytes; y++ {
		if inverted[y] != ^plain[y] {
			t.Errorf("row %d: plain=0x%02x inverted=0x%02x, want ^plain=0x%02x", y, plain[y], inverted[y], ^plain[y])
		}
	}
}

// TestGlyphAttrAltBank verifies that attr bit 7 shifts to the alternate glyph bank.
func TestGlyphAttrAltBank(t *testing.T) {
	rom := Builtin()
	// With attr=0x80, code 0x41 should resolve to index 0x41-0x20=0x21.
	altGlyph := rom.Glyph(0x41, 0x80)
	directGlyph := rom.Glyph(0x21, 0x00)
	for y := 0; y < GlyphBytes; y++ {
		if altGlyph[y] != directGlyph[y] {
			t.Errorf("alt bank row %d: got 0x%02x, want 0x%02x", y, altGlyph[y], directGlyph[y])
		}
	}
}
