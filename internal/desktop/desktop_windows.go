package desktop

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procRegisterHotKey                = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey              = user32.NewProc("UnregisterHotKey")
	procAddClipboardFormatListener    = user32.NewProc("AddClipboardFormatListener")
	procRemoveClipboardFormatListener = user32.NewProc("RemoveClipboardFormatListener")
	procGetClipboardSequenceNumber    = user32.NewProc("GetClipboardSequenceNumber")
	procRegisterClipboardFormatW      = user32.NewProc("RegisterClipboardFormatW")
	procIsClipboardFormatAvailable    = user32.NewProc("IsClipboardFormatAvailable")
	procOpenClipboard                 = user32.NewProc("OpenClipboard")
	procCloseClipboard                = user32.NewProc("CloseClipboard")
	procEmptyClipboard                = user32.NewProc("EmptyClipboard")
	procSetClipboardData              = user32.NewProc("SetClipboardData")
	procGetClipboardData              = user32.NewProc("GetClipboardData")
	procRegisterWindowMessageW        = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu               = user32.NewProc("CreatePopupMenu")
	procAppendMenuW                   = user32.NewProc("AppendMenuW")
	procTrackPopupMenu                = user32.NewProc("TrackPopupMenu")
	procDestroyMenu                   = user32.NewProc("DestroyMenu")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procLoadIconW                     = user32.NewProc("LoadIconW")
	procShellNotifyIconW              = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
	procGlobalLock                    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock                  = kernel32.NewProc("GlobalUnlock")
	procGlobalAlloc                   = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                    = kernel32.NewProc("GlobalFree")
	procGlobalSize                    = kernel32.NewProc("GlobalSize")
	procRtlMoveMemory                 = kernel32.NewProc("RtlMoveMemory")
	procCreateMutexW                  = kernel32.NewProc("CreateMutexW")
)

const (
	wmNull            = 0x0000
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmCommand         = 0x0111
	wmHotkey          = 0x0312
	wmClipboardUpdate = 0x031D
	wmLButtonUp       = 0x0202
	wmRButtonUp       = 0x0205
	wmApp             = 0x8000
	wmTray            = wmApp + 1 // the tray icon's callback message
	wmUpdate          = wmApp + 2 // apply the tooltip and notifications queued from other goroutines

	nimAdd      = 0
	nimModify   = 1
	nimDelete   = 2
	nifMessage  = 0x01
	nifIcon     = 0x02
	nifTip      = 0x04
	nifInfo     = 0x10
	niifInfo    = 0x01
	niifWarning = 0x02

	mfString       = 0x0000
	mfGrayed       = 0x0001
	mfChecked      = 0x0008
	mfSeparator    = 0x0800
	tpmRightButton = 0x0002
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	modNoRepeat        = 0x4000
	idiApplication     = 32512
	hotkeyID           = 1
	errorAlreadyExists = 183

	cfBitmap      = 2
	cfDIB         = 8
	cfUnicodeText = 13
	cfHDrop       = 15
	cfDIBV5       = 17
	gmemMoveable  = 0x0002
)

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSm     uintptr
}

type point struct{ x, y int32 }

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
	private uint32
}

type guid struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

// notifyIconData is NOTIFYICONDATAW (the full, Vista+ layout).
type notifyIconData struct {
	size            uint32
	wnd             uintptr
	id              uint32
	flags           uint32
	callbackMessage uint32
	icon            uintptr
	tip             [128]uint16
	state           uint32
	stateMask       uint32
	info            [256]uint16
	version         uint32
	infoTitle       [64]uint16
	infoFlags       uint32
	guidItem        guid
	balloonIcon     uintptr
}

// Desktop is a running message loop with its window, hotkey, clipboard listener and tray icon.
type Desktop struct {
	opt Options
	// HotkeyErr says why the hotkey could not be registered (another program holds it); the rest keeps working.
	HotkeyErr error

	hwnd           uintptr
	taskbarCreated uint32
	done           chan struct{}
	mu             sync.Mutex
	tooltip        string
	menu           []MenuItem
	notes          [][2]string
	writes         []textWrite
	trayAdded      bool
	closeOnce      sync.Once
}

var (
	current   *Desktop // the window procedure is a plain function: it finds its Desktop here (one per process)
	currentMu sync.Mutex
	wndProcCb = syscall.NewCallback(wndProc)
)

