package watchcmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/workdirs"
)

// fakeGitsi stands in for the gitsi child processes the menu starts.
type fakeGitsi struct {
	mu    sync.Mutex
	calls []string
	dirs  map[string]string // a call -> the folder it ran in
	list  []mailbox.Item
	json  map[string]any    // a --json call -> what it prints
	fail  map[string]string // a call (its arguments joined by spaces) -> the error line it prints
	out   map[string]string
}

func (f *fakeGitsi) run(_ context.Context, dir string, args ...string) (string, string, error) {
	call := strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.dirs == nil {
		f.dirs = map[string]string{}
	}
	f.dirs[call] = dir
	if msg, ok := f.fail[call]; ok {
		return "", "gitsi: " + msg + "\n", errors.New("exit status 11")
	}
	if call == "--json take -list" {
		b, _ := json.Marshal(f.list)
		return string(b), "", nil
	}
	if v, ok := f.json[call]; ok {
		b, _ := json.Marshal(v)
		return string(b), "", nil
	}
	if strings.HasPrefix(call, "--json ") {
		return "[]", "", nil
	}
	return f.out[call], "", nil
}

func (f *fakeGitsi) called(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *fakeGitsi) set(fn func()) { f.mu.Lock(); fn(); f.mu.Unlock() }

// trayMenu starts a tray session with a fake desktop and fake gitsi children and waits for the first lists.
func trayMenu(t *testing.T, env *module.Env, g *fakeGitsi) (*session, *fakeUI) {
	t.Helper()
	u, _ := fakeDesktop(t, nil)
	runSelf = g.run
	s, err := newSession(env, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); s.close() })
	s.cancel = cancel
	if err := s.startUI(ctx); err != nil {
		t.Fatal(err)
	}
	return s, u
}

func waitForEntry(t *testing.T, u *fakeUI, prefix string) *desktop.MenuItem {
	t.Helper()
	var e *desktop.MenuItem
	waitUntil(t, "the entry "+prefix, func() bool { e = findEntry(u.lastMenu(), prefix); return e != nil })
	return e
}

// edgeEnv is an edge with the given policies, read live by the menu through loadConfig.
func edgeEnv(t *testing.T, policies ...string) *module.Env {
	cfg := config.Default()
	for _, p := range policies {
		switch p {
		case "take":
			cfg.Policy.AllowTake = true
		case "send":
			cfg.Policy.AllowSend = true
		case "services":
			cfg.Policy.AllowServices = true
		case "exec":
			cfg.Policy.AllowExec = true
		}
	}
	old := loadConfig
	loadConfig = func() (*config.Config, error) { return cfg, nil }
	t.Cleanup(func() { loadConfig = old })
	return &module.Env{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, Config: cfg}
}

var (
	textItem = mailbox.Item{ID: "0123456789abcdef", Kind: mailbox.Text, Name: "notes & ideas", Size: 12, Created: time.Now()}
	fileItem = mailbox.Item{ID: "fedcba9876543210", Kind: mailbox.File, Name: "report.pdf", Size: 2 << 20, Created: time.Now()}
)

