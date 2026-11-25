package watchcmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/clip"
	"github.com/ShizukaJiku/gitsi/internal/clipboard"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/workdirs"
)

// fakeUI records what watch asks of the desktop and lets the test press the hotkey or pick a menu entry.
type fakeUI struct {
	mu     sync.Mutex
	opt    desktop.Options
	tips   []string
	menus  [][]desktop.MenuItem
	notes  []string
	icons  []desktop.IconState
	asked  []string
	answer bool   // what Confirm answers
	pick   string // what PickFolder answers ("" = cancelled)
	shown  []string
	closed bool
	done   chan struct{}
}

// options returns what watch asked of the desktop (it is set from watch's goroutine).
func (f *fakeUI) options() desktop.Options { f.mu.Lock(); defer f.mu.Unlock(); return f.opt }

func (f *fakeUI) SetTooltip(s string) { f.mu.Lock(); f.tips = append(f.tips, s); f.mu.Unlock() }
func (f *fakeUI) SetMenu(m []desktop.MenuItem) {
	f.mu.Lock()
	f.menus = append(f.menus, m)
	f.mu.Unlock()
}
func (f *fakeUI) Notify(t, m string, _ bool) {
	f.mu.Lock()
	f.notes = append(f.notes, t+": "+m)
	f.mu.Unlock()
}
func (f *fakeUI) SetIcon(st desktop.IconState) {
	f.mu.Lock()
	f.icons = append(f.icons, st)
	f.mu.Unlock()
}
func (f *fakeUI) Confirm(_, text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, text)
	return f.answer
}
func (f *fakeUI) Message(title, text string) {
	f.mu.Lock()
	f.shown = append(f.shown, title+": "+text)
	f.mu.Unlock()
}
func (f *fakeUI) PickFolder(string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pick, f.pick != ""
}

// set changes the fake's answers while watch may be reading them.
func (f *fakeUI) set(answer bool, pick string) {
	f.mu.Lock()
	f.answer, f.pick = answer, pick
	f.mu.Unlock()
}

func (f *fakeUI) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *fakeUI) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.shown...)
}

// lastMenu is the menu as watch last set it (or as it started).
func (f *fakeUI) lastMenu() []desktop.MenuItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.menus) > 0 {
		return f.menus[len(f.menus)-1]
	}
	return f.opt.Menu
}

func (f *fakeUI) noted() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.notes, " | ")
}

// findEntry finds a menu entry, at any depth, whose label starts with the given text.
func findEntry(items []desktop.MenuItem, prefix string) *desktop.MenuItem {
	for i := range items {
		if strings.HasPrefix(items[i].Label, prefix) {
			return &items[i]
		}
		if e := findEntry(items[i].Sub, prefix); e != nil {
			return e
		}
	}
	return nil
}

func (f *fakeUI) Close()                 { f.mu.Lock(); f.closed = true; f.mu.Unlock() }
func (f *fakeUI) Done() <-chan struct{}  { return f.done }
func (f *fakeUI) WriteText(string) error { return errors.New("the fake writes through the board") }

// fakeBoard is the clipboard watch reads and writes in these tests.
type fakeBoard struct {
	mu sync.Mutex
	c  clipboard.Content
	n  uint32
}

func (b *fakeBoard) read(context.Context) (clipboard.Content, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c.Text == "" && len(b.c.Files) == 0 && len(b.c.PNG) == 0 {
		return clipboard.Content{}, clipboard.ErrEmpty
	}
	return b.c, nil
}
func (b *fakeBoard) write(_ context.Context, c clipboard.Content) error {
	b.mu.Lock()
	b.c, b.n = c, b.n+1
	b.mu.Unlock()
	return nil
}
func (b *fakeBoard) text() string { b.mu.Lock(); defer b.mu.Unlock(); return b.c.Text }
func (b *fakeBoard) seq() uint32  { b.mu.Lock(); defer b.mu.Unlock(); return b.n }

