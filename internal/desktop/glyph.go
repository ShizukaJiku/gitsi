package desktop

import "math"

// Glyph names a small line icon shown next to a menu entry. Like the tray icon, glyphs are drawn in code at the size the menu
// asks for: thin strokes on a 24x24 grid, in the menu's text color.
type Glyph string

// The menu's glyphs.
const (
	GlyphSend     Glyph = "send"
	GlyphTake     Glyph = "take"
	GlyphUpload   Glyph = "upload"
	GlyphInbox    Glyph = "inbox"
	GlyphRepo     Glyph = "repo"
	GlyphPlug     Glyph = "plug"
	GlyphTerminal Glyph = "terminal"
	GlyphPulse    Glyph = "pulse"
	GlyphSettings Glyph = "settings"
	GlyphFile     Glyph = "file"
	GlyphText     Glyph = "text"
	GlyphImage    Glyph = "image"
	GlyphFolder   Glyph = "folder"
	GlyphQuit     Glyph = "quit"
	GlyphRefresh  Glyph = "refresh"
	GlyphTrash    Glyph = "trash"
	GlyphEye      Glyph = "eye"
	GlyphCopy     Glyph = "copy"
	GlyphMonitor  Glyph = "monitor"
	GlyphLock     Glyph = "lock"
	GlyphStop     Glyph = "stop"
	GlyphLogo     Glyph = "logo" // the tray icon's mark, in the state given to the menu (the header)

	glyphCheck   Glyph = "check"   // a checked entry
	glyphChevron Glyph = "chevron" // an entry that opens a submenu
)

type pt struct{ x, y float64 }

// part is one piece of a glyph: a polyline of the stroke's width, or a ring around pts[0] when r is set.
type part struct {
	pts []pt
	r   float64
}

func line(xy ...float64) part {
	p := part{}
	for i := 0; i+1 < len(xy); i += 2 {
		p.pts = append(p.pts, pt{xy[i], xy[i+1]})
	}
	return p
}

func ring(cx, cy, r float64) part { return part{pts: []pt{{cx, cy}}, r: r} }

// arc is a polyline along a circle from angle a0 to a1 (degrees, clockwise from the right, y down).
func arc(cx, cy, r, a0, a1 float64) part {
	p := part{}
	n := int(math.Abs(a1-a0)/10) + 2
	for i := 0; i <= n; i++ {
		a := (a0 + (a1-a0)*float64(i)/float64(n)) * math.Pi / 180
		p.pts = append(p.pts, pt{cx + r*math.Cos(a), cy + r*math.Sin(a)})
	}
	return p
}

// rect is a rounded rectangle's outline.
func rect(x, y, w, h, r float64) []part {
	return []part{
		line(x+r, y, x+w-r, y), arc(x+w-r, y+r, r, -90, 0), line(x+w, y+r, x+w, y+h-r), arc(x+w-r, y+h-r, r, 0, 90),
		line(x+w-r, y+h, x+r, y+h), arc(x+r, y+h-r, r, 90, 180), line(x, y+h-r, x, y+r), arc(x+r, y+r, r, 180, 270),
	}
}

