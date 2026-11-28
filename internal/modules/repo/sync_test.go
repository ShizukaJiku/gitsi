package repo

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// syncRig is a pubRig whose branch is on a bridge, with the hub's working clone.
type syncRig struct {
	*pubRig
	bridge, hub string
	hubEnv      *module.Env
	hubOut      *bytes.Buffer
}

func newSyncRig(t *testing.T) *syncRig {
	t.Helper()
	p := newPubRig(t)
	r := &syncRig{pubRig: p, bridge: filepath.Join(p.tmp, "bridge.git"), hub: filepath.Join(p.tmp, "hubclone")}
	gitCmd(t, p.tmp, "init", "-q", "--bare", "-b", "main", r.bridge)
	gitCmd(t, r.w, "remote", "add", "hub", r.bridge)
	gitCmd(t, r.w, "push", "-q", "hub", "--all")
	gitCmd(t, p.tmp, "clone", "-q", "-c", "core.autocrlf=false", "-b", "feature/x", r.bridge, r.hub)
	r.hubEnv, r.hubOut = newEnv("")
	r.hubEnv.Config.Role = config.RoleHub
	return r
}

func (r *syncRig) edgeSync(args ...string) error {
	r.t.Chdir(r.w)
	r.out.Reset()
	return syncCmd(context.Background(), r.env, args)
}

func (r *syncRig) hubSync() error {
	r.t.Chdir(r.hub)
	r.hubOut.Reset()
	return syncCmd(context.Background(), r.hubEnv, nil)
}

func (r *syncRig) must(err error) {
	r.t.Helper()
	if err != nil {
		r.t.Fatalf("%v\n%s\n%s", err, r.out, r.hubOut)
	}
}

func (r *syncRig) stops(err error, want string) {
	r.t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		r.t.Fatalf("want an error with %q, got %v\n%s", want, err, r.out)
	}
}

// equal checks that the three copies of the branch have the edge's version.
func (r *syncRig) equal() {
	r.t.Helper()
	e := gitCmd(r.t, r.w, "rev-parse", "HEAD")
	if b, h := gitCmd(r.t, r.bridge, "rev-parse", "feature/x"), gitCmd(r.t, r.hub, "rev-parse", "HEAD"); b != e || h != e {
		r.t.Fatalf("edge %s, bridge %s, hub %s", e, b, h)
	}
}

func (r *syncRig) hubCommit(file string) string {
	r.t.Helper()
	r.commit(r.hub, file, "agent")
	return gitCmd(r.t, r.hub, "rev-parse", "HEAD")
}

// Edge commits on top reach the hub with a sync on each side.
func TestSyncEdgeChangesReachTheHub(t *testing.T) {
	r := newSyncRig(t)
	r.commit(r.w, "three", "t")
	r.must(r.edgeSync())
	r.must(r.hubSync())
	r.equal()
}

// The edge rewrote what it had already seen: it replaces without asking, and the hub, with nothing of its own, adopts it.
func TestSyncReplacesWhatTheEdgeRewrote(t *testing.T) {
	r := newSyncRig(t)
	r.hubCommit("fromhub")
	r.must(r.hubSync())
	r.must(r.edgeSync())
	gitCmd(t, r.w, "reset", "-q", "--soft", "main")
	gitCmd(t, r.w, "commit", "-qm", "squashed")
	r.must(r.edgeSync())
	r.write(r.hub, "untracked", "mine\n")
	r.must(r.hubSync())
	r.equal()
	if st := gitCmd(t, r.hub, "status", "--porcelain"); st != "?? untracked" {
		t.Fatalf("the hub's uncommitted work changed: %q", st)
	}
}

