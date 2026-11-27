package mirrorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := procx.Background(exec.Command("git", args...))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fakeHub runs the bridge's PowerShell (only the bare repo script) against a local folder.
type fakeHub struct{ t *testing.T }

var initRe = regexp.MustCompile(`git init --bare -b '([^']+)' '([^']+)'`)

func (f fakeHub) Run(_ context.Context, cmd string) (transport.Result, error) {
	m := initRe.FindStringSubmatch(cmd)
	if m == nil {
		return transport.Result{}, errors.New("unexpected command: " + cmd)
	}
	if _, err := os.Stat(filepath.Join(m[2], "HEAD")); err == nil {
		return transport.Result{Stdout: "EXISTS"}, nil
	}
	run(f.t, "", "init", "-q", "--bare", "-b", m[1], m[2])
	return transport.Result{Stdout: "CREATED"}, nil
}
func (fakeHub) Stream(context.Context, string) (transport.Stream, error) {
	return nil, errors.New("not used")
}
func (fakeHub) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, errors.New("not used")
}

// rig: a profile of its own, a "hub" whose repos folder is local (git reaches it through insteadOf of the hub's alias), and
// origins at https://origin.test/<name>.git that insteadOf turns into local bare repos.
type rig struct {
	t       *testing.T
	tmp     string
	repos   string
	env     *module.Env
	out     *strings.Builder
	layouts int        // times the hub was asked for its layout
	gitArgs [][]string // every git command the module ran
}

func newRig(t *testing.T) *rig {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", filepath.Join(tmp, "profile"))
	t.Setenv("HOME", filepath.Join(tmp, "profile"))
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	repos := filepath.ToSlash(filepath.Join(tmp, "hub", "repos"))
	origins := filepath.ToSlash(filepath.Join(tmp, "origins"))
	for _, d := range []string{repos, origins} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gc := filepath.Join(tmp, "gitconfig")
	rules := "[url \"" + repos + "/\"]\n\tinsteadOf = gitsi-hub:" + repos + "/\n[url \"" + origins + "/\"]\n\tinsteadOf = https://origin.test/\n"
	if err := os.WriteFile(gc, []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gc)

	r := &rig{t: t, tmp: tmp, repos: repos, out: &strings.Builder{}}
	prevGit, prevOrigin := newGit, originConfig
	newGit = func(dir string) *gitx.Git {
		g := prevGit(dir)
		inner := g.Run
		g.Run = func(ctx context.Context, d string, args ...string) (string, string, error) {
			r.gitArgs = append(r.gitArgs, args)
			return inner(ctx, d, args...)
		}
		return g
	}
	originConfig = append(slices.Clone(prevOrigin), "-c", "protocol.file.allow=always") // the test's origins are folders
	t.Cleanup(func() { newGit, originConfig = prevGit, prevOrigin })

	cfg := config.Default()
	cfg.Hub.Host, cfg.Hub.User = "192.0.2.1", "user"
	r.env = &module.Env{
		Stdin: strings.NewReader(""), Stdout: r.out, Stderr: r.out, Config: cfg, Transport: fakeHub{t},
		LayoutFn: func(context.Context) (layout.Layout, error) { r.layouts++; return layout.Layout{Repos: repos}, nil },
	}
	return r
}

// origin creates https://origin.test/<name>.git with a commit on each branch, and returns a working clone to add commits.
func (r *rig) origin(name string, branches ...string) string {
	r.t.Helper()
	bare := filepath.Join(r.tmp, "origins", name+".git")
	run(r.t, "", "init", "-q", "--bare", "-b", branches[0], bare)
	work := filepath.Join(r.tmp, "work-"+name)
	run(r.t, "", "clone", "-q", bare, work)
	for _, b := range branches {
		run(r.t, work, "checkout", "-q", "-B", b)
		r.commit(work, b+" init")
		run(r.t, work, "push", "-q", "origin", b)
	}
	return work
}

