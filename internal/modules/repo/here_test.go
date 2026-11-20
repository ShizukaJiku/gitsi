package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHereComparesTheCurrentRepoWithTheBridge(t *testing.T) {
	gitEnv(t)
	tmp := t.TempDir()
	env, _ := newEnv("")
	ctx := context.Background()

	t.Chdir(tmp)
	if got, _ := Here(ctx, env); got != "this folder is not a Git repo" {
		t.Fatalf("%q", got)
	}

	bare, proj := filepath.Join(tmp, "bridge.git"), filepath.Join(tmp, "proy")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", bare)
	gitCmd(t, tmp, "init", "-q", "-b", "main", proj)
	commit := func(dir, file string) {
		os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644)
		gitCmd(t, dir, "add", ".")
		gitCmd(t, dir, "commit", "-qm", file)
	}
	commit(proj, "uno")
	t.Chdir(proj)
	if got, _ := Here(ctx, env); got != "proy (main): not on the hub (gitsi share)" {
		t.Fatalf("%q", got)
	}
	gitCmd(t, proj, "remote", "add", "hub", bare)
	if got, _ := Here(ctx, env); got != "proy (main): the branch is not on the hub (gitsi sync)" {
		t.Fatalf("%q", got)
	}
	gitCmd(t, proj, "push", "-q", "hub", "main")
	if got, _ := Here(ctx, env); got != "proy (main): up to date with the hub" {
		t.Fatalf("%q", got)
	}
	// another clone pushes a commit to the bridge and here there is an unsent local one: Here fetches and counts both sides
	other := filepath.Join(tmp, "otro")
	gitCmd(t, tmp, "clone", "-q", bare, other)
	commit(other, "dos")
	gitCmd(t, other, "push", "-q", "origin", "main")
	commit(proj, "tres")
	if got, _ := Here(ctx, env); got != "proy (main): 1 to send, 1 to receive (gitsi sync)" {
		t.Fatalf("%q", got)
	}
	if strings.Contains(gitCmd(t, proj, "log", "--oneline"), "dos") {
		t.Fatal("status fetches the remote branches but does not touch the local branch")
	}
	// unreachable bridge: compares with the last fetch and says so
	gitCmd(t, proj, "remote", "set-url", "hub", filepath.Join(tmp, "missing.git"))
	if got, _ := Here(ctx, env); !strings.HasSuffix(got, "(not fetched: the hub did not answer)") {
		t.Fatalf("%q", got)
	}
}
