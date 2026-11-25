package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// The structs passed to Windows must have its exact layout: a wrong size makes Shell_NotifyIcon fail (or read garbage).
func TestStructLayouts(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("sizes checked for 64-bit Windows")
	}
	if n := unsafe.Sizeof(notifyIconData{}); n != 976 {
		t.Fatalf("NOTIFYICONDATAW is 976 bytes on 64-bit Windows, got %d", n)
	}
	if n := unsafe.Sizeof(wndClassEx{}); n != 80 {
		t.Fatalf("WNDCLASSEXW is 80 bytes on 64-bit Windows, got %d", n)
	}
	if n := unsafe.Sizeof(msg{}); n != 48 {
		t.Fatalf("MSG is 48 bytes on 64-bit Windows, got %d", n)
	}
	if n := unsafe.Sizeof(bitmapInfoHeader{}); n != 40 {
		t.Fatalf("BITMAPINFOHEADER is 40 bytes, got %d", n)
	}
	if n := unsafe.Sizeof(iconInfo{}); n != 32 {
		t.Fatalf("ICONINFO is 32 bytes on 64-bit Windows, got %d", n)
	}
	for name, sz := range map[string][2]uintptr{
		"MEASUREITEMSTRUCT": {unsafe.Sizeof(measureItem{}), 32}, "DRAWITEMSTRUCT": {unsafe.Sizeof(drawItem{}), 64},
		"MENUINFO": {unsafe.Sizeof(menuInfo{}), 40}, "LOGFONTW": {unsafe.Sizeof(logFont{}), 92},
		"NONCLIENTMETRICSW": {unsafe.Sizeof(nonClientMetrics{}), 504},
	} {
		if sz[0] != sz[1] {
			t.Fatalf("%s is %d bytes on 64-bit Windows, got %d", name, sz[1], sz[0])
		}
	}
	if n := unsafe.Sizeof(browseInfo{}); n != 64 {
		t.Fatalf("BROWSEINFOW is 64 bytes on 64-bit Windows, got %d", n)
	}
	if n := unsafe.Sizeof(osVersionInfo{}); n != 276 {
		t.Fatalf("RTL_OSVERSIONINFOW is 276 bytes, got %d", n)
	}
}

// The drawn menu: every entry measures and draws into a real device context, with the title's two lines, a badge, a grey
// column, a check mark, a separator and a submenu; and everything it created is freed.
func TestOwnerDrawnMenu(t *testing.T) {
	b := newMenuBuild(IconHub, false)
	menu := b.build([]MenuItem{
		{Label: "Hub, sharing the clipboard", Note: "1 edge listening", Default: true, Icon: GlyphLogo},
		{},
		{Label: "Inbox", Badge: "2", Icon: GlyphInbox, Sub: []MenuItem{{Label: "Refresh", Icon: GlyphRefresh, Action: func() {}}}},
		{Label: "Send what I copied\tCtrl+Shift+Alt+C", Icon: GlyphSend, Action: func() {}},
		{Label: "Send every copy", Checked: true, Action: func() {}},
		{Label: "Reading the list...", Disabled: true},
	})
	if menu == 0 || len(b.entries) != 7 || b.font == 0 || b.bold == 0 {
		t.Fatalf("menu %v, %d entries, fonts %v %v", menu, len(b.entries), b.font, b.bold)
	}
	defer procDestroyMenu.Call(menu)
	screen, _, _ := procGetDC.Call(0)
	mem, _, _ := procCreateCompatibleDC.Call(screen)
	procReleaseDC.Call(0, screen)
	defer procDeleteDC.Call(mem)
	heights := map[int]uint32{}
	for i := range b.entries {
		m := measureItem{ctlType: odtMenu, itemData: uintptr(i)}
		b.measure(&m)
		if m.itemWidth == 0 || m.itemHeight == 0 {
			t.Fatalf("entry %d has no size", i)
		}
		heights[i] = m.itemHeight
		for _, state := range []uint32{0, odsSelected} {
			b.draw(&drawItem{ctlType: odtMenu, hdc: mem, rc: winRect{0, 0, int32(m.itemWidth), int32(m.itemHeight)}, itemData: uintptr(i), itemState: state})
		}
	}
	if heights[0] != titleHeight || heights[1] != sepHeight || heights[2] != rowHeight {
		t.Fatalf("title, separator and row heights: %v", heights)
	}
	b.free()
	if len(b.bitmaps) != 0 || len(b.brushes) != 0 || b.font != 0 {
		t.Fatal("freed")
	}
}

