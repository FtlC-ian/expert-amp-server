package main

import (
	"flag"
	"image/color"
	"image/png"
	"log"
	"os"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/font"
	"github.com/FtlC-ian/expert-amp-server/internal/render"
)

func main() {
	outPath := flag.String("out", "preview.png", "output PNG path")
	flag.Parse()

	rom := font.Builtin()
	state := display.DemoState()
	img := render.Image(state, rom, color.Gray{Y: 0xff}, color.Gray{Y: 0x00})

	out, err := os.Create(*outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	if err := png.Encode(out, img); err != nil {
		log.Fatal(err)
	}
}
