package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/transport"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
}

func write(t *testing.T, path, text string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotTakesTheFolderAsIsWithoutTouchingTheIndex(t *testing.T) {
	gitIdentity(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, ".gitignore"), "*.log\n")
	write(t, filepath.Join(dir, "a.txt"), "uno")
	write(t, filepath.Join(dir, "d.txt"), "to delete")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "base")
	head := git(t, dir, "rev-parse", "HEAD")

	write(t, filepath.Join(dir, "a.txt"), "one changed") // uncommitted
	write(t, filepath.Join(dir, "sub", "b.txt"), "new")  // untracked
	write(t, filepath.Join(dir, "c.log"), "ignored")
	os.Remove(filepath.Join(dir, "d.txt"))
	git(t, dir, "add", "a.txt") // something in the user's index: it must stay the same
	before := git(t, dir, "status", "--porcelain")

	sha, err := snapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	files := git(t, dir, "ls-tree", "-r", "--name-only", sha)
	if files != ".gitignore\na.txt\nsub/b.txt" {
		t.Fatalf("the snapshot carries the folder's content (without what is ignored or deleted):\n%s", files)
	}
	if got := git(t, dir, "show", sha+":a.txt"); got != "one changed" {
		t.Fatalf("%q", got)
	}
	if parent := git(t, dir, "rev-parse", sha+"^"); parent != head {
		t.Fatalf("the snapshot hangs from the current HEAD: %s", parent)
	}
	if after := git(t, dir, "status", "--porcelain"); after != before || git(t, dir, "rev-parse", "HEAD") != head {
		t.Fatalf("the snapshot touches neither the index nor the branch:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestSnapshotOfARepoWithoutCommits(t *testing.T) {
	gitIdentity(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "x.txt"), "x")
	sha, err := snapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if files := git(t, dir, "ls-tree", "-r", "--name-only", sha); files != "x.txt" {
		t.Fatal(files)
	}
}

// localHub really runs on this machine what gitsi would send to the hub: the script with powershell.exe, and the live run (which
// is only recorded).
type localHub struct{ exec string }

func (h *localHub) Run(ctx context.Context, script string) (transport.Result, error) {
	// like the real transport: a non-zero exit code goes in the result, it is not a transport error
	out, err := winps.Command(ctx, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return transport.Result{Stdout: string(out), ExitCode: ee.ExitCode()}, nil
	}
	return transport.Result{Stdout: string(out)}, err
}
func (h *localHub) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (h *localHub) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}
func (h *localHub) Exec(_ context.Context, command string, _, _ io.Writer) (int, error) {
	h.exec = command
	return 0, nil
}

func TestRunRepoBringsTheFolderToTheHubWorkingClone(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the hub's script is PowerShell")
	}
	gitIdentity(t)
	tmp := filepath.ToSlash(t.TempDir())
	bridge := tmp + "/hub/repos/proy.git"
	git(t, tmp, "init", "-q", "--bare", "-b", "main", bridge)
	proj := filepath.Join(tmp, "proy")
	git(t, tmp, "init", "-q", "-b", "main", proj)
	write(t, filepath.Join(proj, ".gitignore"), "node_modules/\n")
	write(t, filepath.Join(proj, "app.txt"), "v1")
	git(t, proj, "add", ".")
	git(t, proj, "commit", "-qm", "base")
	// the remote points to the bridge through the ssh alias, as 'share' leaves it; in the test git rewrites it to the local folder
	git(t, proj, "remote", "add", "hub", "gitsi-hub:"+bridge)
	git(t, proj, "config", "url."+bridge+".insteadOf", "gitsi-hub:"+bridge)
	git(t, proj, "push", "-q", "hub", "main")

	hub := &localHub{}
	e, _ := env(t, hub)
	e.Config.Policy.AllowExec = true
	home := tmp + "/hub"
	e.LayoutFn = func(context.Context) (layout.Layout, error) {
		return layout.ForHub(&config.Config{Home: home}, home), nil
	}
	t.Chdir(proj)

	write(t, filepath.Join(proj, "app.txt"), "v2 uncommitted")
	write(t, filepath.Join(proj, "new.txt"), "new")
	if err := run(context.Background(), e, []string{"-repo", "npm", "test"}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(filepath.FromSlash(home), "work", "proy")
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(work, name)); return string(b) }
	if read("app.txt") != "v2 uncommitted" || read("new.txt") != "new" {
		t.Fatalf("the working clone has the folder as is: %q %q", read("app.txt"), read("new.txt"))
	}
	if !strings.Contains(hub.exec, "Set-Location -LiteralPath '"+home+"/work/proy'") || !strings.Contains(hub.exec, "npm test") {
		t.Fatalf("the command runs in the working clone: %s", hub.exec)
	}
	if got := git(t, proj, "ls-remote", "--heads", "hub"); strings.Count(got, "\n") != 0 || !strings.HasSuffix(got, "refs/heads/main") {
		t.Fatalf("the bridge's branches are not touched: %s", got)
	}

	// second run: what the hub ignores (node_modules) is kept; what is no longer in the folder goes away
	write(t, filepath.Join(work, "node_modules", "dep.js"), "cache")
	write(t, filepath.Join(work, "junk.tmp"), "from an earlier run")
	os.Remove(filepath.Join(proj, "new.txt"))
	if err := run(context.Background(), e, []string{"-repo", "npm", "test"}); err != nil {
		t.Fatal(err)
	}
	if read("node_modules/dep.js") != "cache" {
		t.Fatal("what is ignored is kept to build fast")
	}
	for _, gone := range []string{"new.txt", "junk.tmp"} {
		if _, err := os.Stat(filepath.Join(work, gone)); !os.IsNotExist(err) {
			t.Errorf("%s is not in the edge's folder and must leave the working clone", gone)
		}
	}
}