// Every state becomes a real icon Windows accepts (the tray would otherwise fall back to the generic one).
func TestMakeIconForEveryState(t *testing.T) {
	for _, st := range []IconState{IconConnected, IconHub, IconRetrying} {
		h := makeIcon(st)
		if h == 0 {
			t.Fatalf("state %d: Windows refused the icon", st)
		}
		procDestroyIcon.Call(h)
	}
}

// Submenus are built with every entry's action at the id the menu returns, separators and submenu titles taking none.
func TestBuildMenuKeepsEachActionAtItsID(t *testing.T) {
	var got []string
	act := func(s string) func() { return func() { got = append(got, s) } }
	actions := []func(){nil}
	menu := buildMenu([]MenuItem{
		{Label: "Status", Disabled: true},
		{},
		{Label: "Mailbox", Sub: []MenuItem{
			{Label: "report.pdf", Sub: []MenuItem{{Label: "Copy", Action: act("copy")}, {Label: "Discard", Action: act("discard")}}},
			{Label: "Refresh", Action: act("refresh")},
		}},
		{Label: "Quit", Action: act("quit")},
	}, &actions)
	if menu == 0 {
		t.Fatal("the menu was not created")
	}
	defer procDestroyMenu.Call(menu)
	if len(actions) != 6 || actions[1] != nil {
		t.Fatalf("ids 1..5: status, copy, discard, refresh, quit; got %d", len(actions)-1)
	}
	for _, i := range []int{2, 3, 4, 5} {
		actions[i]()
	}
	if strings.Join(got, ",") != "copy,discard,refresh,quit" {
		t.Fatal(got)
	}
}

