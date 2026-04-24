package render

import (
	"fmt"
	"strings"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/font"
)

func SVG(state display.State, rom *font.ROM, fg string, bg string, scale int) string {
	if scale <= 0 {
		scale = 1
	}
	width := display.Cols * CellWidth * scale
	height := display.Rows * CellHeight * scale

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges">`, width, height, width, height))
	b.WriteString(fmt.Sprintf(`<rect width="100%%" height="100%%" fill="%s"/>`, bg))

	for row := 0; row < display.Rows; row++ {
		for col := 0; col < display.Cols; col++ {
			glyph := rom.Glyph(state.Chars[row][col], state.Attrs[row][col])
			b.WriteString(cellSVG(col, row, glyph, fg, bg, scale))
		}
	}

	b.WriteString(`</svg>`)
	return b.String()
}

func cellSVG(col int, row int, glyph [font.GlyphBytes]byte, fg string, bg string, scale int) string {
	x0 := col * CellWidth * scale
	y0 := row * CellHeight * scale
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<g id="cell-r%d-c%d"><rect x="%d" y="%d" width="%d" height="%d" fill="%s"/>`, row, col, x0, y0, CellWidth*scale, CellHeight*scale, bg))
	for y := 0; y < CellHeight; y++ {
		scan := glyph[y]
		for x := 0; x < CellWidth; x++ {
			mask := byte(1 << (7 - x))
			if scan&mask == 0 {
				continue
			}
			b.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" fill="%s"/>`, x0+x*scale, y0+y*scale, scale, scale, fg))
		}
	}
	b.WriteString(`</g>`)
	return b.String()
}
