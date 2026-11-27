package repo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/transport"
	"github.com/ShizukaJiku/gitsi/internal/workdirs"
)

// fakeHub is a Transport whose Run delegates to a test function. The "real" hub is a temporary folder: git reaches it because
// insteadOf maps the hub's SSH alias to that path, so share and get run the real git.exe end to end.
type fakeHub struct {
	run   func(cmd string) (transport.Result, error)
	calls []string
}

func (f *fakeHub) Run(_ context.Context, cmd string) (transport.Result, error) {
	f.calls = append(f.calls, cmd)
	return f.run(cmd)
}
func (*fakeHub) Stream(context.Context, string) (transport.Stream, error) {
	return nil, errors.New("not used")
}
func (*fakeHub) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, errors.New("not used")
}

var initRe = regexp.MustCompile(`git init --bare -b '([^']+)' '([^']+)'`)

type hubRig struct {
	t     *testing.T
	repos string // repos folder of the "hub" (with "/")
	hub   *fakeHub
	env   *module.Env
	out   *strings.Builder
}

func newHubRig(t *testing.T) *hubRig {
	t.Helper()
	gitEnv(t)
	tmp := t.TempDir()
	repos := filepath.ToSlash(filepath.Join(tmp, "hub", "repos"))
	if err := os.MkdirAll(repos, 0o755); err != nil {
		t.Fatal(err)
	}
	// The hub's alias (gitsi-hub:<repos>/x.git) becomes the local path.
	cfgFile := filepath.Join(tmp, "gitconfig")
	rule := "[url \"" + repos + "/\"]\n\tinsteadOf = gitsi-hub:" + repos + "/\n"
	if err := os.WriteFile(cfgFile, []byte(rule), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfgFile)

	r := &hubRig{t: t, repos: repos, out: &strings.Builder{}}
	r.hub = &fakeHub{run: func(cmd string) (transport.Result, error) {
		if strings.HasPrefix(cmd, "if (-not (Test-Path") { // bridge.EnsureBareScript: creates the bare repo (or says it already exists)
			m := initRe.FindStringSubmatch(cmd)
			if m == nil {
				t.Fatalf("unrecognized command: %s", cmd)
			}
			branch, bare := m[1], m[2]
			if _, err := os.Stat(filepath.Join(bare, "HEAD")); err == nil {
				return transport.Result{Stdout: "EXISTS"}, nil
			}
			gitCmd(t, tmp, "init", "-q", "--bare", "-b", branch, bare)
			return transport.Result{Stdout: "CREATED"}, nil
		}
		return transport.Result{}, errors.New("unexpected command: " + cmd)
	}}
	cfg := config.Default()
	cfg.Role = config.RoleEdge
	cfg.Hub.User = "user"
	r.env = &module.Env{
		Stdout: r.out, Stderr: r.out, Config: cfg, Transport: r.hub,
		Stdin:    strings.NewReader(""),
		LayoutFn: func(context.Context) (layout.Layout, error) { return layout.Layout{Repos: repos}, nil },
	}
	return r
}

// project creates a local repo with a commit on `branch`.
func project(t *testing.T, branch string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, d, "init", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(d, "a.txt"), []byte("hola"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "commit", "-qm", "init")
	return d
}

func TestShareThenGetRoundTrip(t *testing.T) {
	r := newHubRig(t)
	src := project(t, "trunk") // a branch that is not main: it must be respected
	t.Chdir(src)

	if err := share(context.Background(), r.env, []string{"-yes"}); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if !strings.Contains(r.out.String(), "Repo created on the hub") {
		t.Fatal(r.out.String())
	}
	if !recorded(t, "demo", src) {
		t.Fatal("the shared folder is recorded for the tray")
	}
	bare := r.repos + "/demo.git"
	if got := gitCmd(t, bare, "log", "--oneline", "trunk"); !strings.Contains(got, "init") {
		t.Fatalf("the hub did not receive the commit: %s", got)
	}
	// config instead of get-url: the latter applies insteadOf and would return the local path
	if url := gitCmd(t, src, "config", "--get", "remote.hub.url"); url != "gitsi-hub:"+bare {
		t.Fatalf("remote = %q", url)
	}
	// no -u: the branch does not track the hub
	if up, err := gitxOut(src, "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
		t.Fatalf("share must not set an upstream: %s", up)
	}

	// sharing again reuses the repo
	r.out.Reset()
	if err := share(context.Background(), r.env, []string{"-yes"}); err != nil || !strings.Contains(r.out.String(), "already existed") {
		t.Fatalf("%v\n%s", err, r.out)
	}

	// get on the other machine
	dst := filepath.Join(t.TempDir(), "copy")
	r.out.Reset()
	if err := get(context.Background(), r.env, []string{"demo", "-dest", dst}); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(b) != "hola" {
		t.Fatal("get did not leave the checkout")
	}
	if gitCmd(t, dst, "branch", "--show-current") != "trunk" {
		t.Fatal("get must end up on the real branch")
	}
	if !recorded(t, "demo", dst) {
		t.Fatal("the folder get created is recorded for the tray")
	}
	if err := get(context.Background(), r.env, []string{"demo", "-dest", dst}); err == nil {
		t.Fatal("get on an existing target must fail")
	}
}