func TestLockIsExclusiveUntilReleased(t *testing.T) {
	name := fmt.Sprintf("gitsi-test-%d", time.Now().UnixNano())
	release, err := Lock(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(name); !errors.Is(err, ErrRunning) {
		t.Fatalf("a second lock must say it is running: %v", err)
	}
	release()
	release() // twice is harmless
	again, err := Lock(name)
	if err != nil {
		t.Fatalf("after release the lock is free: %v", err)
	}
	again()
}

// Over a temporary key, never the user's real Run key.
func TestAutostartOverATemporaryKey(t *testing.T) {
	a := Autostart{Key: fmt.Sprintf(`Software\gitsi-test-%d`, time.Now().UnixNano()), Name: "gitsi-watch"}
	defer a.deleteKey()
	if _, ok, err := a.Get(); err != nil || ok {
		t.Fatalf("a missing entry: %v %v", ok, err)
	}
	cmd := `"C:\Program Files\gitsi\gitsi.exe" watch -tray`
	if err := a.Set(cmd); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := a.Get(); err != nil || !ok || got != cmd {
		t.Fatalf("%q %v %v", got, ok, err)
	}
	if err := a.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(); err != nil {
		t.Fatalf("removing a missing entry is not an error: %v", err)
	}
	if _, ok, _ := a.Get(); ok {
		t.Fatal("the entry must be gone")
	}
}

func setClipboard(t *testing.T, script string) {
	t.Helper()
	out, err := procx.Background(winps.Command(context.Background(), "-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command",
		"Add-Type -AssemblyName System.Windows.Forms; "+script)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
}

// The real loop against the real Windows API: the listener hears a change, Seq moves, a private copy is recognized, the tray icon
// and the hotkey are registered and removed. Opt-in: it changes the user's clipboard (and restores the text that was there).
func TestRealDesktopLoop(t *testing.T) {
	if os.Getenv("GITSI_REAL_CLIPBOARD") != "1" {
		t.Skip("opt-in: changes the real clipboard (GITSI_REAL_CLIPBOARD=1)")
	}
	saved, _ := procx.Background(winps.Command(context.Background(), "-NoProfile", "-STA", "-Command", "Get-Clipboard -Raw")).Output()
	if text := strings.TrimRight(string(saved), "\r\n"); text != "" { // SetText refuses an empty text: then there is nothing to restore
		defer setClipboard(t, "[Windows.Forms.Clipboard]::SetText('"+strings.ReplaceAll(text, "'", "''")+"')")
	}

	var changes atomic.Int32
	d, err := Start(Options{Hotkey: "ctrl+shift+alt+f23", Clipboard: true, Tray: true, Tooltip: "gitsi test",
		Menu: []MenuItem{{Label: "More", Sub: []MenuItem{{Label: "Quit"}}}}, Icon: IconRetrying, OnClipboard: func() { changes.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.HotkeyErr != nil {
		t.Fatalf("the hotkey must register: %v", d.HotkeyErr)
	}
	if _, err := Start(Options{}); err == nil {
		t.Fatal("only one loop per process")
	}
	before := Seq()
	setClipboard(t, "[Windows.Forms.Clipboard]::SetText('gitsi desktop test')")
	deadline := time.Now().Add(3 * time.Second)
	for changes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if changes.Load() == 0 || Seq() == before {
		t.Fatalf("the change must be heard (%d) and the counter move (%d -> %d)", changes.Load(), before, Seq())
	}
	if Private() {
		t.Fatal("a plain text is not private")
	}
	setClipboard(t, `$o = New-Object Windows.Forms.DataObject; $o.SetText('secret'); `+
		`$o.SetData('ExcludeClipboardContentFromMonitorProcessing', (New-Object IO.MemoryStream(,[byte[]](0)))); `+
		`[Windows.Forms.Clipboard]::SetDataObject($o, $true)`)
	if !Private() {
		t.Fatal("a copy marked ExcludeClipboardContentFromMonitorProcessing is private")
	}
	setClipboard(t, `$o = New-Object Windows.Forms.DataObject; $o.SetText('secret'); `+
		`$o.SetData('CanIncludeInClipboardHistory', (New-Object IO.MemoryStream(,[byte[]](0,0,0,0)))); `+
		`[Windows.Forms.Clipboard]::SetDataObject($o, $true)`)
	if !Private() {
		t.Fatal("a copy with CanIncludeInClipboardHistory = 0 is private")
	}
	// the direct text path: written by the loop's window, read back without PowerShell, the counter moving
	before = Seq()
	if err := d.WriteText("gitsi: texto directo, ñ y emoji 😀"); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := ReadText(); err != nil || !ok || got != "gitsi: texto directo, ñ y emoji 😀" || Seq() == before {
		t.Fatalf("%q %v %v (seq %d -> %d)", got, ok, err, before, Seq())
	}
	setClipboard(t, `$f = New-Object Collections.Specialized.StringCollection; [void]$f.Add($env:WINDIR); [Windows.Forms.Clipboard]::SetFileDropList($f)`)
	if _, ok, _ := ReadText(); ok {
		t.Fatal("files copied in Explorer go through the full reader, not as text")
	}
	d.SetTooltip("gitsi test: updated")
	d.SetIcon(IconConnected)
	d.Notify("gitsi test", "a notification", false)
	time.Sleep(100 * time.Millisecond)
	d.Close()
	again, err := Start(Options{Hotkey: "ctrl+shift+alt+f23"})
	if err != nil || again.HotkeyErr != nil {
		t.Fatalf("after Close everything is released (window class, hotkey): %v %v", err, again.HotkeyErr)
	}
	again.Close()
}
