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
		Menu: []MenuItem{{ID: 1, Label: "Quit"}}, OnClipboard: func() { changes.Add(1) }})
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
	d.Notify("gitsi test", "a notification", false)
	time.Sleep(100 * time.Millisecond)
	d.Close()
	again, err := Start(Options{Hotkey: "ctrl+shift+alt+f23"})
	if err != nil || again.HotkeyErr != nil {
		t.Fatalf("after Close everything is released (window class, hotkey): %v %v", err, again.HotkeyErr)
	}
	again.Close()
}
