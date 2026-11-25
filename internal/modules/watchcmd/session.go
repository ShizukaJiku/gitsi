package watchcmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/cleanup"
	"github.com/ShizukaJiku/gitsi/internal/clip"
	"github.com/ShizukaJiku/gitsi/internal/clipboard"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// childEnv marks the process 'watch -tray' starts detached: it is the one that stays in the tray.
const childEnv = "GITSI_WATCH_TRAY"

// lockName names the lock: one watch per profile, in the tray or in a terminal. It includes the configuration's path, which is
// what two instances would fight over (state, log, clipboard): a user's real watch and one run with another profile (the
// end-to-end tests) do not block each other.
func lockName() string {
	p, _ := config.Path()
	sum := sha256.Sum256([]byte(strings.ToLower(p)))
	return "gitsi-watch-" + hex.EncodeToString(sum[:8])
}

// ui is the part of the desktop watch uses (a fake in tests).
type ui interface {
	SetTooltip(string)
	SetMenu([]desktop.MenuItem)
	SetIcon(desktop.IconState)
	Notify(title, message string, warning bool)
	Confirm(title, text string) bool
	Message(title, text string)
	PickFolder(title string) (string, bool)
	Close()
	Done() <-chan struct{}
	WriteText(string) error
}

// Seams for tests: the desktop, the clipboard and the lock.
var (
	startUI = func(o desktop.Options) (ui, error, error) {
		d, err := desktop.Start(o)
		if err != nil {
			return nil, nil, err
		}
		return d, d.HotkeyErr, nil
	}
	readText                   = desktop.ReadText
	readClip  clipboard.Reader = clipboard.Read
	writeClip clipboard.Writer = clipboard.Write
	clipSeq                    = desktop.Seq
	clipPriv                   = desktop.Private
	lock                       = func() (func(), error) { return desktop.Lock(lockName()) }
)

// session is one run of watch: its output (a terminal or the log file), its clipboard sharing and its tray.
type session struct {
	env    *module.Env
	tray   bool
	clipOn bool
	out    io.Writer
	log    *rotatingLog
	sharer *clip.Sharer
	ui     ui
	ctx    context.Context // the session's, for what the menu starts
	cancel func()

	// What the tray menu shows, read in the background (menu*.go), and the tunnels it opened.
	mailbox  cache[[]mailbox.Item]
	repos    cache[[]repoState]
	bridge   cache[[]string]
	services cache[[]svcRow]
	jobs     cache[[]jobRow]
	edges    cache[[]watch.EdgeState]
	tunnels  tunnels
	bg       sync.WaitGroup // what the session started in the background (close waits for it)
	closing  bool           // set by close under mu: nothing new starts in the background after it

	mu        sync.Mutex
	connected bool
	last      string // the last clipboard line, for the tooltip
}

func newSession(env *module.Env, tray bool) (*session, error) {
	s := &session{env: env, tray: tray, clipOn: policy.Check(env.Config.Policy, policy.Clip) == nil}
	s.out = &syncWriter{w: env.Stdout}
	if tray {
		p, err := logPath()
		if err != nil {
			return nil, err
		}
		if s.log, err = openLog(p); err != nil {
			return nil, err
		}
		s.out = s.log
	}
	if s.clipOn {
		root, err := cleanup.ClipRoot()
		if err != nil {
			return nil, err
		}
		s.sharer = &clip.Sharer{
			Read: s.read, Write: s.write, Seq: clipSeq, Private: clipPriv,
			MaxSize: env.Config.Clip.MaxBytes(), FilesRoot: root,
			Log:   s.clipLine,
			Audit: s.audit,
		}
		s.sharer.SetAuto(env.Config.Clip.Auto)
	}
	return s, nil
}

// read reads the clipboard: a text directly (milliseconds), anything else through PowerShell (most of a second).
func (s *session) read(ctx context.Context) (clipboard.Content, error) {
	if t, ok, err := readText(); err == nil && ok {
		if t == "" {
			return clipboard.Content{}, clipboard.ErrEmpty
		}
		return clipboard.Content{Kind: clipboard.Text, Text: t}, nil
	}
	return readClip(ctx)
}

// write writes the clipboard: a text directly through the desktop window, anything else (or a text the direct way refuses)
// through PowerShell.
func (s *session) write(ctx context.Context, c clipboard.Content) error {
	s.mu.Lock()
	u := s.ui
	s.mu.Unlock()
	if u != nil && c.Kind == clipboard.Text && len(c.PNG) == 0 && len(c.Files) == 0 {
		if err := u.WriteText(c.Text); err == nil {
			return nil
		}
	}
	return writeClip(ctx, c)
}

