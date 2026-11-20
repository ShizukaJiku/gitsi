package gitx

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests use the real git.exe: fakes do not see differences in version, output or Windows paths.

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the PATH")
	}
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
}

func newRepo(t *testing.T) *Git {
	t.Helper()
	needGit(t)
	d := t.TempDir()
	g := New(d)
	if _, err := g.Out(context.Background(), "init", "-q", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	return g
}

func commit(t *testing.T, g *Git, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(g.Dir, file), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := g.Out(ctx, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Out(ctx, "commit", "-qm", file); err != nil {
		t.Fatal(err)
	}
}

func TestRealRepoLifecycle(t *testing.T) {
	g := newRepo(t)
	ctx := context.Background()

	if g.HasCommits(ctx) {
		t.Fatal("a freshly created repo has no commits")
	}
	if b := g.CurrentBranch(ctx); b != "main" {
		t.Fatalf("branch = %q", b)
	}
	commit(t, g, "a.txt")
	if !g.HasCommits(ctx) {
		t.Fatal("it must have commits")
	}
	if !g.HasRef(ctx, "refs/heads/main") || g.HasRef(ctx, "refs/heads/nothing") {
		t.Fatal("HasRef does not tell existing branches apart")
	}

	root, err := g.Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(g.Dir)
	got, _ := filepath.EvalSymlinks(filepath.FromSlash(root))
	if !strings.EqualFold(got, want) { // git returns the path with "/" and the drive letter case may vary
		t.Fatalf("Root = %q, want %q", got, want)
	}

	// Root from a subfolder
	sub := filepath.Join(g.Dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if r2, err := New(sub).Root(ctx); err != nil || r2 != root {
		t.Fatalf("Root from a subfolder = %q (%v), want %q", r2, err, root)
	}
}

func TestRealRootOutsideRepoFails(t *testing.T) {
	needGit(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir())) // keep git from climbing into an unrelated repo
	if _, err := New(t.TempDir()).Root(context.Background()); err == nil {
		t.Fatal("outside a repo, Root must fail")
	}
}

func TestRealRemotes(t *testing.T) {
	g := newRepo(t)
	ctx := context.Background()
	if g.HasRemote(ctx, "hub") {
		t.Fatal("it must not exist yet")
	}
	if err := g.SetRemote(ctx, "hub", "ssh://x/one.git"); err != nil { // creates
		t.Fatal(err)
	}
	if err := g.SetRemote(ctx, "hub", "ssh://x/two.git"); err != nil { // updates, does not duplicate
		t.Fatal(err)
	}
	if url, _ := g.Out(ctx, "remote", "get-url", "hub"); url != "ssh://x/two.git" {
		t.Fatalf("url = %q", url)
	}
	if !g.HasRemote(ctx, "hub") {
		t.Fatal("it must exist")
	}
}

func TestRealRemoteBranchesSkipsHeadAlias(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "b.git")
	if _, err := New(tmp).Out(ctx, "init", "-q", "--bare", "-b", "main", bare); err != nil {
		t.Fatal(err)
	}
	src := New(filepath.Join(tmp, "src"))
	_ = os.Mkdir(src.Dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", bare}} {
		if _, err := src.Out(ctx, a...); err != nil {
			t.Fatal(err)
		}
	}
	commit(t, src, "x")
	if _, err := src.Out(ctx, "push", "-q", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	dst := New(tmp)
	if _, err := dst.Out(ctx, "clone", "-q", bare, "dst"); err != nil {
		t.Fatal(err)
	}
	brs, err := New(filepath.Join(tmp, "dst")).RemoteBranches(ctx)
	if err != nil || len(brs) != 1 || brs[0] != "origin/main" { // without the "origin/HEAD -> origin/main" line
		t.Fatalf("%v %v", brs, err)
	}
}

func TestRealOutErrorCarriesStderr(t *testing.T) {
	g := newRepo(t)
	_, err := g.Out(context.Background(), "checkout", "branch-that-does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "branch-that-does-not-exist") {
		t.Fatalf("the error must include what git said: %v", err)
	}
}

func TestRealPassthroughStreamsOutput(t *testing.T) {
	g := newRepo(t)
	commit(t, g, "a.txt")
	var out, errb bytes.Buffer
	if err := g.Passthrough(context.Background(), &out, &errb, "log", "--oneline"); err != nil || !strings.Contains(out.String(), "a.txt") {
		t.Fatalf("%v %q", err, out.String())
	}
	if err := g.Passthrough(context.Background(), &out, &errb, "checkout", "nothing"); err == nil {
		t.Fatal("a git failure must return an error")
	}
	if errb.Len() == 0 {
		t.Fatal("git's stderr must reach the writer")
	}
}