// The menu lists the mailbox from `gitsi --json take -list`, one submenu per item; taking one runs `gitsi take`, says how it went
// and reads the list again. An id that is not an id (it would read as an option) never reaches the menu.
func TestTrayMenuListsTheMailboxAndTakes(t *testing.T) {
	env, _ := clipHubEnv(t, true)
	g := &fakeGitsi{list: []mailbox.Item{textItem, fileItem, {ID: "-rm", Kind: mailbox.File, Name: "trap"}},
		out: map[string]string{"take -clip " + textItem.ID: "Copied to the clipboard: notes & ideas"}}
	_, u := trayMenu(t, env, g)

	waitUntil(t, "the list", func() bool { e := findEntry(u.lastMenu(), "Inbox"); return e != nil && e.Badge == "2" })
	inbox := findEntry(u.lastMenu(), "Inbox")
	if findEntry(inbox.Sub, "trap") != nil {
		t.Fatal("an item with an invalid id must not be listed")
	}
	text := findEntry(inbox.Sub, "notes && ideas\ttext")
	if text == nil || findEntry(inbox.Sub, "report.pdf\t2.0 MiB") == nil {
		t.Fatalf("each item with its size (& escaped for the menu): %+v", inbox.Sub)
	}
	if text.Icon != desktop.GlyphText || findEntry(inbox.Sub, "report.pdf").Icon != desktop.GlyphFile {
		t.Fatal("each item with its kind's glyph")
	}
	if findEntry(findEntry(inbox.Sub, "report.pdf").Sub, "View it") != nil || findEntry(text.Sub, "View it") == nil {
		t.Fatal("only a text can be viewed")
	}
	findEntry(text.Sub, "Take it to the clipboard").Action()
	if g.called("take -clip "+textItem.ID) != 1 || g.called("--json take -list") < 2 {
		t.Fatalf("take, then read the list again: %v", g.calls)
	}
	if !strings.Contains(u.noted(), "Copied to the clipboard: notes & ideas") {
		t.Fatalf("the result is notified: %s", u.noted())
	}

	g.set(func() { g.fail = map[string]string{"take " + fileItem.ID: "could not connect to the hub"} })
	findEntry(findEntry(u.lastMenu(), "report.pdf").Sub, `Take it to Downloads\gitsi`).Action()
	if !strings.Contains(u.noted(), "could not take report.pdf: gitsi: could not connect to the hub") {
		t.Fatalf("a failure is notified with its cause: %s", u.noted())
	}
}

// The header names the state in bold with the mark; the first level is the menu of the design.
func TestTrayMenuLayout(t *testing.T) {
	env, _ := clipHubEnv(t, true)
	_, u := trayMenu(t, env, &fakeGitsi{})
	waitForEntry(t, u, "Inbox")
	top := u.lastMenu()
	if !top[0].Default || top[0].Icon != desktop.GlyphLogo || top[0].Label != "Sharing the hub's clipboard" || top[0].Note != "No edge listening" {
		t.Fatalf("the header: %+v", top[0])
	}
	var labels []string
	for _, it := range top {
		if it.Label != "" && !it.Disabled && !it.Default {
			labels = append(labels, strings.SplitN(it.Label, "\t", 2)[0])
		}
	}
	want := "Send what I copied,Take the latest to the clipboard,Inbox,Repos,Edges,Check this hub,Settings,Open the log,Quit"
	if strings.Join(labels, ",") != want {
		t.Fatalf("hub menu:\n%s\nwant\n%s", strings.Join(labels, ","), want)
	}
	if findEntry(top, "Send every copy") == nil || findEntry(findEntry(top, "Settings").Sub, "Send every copy") == nil {
		t.Fatal("Send every copy lives in Settings")
	}
}

// Discarding asks first, with No as the answer unless the user says yes.
func TestTrayDiscardAsksFirst(t *testing.T) {
	env, _ := clipHubEnv(t, true)
	g := &fakeGitsi{list: []mailbox.Item{fileItem}}
	_, u := trayMenu(t, env, g)
	discard := findEntry(waitForEntry(t, u, "report.pdf").Sub, "Discard")
	discard.Action()
	if q := u.questions(); g.called("take -rm "+fileItem.ID) != 0 || len(q) != 1 || !strings.Contains(q[0], "Discard report.pdf?") {
		t.Fatalf("not without a yes: %v %v", g.calls, q)
	}
	u.set(true, "")
	discard.Action()
	if g.called("take -rm "+fileItem.ID) != 1 {
		t.Fatalf("discarded after the yes: %v", g.calls)
	}
}

