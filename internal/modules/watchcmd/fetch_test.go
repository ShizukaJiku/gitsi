package watchcmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

var moved = watch.Event{Type: watch.EvRepo, Repo: "proy", Branch: "feature/x", Commit: strings.Repeat("1", 40)}

func TestAutoFetchOnlyWhenOnAndOnlyForTheRepoHere(t *testing.T) {
	var asked []string
	fetch := func(here bool, err error) fetcher {
		return func(_ context.Context, repo string) (bool, error) { asked = append(asked, repo); return here, err }
	}
	r, out := newReactor(config.Watch{}, &fakeSelf{})
	r.fetch = fetch(true, nil)
	if r.react(context.Background(), moved) || len(asked) != 0 {
		t.Fatal("without watch.auto_fetch it only notifies")
	}
	r.cfg.AutoFetch = true
	if r.react(context.Background(), moved) || len(asked) != 1 || !strings.Contains(out.String(), "fetched proy: branch feature/x is now at hub/feature/x") {
		t.Fatalf("%v %s", asked, out)
	}
	if strings.Join(r.names(), ",") != "fetch" {
		t.Fatalf("it is announced to the hub: %v", r.names())
	}
	out.Reset()
	r.fetch = fetch(false, nil)
	r.react(context.Background(), moved)
	if out.Len() != 0 {
		t.Fatalf("when watch does not run in that repo, it does and says nothing: %s", out)
	}
	r.fetch = fetch(true, errors.New("no network"))
	r.react(context.Background(), moved)
	if !strings.Contains(out.String(), "could not fetch proy: no network") {
		t.Fatal(out.String())
	}
}

// fetchHere with real git: it acts only in the repo whose bridge remote is that repo, and only moves the remote branches.
func TestFetchHereWithRealGit(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	tmp := filepath.ToSlash(t.TempDir())
	git := func(d string, args ...string) string {
		cmd := procx.Background(exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...))
		cmd.Dir = d
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	bridge := tmp + "/repos/proy.git"
	git(tmp, "init", "-q", "--bare", "-b", "main", bridge)
	proj := filepath.Join(tmp, "proy")
	git(tmp, "init", "-q", "-b", "main", proj)
	os.WriteFile(filepath.Join(proj, "a"), []byte("a"), 0o644)
	git(proj, "add", ".")
	git(proj, "commit", "-qm", "a")
	git(proj, "remote", "add", "hub", "gitsi-hub:"+bridge)
	git(proj, "config", "url."+bridge+".insteadOf", "gitsi-hub:"+bridge)
	git(proj, "push", "-q", "hub", "main")
	// another clone (an agent on the hub) pushes a new branch to the bridge
	other := filepath.Join(tmp, "other")
	git(tmp, "clone", "-q", bridge, other)
	git(other, "push", "-q", "origin", "main:feature/x")

	t.Chdir(proj)
	f := fetchHere("hub", "gitsi-hub")
	if here, err := f(context.Background(), "other-repo"); here || err != nil {
		t.Fatalf("another repo: it does nothing (%v %v)", here, err)
	}
	head := git(proj, "rev-parse", "HEAD")
	if here, err := f(context.Background(), "proy"); !here || err != nil {
		t.Fatalf("%v %v", here, err)
	}
	if got := git(proj, "branch", "-r"); !strings.Contains(got, "hub/feature/x") {
		t.Fatalf("it fetches the new branch: %s", got)
	}
	if git(proj, "rev-parse", "HEAD") != head || git(proj, "status", "--porcelain") != "" {
		t.Fatal("it touches neither the branch nor the files")
	}
	t.Chdir(t.TempDir())
	if here, _ := f(context.Background(), "proy"); here {
		t.Fatal("outside a repo it does nothing")
	}
}