// Start creates the window on a dedicated OS thread and runs its message loop until Close. It returns once the window, the
// hotkey, the clipboard listener and the tray icon exist.
func Start(opt Options) (*Desktop, error) {
	currentMu.Lock()
	if current != nil {
		currentMu.Unlock()
		return nil, errors.New("the desktop loop is already running in this process")
	}
	d := &Desktop{opt: opt, tooltip: opt.Tooltip, menu: append([]MenuItem(nil), opt.Menu...), done: make(chan struct{})}
	current = d
	currentMu.Unlock()

	ready := make(chan error, 1)
	go d.loop(ready)
	if err := <-ready; err != nil {
		currentMu.Lock()
		current = nil
		currentMu.Unlock()
		return nil, err
	}
	return d, nil
}

func (d *Desktop) loop(ready chan<- error) {
	runtime.LockOSThread() // a window belongs to the thread that created it: its messages arrive on that thread only
	defer runtime.UnlockOSThread()
	defer close(d.done)
	defer func() {
		currentMu.Lock()
		current = nil
		currentMu.Unlock()
	}()

	inst, _, _ := procGetModuleHandleW.Call(0)
	cls, _ := syscall.UTF16PtrFromString("gitsiDesktopWindow")
	wc := wndClassEx{wndProc: wndProcCb, instance: inst, className: cls}
	wc.size = uint32(unsafe.Sizeof(wc))
	// a class registered earlier in this process (Start after Close) is reused: ERROR_CLASS_ALREADY_EXISTS is not a failure
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	title, _ := syscall.UTF16PtrFromString("gitsi")
	// A hidden top-level window (not a message-only one): only top-level windows receive the TaskbarCreated broadcast, which says
	// the tray must be filled again after Explorer restarts.
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)), 0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("could not create the desktop window: %v", err)
		return
	}
	d.hwnd = hwnd
	tc, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tc)))
	d.taskbarCreated = uint32(r)

	if d.opt.Hotkey != "" {
		mods, vk, perr := ParseHotkey(d.opt.Hotkey)
		if perr != nil {
			d.HotkeyErr = perr
		} else if ok, _, err := procRegisterHotKey.Call(hwnd, hotkeyID, uintptr(mods|modNoRepeat), uintptr(vk)); ok == 0 {
			d.HotkeyErr = fmt.Errorf("%s is already used by another program (%v): choose another one with gitsi config set clip.hotkey <keys>", HotkeyLabel(d.opt.Hotkey), err)
		}
	}
	if d.opt.Clipboard {
		if ok, _, err := procAddClipboardFormatListener.Call(hwnd); ok == 0 {
			procDestroyWindow.Call(hwnd)
			ready <- fmt.Errorf("could not listen to clipboard changes: %v", err)
			return
		}
	}
	if d.opt.Tray {
		if err := d.addTray(); err != nil {
			procDestroyWindow.Call(hwnd)
			ready <- err
			return
		}
	}
	ready <- nil

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0: WM_QUIT; -1: error
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	currentMu.Lock()
	d := current
	currentMu.Unlock()
	if d == nil || d.hwnd != hwnd {
		r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
		return r
	}
	switch uint32(message) {
	case wmHotkey:
		if wParam == hotkeyID && d.opt.OnHotkey != nil {
			d.opt.OnHotkey()
		}
		return 0
	case wmClipboardUpdate:
		if d.opt.OnClipboard != nil {
			d.opt.OnClipboard()
		}
		return 0
	case wmTray:
		switch uint32(lParam) {
		case wmRButtonUp, wmLButtonUp:
			d.popup()
		}
		return 0
	case wmUpdate:
		d.apply()
		return 0
	case wmClose:
		d.teardown()
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	if d.taskbarCreated != 0 && uint32(message) == d.taskbarCreated && d.opt.Tray {
		_ = d.addTray() // Explorer restarted: the icon must be added again
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

func (d *Desktop) iconData() notifyIconData {
	var n notifyIconData
	n.size = uint32(unsafe.Sizeof(n))
	n.wnd = d.hwnd
	n.id = 1
	return n
}

func (d *Desktop) addTray() error {
	n := d.iconData()
	n.flags = nifMessage | nifIcon | nifTip
	n.callbackMessage = wmTray
	n.icon, _, _ = procLoadIconW.Call(0, idiApplication)
	d.mu.Lock()
	copyUTF16(n.tip[:], d.tooltip)
	d.mu.Unlock()
	if ok, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&n))); ok == 0 {
		return fmt.Errorf("could not add the tray icon: %v", err)
	}
	d.trayAdded = true
	return nil
}

