package watchcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// The tray menu is gitsi for people who would rather not type commands: the mailbox, the repos, the hub's services and jobs, the
// edges, the settings. Each entry runs gitsi itself (`gitsi take ...`) as a child process, never another module's code: the same
// policy checks, audit and messages as at a terminal, and watch keeps depending on no other module (CONTRIBUTING.md, D27). The
// lists it shows come from caches refreshed in the background, because the menu is built before it opens and must not wait for
// the network.

// runSelf runs gitsi with some arguments, in dir ("" = this process's folder), without interactive input (a missing policy fails
// and says so instead of asking), and returns its output and its errors apart: stdout may be JSON (a variable in tests).
var runSelf = func(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(""), &out, &errb
	err = procx.Background(cmd).Run()
	return out.String(), errb.String(), err
}

// openFolder shows a folder in Explorer (a variable in tests).
var openFolder = func(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return exec.Command("explorer.exe", dir).Start()
}

// openFile opens a text file in Notepad (a variable in tests).
var openFile = func(path string) error { return exec.Command("notepad.exe", path).Start() }

// Timeouts of the menu's children: a list is quick or the hub is not there; an action may move a large file.
var (
	listTimeout   = time.Minute
	actionTimeout = 2 * time.Hour
)

// How old each list may be when the menu opens before it is read again (for the next opening).
const (
	listStale  = 20 * time.Second
	reposStale = time.Minute // a repo's state needs a fetch from the hub
	edgesStale = 5 * time.Second
)

// cache is the last value of a list the menu shows, read in the background.
type cache[T any] struct {
	mu   sync.Mutex
	val  T
	err  string // why the last read failed ("" = it worked)
	at   time.Time
	busy bool
}

func (c *cache[T]) get() (T, string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val, c.err, c.at
}

func (c *cache[T]) stale(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.busy && time.Since(c.at) > d
}

// load reads the value again with fn, unless a read is already under way (then that one is enough). A failed read keeps the last
// value and records why.
func (c *cache[T]) load(fn func() (T, string)) {
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return
	}
	c.busy = true
	c.mu.Unlock()
	v, reason := fn()
	c.mu.Lock()
	c.busy, c.at, c.err = false, time.Now(), reason
	if reason == "" {
		c.val = v
	}
	c.mu.Unlock()
}

// listJSON runs `gitsi --json <args>` and decodes its output into v; it returns why it failed ("" = it worked).
func listJSON(ctx context.Context, v any, args ...string) string {
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	out, errOut, err := runSelf(lctx, "", append([]string{"--json"}, args...)...)
	if err != nil {
		return lastLine(errOut, err)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return "could not read the list: " + err.Error()
	}
	return ""
}

// refreshAll reads every list the menu shows (when watch starts).
func (s *session) refreshAll(ctx context.Context) {
	s.refreshMailbox(ctx)
	s.refreshEdges()
	s.refreshServices(ctx)
	s.refreshJobs(ctx)
	s.refreshRepos(ctx, true)
}

// onOpen reads again the lists that are old when the menu opens: this opening shows the cache, the next one the new lists.
func (s *session) onOpen(ctx context.Context) {
	if s.mailbox.stale(listStale) {
		s.refreshMailbox(ctx)
	}
	if s.edges.stale(edgesStale) {
		s.refreshEdges()
	}
	if s.services.stale(listStale) {
		s.refreshServices(ctx)
	}
	if s.jobs.stale(listStale) {
		s.refreshJobs(ctx)
	}
	if s.repos.stale(reposStale) {
		s.refreshRepos(ctx, true)
	}
}

func (s *session) isHub() bool { return s.env.Config.Role == config.RoleHub }