func (r *rig) commit(work, msg string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte(msg+time.Now().String()), 0o644); err != nil {
		r.t.Fatal(err)
	}
	run(r.t, work, "add", ".")
	run(r.t, work, "commit", "-qm", msg)
	return run(r.t, work, "rev-parse", "HEAD")
}

func (r *rig) bridgeRef(name, ref string) string {
	cmd := procx.Background(exec.Command("git", "rev-parse", "--verify", "--quiet", ref))
	cmd.Dir = filepath.Join(r.repos, name+".git")
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func (r *rig) do(f func(context.Context, *module.Env, []string) error, args ...string) error {
	r.out.Reset()
	r.gitArgs = nil
	return f(context.Background(), r.env, args)
}

func (r *rig) pushes() int {
	n := 0
	for _, a := range r.gitArgs {
		if slices.Contains(a, "push") {
			n++
		}
	}
	return n
}

// The whole cycle: add, sync, ls. The mirror lands in the hidden ref; the bridge's branches (here, a development branch pushed by
// an old script) stay as they were.
func TestAddSyncAndLs(t *testing.T) {
	r := newRig(t)
	work := r.origin("app", "development")
	old := filepath.Join(r.repos, "app.git")
	run(t, "", "init", "-q", "--bare", "-b", "development", old)
	run(t, work, "push", "-q", old, "development:development")
	oldTip := r.bridgeRef("app", "refs/heads/development")
	tip := r.commit(work, "new")
	run(t, work, "push", "-q", "origin", "development")

	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "development"); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if !strings.Contains(r.out.String(), "reused") {
		t.Errorf("the bridge repo existed: %s", r.out)
	}
	if err := r.do(syncCmd); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if got := r.bridgeRef("app", "refs/gitsi/mirror/development"); got != tip {
		t.Fatalf("mirror ref %q, want %q", got, tip)
	}
	if got := r.bridgeRef("app", "refs/heads/development"); got != oldTip {
		t.Fatalf("the bridge's own branch changed: %q", got)
	}
	if !strings.Contains(r.out.String(), "updated development") {
		t.Errorf("output: %s", r.out)
	}

	r.env.JSON = true
	if err := r.do(ls); err != nil {
		t.Fatal(err)
	}
	var rows []lsRow
	if err := json.Unmarshal([]byte(r.out.String()), &rows); err != nil || len(rows) != 1 || rows[0].Tip != tip || rows[0].CheckedAt.IsZero() || rows[0].ChangedAt.IsZero() {
		t.Fatalf("%v %s", err, r.out)
	}
}

// The first fetch from the origin brings only what the bridge lacks: the local copy is seeded from the bridge.
func TestAddSeedsFromBridge(t *testing.T) {
	r := newRig(t)
	work := r.origin("big", "main")
	for i := range 30 {
		r.commit(work, "c"+strconv.Itoa(i))
	}
	run(t, work, "push", "-q", "origin", "main")
	bare := filepath.Join(r.repos, "big.git")
	run(t, "", "init", "-q", "--bare", "-b", "main", bare)
	run(t, work, "push", "-q", bare, "main") // the bridge already has the history (e.g. shared before)
	r.commit(work, "one more")
	run(t, work, "push", "-q", "origin", "main")

	if err := r.do(add, "big", "https://origin.test/big.git", "-branch", "main"); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	dir, _ := localDir("big")
	before := looseObjects(t, dir)
	if err := r.do(syncCmd); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if got := looseObjects(t, dir) - before; got > 5 {
		t.Fatalf("the origin sent %d objects: the seed was not used", got)
	}
	if out := run(t, dir, "for-each-ref", "refs/seed/"); out != "" {
		t.Fatalf("the seed refs stay after the first fetch: %s", out)
	}
}