// New commits from the hub are shown and the edge is asked; what is replaced stays recoverable.
func TestSyncReplaceVerifiesNewHubCommits(t *testing.T) {
	r := newSyncRig(t)
	hubTip := r.hubCommit("fromhub")
	r.must(r.hubSync())
	gitCmd(t, r.w, "fetch", "-q", "hub") // having fetched it is not having had it in the branch
	r.commit(r.w, "fromedge", "t")
	r.stops(r.edgeSync(), "diverge")
	if gitCmd(t, r.bridge, "rev-parse", "feature/x") != hubTip {
		t.Fatal("the bridge changed without confirmation")
	}
	r.env.Stdin = strings.NewReader("y\n")
	r.must(r.edgeSync())
	for _, want := range []string{"1 commit(s)", "agent <agent@x>: 1", "fromhub"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
	if gitCmd(t, r.bridge, "rev-parse", "refs/gitsi/replaced/feature/x/tip") != hubTip {
		t.Fatal("the replaced version is not recoverable")
	}
	r.must(r.hubSync())
	r.equal()
}

// The edge had the hub's commit and dropped it, and the hub is strictly ahead of what it fetches: it must adopt the edge's version,
// not push its commit back.
func TestSyncHubAheadOfTheEdgeVersion(t *testing.T) {
	r := newSyncRig(t)
	r.replaceAhead()
	gitCmd(t, r.hub, "fetch", "-q") // a fetch by hand in between does not hide the replacement
	r.must(r.hubSync())
	if gitCmd(t, r.bridge, "log", "-1", "--format=%s", "feature/x") != "two" {
		t.Fatal("the edge's version must win")
	}
	r.equal()
}

// replaceAhead leaves the bridge behind the hub: the hub pushes a commit and the edge replaces it with its branch without it.
func (r *syncRig) replaceAhead() {
	r.t.Helper()
	tip := r.hubCommit("dropped")
	r.must(r.hubSync())
	gitCmd(r.t, r.w, "fetch", "-q", "hub")
	r.t.Chdir(r.w)
	r.must(replaceBridge(context.Background(), r.env, gitx.New(r.w), "hub", "feature/x", tip, false))
}

// Work the hub never pushed is not lost: neither commits nor uncommitted changes in the same files.
func TestSyncHubKeepsItsOwnWork(t *testing.T) {
	r := newSyncRig(t)
	gitCmd(t, r.w, "commit", "-q", "--amend", "-m", "rewritten")
	r.write(r.w, "two", "edge version\n")
	gitCmd(t, r.w, "commit", "-qam", "change two")
	r.must(r.edgeSync())
	r.write(r.hub, "two", "hub, uncommitted\n")
	r.stops(r.hubSync(), "uncommitted changes")
	gitCmd(t, r.hub, "checkout", "-q", "two")
	own := r.hubCommit("unpushed")
	r.stops(r.hubSync(), "git cherry-pick")
	if gitCmd(t, r.hub, "rev-parse", "HEAD") != own {
		t.Fatal("the hub's commit was lost")
	}
	// The way out it proposes works.
	err := r.hubSync()
	_, after, _ := strings.Cut(err.Error(), "git cherry-pick ")
	sha, _, _ := strings.Cut(after, "'")
	gitCmd(t, r.hub, "reset", "-q", "--keep", "origin/feature/x")
	gitCmd(t, r.hub, "cherry-pick", sha)
	r.must(r.hubSync())
	r.must(r.edgeSync())
	r.equal()
}

// Unpushed hub commits over something the edge dropped: pushing them would bring it back.
func TestSyncHubDoesNotBringBackWhatTheEdgeDropped(t *testing.T) {
	r := newSyncRig(t)
	r.replaceAhead()
	r.hubCommit("unpushed")
	r.stops(r.hubSync(), "1 commit(s) that never reached the bridge")
	if gitCmd(t, r.bridge, "log", "-1", "--format=%s", "feature/x") != "two" {
		t.Fatal("the hub brought back what the edge dropped")
	}
}

// A hub push after the check is rejected by the lease.
func TestSyncReplaceLease(t *testing.T) {
	r := newSyncRig(t)
	old := gitCmd(t, r.w, "rev-parse", "HEAD")
	gitCmd(t, r.w, "commit", "-q", "--amend", "-m", "rewritten")
	hubTip := r.hubCommit("late")
	r.must(r.hubSync())
	gitCmd(t, r.w, "fetch", "-q", "hub")
	t.Chdir(r.w)
	if err := replaceBridge(context.Background(), r.env, gitx.New(r.w), "hub", "feature/x", old, false); err == nil {
		t.Fatal("it must be rejected")
	}
	if gitCmd(t, r.bridge, "rev-parse", "feature/x") != hubTip {
		t.Fatal("the hub's commit was lost")
	}
}

func TestSyncNeedsABranch(t *testing.T) {
	r := newSyncRig(t)
	gitCmd(t, r.w, "checkout", "-q", "--detach")
	r.stops(r.edgeSync(), "not on a branch")
}

// The origin mirrors an edge keeps on the bridge (gitsi mirror: refs/gitsi/mirror/<branch>) show in the hub's working clone as
// mirror/<branch> after a sync. A clone made before gains the refspec on its first sync, and only once.
func TestHubSyncShowsTheMirrors(t *testing.T) {
	r := newSyncRig(t)
	tip := gitCmd(t, r.w, "rev-parse", "HEAD")
	gitCmd(t, r.w, "push", "-q", r.bridge, "HEAD:refs/gitsi/mirror/release/1")
	r.must(r.hubSync())
	r.must(r.hubSync())
	if got := gitCmd(t, r.hub, "rev-parse", "refs/remotes/mirror/release/1"); got != tip {
		t.Fatalf("mirror/release/1 = %q, want %q", got, tip)
	}
	if n := strings.Count(gitCmd(t, r.hub, "config", "--get-all", "remote.origin.fetch"), mirrorRefspec); n != 1 {
		t.Fatalf("the refspec is there %d times", n)
	}
}
