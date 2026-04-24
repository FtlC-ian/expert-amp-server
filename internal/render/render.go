package render

import (
	"image"
	"image/color"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/font"
)

const (
	// CellWidth is 6 pixels wide: the SPE Expert LCD font uses 5 active pixel
	// columns (bits 4-0 of each scanline byte). With CellWidth=8 and mask
	// 1<<(7-x), bits 7-5 are always zero, producing a 3-pixel blank gap at the
	// left of every column. With CellWidth=6 and mask 1<<(5-x), only bit 5 is
	// always zero, giving a natural 1-pixel inter-character gap.
	CellWidth  = 6
	CellHeight = 8
)

func Image(state display.State, rom *font.ROM, fg color.Color, bg color.Color) *image.Gray {
	bounds := image.Rect(0, 0, display.Cols*CellWidth, display.Rows*CellHeight)
	img := image.NewGray(bounds)
	fgGray := color.GrayModel.Convert(fg).(color.Gray)
	bgGray := color.GrayModel.Convert(bg).(color.Gray)

	for row := 0; row < display.Rows; row++ {
		for col := 0; col < display.Cols; col++ {
			glyph := rom.Glyph(state.Chars[row][col], state.Attrs[row][col])
			drawGlyph(img, col, row, glyph, fgGray, bgGray)
		}
	}

	return img
}

func drawGlyph(img *image.Gray, col int, row int, glyph [font.GlyphBytes]byte, fg color.Gray, bg color.Gray) {
	x0 := col * CellWidth
	y0 := row * CellHeight
	for y := 0; y < CellHeight; y++ {
		scan := glyph[y]
		for x := 0; x < CellWidth; x++ {
			mask := byte(1 << (CellWidth - 1 - x))
			if scan&mask != 0 {
				img.SetGray(x0+x, y0+y, fg)
			} else {
				img.SetGray(x0+x, y0+y, bg)
			}
		}
	}
}

func ScaleGray(src *image.Gray, factor int) *image.Gray {
	if factor <= 1 {
		return src
	}
	sb := src.Bounds()
	dst := image.NewGray(image.Rect(0, 0, sb.Dx()*factor, sb.Dy()*factor))
	for y := sb.Min.Y; y < sb.Max.Y; y++ {
		for x := sb.Min.X; x < sb.Max.X; x++ {
			v := src.GrayAt(x, y)
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					dst.SetGray(x*factor+dx, y*factor+dy, v)
				}
			}
		}
	}
	return dst
}
