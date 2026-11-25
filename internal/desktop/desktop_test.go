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