func looseObjects(t *testing.T, dir string) int {
	t.Helper()
	for _, l := range strings.Split(run(t, dir, "count-objects", "-v"), "\n") {
		if v, ok := strings.CutPrefix(l, "count: "); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	t.Fatal("no count")
	return 0
}

// A cycle with no news on the origin connects to neither the bridge nor the hub.
func TestSyncUnchangedDoesNotPushOrAskTheHub(t *testing.T) {
	r := newRig(t)
	r.origin("app", "main")
	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.do(syncCmd); err != nil || r.pushes() != 1 {
		t.Fatalf("%v pushes=%d\n%s", err, r.pushes(), r.out)
	}
	r.layouts = 0
	if err := r.do(syncCmd); err != nil {
		t.Fatal(err)
	}
	if r.pushes() != 0 || r.layouts != 0 || !strings.Contains(r.out.String(), "up to date") {
		t.Fatalf("pushes=%d layouts=%d\n%s", r.pushes(), r.layouts, r.out)
	}
}

// The mirror is exact: when the origin rewrites the branch, the bridge follows. A branch with folders (release/1) keeps its name.
func TestSyncFollowsRewrites(t *testing.T) {
	r := newRig(t)
	work := r.origin("app", "main", "release/1")
	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "main", "-branch", "release/1"); err != nil {
		t.Fatal(err)
	}
	if err := r.do(syncCmd); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	run(t, work, "checkout", "-q", "main")
	run(t, work, "commit", "-q", "--amend", "-m", "rewritten")
	rewritten := run(t, work, "rev-parse", "HEAD")
	run(t, work, "push", "-q", "-f", "origin", "main")
	if err := r.do(syncCmd); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if got := r.bridgeRef("app", "refs/gitsi/mirror/main"); got != rewritten {
		t.Fatalf("main: %q, want %q", got, rewritten)
	}
	if r.bridgeRef("app", "refs/gitsi/mirror/release/1") == "" {
		t.Fatal("release/1 is missing")
	}
}

// One repo that fails does not stop the others; the command fails at the end, after reporting all.
func TestOneFailureDoesNotStopOthers(t *testing.T) {
	r := newRig(t)
	r.origin("good", "main")
	r.origin("gone", "main")
	for _, n := range []string{"gone", "good"} {
		if err := r.do(add, n, "https://origin.test/"+n+".git", "-branch", "main"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(r.tmp, "origins", "gone.git")); err != nil {
		t.Fatal(err)
	}
	err := r.do(syncCmd)
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(r.out.String(), "gone") || !strings.Contains(r.out.String(), "error:") || r.bridgeRef("good", "refs/gitsi/mirror/main") == "" {
		t.Fatalf("%s", r.out)
	}
	st := loadState()
	if st.get("gone", "main").Error == "" || st.get("good", "main").Error != "" {
		t.Fatalf("%+v", st)
	}
}

// The local record of the bridge fell behind (another edge, a lost state): the lease fails, the real tip is read, and the push
// goes through.
func TestLeaseRecovers(t *testing.T) {
	r := newRig(t)
	work := r.origin("app", "main")
	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.do(syncCmd); err != nil {
		t.Fatal(err)
	}
	dir, _ := localDir("app")
	run(t, dir, "update-ref", "-d", "refs/gitsi/pushed/main") // this edge forgets what the bridge has
	tip := r.commit(work, "next")
	run(t, work, "push", "-q", "origin", "main")
	if err := r.do(syncCmd); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if got := r.bridgeRef("app", "refs/gitsi/mirror/main"); got != tip {
		t.Fatalf("%q, want %q", got, tip)
	}
}

// git toward the origin may only use https and ssh, also when the user's git configuration rewrites the URL into something else.
func TestOriginProtocolsAreLimited(t *testing.T) {
	r := newRig(t)
	r.origin("app", "main")
	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "main"); err != nil {
		t.Fatal(err)
	}
	originConfig = originConfig[:len(originConfig)-2] // the real setting: the test's insteadOf turns the origin into a folder
	err := r.do(syncCmd)
	if err == nil || !strings.Contains(r.out.String(), "not allowed") {
		t.Fatalf("%v\n%s", err, r.out)
	}
}