// menu builds the tray menu from the current state. Every action runs on a goroutine of its own (the desktop starts it).
func (s *session) menu(ctx context.Context) []desktop.MenuItem {
	s.mu.Lock()
	state, last := s.state(), s.last
	s.mu.Unlock()
	items := []desktop.MenuItem{{Label: menuText(strings.ToUpper(state[:1])+state[1:], 60), Note: s.headerNote(last),
		Default: true, Icon: desktop.GlyphLogo}, {}}
	if s.clipOn {
		items = append(items, desktop.MenuItem{Label: "Send what I copied\t" + desktop.HotkeyLabel(s.env.Config.Clip.HotkeyOrDefault()),
			Icon: desktop.GlyphSend, Action: func() { _ = s.sharer.Share(ctx, false) }})
	}
	quit := desktop.MenuItem{Label: "Quit", Icon: desktop.GlyphQuit, Action: func() {
		s.printf("quitting from the tray menu.")
		if s.cancel != nil {
			s.cancel()
		}
	}}
	if !s.tray {
		return append(items, desktop.MenuItem{}, quit) // without the tray nobody opens it: no lists to read
	}
	items = append(items, s.takeLatest(ctx)...)
	items = append(items, desktop.MenuItem{}, s.mailboxMenu(ctx), s.reposMenu(ctx))
	if s.isHub() {
		items = append(items, s.edgesMenu(ctx))
	} else {
		items = append(items, s.servicesMenu(ctx), s.jobsMenu(ctx))
	}
	check := "Check the connection"
	if s.isHub() {
		check = "Check this hub"
	}
	items = append(items, desktop.MenuItem{},
		desktop.MenuItem{Label: check, Icon: desktop.GlyphPulse, Action: func() { s.check(ctx) }},
		s.settingsMenu(ctx))
	if s.log != nil {
		items = append(items, desktop.MenuItem{Label: "Open the log", Icon: desktop.GlyphFile, Action: func() { _ = openFile(s.log.path) }})
	}
	return append(items, desktop.MenuItem{}, quit)
}

// headerNote is the title's second line: the last clipboard item, else who listens (hub) or that watch waits for the hub.
func (s *session) headerNote(last string) string {
	if last != "" {
		return menuText("Last: "+last, 70)
	}
	if s.isHub() {
		es, _, _ := s.edges.get()
		n := 0
		for _, e := range es {
			if e.Connected {
				n++
			}
		}
		switch n {
		case 0:
			return "No edge listening"
		case 1:
			return "1 edge listening"
		}
		return fmt.Sprintf("%d edges listening", n)
	}
	return ""
}

// act runs gitsi for a menu action and says how it went in a notification: gitsi's last line, or done when it printed nothing.
// what names the action for a failure ("take report.pdf"). It reports whether it worked.
func (s *session) act(ctx context.Context, dir, what, done string, args ...string) bool {
	actx, cancel := context.WithTimeout(ctx, actionTimeout)
	out, errOut, err := runSelf(actx, dir, args...)
	cancel()
	if err != nil {
		s.printf("tray: gitsi %s failed: %s", strings.Join(args, " "), lastLine(errOut, err))
		s.notify("gitsi: could not "+what, lastLine(errOut, err), true)
		return false
	}
	s.printf("tray: gitsi %s", strings.Join(args, " "))
	msg := done
	if l := lastNonEmpty(out); l != "" {
		msg = l
	}
	s.notify("gitsi", msg, false)
	return true
}

// check runs `gitsi check` and shows its report.
func (s *session) check(ctx context.Context) {
	actx, cancel := context.WithTimeout(ctx, listTimeout)
	out, errOut, _ := runSelf(actx, "", "check")
	cancel()
	if u := s.currentUI(); u != nil {
		u.Message("gitsi check", cutText(strings.TrimSpace(out+"\n"+errOut), "run gitsi check in a terminal for the rest"))
	}
}

// maxView caps the text shown in a dialog: a dialog is for a glance.
const maxView = 2000

// cutText cuts a text for a dialog, saying where the rest is.
func cutText(text, rest string) string {
	text = strings.ToValidUTF8(text, "?")
	if r := []rune(text); len(r) > maxView {
		return string(r[:maxView]) + "\n\n... (" + rest + ")"
	}
	return text
}

