package desktop

import (
	"testing"
)

func TestParseHotkey(t *testing.T) {
	for in, want := range map[string][2]uint32{
		"ctrl+shift+alt+c": {ModControl | ModShift | ModAlt, 'C'},
		"Ctrl + Alt + 5":   {ModControl | ModAlt, '5'},
		"win+shift+f12":    {ModWin | ModShift, 0x7B},
		"control+alt+F1":   {ModControl | ModAlt, 0x70},
	} {
		m, vk, err := ParseHotkey(in)
		if err != nil || m != want[0] || vk != want[1] {
			t.Errorf("%q: %#x %#x %v", in, m, vk, err)
		}
	}
	for _, bad := range []string{"", "c", "ctrl+c", "shift+v", "ctrl+ctrl+c", "ctrl+alt+", "ctrl+alt+ab", "ctrl+alt+f0", "ctrl+alt+f25",
		"ctrl+alt+f01", "hyper+alt+c", "ctrl+alt+é"} {
		if _, _, err := ParseHotkey(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestHotkeyLabel(t *testing.T) {
	if got := HotkeyLabel("ctrl+shift+alt+c"); got != "Ctrl+Shift+Alt+C" {
		t.Fatal(got)
	}
	if got := HotkeyLabel("control+win+f5"); got != "Ctrl+Win+F5" {
		t.Fatal(got)
	}
}

// The mark: transparent outside the rounded tile, white on the hub's disc, the tile's color around it, and the state's dot in the
// corner with a transparent ring.
func TestIconPixels(t *testing.T) {
	at := func(px []byte, size, x, y int) [4]byte {
		i := (y*size + x) * 4
		return [4]byte{px[i], px[i+1], px[i+2], px[i+3]} // B, G, R, A
	}
	for _, size := range []int{16, 20, 24, 32} {
		px := IconPixels(size, IconConnected)
		if len(px) != size*size*4 {
			t.Fatalf("%d: %d bytes", size, len(px))
		}
		if a := at(px, size, 0, 0)[3]; a != 0 {
			t.Fatalf("%d: the corner outside the rounded tile is transparent, alpha %d", size, a)
		}
		k := float64(size) / 32
		// the hub's disc, mapped through the glyph's scale: (9,23) in glyph space
		hx, hy := int((16+(9-16)*glyphScale)*k), int((16+(23-16)*glyphScale)*k)
		if c := at(px, size, hx, hy); c != [4]byte{255, 255, 255, 255} {
			t.Fatalf("%d: the hub's disc is white, got %v", size, c)
		}
		if c := at(px, size, int(16*k), int(2*k)); c != [4]byte{tileColor.b, tileColor.g, tileColor.r, 255} {
			t.Fatalf("%d: the tile's color at the top, got %v", size, c)
		}
		if c := at(px, size, int(dotX*k), int(dotY*k)); c != [4]byte{0x5e, 0xc5, 0x22, 255} {
			t.Fatalf("%d: the green dot, got %v", size, c)
		}
	}
	if c := at(IconPixels(32, IconRetrying), 32, 31, 25); c[3] != 0 {
		t.Fatalf("the ring around the dot is cut out of the tile: %v", c)
	}
}