// An origin that asks for a password is an error, never a wait: neither a prompt nor a credential manager window.
func TestCredentialRequestFailsFast(t *testing.T) {
	r := newRig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	gc := os.Getenv("GIT_CONFIG_GLOBAL")
	b, _ := os.ReadFile(gc)
	extra := fmt.Sprintf("[url \"%s/\"]\n\tinsteadOf = https://secret.test/\n", srv.URL)
	if err := os.WriteFile(gc, append(b, extra...), 0o644); err != nil {
		t.Fatal(err)
	}
	originConfig = append(originConfig, "-c", "protocol.http.allow=always")
	r.env.Config.Mirror.Repos = []config.MirrorRepo{{Name: "priv", URL: "https://secret.test/priv.git", Branches: []string{"main"}}}
	dir, _ := localDir("priv")
	run(t, "", "init", "-q", "--bare", dir)
	perRepo = 30 * time.Second
	t.Cleanup(func() { perRepo = 2 * time.Minute })
	start := time.Now()
	err := r.do(syncCmd)
	if err == nil || time.Since(start) > 25*time.Second {
		t.Fatalf("%v after %s\n%s", err, time.Since(start), r.out)
	}
	if strings.Contains(r.out.String(), "no answer within") {
		t.Fatalf("it waited until the deadline: %s", r.out)
	}
}

// A list is checked whole before anything is touched.
func TestAddListValidatesEverythingFirst(t *testing.T) {
	r := newRig(t)
	f := filepath.Join(r.tmp, "repos.txt")
	list := "# devstack\napp https://origin.test/app.git development\n\nbad file:///C:/x main\n"
	if err := os.WriteFile(f, []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.do(add, "-list", f, "-yes"); err == nil || !strings.Contains(err.Error(), "invalid origin URL") {
		t.Fatalf("%v", err)
	}
	if len(r.env.Config.Mirror.Repos) != 0 || r.layouts != 0 {
		t.Fatalf("something was added: %+v", r.env.Config.Mirror)
	}
	// a good list asks first; without an answer, nothing happens
	if err := os.WriteFile(f, []byte("app https://origin.test/app.git development\nlib https://origin.test/lib.git main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.do(add, "-list", f); err != nil || !strings.Contains(r.out.String(), "Cancelled") || len(r.env.Config.Mirror.Repos) != 0 {
		t.Fatalf("%v %s", err, r.out)
	}
	if err := r.do(add, "-list", f, "-yes"); err != nil || len(r.env.Config.Mirror.Repos) != 2 {
		t.Fatalf("%v %s", err, r.out)
	}
}

// Adding a branch to a followed repo merges; another origin for the same repo is refused.
func TestAddMerges(t *testing.T) {
	r := newRig(t)
	r.origin("app", "main", "dev")
	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "dev", "-branch", "main"); err != nil {
		t.Fatal(err)
	}
	if got := r.env.Config.Mirror.Repos[0].Branches; !slices.Equal(got, []string{"main", "dev"}) {
		t.Fatal(got)
	}
	if err := r.do(add, "app", "https://origin.test/other.git", "-branch", "main"); err == nil || !strings.Contains(err.Error(), "already follows") {
		t.Fatalf("%v", err)
	}
}

func TestRm(t *testing.T) {
	r := newRig(t)
	r.origin("app", "main", "dev")
	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "main", "-branch", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := r.do(syncCmd); err != nil {
		t.Fatal(err)
	}
	if err := r.do(rm, "app", "-branch", "dev"); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if r.bridgeRef("app", "refs/gitsi/mirror/dev") != "" || r.bridgeRef("app", "refs/gitsi/mirror/main") == "" {
		t.Fatal("only dev must go")
	}
	if got := r.env.Config.Mirror.Repos[0].Branches; !slices.Equal(got, []string{"main"}) {
		t.Fatal(got)
	}
	if err := r.do(rm, "app"); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	dir, _ := localDir("app")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the local copy stays: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.repos, "app.git", "HEAD")); err != nil || r.bridgeRef("app", "refs/gitsi/mirror/main") != "" || len(r.env.Config.Mirror.Repos) != 0 {
		t.Fatal("the mirror refs must go, the bridge repo stays")
	}
	if len(loadState()) != 0 {
		t.Fatalf("%+v", loadState())
	}
}