// recorded reports whether internal/workdirs holds that folder for that repo.
func recorded(t *testing.T, repo, dir string) bool {
	t.Helper()
	dirs, err := workdirs.List()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	for _, d := range dirs {
		got, _ := filepath.EvalSymlinks(d.Path)
		if d.Repo == repo && strings.EqualFold(got, want) {
			return true
		}
	}
	return false
}

func gitxOut(dir string, args ...string) (string, error) {
	c := procx.Background(exec.Command("git", args...))
	c.Dir = dir
	b, err := c.Output()
	return strings.TrimSpace(string(b)), err
}

func TestShareCancelledAsksBeforeTouchingHub(t *testing.T) {
	r := newHubRig(t)
	t.Chdir(project(t, "main"))
	r.env.Stdin = strings.NewReader("n\n")
	if err := share(context.Background(), r.env, nil); err != nil {
		t.Fatal(err)
	}
	if len(r.hub.calls) != 0 || !strings.Contains(r.out.String(), "Cancelled") {
		t.Fatalf("without confirmation the hub must not be touched: %v\n%s", r.hub.calls, r.out)
	}
}

func TestShareNoPushAndHubFailure(t *testing.T) {
	r := newHubRig(t)
	src := project(t, "main")
	t.Chdir(src)
	if err := share(context.Background(), r.env, []string{"-yes", "-no-push"}); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if !strings.Contains(r.out.String(), "Push skipped") {
		t.Fatal(r.out.String())
	}
	if _, err := os.Stat(filepath.Join(r.repos, "demo.git", "refs", "heads", "main")); err == nil {
		t.Fatal("-no-push must push nothing")
	}

	// the hub answers something that is neither CREATED nor EXISTS
	r.hub.run = func(string) (transport.Result, error) {
		return transport.Result{ExitCode: 1, Stderr: "access denied"}, nil
	}
	if err := share(context.Background(), r.env, []string{"-yes", "-name", "other"}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("the hub's error must reach the user: %v", err)
	}
	// ssh could not run
	r.hub.run = func(string) (transport.Result, error) { return transport.Result{}, errors.New("no network") }
	if err := share(context.Background(), r.env, []string{"-yes", "-name", "other"}); err == nil || !strings.Contains(err.Error(), "ssh") {
		t.Fatalf("%v", err)
	}
}

func TestShareRejectsUnsafeNameAndNonRepo(t *testing.T) {
	r := newHubRig(t)
	t.Chdir(project(t, "main"))
	if err := share(context.Background(), r.env, []string{"-yes", "-name", "x';calc"}); err == nil {
		t.Fatal("a dangerous name must be rejected")
	}
	t.Chdir(t.TempDir())
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	if err := share(context.Background(), r.env, []string{"-yes"}); err == nil {
		t.Fatal("outside a repo it must fail")
	}
}

func TestGetValidatesInput(t *testing.T) {
	r := newHubRig(t)
	if err := get(context.Background(), r.env, nil); err == nil {
		t.Fatal("without a name it must explain the usage")
	}
	if err := get(context.Background(), r.env, []string{"x';calc"}); err == nil {
		t.Fatal("dangerous name")
	}
	if err := get(context.Background(), r.env, []string{"missing", "-dest", filepath.Join(t.TempDir(), "d")}); err == nil {
		t.Fatal("a missing repo must fail")
	}
}

func TestListOutputAndErrors(t *testing.T) {
	r := newHubRig(t)
	r.hub.run = func(string) (transport.Result, error) { return transport.Result{Stdout: "uno\r\ndos\r\n"}, nil }
	if err := list(context.Background(), r.env, nil); err != nil || !strings.Contains(r.out.String(), "uno") || !strings.Contains(r.out.String(), "dos") {
		t.Fatalf("%v\n%s", err, r.out)
	}
	r.out.Reset()
	r.hub.run = func(string) (transport.Result, error) { return transport.Result{}, nil }
	if err := list(context.Background(), r.env, nil); err != nil || !strings.Contains(r.out.String(), "No repos") {
		t.Fatalf("%v\n%s", err, r.out)
	}
	r.hub.run = func(string) (transport.Result, error) { return transport.Result{ExitCode: 2, Stderr: "boom"}, nil }
	if err := list(context.Background(), r.env, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("%v", err)
	}
	r.hub.run = func(string) (transport.Result, error) { return transport.Result{}, errors.New("no network") }
	if err := list(context.Background(), r.env, nil); err == nil {
		t.Fatal("an ssh failure must be returned")
	}
}