func TestRunRepoNeedsASharedRepoAndNoCwd(t *testing.T) {
	e, _ := env(t, &localHub{})
	if err := run(context.Background(), e, []string{"-repo", "-cwd", "C:/x", "make"}); err == nil || !strings.Contains(err.Error(), "does not combine with -cwd") {
		t.Fatalf("%v", err)
	}
	t.Chdir(t.TempDir())
	if err := run(context.Background(), e, []string{"-repo", "make"}); err == nil || !strings.Contains(err.Error(), "gitsi share") {
		t.Fatalf("%v", err)
	}
}

// jobsHub is the test hub for jobs: it prepares the real clone (localHub) but does NOT start the job (it records the script) and
// lists the jobs it is told.
type jobsHub struct {
	localHub
	listing string
	started string
}

func (h *jobsHub) Run(ctx context.Context, script string) (transport.Result, error) {
	switch {
	case strings.Contains(script, "Win32_Process"):
		h.started = script
		id := script[strings.Index(script, "/jobs/")+6:]
		return transport.Result{Stdout: "STARTED|" + id[:8]}, nil
	case strings.Contains(script, `"JOB|$id`):
		return transport.Result{Stdout: h.listing}, nil
	}
	return h.localHub.Run(ctx, script)
}

// repoRig sets up a shared repo with a local bridge and a test hub; it returns the environment and the working clone's folder.
func repoRig(t *testing.T, hub transport.Transport) (*module.Env, string, string) {
	t.Helper()
	gitIdentity(t)
	tmp := filepath.ToSlash(t.TempDir())
	bridge := tmp + "/hub/repos/proy.git"
	git(t, tmp, "init", "-q", "--bare", "-b", "main", bridge)
	proj := filepath.Join(tmp, "proy")
	git(t, tmp, "init", "-q", "-b", "main", proj)
	write(t, filepath.Join(proj, "app.txt"), "v1")
	git(t, proj, "add", ".")
	git(t, proj, "commit", "-qm", "base")
	git(t, proj, "remote", "add", "hub", "gitsi-hub:"+bridge)
	git(t, proj, "config", "url."+bridge+".insteadOf", "gitsi-hub:"+bridge)
	e, _ := env(t, hub)
	e.Config.Policy.AllowExec = true
	home := tmp + "/hub"
	e.LayoutFn = func(context.Context) (layout.Layout, error) {
		return layout.ForHub(&config.Config{Home: home}, home), nil
	}
	t.Chdir(proj)
	return e, proj, home + "/work/proy"
}

func TestJobStartRepoLaunchesInTheWorkingCloneAndRemembersIt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the hub's script is PowerShell")
	}
	hub := &jobsHub{}
	e, proj, work := repoRig(t, hub)
	write(t, filepath.Join(proj, "app.txt"), "v2 uncommitted")
	if err := startCmd(context.Background(), e, []string{"-repo", "dotnet", "test"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.FromSlash(work), "app.txt")); string(b) != "v2 uncommitted" {
		t.Fatalf("the working clone has the folder as is: %q", b)
	}
	if !strings.Contains(hub.started, "|"+work+"'") {
		t.Fatalf("the job saves its folder in meta (so it is not overwritten later): %s", hub.started)
	}
}

func TestRepoRefusesWhileAJobRunsInTheWorkingClone(t *testing.T) {
	hub := &jobsHub{}
	e, _, work := repoRig(t, hub)
	hub.listing = "JOB|aaaaaaaa|exited|0|2026-11-19T08:00:00-05:00|dotnet|" + work + "\r\n" +
		"JOB|bbbbbbbb|running||2026-11-19T09:00:00-05:00|dotnet|" + work + "\r\n"
	err := run(context.Background(), e, []string{"-repo", "npm", "test"})
	if err == nil || !strings.Contains(err.Error(), "job bbbbbbbb is still running") || hub.exec != "" {
		t.Fatalf("the clone is not touched while a job runs there: %v", err)
	}
	hub.listing = "JOB|bbbbbbbb|running||2026-11-19T09:00:00-05:00|dotnet|C:/other/folder\r\n"
	if _, err := runningIn(context.Background(), e, work); err != nil {
		t.Fatal(err)
	}
	if id, _ := runningIn(context.Background(), e, work); id != "" {
		t.Fatalf("a job in another folder does not block: %s", id)
	}
}
