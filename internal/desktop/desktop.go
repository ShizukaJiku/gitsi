// Package desktop is the part of the shared clipboard (docs/CLIP.md) that needs the user's desktop session: a global hotkey, the
// clipboard change notification, its change counter, the tray icon with its menu and notifications, a single instance per user,
// and the logon entry the hub may use. On Windows it calls user32/shell32/kernel32/advapi32 through the standard library's
// syscall (no cgo, no third-party packages); elsewhere it reports ErrUnsupported.
//
// The window that receives the messages is never shown. Every callback runs on the message loop's thread and must return quickly:
// whoever uses it hands the work to a goroutine.
package desktop

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported is returned where there is no desktop to use (any system other than Windows).
var ErrUnsupported = errors.New("the tray, the hotkey and the shared clipboard need Windows")

// ErrRunning is returned by Lock when another instance holds the lock.
var ErrRunning = errors.New("already running")

// MenuItem is an entry of the tray menu. An empty Label is a separator. An entry with Sub opens a submenu (its own Action is not
// used). Action runs on a goroutine of its own when the entry is chosen: it may take as long as it needs. A tab in Label starts
// the right-hand column, in grey (a shortcut, a state). Icon shows a glyph beside it (a check mark replaces it when Checked).
// Default shows it in bold (a title) and Note adds a second, grey line under it. Badge shows a count as a colored pill.
type MenuItem struct {
	Label    string
	Note     string
	Badge    string
	Checked  bool
	Disabled bool
	Default  bool
	Icon     Glyph
	Action   func()
	Sub      []MenuItem
}

// IconState is what the tray icon shows in its corner: the state of watch at a glance.
type IconState int

// The tray icon's states.
const (
	IconConnected IconState = iota // edge: connected to the hub (green dot)
	IconHub                        // hub: sharing its clipboard (blue dot)
	IconRetrying                   // edge: not connected, retrying (amber dot)
)

// Options configures Start.
type Options struct {
	Hotkey    string // "" = no hotkey; otherwise parsed with ParseHotkey
	Clipboard bool   // be told of every clipboard change (auto mode)
	Tray      bool   // show the tray icon
	Tooltip   string
	Icon      IconState
	Menu      []MenuItem

	OnHotkey    func()
	OnClipboard func()
	OnOpen      func() // the menu is about to open (it shows what SetMenu gave last; a refresh shows on the next opening)
}

// Hotkey modifiers (RegisterHotKey's MOD_* values).
const (
	ModAlt     = 0x0001
	ModControl = 0x0002
	ModShift   = 0x0004
	ModWin     = 0x0008
)

// ParseHotkey turns "ctrl+shift+alt+c" into modifiers and a virtual key. It requires at least two modifiers: with only one, the
// hotkey would steal shortcuts every program uses (Ctrl+C, Alt+F4...). The key is a letter, a digit or F1-F24.
func ParseHotkey(s string) (mods, vk uint32, err error) {
	parts := strings.Split(strings.ToLower(strings.ReplaceAll(s, " ", "")), "+")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("invalid hotkey %q: use modifiers and a key, e.g. ctrl+shift+alt+c", s)
	}
	n := 0
	for _, p := range parts[:len(parts)-1] {
		var m uint32
		switch p {
		case "ctrl", "control":
			m = ModControl
		case "alt":
			m = ModAlt
		case "shift":
			m = ModShift
		case "win":
			m = ModWin
		default:
			return 0, 0, fmt.Errorf("invalid hotkey %q: unknown modifier %q (ctrl, alt, shift, win)", s, p)
		}
		if mods&m != 0 {
			return 0, 0, fmt.Errorf("invalid hotkey %q: %q is repeated", s, p)
		}
		mods |= m
		n++
	}
	if n < 2 {
		return 0, 0, fmt.Errorf("invalid hotkey %q: use at least two modifiers (e.g. ctrl+shift+alt+c), so it does not steal a common shortcut", s)
	}
	key := parts[len(parts)-1]
	switch {
	case len(key) == 1 && key[0] >= 'a' && key[0] <= 'z':
		vk = uint32(key[0]-'a') + 'A'
	case len(key) == 1 && key[0] >= '0' && key[0] <= '9':
		vk = uint32(key[0])
	case len(key) >= 2 && key[0] == 'f':
		var f int
		if _, err := fmt.Sscanf(key[1:], "%d", &f); err != nil || f < 1 || f > 24 || fmt.Sprint(f) != key[1:] {
			return 0, 0, fmt.Errorf("invalid hotkey %q: unknown key %q (a letter, a digit or f1-f24)", s, key)
		}
		vk = 0x70 + uint32(f-1) // VK_F1
	default:
		return 0, 0, fmt.Errorf("invalid hotkey %q: unknown key %q (a letter, a digit or f1-f24)", s, key)
	}
	return mods, vk, nil
}

// HotkeyLabel is how a hotkey is shown: "Ctrl+Shift+Alt+C".
func HotkeyLabel(s string) string {
	parts := strings.Split(strings.ReplaceAll(s, " ", ""), "+")
	for i, p := range parts {
		switch p = strings.ToLower(p); {
		case p == "ctrl" || p == "control":
			parts[i] = "Ctrl"
		case len(p) > 0:
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "+")
}

// RunKey is the registry key (under HKEY_CURRENT_USER) whose values Windows starts when the user logs on.
const RunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Autostart is one value of a Run key: a command Windows starts at logon. Only the hub uses it (docs/PHILOSOPHY.md: nothing
// starts on its own on the edge).
type Autostart struct {
	Key  string // under HKEY_CURRENT_USER; RunKey
	Name string
}
