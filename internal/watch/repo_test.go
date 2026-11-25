package watch

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/procx"
)

const (
	c1 = "1111111111111111111111111111111111111111"
	c2 = "2222222222222222222222222222222222222222"
	c3 = "3333333333333333333333333333333333333333"
)

func TestRepoChangesAreAnnouncedOnceAndExistingBranchesAreNotNews(t *testing.T) {
	w := newWorld(t)
	w.src.set(func(s *fakeSource) { s.heads = map[string]string{"proy/main": c1} })
	stop := w.run(t)
	waitFor(t, "hello", func() bool { return strings.Contains(w.out.String(), "listening to the hub") })
	time.Sleep(60 * time.Millisecond)
	if strings.Contains(w.out.String(), "repo proy changed") {
		t.Fatalf("branches that already existed are not news:\n%s", w.out)
	}
	w.src.set(func(s *fakeSource) { s.heads = map[string]string{"proy/main": c2, "proy/feature/x": c3} })
	waitFor(t, "notices of a moved branch and a new branch", func() bool {
		s := w.out.String()
		return strings.Contains(s, "repo proy changed: branch main is at 222222222222") &&
			strings.Contains(s, "repo proy changed: branch feature/x is at 333333333333")
	})
	stop()
	if strings.Count(w.out.String(), "branch main") != 1 {
		t.Fatalf("each change is notified once:\n%s", w.out)
	}

	// on reconnect, what was notified is not repeated; what changed meanwhile is
	w.src.set(func(s *fakeSource) { s.heads["proy/main"] = c3 })
	w.out = &syncBuf{}
	w.client.Out = w.out
	stop = w.run(t)
	waitFor(t, "what changed while not listening", func() bool { return strings.Contains(w.out.String(), "branch main is at 333333333333") })
	time.Sleep(60 * time.Millisecond)
	stop()
	if strings.Contains(w.out.String(), "feature/x") {
		t.Fatalf("feature/x was already notified:\n%s", w.out)
	}
}

func TestStateWithoutHeadsAsksForABaseline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "watch-state.json")
	c := &Client{StatePath: p}
	os.WriteFile(p, []byte(`{"announced": "", "outbox": [], "jobs": []}`), 0o600)
	if _, first := c.load(); !first {
		t.Fatal("a state without branch tips asks for a baseline: existing branches are not news")
	}
	b, _ := json.Marshal(state{Heads: map[string]string{}})
	os.WriteFile(p, b, 0o600)
	if _, first := c.load(); first {
		t.Fatal("with branch tips (even empty) no baseline is needed")
	}
}

func TestRepoEventValidation(t *testing.T) {
	ok := Event{Type: EvRepo, Repo: "proy", Branch: "feature/x", Commit: c1}
	if !ok.Valid() {
		t.Fatal("valid notice rejected")
	}
	for _, e := range []Event{
		{Type: EvRepo, Repo: "pr oy", Branch: "main", Commit: c1},
		{Type: EvRepo, Repo: "proy", Branch: "../x", Commit: c1},
		{Type: EvRepo, Repo: "proy", Branch: "-x", Commit: c1},
		{Type: EvRepo, Repo: "proy", Branch: "main", Commit: "abc"},
		{Type: EvRepo, Repo: "proy", Branch: "ma\x1bin", Commit: c1},
		{Type: EvBaseline, Heads: map[string]string{"noslash": c1}},
		{Type: EvBaseline, Heads: map[string]string{"proy/main": "not-a-commit"}},
	} {
		if e.Valid() {
			t.Errorf("should be discarded: %+v", e)
		}
	}
}

// Heads reads the branches of the real bare bridge repos, loose and packed, without starting git.
func TestFSSourceReadsLooseAndPackedHeads(t *testing.T) {
	dir := t.TempDir()
	repos := filepath.Join(dir, "repos")
	git := func(d string, args ...string) string {
		cmd := procx.Background(exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...))
		cmd.Dir = d
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	bare := filepath.Join(repos, "proy.git")
	git(dir, "init", "-q", "--bare", "-b", "main", bare)
	work := filepath.Join(dir, "w")
	git(dir, "init", "-q", "-b", "main", work)
	os.WriteFile(filepath.Join(work, "a"), []byte("a"), 0o644)
	git(work, "add", ".")
	git(work, "commit", "-qm", "a")
	git(work, "push", "-q", bare, "main", "main:feature/x")
	git(bare, "pack-refs", "--all") // main and feature/x end up packed
	os.WriteFile(filepath.Join(work, "b"), []byte("b"), 0o644)
	git(work, "add", ".")
	git(work, "commit", "-qm", "b")
	git(work, "push", "-q", bare, "main") // main becomes loose again and wins over packed-refs
	os.MkdirAll(filepath.Join(repos, "no-es-repo"), 0o755)

	heads, err := (&FSSource{ReposDir: repos}).Heads()
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 || heads["proy/main"] != git(work, "rev-parse", "HEAD") || heads["proy/feature/x"] != git(work, "rev-parse", "HEAD~1") {
		t.Fatalf("%v", heads)
	}
	if h, err := (&FSSource{ReposDir: filepath.Join(dir, "missing")}).Heads(); err != nil || len(h) != 0 {
		t.Fatalf("no repos folder yet: %v %v", h, err)
	}
}
