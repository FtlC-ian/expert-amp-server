package font

const (
	GlyphCount  = 256
	GlyphHeight = 8
	GlyphBytes  = 8
)

type ROM struct {
	Glyphs [GlyphCount][GlyphBytes]byte
}

// Builtin returns the SPE Expert 1.3K display ROM, loaded directly from the
// 256-glyph SPE-style LCD font table.
//
// Every glyph is already in MSB-left bit order, matching the renderer directly.
// No normalization or override step is needed.
func Builtin() *ROM {
	var rom ROM
	rom.Glyphs = spe1300ROMFont
	return &rom
}

// Glyph returns the 8-byte glyph bitmap for the given character code and attribute byte.
//
// Attribute byte semantics (from reverse-engineered cell renderer FUN_00402e3c):
//   - bit 7 set:         alternate glyph bank (subtract 0x20 from code before lookup)
//   - bits 0..6 nonzero: invert all glyph pixels (highlight/reverse-video)
func (r *ROM) Glyph(code byte, attr byte) [GlyphBytes]byte {
	idx := code
	if attr&0x80 != 0 {
		idx = code - 0x20
	}
	glyph := r.Glyphs[idx]
	if attr&0x7f != 0 {
		for i := range glyph {
			glyph[i] = ^glyph[i]
		}
	}
	return glyph
}