// liveConfig is the configuration as it is on disk now: the menu itself changes it (settings, policies) while watch runs.
func (s *session) liveConfig() *config.Config {
	if cfg, err := loadConfig(); err == nil {
		return cfg
	}
	return s.env.Config
}

// featureOn reports whether this machine may use a feature now. On the hub only clip is checked (the rest need no policy there).
func (s *session) featureOn(f policy.Feature) bool {
	cfg := s.liveConfig()
	if cfg.Role == config.RoleHub && f != policy.Clip {
		return true
	}
	return policy.Check(cfg.Policy, f) == nil
}

// featureTitles name the features in the menu.
var featureTitles = map[policy.Feature]string{
	policy.Send:     "Sending to the hub's mailbox",
	policy.Take:     "Receiving from the hub",
	policy.Services: "Hub services (tunnels)",
	policy.Exec:     "Running commands on the hub",
	policy.Clip:     "Shared clipboard",
}

// enableFeature turns a feature on after the user agrees, through `gitsi policy set`, which records it in audit.log: the same
// conscious, audited act as at a terminal. It reports whether the feature is on now.
func (s *session) enableFeature(ctx context.Context, f policy.Feature) bool {
	u := s.currentUI()
	if u == nil {
		return false
	}
	if !u.Confirm("gitsi", fmt.Sprintf("Turn on %s?\n\nIt %s\n\nIt is recorded in audit.log. Turn it off again in Settings > Features.",
		strings.ToLower(featureTitles[f]), policy.Effects[f])) {
		return false
	}
	actx, cancel := context.WithTimeout(ctx, listTimeout)
	_, errOut, err := runSelf(actx, "", "policy", "set", string(f), "on")
	cancel()
	if err != nil {
		s.notify("gitsi: could not turn it on", lastLine(errOut, err), true)
		return false
	}
	s.printf("tray: the %s policy was turned on", f)
	if f == policy.Clip {
		s.notify("gitsi", "The shared clipboard starts with watch: quit it from this menu and run gitsi watch -tray again.", false)
	}
	s.refreshAll(ctx)
	return true
}

// offNote is what a submenu shows while its feature is off: why, and how to turn it on.
func (s *session) offNote(ctx context.Context, f policy.Feature, what string) []desktop.MenuItem {
	return []desktop.MenuItem{
		{Label: what + " is off on this machine", Disabled: true},
		{Label: "Turn it on...", Icon: desktop.GlyphLock, Action: func() { s.enableFeature(ctx, f) }},
	}
}

func (s *session) currentUI() ui {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ui
}

// notify shows a notification; the texts may come from a child's output, so they are cleaned first.
func (s *session) notify(title, msg string, warning bool) {
	if u := s.currentUI(); u != nil {
		u.Notify(watch.Printable(title, 63), watch.Printable(msg, 255), warning)
	}
}

// confirm asks a question; without a desktop the answer is no.
func (s *session) confirm(text string) bool {
	u := s.currentUI()
	return u != nil && u.Confirm("gitsi", text)
}

// lastNonEmpty is the last line of a child's output that has something on it ("" when none).
func lastNonEmpty(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// readAt says when a list was read, for a submenu's first line.
func readAt(at time.Time) string { return "read at " + at.Format("15:04") }

// countLabel is a submenu title with a count in the right-hand column ("Jobs   2 running"), or the title alone.
func countLabel(title string, n int, what string) string {
	if n <= 0 {
		return title
	}
	if what == "" {
		return fmt.Sprintf("%s\t%d", title, n)
	}
	return fmt.Sprintf("%s\t%d %s", title, n, what)
}

// menuText makes an outside text safe for a menu label: no control characters (watch.Printable; a tab would start the right-hand
// column) and an "&" that shows as itself instead of underlining the next letter.
func menuText(s string, max int) string {
	return strings.ReplaceAll(watch.Printable(s, max), "&", "&&")
}