// textWrite is a text to put on the clipboard from the loop's thread, and where to say how it went.
type textWrite struct {
	text string
	done chan error
}

// apply runs on the loop's thread: the clipboard writes, the tooltip and the notifications other goroutines queued.
func (d *Desktop) apply() {
	d.mu.Lock()
	tip, notes, writes := d.tooltip, d.notes, d.writes
	d.notes, d.writes = nil, nil
	d.mu.Unlock()
	for _, w := range writes {
		w.done <- writeText(d.hwnd, w.text)
	}
	if !d.trayAdded {
		return
	}
	n := d.iconData()
	n.flags = nifTip
	copyUTF16(n.tip[:], tip)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
	for _, note := range notes {
		n := d.iconData()
		n.flags = nifInfo
		n.infoFlags = niifInfo
		if note[0] != "" && note[0][0] == '!' {
			n.infoFlags, note[0] = niifWarning, note[0][1:]
		}
		copyUTF16(n.infoTitle[:], note[0])
		copyUTF16(n.info[:], note[1])
		procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
	}
}

func (d *Desktop) popup() {
	d.mu.Lock()
	items := append([]MenuItem(nil), d.menu...)
	d.mu.Unlock()
	if len(items) == 0 {
		return
	}
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	for _, it := range items {
		if it.ID == 0 && it.Label == "" {
			procAppendMenuW.Call(menu, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if it.Checked {
			flags |= mfChecked
		}
		if it.Disabled {
			flags |= mfGrayed
		}
		label, _ := syscall.UTF16PtrFromString(it.Label)
		procAppendMenuW.Call(menu, flags, uintptr(it.ID), uintptr(unsafe.Pointer(label)))
	}
	var p point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	// Without the foreground the menu does not close when the user clicks elsewhere (a documented quirk of tray menus).
	procSetForegroundWindow.Call(d.hwnd)
	id, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd|tpmNoNotify, uintptr(p.x), uintptr(p.y), 0, d.hwnd, 0)
	procPostMessageW.Call(d.hwnd, wmNull, 0, 0)
	if id != 0 && d.opt.OnMenu != nil {
		d.opt.OnMenu(int(id))
	}
}

func (d *Desktop) teardown() {
	if d.opt.Hotkey != "" && d.HotkeyErr == nil {
		procUnregisterHotKey.Call(d.hwnd, hotkeyID)
	}
	if d.opt.Clipboard {
		procRemoveClipboardFormatListener.Call(d.hwnd)
	}
	if d.trayAdded {
		n := d.iconData()
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&n)))
		d.trayAdded = false
	}
}

// SetTooltip changes the tray icon's tooltip.
func (d *Desktop) SetTooltip(s string) {
	d.mu.Lock()
	d.tooltip = s
	d.mu.Unlock()
	procPostMessageW.Call(d.hwnd, wmUpdate, 0, 0)
}

// SetMenu replaces the tray menu (it is built each time it opens).
func (d *Desktop) SetMenu(items []MenuItem) {
	d.mu.Lock()
	d.menu = append([]MenuItem(nil), items...)
	d.mu.Unlock()
}

// Notify shows a tray notification. warning shows it with the warning icon.
func (d *Desktop) Notify(title, message string, warning bool) {
	if warning {
		title = "!" + title
	}
	d.mu.Lock()
	d.notes = append(d.notes, [2]string{title, message})
	d.mu.Unlock()
	procPostMessageW.Call(d.hwnd, wmUpdate, 0, 0)
}

// Done is closed when the message loop ends: after Close, or when the window is closed from outside (watch then stops too, instead
// of living on without its icon).
func (d *Desktop) Done() <-chan struct{} { return d.done }

// Close removes the icon, the hotkey and the listener, destroys the window and waits for the loop to end.
func (d *Desktop) Close() {
	d.closeOnce.Do(func() {
		procPostMessageW.Call(d.hwnd, wmClose, 0, 0)
		select {
		case <-d.done:
		case <-time.After(5 * time.Second):
		}
	})
}

func copyUTF16(dst []uint16, s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		u = []uint16{0}
	}
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

