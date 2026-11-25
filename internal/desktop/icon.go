package desktop

import "math"

// The mark is drawn in code, at the size the tray asks for, instead of shipping an .ico: no resource step in the build, and the
// state dot needs one image per state anyway. It is two nodes joined by a link, the hub (filled) and the edge (a ring), white on a
// rounded tile, with the state as a dot cut into the bottom-right corner.

type rgb struct{ r, g, b uint8 }

var (
	tileColor = rgb{0x0f, 0x76, 0x6e}
	white     = rgb{0xff, 0xff, 0xff}
	dotColors = map[IconState]rgb{
		IconConnected: {0x22, 0xc5, 0x5e},
		IconHub:       {0x60, 0xa5, 0xfa},
		IconRetrying:  {0xf5, 0x9e, 0x0b},
	}
)

// Geometry in a 32x32 space, scaled to the requested size.
const (
	tileRadius = 7.0
	dotX, dotY = 25.5, 25.5
	dotRadius  = 5.0
	cutRadius  = 7.5 // a transparent ring around the dot, so it reads on any taskbar color
	glyphScale = 0.74
)

// IconPixels draws the mark for a state at size x size pixels: rows top to bottom, 4 bytes per pixel in B, G, R, A order with
// straight (not premultiplied) alpha, the layout a 32-bit Windows icon takes. Edges are smoothed with 4x4 samples per pixel.
func IconPixels(size int, st IconState) []byte {
	const n = 4
	out := make([]byte, size*size*4)
	scale := 32.0 / float64(size)
	dot, hasDot := dotColors[st]
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/n) * scale
					y := (float64(py) + (float64(sy)+0.5)/n) * scale
					c, ok := sample(x, y, dot, hasDot)
					if !ok {
						continue
					}
					r, g, b, a = r+float64(c.r), g+float64(c.g), b+float64(c.b), a+1
				}
			}
			if a == 0 {
				continue
			}
			i := (py*size + px) * 4
			out[i] = uint8(b/a + 0.5)
			out[i+1] = uint8(g/a + 0.5)
			out[i+2] = uint8(r/a + 0.5)
			out[i+3] = uint8(a/(n*n)*255 + 0.5)
		}
	}
	return out
}

// sample returns the color at a point of the 32x32 space, or false where the icon is transparent. Without a dot (the
// executable's icon) the tile keeps its corner.
func sample(x, y float64, dot rgb, hasDot bool) (rgb, bool) {
	if hasDot {
		d := math.Hypot(x-dotX, y-dotY)
		if d <= dotRadius {
			return dot, true
		}
		if d <= cutRadius {
			return rgb{}, false
		}
	}
	if !inRoundedSquare(x, y, 32, tileRadius) {
		return rgb{}, false
	}
	if inGlyph((x-16)/glyphScale+16, (y-16)/glyphScale+16) {
		return white, true
	}
	return tileColor, true
}

func inRoundedSquare(x, y, side, r float64) bool {
	if x < 0 || y < 0 || x > side || y > side {
		return false
	}
	cx := math.Max(r, math.Min(side-r, x))
	cy := math.Max(r, math.Min(side-r, y))
	return math.Hypot(x-cx, y-cy) <= r
}

// inGlyph: the link from the hub to the edge, the hub as a disc, the edge as a ring.
func inGlyph(x, y float64) bool {
	if segmentDistance(x, y, 10, 22, 22, 10) <= 2.0 {
		return true
	}
	if math.Hypot(x-9, y-23) <= 6.5 {
		return true
	}
	return math.Abs(math.Hypot(x-23, y-9)-5) <= 1.8
}

func segmentDistance(x, y, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := ((x-ax)*dx + (y-ay)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(x-(ax+t*dx), y-(ay+t*dy))
}