func (s *session) printf(format string, a ...any) {
	fmt.Fprintf(s.out, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

// clipLine prints a clipboard line and keeps it for the tooltip.
func (s *session) clipLine(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	s.printf("%s", line)
	s.mu.Lock()
	s.last = time.Now().Format("15:04") + " " + line
	s.mu.Unlock()
	s.refresh()
}

func (s *session) audit(action, detail string) { _ = policy.Audit(s.env.Config.Policy, action, detail) }

func (s *session) quitHint() string {
	if s.tray {
		return "Quit it from the tray icon's menu."
	}
	return "Ctrl+C to quit."
}

// setConnected is told by the edge's client when a session starts or ends.
func (s *session) setConnected(on bool) {
	s.mu.Lock()
	s.connected = on
	s.mu.Unlock()
	s.refresh()
}

// startUI starts the desktop when something needs it: the clipboard (hotkey and change listener) or the tray. Without either,
// watch runs exactly as a plain terminal program.
func (s *session) startUI(ctx context.Context) error {
	if s.clipOn {
		hk := desktop.HotkeyLabel(s.env.Config.Clip.HotkeyOrDefault())
		how := hk + " sends what you copied"
		if s.sharer.Auto() {
			how = "every copy is sent (clip.auto)"
		}
		s.printf("clipboard shared: %s; what the other machine sends is pasted with Ctrl+V.", how)
	} else if s.env.Config.Role != config.RoleHub {
		s.printf("clipboard sharing is off (to share it: gitsi policy set clip on).")
	}
	if !s.clipOn && !s.tray {
		return nil
	}
	o := desktop.Options{Tray: s.tray, Tooltip: s.tooltip(), Icon: s.icon(), Menu: s.menu(ctx), OnOpen: func() { s.background(func() { s.onOpen(ctx) }) }}
	if s.clipOn {
		o.Hotkey, o.Clipboard = s.env.Config.Clip.HotkeyOrDefault(), true
		o.OnHotkey = func() { go func() { _ = s.sharer.Share(ctx, false) }() }
		o.OnClipboard = func() { s.sharer.Changed(ctx) }
	}
	u, hotkeyErr, err := startUI(o)
	if err != nil {
		return fmt.Errorf("could not start the clipboard sharing or the tray: %w", err)
	}
	s.mu.Lock()
	s.ui, s.ctx = u, ctx
	s.mu.Unlock()
	s.background(func() { s.refreshAll(ctx) }) // the first lists, so the menu has them by the time it opens
	go func() {
		select {
		case <-u.Done(): // the window was closed from outside: watch stops with it
			if s.cancel != nil {
				s.cancel()
			}
		case <-ctx.Done():
		}
	}()
	if s.clipOn && s.tray {
		s.sharer.Notify = func(title, msg string) { u.Notify(title, msg, true) }
	}
	if hotkeyErr != nil {
		s.printf("the hotkey does not work: %v", hotkeyErr)
		u.Notify("gitsi: clipboard", "The hotkey does not work: "+hotkeyErr.Error(), true)
	}
	return nil
}

func (s *session) tooltip() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := "gitsi watch: " + s.state()
	if s.last != "" {
		t += "\n" + s.last
	}
	return t
}

// state describes the connection; the caller holds mu.
func (s *session) state() string {
	switch {
	case s.env.Config.Role == config.RoleHub:
		return "sharing the hub's clipboard"
	case s.connected:
		return "connected to the hub"
	}
	return "not connected (retrying)"
}

// icon is the state the tray icon shows.
func (s *session) icon() desktop.IconState {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.env.Config.Role == config.RoleHub:
		return desktop.IconHub
	case s.connected:
		return desktop.IconConnected
	}
	return desktop.IconRetrying
}

// refresh updates the tooltip, the icon and the menu after a change.
func (s *session) refresh() {
	s.mu.Lock()
	u, ctx := s.ui, s.ctx
	s.mu.Unlock()
	if u != nil {
		u.SetTooltip(s.tooltip())
		u.SetIcon(s.icon())
		u.SetMenu(s.menu(ctx))
	}
}

// saveAuto stores clip.auto in the configuration read again from disk: this process has been running for a while and its copy
// may be older than what another command saved since.
var saveAuto = func(on bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Clip.Auto = on
	return cfg.Save()
}

// background runs f on a goroutine that close waits for; once close has begun, f does not run.
func (s *session) background(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		f()
	}()
}

// close ends the session: its context (a child gitsi the menu started is stopped with it), the background work, the desktop and
// the log.
func (s *session) close() {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.tunnels.closeAll() // their goroutines end with them; close waits for those below
	s.bg.Wait()
	s.mu.Lock()
	u := s.ui
	s.ui = nil
	s.mu.Unlock()
	if u != nil {
		u.Close()
	}
	if s.log != nil {
		s.log.Close()
	}
}

// syncWriter serializes writes from several goroutines (the session, the hotkey, the reactions) so lines do not mix.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// logMaxSize is the size past which watch.log becomes watch.log.1 (nothing piles up).
var logMaxSize int64 = 1 << 20

// rotatingLog is where watch writes in the tray (there is no terminal).
type rotatingLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// logPath is %LOCALAPPDATA%\gitsi\watch.log.
func logPath() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "gitsi", "watch.log"), nil
}

func openLog(p string) (*rotatingLog, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &rotatingLog{path: p, f: f}, nil
}

func (l *rotatingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return len(b), nil
	}
	if st, err := l.f.Stat(); err == nil && st.Size() >= logMaxSize {
		l.f.Close()
		_ = os.Remove(l.path + ".1")
		_ = os.Rename(l.path, l.path+".1")
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			l.f = nil
			return 0, err
		}
		l.f = f
	}
	return l.f.Write(b)
}

func (l *rotatingLog) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// tail returns the last lines of the log (what the tray process said before it stopped).
func tail(p string, n int) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