// fakeDesktop replaces the desktop, the clipboard and the lock for one test.
func fakeDesktop(t *testing.T, hotkeyErr error) (*fakeUI, *fakeBoard) {
	u, b := &fakeUI{done: make(chan struct{})}, &fakeBoard{}
	oldStart, oldR, oldW, oldSeq, oldPriv, oldLock, oldPoll, oldText := startUI, readClip, writeClip, clipSeq, clipPriv, lock, slotPoll, readText
	oldRun, oldTunnel, oldDirs, oldInspect := runSelf, startTunnel, listWorkdirs, inspectRepo
	oldFolder, oldFile, oldTerm := openFolder, openFile, openTerminal
	runSelf = func(context.Context, string, ...string) (string, string, error) { return "[]", "", nil } // never the test binary itself
	startTunnel = func(string) (func(), <-chan string, error) { return nil, nil, errors.New("no tunnels in this test") }
	listWorkdirs = func() ([]workdirs.Dir, error) { return nil, nil }
	openFolder = func(string) error { return nil }
	openFile = func(string) error { return nil }
	openTerminal = func(string) error { return nil }
	readText = func() (string, bool, error) { return "", false, nil }
	startUI = func(o desktop.Options) (ui, error, error) {
		u.mu.Lock()
		u.opt = o
		u.mu.Unlock()
		return u, hotkeyErr, nil
	}
	readClip, writeClip, clipSeq, clipPriv = b.read, b.write, b.seq, func() bool { return false }
	lock = func() (func(), error) { return func() {}, nil }
	slotPoll = 10 * time.Millisecond
	t.Cleanup(func() {
		startUI, readClip, writeClip, clipSeq, clipPriv, lock, slotPoll, readText = oldStart, oldR, oldW, oldSeq, oldPriv, oldLock, oldPoll, oldText
		runSelf, startTunnel, listWorkdirs, inspectRepo = oldRun, oldTunnel, oldDirs, oldInspect
		openFolder, openFile, openTerminal = oldFolder, oldFile, oldTerm
	})
	return u, b
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func clipHubEnv(t *testing.T, clipOn bool) (*module.Env, *syncBuffer) {
	cfg := config.Default()
	cfg.Role = config.RoleHub
	cfg.Home = filepath.ToSlash(t.TempDir())
	cfg.Policy.AllowClip = clipOn
	out := &syncBuffer{}
	return &module.Env{Stdin: strings.NewReader(""), Stdout: out, Stderr: io.Discard, Config: cfg}, out
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("did not happen: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// On the hub, the hotkey stores the copy in the slot the edges receive from, and an edge's copy lands on the hub's clipboard; the
// tray's Quit ends watch.
func TestHubWatchSharesItsClipboardThroughTheSlot(t *testing.T) {
	u, board := fakeDesktop(t, nil)
	env, out := clipHubEnv(t, true)
	slot := hubSlot(env.Config)
	done := make(chan error, 1)
	go func() { done <- watchCmd(context.Background(), env, nil) }()
	waitUntil(t, "the desktop", func() bool { return u.options().OnHotkey != nil })
	if u.options().Hotkey != config.DefaultClipHotkey || !u.options().Clipboard || u.options().Tray {
		t.Fatalf("a terminal watch with the clipboard: hotkey and listener, no tray: %+v", u.options())
	}

	board.write(context.Background(), clipboard.Content{Kind: clipboard.Text, Text: "copied on the hub"})
	u.options().OnHotkey()
	waitUntil(t, "the slot", func() bool { _, ok := slot.Current(time.Now()); return ok })
	cur, _ := slot.Current(time.Now())
	if d, _ := slot.Data(cur); cur.Origin != clip.OriginHub || string(d) != "copied on the hub" {
		t.Fatalf("%+v %q", cur, d)
	}

	data := []byte("copied on the edge")
	it := clip.Item{ID: clip.NewID(), Kind: clip.KindText, Size: int64(len(data)), SHA256: clip.Sum(data), Origin: "u@EDGE", Time: time.Now()}
	slot.Put(it, data)
	waitUntil(t, "the hub's clipboard", func() bool { return board.text() == "copied on the edge" })

	findEntry(u.options().Menu, "Quit").Action()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Quit must end watch")
	}
	if !u.closed || !strings.Contains(out.String(), "received text") || !strings.Contains(out.String(), "sent text") {
		t.Fatalf("closed=%v\n%s", u.closed, out)
	}
}

// What was in the slot before the hub's watch started is not pasted.
func TestHubWatchIgnoresOlderCopies(t *testing.T) {
	_, board := fakeDesktop(t, nil)
	env, _ := clipHubEnv(t, true)
	data := []byte("old")
	hubSlot(env.Config).Put(clip.Item{ID: clip.NewID(), Kind: clip.KindText, Size: 3, SHA256: clip.Sum(data), Origin: "u@EDGE",
		Time: time.Now().Add(-time.Minute)}, data)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := watchCmd(ctx, env, nil); err != nil {
		t.Fatal(err)
	}
	if board.text() != "" {
		t.Fatal("an older copy must not be pasted")
	}
}

// On the hub, watch only shares the clipboard: without the clip policy it says how to enable it.
func TestHubWatchNeedsTheClipPolicy(t *testing.T) {
	u, _ := fakeDesktop(t, nil)
	env, _ := clipHubEnv(t, false)
	err := watchCmd(context.Background(), env, nil)
	if fe, ok := fault.As(err); !ok || fe.Kind != fault.FeatureOff || !strings.Contains(fe.Hint, "policy set clip on") {
		t.Fatalf("%v", err)
	}
	if u.options().OnHotkey != nil {
		t.Fatal("no desktop without the policy")
	}
}

// A hotkey another program holds is reported, and the rest keeps working.
func TestHotkeyTakenIsReported(t *testing.T) {
	u, _ := fakeDesktop(t, errors.New("Ctrl+Shift+Alt+C is already used by another program"))
	env, out := clipHubEnv(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	watchCmd(ctx, env, nil)
	if !strings.Contains(out.String(), "the hotkey does not work") || len(u.notes) != 1 {
		t.Fatalf("%v\n%s", u.notes, out)
	}
}

// The tray's "Send every copy" switches auto mode and saves it in the configuration read again from disk.
func TestTrayMenuTogglesAutoAndSavesIt(t *testing.T) {
	env, _ := clipHubEnv(t, true)
	if err := env.Config.Save(); err != nil {
		t.Fatal(err)
	}
	_, u := trayMenu(t, env, &fakeGitsi{})
	auto := findEntry(findEntry(u.options().Menu, "Settings").Sub, "Send every copy")
	if auto == nil || auto.Checked {
		t.Fatalf("Settings offers auto mode, off by default: %+v", u.options().Menu)
	}
	auto.Action()
	waitUntil(t, "the saved configuration", func() bool { c, _ := config.Load(); return c.Clip.Auto })
}

func TestAutostartIsForTheHubOnly(t *testing.T) {
	fa := &fakeAutostart{}
	old := autostart
	autostart = fa
	defer func() { autostart = old }()

	env, out := clipHubEnv(t, true)
	if err := watchCmd(context.Background(), env, []string{"-autostart", "on"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fa.cmd, `" watch -tray`) || !strings.HasPrefix(fa.cmd, `"`) || !strings.Contains(out.String(), "log on") {
		t.Fatalf("%q\n%s", fa.cmd, out)
	}
	if err := watchCmd(context.Background(), env, []string{"-autostart", "off"}); err != nil || fa.cmd != "" {
		t.Fatalf("%v %q", err, fa.cmd)
	}
	if err := watchCmd(context.Background(), env, []string{"-autostart", "maybe"}); err == nil {
		t.Fatal("only on or off")
	}

	edge := &module.Env{Stdout: io.Discard, Stderr: io.Discard, Config: config.Default()}
	if err := watchCmd(context.Background(), edge, []string{"-autostart", "on"}); err == nil || !strings.Contains(err.Error(), "for the hub") || fa.cmd != "" {
		t.Fatalf("the edge never starts on its own: %v %q", err, fa.cmd)
	}
}

type fakeAutostart struct{ cmd string }

func (f *fakeAutostart) Set(c string) error         { f.cmd = c; return nil }
func (f *fakeAutostart) Remove() error              { f.cmd = ""; return nil }
func (f *fakeAutostart) Get() (string, bool, error) { return f.cmd, f.cmd != "", nil }

// A second watch on the same machine is refused with an explanation.
func TestSecondInstanceIsRefused(t *testing.T) {
	fakeDesktop(t, nil)
	lock = func() (func(), error) { return nil, desktop.ErrRunning }
	env, _ := clipHubEnv(t, true)
	if err := watchCmd(context.Background(), env, nil); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("%v", err)
	}
	if err := detach(env); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("-tray must refuse too: %v", err)
	}
}

// The tray process writes to a log that rotates instead of growing without end.
func TestTrayLogRotates(t *testing.T) {
	old := logMaxSize
	logMaxSize = 100
	defer func() { logMaxSize = old }()
	p := filepath.Join(t.TempDir(), "watch.log")
	l, err := openLog(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		l.Write([]byte("a line of the log\n"))
	}
	l.Close()
	cur, _ := os.Stat(p)
	prev, err := os.Stat(p + ".1")
	if err != nil || cur.Size() > 200 || prev.Size() > 200 {
		t.Fatalf("rotated: %v", err)
	}
	if got := tail(p, 2); strings.Count(got, "\n") != 1 {
		t.Fatalf("tail: %q", got)
	}
}

// An edge without the clip policy says how to enable it and starts no desktop (watch stays a plain terminal program).
func TestEdgeWithoutClipStartsNoDesktop(t *testing.T) {
	u, _ := fakeDesktop(t, nil)
	out := &syncBuffer{}
	env := &module.Env{Stdout: out, Stderr: io.Discard, Config: config.Default()}
	s, err := newSession(env, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.startUI(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.options().Menu != nil || !strings.Contains(out.String(), "policy set clip on") {
		t.Fatalf("%+v\n%s", u.options(), out)
	}
}

// When the desktop window goes away from outside, watch stops instead of living on without its icon.
func TestWatchStopsWhenTheWindowCloses(t *testing.T) {
	u, _ := fakeDesktop(t, nil)
	env, _ := clipHubEnv(t, true)
	done := make(chan error, 1)
	go func() { done <- watchCmd(context.Background(), env, nil) }()
	waitUntil(t, "the desktop", func() bool { return u.options().Menu != nil })
	close(u.done)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watch must stop with its window")
	}
}

// The hub always offers the clipboard and reads its policy again (at most once a second) instead of fixing it per session.
func TestHubHandlerReadsThePolicyLive(t *testing.T) {
	env, _ := clipHubEnv(t, false)
	_, caps := HubHandler(env)
	if strings.Join(caps, ",") != "watch,clip" {
		t.Fatalf("the clipboard is always offered (the policy is checked live): %v", caps)
	}
	on := false
	old := loadConfig
	loadConfig = func() (*config.Config, error) { c := config.Default(); c.Policy.AllowClip = on; return c, nil }
	defer func() { loadConfig = old }()
	now := time.Now()
	allowed := livePolicy(false, func() time.Time { return now })
	on = true
	if allowed() {
		t.Fatal("within a second the last value stands")
	}
	now = now.Add(time.Second)
	if !allowed() {
		t.Fatal("after a second the policy is read again")
	}
	loadConfig = func() (*config.Config, error) { return nil, errors.New("being replaced") }
	now = now.Add(time.Second)
	if !allowed() {
		t.Fatal("an unreadable file keeps the last known value")
	}
}

// The lock belongs to the profile: another profile (the end-to-end tests) is not blocked by the user's real watch.
func TestLockNameDependsOnTheProfile(t *testing.T) {
	a := lockName()
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", os.Getenv("USERPROFILE"))
	if b := lockName(); a == b || !strings.HasPrefix(b, "gitsi-watch-") {
		t.Fatalf("%q %q", a, b)
	}
}
