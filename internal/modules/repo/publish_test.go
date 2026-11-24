package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
)

func TestPRURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/app.git":    "https://github.com/acme/app/pull/new/feat",
		"git@github.com:acme/app.git":        "https://github.com/acme/app/pull/new/feat",
		"https://github.com/acme/app":        "https://github.com/acme/app/pull/new/feat",
		"https://gitlab.example.com/a/b.git": "",
	}
	for in, want := range cases {
		if got := prURL(in, "", "feat"); got != want {
			t.Errorf("prURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := prURL("https://github.com/acme/app", "next", "feat"); got != "https://github.com/acme/app/compare/next...feat?expand=1" {
		t.Error(got)
	}
}

func TestBridgeURL(t *testing.T) {
	cfg := config.Default()
	if got := bridgeURL(cfg, "C:/Users/user/repos/", "x"); got != "gitsi-hub:C:/Users/user/repos/x.git" {
		t.Fatal(got)
	}
}

// publish pushes to origin (not to the bridge) and leaves the upstream on origin even if the branch tracked the bridge.
func TestPublishGoesToOriginAndFixesUpstream(t *testing.T) {
	gitEnv(t)
	tmp := t.TempDir()
	bridge := filepath.Join(tmp, "bridge.git")
	origin := filepath.Join(tmp, "origin.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", bridge)
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", origin)
	w := filepath.Join(tmp, "w")
	gitCmd(t, tmp, "init", "-q", "-b", "main", w)
	if err := os.WriteFile(filepath.Join(w, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, w, "add", ".")
	gitCmd(t, w, "commit", "-qm", "uno")
	gitCmd(t, w, "remote", "add", "hub", bridge)
	gitCmd(t, w, "remote", "add", "origin", origin)
	gitCmd(t, w, "push", "-q", "-u", "hub", "main") // wrong upstream on purpose

	env, out := newEnv("")
	t.Chdir(w)
	if err := publish(context.Background(), env, nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := gitCmd(t, origin, "log", "--oneline", "main"); !strings.Contains(got, "uno") {
		t.Fatalf("origin did not receive the commit: %s", got)
	}
	if up := gitCmd(t, w, "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/main" {
		t.Fatalf("upstream = %q, want origin/main", up)
	}

	// publishing to the bridge is not allowed
	if err := publish(context.Background(), env, []string{"-remote", "hub"}); err == nil {
		t.Fatal("publish to the bridge must be rejected")
	}
}

func TestPublishNeedsOrigin(t *testing.T) {
	gitEnv(t)
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	t.Chdir(d)
	env, _ := newEnv("")
	if err := publish(context.Background(), env, nil); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("expected an error for the missing origin: %v", err)
	}
}

func TestRepoStatus(t *testing.T) {
	gitEnv(t)
	tmp := t.TempDir()
	bridge := filepath.Join(tmp, "bridge.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", bridge)
	w := filepath.Join(tmp, "w")
	gitCmd(t, tmp, "init", "-q", "-b", "main", w)
	commit := func(f string) {
		if err := os.WriteFile(filepath.Join(w, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, w, "add", ".")
		gitCmd(t, w, "commit", "-qm", f)
	}
	commit("uno")
	gitCmd(t, w, "remote", "add", "hub", bridge)
	gitCmd(t, w, "push", "-q", "hub", "main")
	commit("dos") // 1 to send to the bridge
	if err := os.WriteFile(filepath.Join(w, "dirty"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	env, out := newEnv("")
	t.Chdir(w)
	if err := repoStatus(context.Background(), env, []string{"-fetch"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := out.String()
	for _, want := range []string{"Branch: main", "Uncommitted changes: 1", "1 to send, 0 to receive", "no 'origin' remote"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