// WriteText puts a text on the clipboard directly, in a few milliseconds (starting PowerShell takes most of a second). It runs on
// the loop's thread, with the window as the clipboard's owner, as Windows asks of SetClipboardData.
func (d *Desktop) WriteText(s string) error {
	w := textWrite{text: s, done: make(chan error, 1)}
	d.mu.Lock()
	d.writes = append(d.writes, w)
	d.mu.Unlock()
	procPostMessageW.Call(d.hwnd, wmUpdate, 0, 0)
	select {
	case err := <-w.done:
		return err
	case <-d.done:
		return errors.New("the desktop loop ended")
	case <-time.After(5 * time.Second):
		return errors.New("the clipboard did not answer")
	}
}

func writeText(hwnd uintptr, s string) error {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return err // a NUL inside: the caller falls back to PowerShell
	}
	if !openClipboard(hwnd) {
		return errors.New("another program has the clipboard open")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	size := uintptr(len(u) * 2)
	h, _, aerr := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("could not allocate the clipboard text: %v", aerr)
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("could not lock the clipboard text")
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	procGlobalUnlock.Call(h)
	if r, _, serr := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h) // on success the system owns the memory; on failure it is still ours
		return fmt.Errorf("could not set the clipboard text: %v", serr)
	}
	return nil
}

// ReadText returns the clipboard's text directly when text is what it holds. ok is false when it holds files or an image (they
// take precedence, as in the full reader) or nothing readable: then the full reader (PowerShell) decides.
func ReadText() (text string, ok bool, err error) {
	for _, f := range []uintptr{cfHDrop, cfBitmap, cfDIB, cfDIBV5} {
		if r, _, _ := procIsClipboardFormatAvailable.Call(f); r != 0 {
			return "", false, nil
		}
	}
	if available("PNG") != 0 {
		return "", false, nil
	}
	if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
		return "", false, nil
	}
	if !openClipboard(0) {
		return "", false, nil
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false, nil
	}
	size, _, _ := procGlobalSize.Call(h)
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 || size < 2 {
		return "", false, nil
	}
	defer procGlobalUnlock.Call(h)
	buf := make([]uint16, size/2)
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), p, size/2*2)
	return syscall.UTF16ToString(buf), true, nil
}

// openClipboard opens the clipboard, retrying briefly: another program may hold it for an instant.
func openClipboard(hwnd uintptr) bool {
	for i := 0; i < 20; i++ {
		if ok, _, _ := procOpenClipboard.Call(hwnd); ok != 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// Seq returns the clipboard's change counter: it grows with every change, whoever makes it.
func Seq() uint32 {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(r)
}

// Private reports whether what is on the clipboard is marked as not to be monitored or kept: the formats password managers set
// (ExcludeClipboardContentFromMonitorProcessing, or CanIncludeInClipboardHistory / CanUploadToCloudClipboard set to 0), which
// Windows' own clipboard history honors.
func Private() bool {
	if available("ExcludeClipboardContentFromMonitorProcessing") != 0 {
		return true
	}
	for _, name := range []string{"CanIncludeInClipboardHistory", "CanUploadToCloudClipboard"} {
		if f := available(name); f != 0 {
			if v, ok := dwordFormat(f); ok && v == 0 {
				return true
			}
		}
	}
	return false
}

// available returns the format's id when the clipboard holds it, 0 otherwise.
func available(name string) uintptr {
	p, _ := syscall.UTF16PtrFromString(name)
	f, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(p)))
	if f == 0 {
		return 0
	}
	if ok, _, _ := procIsClipboardFormatAvailable.Call(f); ok == 0 {
		return 0
	}
	return f
}

// dwordFormat reads a format that holds a DWORD. Another program may have the clipboard open for an instant: it retries briefly.
func dwordFormat(f uintptr) (uint32, bool) {
	if !openClipboard(0) {
		return 0, false
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(f)
	if h == 0 {
		return 0, false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return 0, false
	}
	defer procGlobalUnlock.Call(h)
	var v uint32
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&v)), p, 4)
	return v, true
}

// Lock takes a named lock for the user's session (a mutex named Local\<name>) so a second instance can tell one is running. It
// returns ErrRunning when another process holds it. The lock lasts until release is called or the process ends.
func Lock(name string) (release func(), err error) {
	p, err := syscall.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return nil, err
	}
	h, _, cerr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return nil, fmt.Errorf("could not create the instance lock: %v", cerr)
	}
	if errno, ok := cerr.(syscall.Errno); ok && errno == errorAlreadyExists {
		syscall.CloseHandle(syscall.Handle(h))
		return nil, ErrRunning
	}
	var once sync.Once
	return func() { once.Do(func() { syscall.CloseHandle(syscall.Handle(h)) }) }, nil
}