// On an edge without the take policy the menu offers to turn it on: explained, asked, and done through `gitsi policy set`, which
// records it.
func TestTrayTurnsTakeOnOnlyAfterAsking(t *testing.T) {
	env := edgeEnv(t)
	g := &fakeGitsi{}
	_, u := trayMenu(t, env, g)
	on := findEntry(waitForEntry(t, u, "Mailbox").Sub, "Turn it on")
	on.Action()
	if q := u.questions(); g.called("policy set take on") != 0 || len(q) != 1 || !strings.Contains(q[0], "receive what the hub leaves") {
		t.Fatalf("explained and asked first: %v %v", g.calls, q)
	}
	u.set(true, "")
	on.Action()
	if g.called("policy set take on") != 1 {
		t.Fatalf("%v", g.calls)
	}
	if g.called("--json take -list") != 0 {
		t.Fatal("without the policy the mailbox is never read")
	}
	if findEntry(u.lastMenu(), "Take the latest") != nil {
		t.Fatal("no shortcut to take while it is off")
	}
}

// When the list cannot be read the menu says why instead of showing an empty mailbox.
func TestTrayMenuSaysWhyTheListIsMissing(t *testing.T) {
	env, _ := clipHubEnv(t, true)
	g := &fakeGitsi{fail: map[string]string{"--json take -list": "could not connect to the hub (192.0.2.10)"}}
	_, u := trayMenu(t, env, g)
	waitForEntry(t, u, "Could not read it: gitsi: could not connect to the hub")
	if e := findEntry(u.lastMenu(), "Take the latest"); e == nil || e.Disabled {
		t.Fatal("with the list unknown, taking the latest stays possible")
	}
}

// Viewing a text shows it in a dialog, cut when it is long.
func TestTrayViewShowsTheText(t *testing.T) {
	env, _ := clipHubEnv(t, true)
	g := &fakeGitsi{list: []mailbox.Item{textItem}, out: map[string]string{"take -show " + textItem.ID: strings.Repeat("a", maxView+50)}}
	_, u := trayMenu(t, env, g)
	findEntry(waitForEntry(t, u, "notes && ideas").Sub, "View it").Action()
	if m := u.messages(); len(m) != 1 || !strings.HasPrefix(m[0], "notes & ideas: aaa") || !strings.Contains(m[0], "take it to the clipboard") {
		t.Fatalf("%q", m)
	}
}

// Sending what was copied asks for the send policy first on an edge without it, then runs `gitsi send`.
func TestTraySendsWhatWasCopied(t *testing.T) {
	env := edgeEnv(t, "take")
	g := &fakeGitsi{}
	_, u := trayMenu(t, env, g)
	send := findEntry(waitForEntry(t, u, "Mailbox").Sub, "Send what I copied to the hub")
	send.Action()
	if g.called("send") != 0 || len(u.questions()) != 1 {
		t.Fatalf("the send policy is asked for first: %v %v", g.calls, u.questions())
	}
	u.set(true, "")
	send.Action()
	if g.called("policy set send on") != 1 || g.called("send") != 1 {
		t.Fatalf("%v", g.calls)
	}
}

// The icon shows the state: blue on the hub; green or amber on the edge as its session comes and goes.
func TestTrayIconFollowsTheState(t *testing.T) {
	env, _ := clipHubEnv(t, true)
	hub, u := trayMenu(t, env, &fakeGitsi{})
	if u.options().Icon != desktop.IconHub {
		t.Fatalf("hub: %v", u.options().Icon)
	}
	hub.close() // before the next session replaces the fakes

	s, u := trayMenu(t, edgeEnv(t), &fakeGitsi{})
	if u.options().Icon != desktop.IconRetrying {
		t.Fatalf("an edge starts not connected: %v", u.options().Icon)
	}
	s.setConnected(true)
	u.mu.Lock()
	last := u.icons[len(u.icons)-1]
	u.mu.Unlock()
	if last != desktop.IconConnected {
		t.Fatalf("connected: %v", last)
	}
}