func join(groups ...[]part) []part {
	var out []part
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

var glyphs = map[Glyph][]part{
	GlyphSend:   {line(22, 2, 11, 13), line(22, 2, 15, 22, 11, 13, 2, 9, 22, 2)},
	GlyphTake:   {line(12, 3, 12, 15), line(7, 10, 12, 15, 17, 10), line(5, 21, 19, 21)},
	GlyphUpload: {line(12, 21, 12, 9), line(7, 14, 12, 9, 17, 14), line(5, 3, 19, 3)},
	GlyphInbox: {line(22, 12, 16, 12, 14, 15, 10, 15, 8, 12, 2, 12),
		line(5.5, 5, 18.5, 5, 22, 12, 22, 18, 20, 20, 4, 20, 2, 18, 2, 12, 5.5, 5)},
	GlyphRepo: {ring(6, 6, 2.5), ring(6, 18, 2.5), ring(18, 8, 2.5), line(6, 8.5, 6, 15.5),
		line(18, 10.5, 17.6, 12.6, 15.5, 14, 11, 15, 8, 16.4)},
	GlyphPlug:     {line(9, 2, 9, 7), line(15, 2, 15, 7), line(6, 11, 6, 7, 18, 7, 18, 11), arc(12, 11, 6, 0, 180), line(12, 17, 12, 22)},
	GlyphTerminal: join(rect(3, 4, 18, 16, 2), []part{line(7, 9, 10, 12, 7, 15), line(13, 15, 17, 15)}),
	GlyphPulse:    {line(3, 12, 7, 12, 10, 4, 14, 20, 17, 12, 21, 12)},
	GlyphSettings: {line(4, 6, 14.8, 6), line(19.2, 6, 20, 6), line(4, 12, 7.8, 12), line(12.2, 12, 20, 12), line(4, 18, 16.8, 18),
		ring(17, 6, 2.2), ring(10, 12, 2.2), ring(19, 18, 2.2)},
	GlyphFile:    {line(14, 2, 6, 2, 4, 4, 4, 20, 6, 22, 18, 22, 20, 20, 20, 8, 14, 2), line(14, 2, 14, 8, 20, 8)},
	GlyphText:    {line(4, 6, 20, 6), line(4, 11, 20, 11), line(4, 16, 14, 16)},
	GlyphImage:   join(rect(3, 4, 18, 16, 2), []part{ring(9, 10, 2), line(21, 17, 16, 12, 7, 20)}),
	GlyphFolder:  {line(3, 18, 3, 6, 5, 4, 9, 4, 11, 6, 19, 6, 21, 8, 21, 18, 19, 20, 5, 20, 3, 18)},
	GlyphQuit:    {line(18, 6, 6, 18), line(6, 6, 18, 18)},
	GlyphRefresh: {arc(12, 12, 8.5, -45, 255), line(15, 6.3, 18, 6.3, 18, 3.3)},
	GlyphTrash:   {line(4, 7, 20, 7), line(10, 11, 10, 17), line(14, 11, 14, 17), line(6, 7, 7, 20, 17, 20, 18, 7), line(9, 7, 9, 4, 15, 4, 15, 7)},
	GlyphEye:     {arc(12, 19, 12, -150, -30), arc(12, 5, 12, 30, 150), ring(12, 12, 3)},
	GlyphCopy:    join(rect(9, 9, 12, 12, 2), []part{line(5, 15, 5, 5, 7, 3, 17, 3)}),
	GlyphMonitor: join(rect(2, 4, 20, 13, 2), []part{line(8, 21, 16, 21), line(12, 17, 12, 21)}),
	GlyphLock:    join(rect(5, 11, 14, 10, 2), []part{line(8, 11, 8, 7), arc(12, 7, 4, 180, 360), line(16, 7, 16, 11)}),
	GlyphStop:    join(rect(5, 5, 14, 14, 2)),
	glyphCheck:   {line(20, 6, 9, 17, 4, 12)},
	glyphChevron: {line(9, 5, 16, 12, 9, 19)},
}

// strokeHalf is half the stroke width on the 24x24 grid: thick enough to read at 16 pixels.
const strokeHalf = 1.05

// GlyphPixels draws a glyph at size x size pixels in the given color: rows top to bottom, B, G, R, A with PREMULTIPLIED alpha,
// what a 32-bit menu bitmap takes. GlyphLogo draws the tray icon's mark for st. An unknown glyph is blank.
func GlyphPixels(g Glyph, size int, r, gr, b uint8, st IconState) []byte {
	if g == GlyphLogo {
		px := IconPixels(size, st)
		for i := 0; i < len(px); i += 4 {
			a := uint32(px[i+3])
			px[i], px[i+1], px[i+2] = uint8(uint32(px[i])*a/255), uint8(uint32(px[i+1])*a/255), uint8(uint32(px[i+2])*a/255)
		}
		return px
	}
	parts := glyphs[g]
	out := make([]byte, size*size*4)
	if parts == nil {
		return out
	}
	const n = 4
	scale := 24.0 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			hits := 0
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/n) * scale
					y := (float64(py) + (float64(sy)+0.5)/n) * scale
					if onGlyph(parts, x, y) {
						hits++
					}
				}
			}
			if hits == 0 {
				continue
			}
			a := uint32(hits * 255 / (n * n))
			i := (py*size + px) * 4
			out[i], out[i+1], out[i+2], out[i+3] = uint8(uint32(b)*a/255), uint8(uint32(gr)*a/255), uint8(uint32(r)*a/255), uint8(a)
		}
	}
	return out
}

func onGlyph(parts []part, x, y float64) bool {
	for _, p := range parts {
		if p.r > 0 {
			d := math.Hypot(x-p.pts[0].x, y-p.pts[0].y)
			if math.Abs(d-p.r) <= strokeHalf {
				return true
			}
			continue
		}
		if len(p.pts) == 1 && math.Hypot(x-p.pts[0].x, y-p.pts[0].y) <= strokeHalf {
			return true
		}
		for i := 0; i+1 < len(p.pts); i++ {
			if segmentDistance(x, y, p.pts[i].x, p.pts[i].y, p.pts[i+1].x, p.pts[i+1].y) <= strokeHalf {
				return true
			}
		}
	}
	return false
}
