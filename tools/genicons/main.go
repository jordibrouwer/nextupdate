// Command genicons writes the PNG icons of the PWA into internal/web/static/icons.
// Run it with: go run ./tools/genicons
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

var (
	blue  = color.NRGBA{0x25, 0x63, 0xeb, 0xff}
	white = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

// glyph reports whether the point (x, y) in unit coordinates is inside the
// white "update" mark: an arrow pointing up above a bar.
func glyph(x, y, scale float64) bool {
	x = 0.5 + (x-0.5)/scale
	y = 0.5 + (y-0.5)/scale
	inTri := func(ax, ay, bx, by, cx, cy float64) bool {
		d := (by-cy)*(ax-cx) + (cx-bx)*(ay-cy)
		if d == 0 {
			return false
		}
		a := ((by-cy)*(x-cx) + (cx-bx)*(y-cy)) / d
		b := ((cy-ay)*(x-cx) + (ax-cx)*(y-cy)) / d
		return a >= 0 && b >= 0 && 1-a-b >= 0
	}
	head := inTri(0.5, 0.22, 0.28, 0.46, 0.72, 0.46)
	shaft := x >= 0.44 && x <= 0.56 && y >= 0.44 && y <= 0.64
	bar := x >= 0.28 && x <= 0.72 && y >= 0.71 && y <= 0.79
	return head || shaft || bar
}

func insideRounded(x, y, r float64) bool {
	cx := math.Min(math.Max(x, r), 1-r)
	cy := math.Min(math.Max(y, r), 1-r)
	return math.Hypot(x-cx, y-cy) <= r
}

func render(size int, maskable bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // supersampling per axis
	glyphScale := 1.0
	if maskable {
		glyphScale = 0.78 // keep the mark inside the safe zone
	}
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, covered float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					var c color.NRGBA
					switch {
					case !maskable && !insideRounded(x, y, 0.22):
						continue
					case glyph(x, y, glyphScale):
						c = white
					default:
						c = blue
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					covered++
				}
			}
			if covered > 0 {
				img.SetNRGBA(px, py, color.NRGBA{uint8(r / covered), uint8(g / covered), uint8(b / covered), uint8(255 * covered / (ss * ss))})
			}
		}
	}
	return img
}

func main() {
	dir := filepath.Join("internal", "web", "static", "icons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, s := range []struct {
		name     string
		size     int
		maskable bool
	}{
		{"icon-192.png", 192, false},
		{"icon-512.png", 512, false},
		{"icon-maskable-512.png", 512, true},
		{"apple-touch-icon.png", 180, true}, // iOS rounds the corners itself
	} {
		f, err := os.Create(filepath.Join(dir, s.name))
		if err != nil {
			log.Fatal(err)
		}
		if err := png.Encode(f, render(s.size, s.maskable)); err != nil {
			log.Fatal(err)
		}
		f.Close()
	}
}