// Repos: each recorded folder with its state; sync runs `gitsi repo sync` in the folder; the hub's repos without a folder here
// can be got into a folder the user picks.
func TestTrayRepos(t *testing.T) {
	env := edgeEnv(t)
	base := t.TempDir()
	web, notes := filepath.Join(base, "webapp"), filepath.Join(base, "notes")
	g := &fakeGitsi{json: map[string]any{"--json repo list": []string{"webapp", "notes", "demo", "-trap"}}}
	fakeDesktop(t, nil)
	listWorkdirs = func() ([]workdirs.Dir, error) {
		return []workdirs.Dir{{Repo: "webapp", Path: web}, {Repo: "notes", Path: notes}}, nil
	}
	var fetched sync.Map
	inspectRepo = func(_ context.Context, d workdirs.Dir, remotes []string, fetch bool) repoState {
		if fetch {
			fetched.Store(d.Repo, true)
		}
		if d.Repo == "webapp" {
			return repoState{Repo: d.Repo, Path: d.Path, Branch: "main", Remote: remotes[0], Behind: 2, OnBridge: true, Changed: 1}
		}
		return repoState{Repo: d.Repo, Path: d.Path, Branch: "trunk"}
	}
	_, u := trayMenuKeepingFakes(t, env, g)
	repos := waitForEntry(t, u, "Repos\t1 to sync")
	webapp := findEntry(repos.Sub, "webapp (main)\t2 to receive, 1 changed")
	if webapp == nil || findEntry(repos.Sub, "notes (trunk)\tnot on the hub") == nil {
		t.Fatalf("each folder with its state: %+v", repos.Sub)
	}
	if _, ok := fetched.Load("webapp"); !ok {
		t.Fatal("the first read fetches from the hub")
	}
	if findEntry(findEntry(repos.Sub, "notes (trunk)").Sub, "Sync") != nil {
		t.Fatal("a folder without the bridge's remote cannot sync")
	}
	findEntry(webapp.Sub, "Sync with the hub").Action()
	if g.called("repo sync") != 1 || g.dirs["repo sync"] != web {
		t.Fatalf("sync runs in the folder: %v %v", g.calls, g.dirs)
	}

	get := findEntry(repos.Sub, "Get a repo from the hub")
	if len(get.Sub) != 1 || !strings.HasPrefix(get.Sub[0].Label, "demo") {
		t.Fatalf("only the hub's repos without a folder here, valid names only: %+v", get.Sub)
	}
	get.Sub[0].Action()
	if g.called("repo get demo -dest "+filepath.Join(base, "demo")) != 0 {
		t.Fatal("nothing without a folder")
	}
	u.set(false, base)
	get.Sub[0].Action()
	if g.called("repo get demo -dest "+filepath.Join(base, "demo")) != 1 {
		t.Fatalf("%v", g.calls)
	}

	publish := findEntry(webapp.Sub, "Publish to origin")
	publish.Action()
	if g.called("repo publish") != 0 {
		t.Fatal("publishing asks first")
	}
	u.set(true, "")
	publish.Action()
	if g.called("repo publish") != 1 || g.dirs["repo publish"] != web {
		t.Fatalf("%v", g.calls)
	}
}

// trayMenuKeepingFakes is trayMenu for a test that set its own fakes after fakeDesktop.
func trayMenuKeepingFakes(t *testing.T, env *module.Env, g *fakeGitsi) (*session, *fakeUI) {
	t.Helper()
	u := &fakeUI{done: make(chan struct{})}
	startUI = func(o desktop.Options) (ui, error, error) {
		u.mu.Lock()
		u.opt = o
		u.mu.Unlock()
		return u, nil, nil
	}
	runSelf = g.run
	s, err := newSession(env, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); s.close() })
	s.cancel = cancel
	if err := s.startUI(ctx); err != nil {
		t.Fatal(err)
	}
	return s, u
}

