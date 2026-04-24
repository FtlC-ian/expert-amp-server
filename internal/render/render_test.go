package render

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/font"
	"github.com/FtlC-ian/expert-amp-server/internal/protocol"
)

// scaleGray upscales a grayscale image by integer factor using nearest-neighbour.
func scaleGray(src *image.Gray, factor int) *image.Gray {
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

func TestImageUsesPerCellAttrs(t *testing.T) {
	state := display.NewState()
	state.Chars[1][5] = 0x21
	state.Chars[2][5] = 0x21
	state.SetAttr(1, 5, 0x01)

	img := Image(state, font.Builtin(), color.Gray{Y: 0xff}, color.Gray{Y: 0x00})

	litHighlighted := 0
	litPlain := 0
	x0 := 5 * CellWidth
	for y := 1 * CellHeight; y < 2*CellHeight; y++ {
		for x := x0; x < x0+CellWidth; x++ {
			if img.GrayAt(x, y).Y > 0 {
				litHighlighted++
			}
		}
	}
	for y := 2 * CellHeight; y < 3*CellHeight; y++ {
		for x := x0; x < x0+CellWidth; x++ {
			if img.GrayAt(x, y).Y > 0 {
				litPlain++
			}
		}
	}
	if litHighlighted == litPlain {
		t.Fatalf("expected per-cell attr to change only highlighted cell, got same lit counts %d", litPlain)
	}
}

// TestRenderLiveFixture renders the real home-status fixture and writes
// both a 1x and 4x upscaled PNG for visual inspection.
// Sanity-checks that the image is not all-black (broken ROM pass-through).
func TestRenderLiveFixture(t *testing.T) {
	frame, err := os.ReadFile("../../fixtures/real_home_status_frame.bin")
	if err != nil {
		t.Skipf("fixture not found, skipping: %v", err)
	}
	state, err := protocol.StateFromFrame(frame)
	if err != nil {
		t.Fatalf("StateFromFrame: %v", err)
	}
	rom := font.Builtin()
	img := Image(state, rom, color.Gray{Y: 0xff}, color.Gray{Y: 0x00})

	// Count lit pixels — if all zero the ROM pass-through is broken
	litPixels := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.GrayAt(x, y).Y > 0 {
				litPixels++
			}
		}
	}
	if litPixels == 0 {
		t.Fatal("rendered image is all-black: no pixels were drawn — ROM lookup broken")
	}
	t.Logf("lit pixels: %d / %d (%.1f%%)", litPixels, b.Dx()*b.Dy(), float64(litPixels)*100/float64(b.Dx()*b.Dy()))

	// Write 1x PNG
	if out, err := os.Create("/tmp/render_live_fix.png"); err == nil {
		_ = png.Encode(out, img)
		out.Close()
		t.Logf("1x preview: /tmp/render_live_fix.png")
	}

	// Write 4x upscaled PNG for easier visual comparison with reference screenshots
	scaled := scaleGray(img, 4)
	if out, err := os.Create("/tmp/render_live_fix_4x.png"); err == nil {
		_ = png.Encode(out, scaled)
		out.Close()
		t.Logf("4x preview: /tmp/render_live_fix_4x.png (%dx%d)", scaled.Bounds().Dx(), scaled.Bounds().Dy())
	}
}
