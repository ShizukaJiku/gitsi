package repo

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/procx"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := procx.Background(exec.Command("git", args...))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitEnv(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
}

func newEnv(reposDir string) (*module.Env, *bytes.Buffer) {
	var out bytes.Buffer
	cfg := config.Default()
	env := &module.Env{Stdout: &out, Stderr: &out, Config: cfg}
	if reposDir != "" {
		env.LayoutFn = func(context.Context) (layout.Layout, error) {
			return layout.ForHub(&config.Config{ReposDir: reposDir}, path.Dir(path.Clean(filepath.ToSlash(reposDir)))), nil
		}
	}
	return env, &out
}

func TestListCmd(t *testing.T) {
	if c := listCmd("C:/r"); !strings.Contains(c, `Get-ChildItem 'C:/r'`) || !strings.Contains(c, `\.git$`) {
		t.Fatal(c)
	}
}

// A bare repo with HEAD=main but the real branch master: clone must leave the checkout on master.
func TestCloneFallsBackToRealBranch(t *testing.T) {
	gitEnv(t)
	tmp := t.TempDir()
	repos := filepath.Join(tmp, "repos")
	bare := filepath.Join(repos, "demo.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", bare)
	src := filepath.Join(tmp, "src")
	gitCmd(t, tmp, "init", "-q", "-b", "master", src)
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hola"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, src, "add", ".")
	gitCmd(t, src, "commit", "-qm", "init")
	gitCmd(t, src, "push", "-q", bare, "master")

	env, out := newEnv(repos)
	dst := filepath.Join(tmp, "work", "demo")
	if err := clone(context.Background(), env, []string{"demo", "-dest", dst}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.txt")); err != nil {
		t.Fatalf("no checkout: %v\n%s", err, out)
	}
	if b := gitCmd(t, dst, "branch", "--show-current"); b != "master" {
		t.Fatalf("branch = %q", b)
	}
	// existing target -> error
	if err := clone(context.Background(), env, []string{"demo", "-dest", dst}); err == nil {
		t.Fatal("it must fail when the target exists")
	}
}

func TestCloneRejectsBadNameAndMissingRepo(t *testing.T) {
	env, _ := newEnv(t.TempDir())
	if err := clone(context.Background(), env, []string{"x';calc"}); err == nil {
		t.Fatal("a dangerous name must be rejected")
	}
	if err := clone(context.Background(), env, []string{"missing"}); err == nil {
		t.Fatal("a missing repo must fail")
	}
}

// sync: fast-forwards what is new and pushes what is local; when they diverge, it stops.
func TestSyncFastForwardAndDivergence(t *testing.T) {
	gitEnv(t)
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "hub.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", bare)
	a, b := filepath.Join(tmp, "a"), filepath.Join(tmp, "b")
	gitCmd(t, tmp, "clone", "-q", bare, a)
	gitCmd(t, a, "checkout", "-q", "-b", "main")
	commit := func(dir, file string) {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, dir, "add", ".")
		gitCmd(t, dir, "commit", "-qm", file)
	}
	commit(a, "uno")
	gitCmd(t, a, "push", "-q", "origin", "main")
	gitCmd(t, tmp, "clone", "-q", bare, b)

	env, out := newEnv("")

	// A publishes a change; B syncs and receives it
	commit(a, "dos")
	gitCmd(t, a, "push", "-q", "origin", "main")
	t.Chdir(b)
	if err := syncCmd(context.Background(), env, nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(b, "dos")); err != nil {
		t.Fatal("B did not receive A's change")
	}

	// B makes a local change; sync pushes it
	commit(b, "tres")
	if err := syncCmd(context.Background(), env, nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := gitCmd(t, bare, "log", "--oneline", "main"); !strings.Contains(got, "tres") {
		t.Fatalf("the bridge did not receive 'tres': %s", got)
	}

	// Divergence: A and B change on the same base
	gitCmd(t, a, "pull", "-q", "origin", "main")
	commit(a, "cuatro-a")
	gitCmd(t, a, "push", "-q", "origin", "main")
	commit(b, "cuatro-b")
	err := syncCmd(context.Background(), env, nil)
	if err == nil || !strings.Contains(err.Error(), "diverge") {
		t.Fatalf("it should detect divergence, got: %v", err)
	}
}

func TestSyncNeedsRemote(t *testing.T) {
	gitEnv(t)
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	t.Chdir(d)
	env, _ := newEnv("")
	if err := syncCmd(context.Background(), env, nil); err == nil {
		t.Fatal("without a remote it must fail")
	}
}

// On the hub, 'repo get' clones from the local bridge and 'repo list' lists its own repos (the same verbs as on the edge).
func TestHubGetAndListUseTheLocalBridge(t *testing.T) {
	gitEnv(t)
	tmp := t.TempDir()
	repos := filepath.Join(tmp, "repos")
	bare := filepath.Join(repos, "demo.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", bare)
	src := filepath.Join(tmp, "src")
	gitCmd(t, tmp, "init", "-q", "-b", "main", src)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("hola"), 0o644)
	gitCmd(t, src, "add", ".")
	gitCmd(t, src, "commit", "-qm", "init")
	gitCmd(t, src, "push", "-q", bare, "main")

	env, out := newEnv("")
	env.Config.Role = config.RoleHub
	env.Config.ReposDir = filepath.ToSlash(repos)
	if err := list(context.Background(), env, nil); err != nil || strings.TrimSpace(out.String()) != "demo" {
		t.Fatalf("%v %q", err, out)
	}
	dst := filepath.Join(tmp, "work", "demo")
	if err := get(context.Background(), env, []string{"demo", "-dest", dst}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(b) != "hola" {
		t.Fatal("get on the hub must clone from the local bridge")
	}
	if !strings.Contains(gitCmd(t, dst, "config", "--get-all", "remote.origin.fetch"), mirrorRefspec) {
		t.Fatal("the hub's working clone shows the origin mirrors as mirror/<branch>")
	}
	if !recorded(t, "demo", dst) {
		t.Fatal("the hub's working folder is recorded for the tray")
	}
	out.Reset()
	env.JSON = true
	if err := list(context.Background(), env, nil); err != nil || strings.Join(strings.Fields(out.String()), "") != `["demo"]` {
		t.Fatalf("list --json: %v %q", err, out)
	}
	env.JSON = false
	out.Reset()
	env.Config.ReposDir = filepath.ToSlash(filepath.Join(tmp, "empty"))
	if err := list(context.Background(), env, nil); err != nil || !strings.Contains(out.String(), "No repos") {
		t.Fatalf("%v %q", err, out)
	}
}