// Services: choosing one opens its tunnel (a gitsi svc up child) and checks it; choosing it again closes it; a tunnel that ends
// on its own is notified; Quit closes the rest. One opened elsewhere is left alone.
func TestTrayServicesOpenAndCloseTunnels(t *testing.T) {
	env := edgeEnv(t, "services")
	g := &fakeGitsi{json: map[string]any{"--json svc ls": []svcRow{
		{Name: "postgres", LocalPort: 5432, RemotePort: 5432, Tunnel: "closed"},
		{Name: "redis", LocalPort: 6379, RemotePort: 6379, Tunnel: "open"},
		{Name: "bad name", LocalPort: 1, RemotePort: 1},
	}}}
	fakeDesktop(t, nil)
	var mu sync.Mutex
	stops := map[string]int{}
	ends := map[string]chan string{}
	startTunnel = func(name string) (func(), <-chan string, error) {
		mu.Lock()
		defer mu.Unlock()
		ch := make(chan string, 1)
		ends[name] = ch
		var once sync.Once
		return func() {
			once.Do(func() {
				mu.Lock()
				stops[name]++
				mu.Unlock()
				ch <- "stopped"
			})
		}, ch, nil
	}
	s, u := trayMenuKeepingFakes(t, env, g)
	waitForEntry(t, u, "postgres")
	svc := findEntry(u.lastMenu(), "Services")
	if findEntry(svc.Sub, "bad name") != nil || findEntry(svc.Sub, "redis\topen elsewhere") == nil {
		t.Fatalf("%+v", svc.Sub)
	}
	pg := findEntry(svc.Sub, "postgres\tlocalhost:5432")
	pg.Action()
	open := waitForEntry(t, u, "Services\t1 open")
	if e := findEntry(open.Sub, "postgres"); e == nil || !e.Checked {
		t.Fatal("an open tunnel is checked")
	}
	findEntry(open.Sub, "postgres").Action()
	waitUntil(t, "closed", func() bool { mu.Lock(); defer mu.Unlock(); return stops["postgres"] == 1 })
	waitUntil(t, "unchecked", func() bool { e := findEntry(u.lastMenu(), "postgres"); return e != nil && !e.Checked })
	if strings.Contains(u.noted(), "closed") {
		t.Fatalf("closing it from the menu is not news: %s", u.noted())
	}

	pg.Action()
	waitForEntry(t, u, "Services\t1 open")
	mu.Lock()
	ends["postgres"] <- "ssh: connection lost"
	mu.Unlock()
	waitUntil(t, "the end notified", func() bool { return strings.Contains(u.noted(), "the tunnel to postgres closed: ssh: connection lost") })

	pg.Action()
	waitForEntry(t, u, "Services\t1 open")
	s.close()
	mu.Lock()
	defer mu.Unlock()
	if stops["postgres"] != 2 {
		t.Fatalf("closing watch closes its tunnels: %v", stops)
	}
}

// Jobs: listed with their state; the output is shown in a dialog; stopping asks first.
func TestTrayJobs(t *testing.T) {
	env := edgeEnv(t, "exec")
	g := &fakeGitsi{json: map[string]any{"--json job ls": []jobRow{
		{ID: "0a1b2c3d", State: "running", Exe: "dotnet test"},
		{ID: "11112222", State: "exited", Exit: "1", Exe: "npm run build"},
		{ID: "../x", State: "running", Exe: "trap"},
	}}, out: map[string]string{"job logs 0a1b2c3d -tail 40": "Passed: 12"}}
	_, u := trayMenu(t, env, g)
	jobs := waitForEntry(t, u, "Jobs\t1 running")
	if findEntry(jobs.Sub, "trap") != nil || findEntry(jobs.Sub, "npm run build\texit 1") == nil {
		t.Fatalf("%+v", jobs.Sub)
	}
	dotnet := findEntry(jobs.Sub, "dotnet test\trunning")
	findEntry(dotnet.Sub, "Show its output").Action()
	if m := u.messages(); len(m) != 1 || !strings.Contains(m[0], "Passed: 12") {
		t.Fatalf("%q", m)
	}
	if findEntry(findEntry(jobs.Sub, "npm run build").Sub, "Stop") != nil {
		t.Fatal("only a running job can be stopped")
	}
	findEntry(dotnet.Sub, "Stop").Action()
	if g.called("job stop 0a1b2c3d") != 0 {
		t.Fatal("stopping asks first")
	}
	u.set(true, "")
	findEntry(dotnet.Sub, "Stop").Action()
	if g.called("job stop 0a1b2c3d") != 1 {
		t.Fatalf("%v", g.calls)
	}
}

// Settings: the features with a check mark; turning one on asks, turning one off does not. On the hub, starting on logon.
func TestTraySettings(t *testing.T) {
	env := edgeEnv(t, "take")
	g := &fakeGitsi{}
	edge, u := trayMenu(t, env, g)
	settings := waitForEntry(t, u, "Settings")
	features := findEntry(settings.Sub, "Features\t1 of 5 on")
	if features == nil {
		t.Fatalf("%+v", settings.Sub)
	}
	findEntry(features.Sub, "Receiving from the hub").Action()
	if g.called("policy set take off") != 1 || len(u.questions()) != 0 {
		t.Fatalf("turning off does not ask: %v %v", g.calls, u.questions())
	}
	findEntry(features.Sub, "Running commands on the hub").Action()
	if g.called("policy set exec on") != 0 || len(u.questions()) != 1 {
		t.Fatal("turning on asks first")
	}
	auto := findEntry(settings.Sub, "Update automatically")
	if auto == nil || auto.Checked {
		t.Fatal("off by default")
	}
	auto.Action()
	if g.called("config set watch.auto_update on") != 1 {
		t.Fatalf("%v", g.calls)
	}

	edge.close() // before the next session replaces the fakes
	fa := &fakeAutostart{}
	old := autostart
	autostart = fa
	t.Cleanup(func() { autostart = old }) // after the session's own cleanup (registered below) has closed it
	hubEnv, _ := clipHubEnv(t, true)
	_, hu := trayMenu(t, hubEnv, g)
	logon := findEntry(waitForEntry(t, hu, "Settings").Sub, "Start when I log on")
	if logon == nil || logon.Checked {
		t.Fatal("the hub offers to start on logon")
	}
	logon.Action()
	if g.called("watch -autostart on") != 1 {
		t.Fatalf("%v", g.calls)
	}
	if f := findEntry(findEntry(hu.lastMenu(), "Settings").Sub, "Features"); len(f.Sub) != 3 {
		t.Fatalf("on the hub only the shared clipboard: %+v", f.Sub)
	}
}

// The hub's Edges: who listens, and publishing this version after a yes.
func TestTrayEdgesAndPublish(t *testing.T) {
	env, _ := clipHubEnv(t, true)
	g := &fakeGitsi{}
	_, u := trayMenu(t, env, g)
	edges := waitForEntry(t, u, "Edges")
	if findEntry(edges.Sub, "No edge has listened yet") == nil {
		t.Fatalf("%+v", edges.Sub)
	}
	publish := findEntry(edges.Sub, "Publish this version")
	publish.Action()
	if g.called("update -publish") != 0 {
		t.Fatal("asks first")
	}
	u.set(true, "")
	publish.Action()
	if g.called("update -publish") != 1 {
		t.Fatalf("%v", g.calls)
	}
}

// Check shows gitsi check's report.
func TestTrayCheckShowsTheReport(t *testing.T) {
	env, _ := clipHubEnv(t, true)
	g := &fakeGitsi{out: map[string]string{"check": "hub: ok\nkey: ok"}}
	_, u := trayMenu(t, env, g)
	waitForEntry(t, u, "Check this hub").Action()
	if m := u.messages(); len(m) != 1 || !strings.Contains(m[0], "hub: ok") {
		t.Fatalf("%q", m)
	}
}

func TestRepoStatus(t *testing.T) {
	for st, want := range map[repoState]string{
		{}:                              "not on the hub",
		{Remote: "hub"}:                 "branch not on the hub",
		{Remote: "hub", OnBridge: true}: "up to date",
		{Remote: "hub", OnBridge: true, Ahead: 3, Behind: 1, Changed: 2}: "3 to send, 1 to receive, 2 changed",
	} {
		if got := repoStatus(st); got != want {
			t.Errorf("%+v: %q, want %q", st, got, want)
		}
	}
}

// Labels from outside cannot inject an accelerator column (a tab), a mnemonic or other control characters.
func TestMenuText(t *testing.T) {
	if got := menuText("a&b\tc\x07d", 60); got != "a&&b?c?d" {
		t.Fatalf("%q", got)
	}
}
