package repo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// pubRig is an edge clone on feature/x with two commits of another author (the hub's agent), over an origin with main.
type pubRig struct {
	t              *testing.T
	tmp, origin, w string
	env            *module.Env
	out            *bytes.Buffer
}

func newPubRig(t *testing.T) *pubRig {
	t.Helper()
	gitEnv(t)
	r := &pubRig{t: t, tmp: t.TempDir()}
	r.origin, r.w = filepath.Join(r.tmp, "origin.git"), filepath.Join(r.tmp, "w")
	gitCmd(t, r.tmp, "init", "-q", "--bare", "-b", "main", r.origin)
	gitCmd(t, r.tmp, "init", "-q", "-b", "main", r.w)
	gitCmd(t, r.w, "config", "core.autocrlf", "false")
	gitCmd(t, r.w, "remote", "add", "origin", r.origin)
	r.commit(r.w, "base", "t")
	gitCmd(t, r.w, "push", "-q", "origin", "main")
	gitCmd(t, r.w, "checkout", "-q", "-b", "feature/x")
	r.commit(r.w, "one", "agent")
	r.commit(r.w, "two", "agent")
	r.env, r.out = newEnv("")
	t.Chdir(r.w)
	return r
}

func (r *pubRig) write(dir, file, text string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *pubRig) commit(dir, file, author string) {
	r.t.Helper()
	r.write(dir, file, file+"\n")
	gitCmd(r.t, dir, "add", file)
	gitCmd(r.t, dir, "commit", "-qm", file, "--author", author+" <"+author+"@x>")
}

func (r *pubRig) publish(args ...string) error {
	r.out.Reset()
	return publish(context.Background(), r.env, args)
}

func (r *pubRig) ok(args ...string) {
	r.t.Helper()
	if err := r.publish(args...); err != nil {
		r.t.Fatalf("%v\n%s", err, r.out)
	}
}

// yes publishes answering yes to the question.
func (r *pubRig) yes(args ...string) {
	r.t.Helper()
	r.env.Stdin = strings.NewReader("y\n")
	r.ok(args...)
	r.env.Stdin = nil
}

func (r *pubRig) fails(want string, args ...string) {
	r.t.Helper()
	before := gitCmd(r.t, r.origin, "for-each-ref")
	if err := r.publish(args...); err == nil || !strings.Contains(err.Error(), want) {
		r.t.Fatalf("want an error with %q, got %v\n%s", want, err, r.out)
	}
	if after := gitCmd(r.t, r.origin, "for-each-ref"); after != before {
		r.t.Fatalf("origin changed:\n%s\n%s", before, after)
	}
}

// pr checks the PR as origin sees it: "author|committer|subject" per commit.
func (r *pubRig) pr(want string) {
	r.t.Helper()
	if got := gitCmd(r.t, r.origin, "log", "--format=%an|%cn|%s", "main..feature/x"); got != want {
		r.t.Fatalf("origin has:\n%s\nwant:\n%s", got, want)
	}
}

// has checks that the PR carries the file.
func (r *pubRig) has(file string) {
	r.t.Helper()
	if got := gitCmd(r.t, r.origin, "show", "feature/x:"+file); got != file {
		r.t.Fatalf("%s = %q", file, got)
	}
}

// reviewer pushes a commit to the PR from another clone.
func (r *pubRig) reviewer() {
	r.t.Helper()
	d := filepath.Join(r.tmp, "reviewer")
	gitCmd(r.t, r.tmp, "clone", "-q", "-b", "feature/x", r.origin, d)
	r.commit(d, "review", "reviewer")
	gitCmd(r.t, d, "push", "-q", "origin", "feature/x")
}

// One commit, of the edge's user, and nothing local moves: not the branch (the bridge would diverge), not uncommitted work.
func TestPublishSquashesWithTheEdgeAuthor(t *testing.T) {
	r := newPubRig(t)
	head := gitCmd(t, r.w, "rev-parse", "HEAD")
	r.write(r.w, "one", "dirty\n")
	r.ok()
	r.pr("t|t|one")
	if body := gitCmd(t, r.origin, "log", "-1", "--format=%b", "feature/x"); !strings.Contains(body, "* one") || !strings.Contains(body, "* two") {
		t.Fatalf("body: %s", body)
	}
	for _, want := range []string{"Branch:  2 commit(s)", "agent <agent@x>: 2", "PR:      1 commit(s)", "t <t@t>: 1"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
	if gitCmd(t, r.w, "rev-parse", "HEAD") != head || gitCmd(t, r.w, "status", "--porcelain") != "M one" {
		t.Fatal("the local branch or the working folder changed")
	}
	r.has("one")
}

// With the bridge, sync keeps working after a publish, and the next publish is again one commit.
func TestPublishDoesNotDivergeFromTheBridge(t *testing.T) {
	r := newPubRig(t)
	bridge := filepath.Join(r.tmp, "bridge.git")
	gitCmd(t, r.tmp, "init", "-q", "--bare", "-b", "main", bridge)
	gitCmd(t, r.w, "remote", "add", "hub", bridge)
	gitCmd(t, r.w, "push", "-q", "hub", "--all")
	r.ok()
	hub := filepath.Join(r.tmp, "hubclone")
	gitCmd(t, r.tmp, "clone", "-q", "-b", "feature/x", bridge, hub)
	r.commit(hub, "three", "agent")
	gitCmd(t, hub, "push", "-q", "origin", "feature/x")
	if err := syncCmd(context.Background(), r.env, nil); err != nil {
		t.Fatalf("sync after publish: %v", err)
	}
	r.ok()
	r.pr("t|t|one")
	r.has("three")
}

// Publishing again: without changes the hash stays; with changes it is replaced, still one commit, without asking.
func TestPublishAgain(t *testing.T) {
	r := newPubRig(t)
	r.ok()
	first := gitCmd(t, r.origin, "rev-parse", "feature/x")
	r.ok()
	if !strings.Contains(r.out.String(), "Already published") || gitCmd(t, r.origin, "rev-parse", "feature/x") != first {
		t.Fatalf("it published again without changes:\n%s", r.out)
	}
	r.commit(r.w, "three", "agent")
	r.out.Reset()
	if err := repoStatus(context.Background(), r.env, nil); err != nil || !strings.Contains(r.out.String(), "1 to send, 0 to receive") {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if err := syncCmd(context.Background(), r.env, nil); err == nil || !strings.Contains(err.Error(), "gitsi publish") {
		t.Fatalf("sync against origin: %v", err)
	}
	r.ok()
	r.pr("t|t|one")
	r.has("three")
	// The local history was rewritten: what is on origin is this clone's, so it is replaced.
	gitCmd(t, r.w, "commit", "-q", "--amend", "-m", "renamed")
	r.commit(r.w, "four", "agent")
	r.ok()
	r.pr("t|t|one")
}

// The only commit, of another author: same message, author reset. One already of this user is pushed untouched.
func TestPublishSingleCommit(t *testing.T) {
	r := newPubRig(t)
	gitCmd(t, r.w, "reset", "-q", "--hard", "HEAD~1")
	r.ok()
	r.pr("t|t|one")
	gitCmd(t, r.w, "checkout", "-q", "-b", "mine", "main")
	r.commit(r.w, "own", "t")
	gitCmd(t, r.w, "commit", "-q", "--amend", "--no-edit", "--reset-author")
	r.ok()
	if gitCmd(t, r.origin, "rev-parse", "mine") != gitCmd(t, r.w, "rev-parse", "HEAD") {
		t.Fatal("a commit that was already right must not be rewritten")
	}
}

// A reviewer's commit on the PR is never overwritten, not even with -yes; once brought, publishing goes on.
func TestPublishKeepsOthersWork(t *testing.T) {
	r := newPubRig(t)
	r.ok()
	r.reviewer()
	r.fails("would be lost")
	err := r.publish()
	if err == nil || !strings.Contains(err.Error(), "reviewer <reviewer@x>: 1") {
		t.Fatalf("the error does not say whose: %v", err)
	}
	_, after, _ := strings.Cut(err.Error(), "git cherry-pick ")
	sha, _, _ := strings.Cut(after, "'")
	gitCmd(t, r.w, "cherry-pick", sha)
	r.yes()
	r.pr("t|t|one")
	r.has("review")
}

// GitHub's "Update branch" adds a merge of main with nothing of its own: it does not block.
func TestPublishAfterUpdateBranch(t *testing.T) {
	r := newPubRig(t)
	r.ok()
	d := filepath.Join(r.tmp, "gh")
	gitCmd(t, r.tmp, "clone", "-q", r.origin, d)
	r.commit(d, "mainwork", "someone")
	gitCmd(t, d, "push", "-q", "origin", "main")
	gitCmd(t, d, "checkout", "-q", "feature/x")
	gitCmd(t, d, "merge", "-q", "--no-edit", "main")
	gitCmd(t, d, "push", "-q", "origin", "feature/x")
	r.commit(r.w, "three", "agent")
	r.yes()
	r.pr("t|t|one")
	r.has("three")
}

// Commits on origin that this clone did not publish are replaced only after asking, even when they are contained here.
func TestPublishAsksBeforeReplacing(t *testing.T) {
	r := newPubRig(t)
	gitCmd(t, r.w, "push", "-q", "origin", "feature/x")
	r.fails("cancelled")
	r.env.Stdin = strings.NewReader("y\n")
	r.ok()
	r.pr("t|t|one")
}

// Another clone of the same branch recognizes the squash the first one published.
func TestPublishFromAnotherClone(t *testing.T) {
	r := newPubRig(t)
	r.ok()
	gitCmd(t, r.w, "config", "--unset", publishedKey("feature/x"))
	r.commit(r.w, "three", "agent")
	r.yes()
	r.pr("t|t|one")
}

// The push is conditioned to what was seen: a push that lands after the check is rejected, not overwritten.
func TestPublishLeaseRejectsARace(t *testing.T) {
	r := newPubRig(t)
	r.ok()
	r.commit(r.w, "three", "agent")
	ctx := context.Background()
	g := gitx.New(r.w)
	tg, err := readTarget(ctx, g, "origin", "feature/x", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &publication{g: g, target: tg}
	if err := p.prepare(ctx, r.out, r.out); err != nil || !p.forced {
		t.Fatalf("%v forced=%v", err, p.forced)
	}
	r.reviewer()
	gitCmd(t, r.w, "fetch", "-q", "origin") // a background fetch must not defeat the lease
	if err := p.push(ctx, r.out, r.out); err == nil {
		t.Fatal("the race must be rejected")
	}
	r.has("review")
}

func TestPublishUnknownBase(t *testing.T) {
	r := newPubRig(t)
	r.fails("does not exist", "-base", "missing")
}

// Shared branches are never rewritten, not even when another base is given.
func TestPublishNeverRewritesSharedBranches(t *testing.T) {
	r := newPubRig(t)
	for _, b := range []string{"develop", "main"} {
		gitCmd(t, r.w, "checkout", "-q", "-B", b, "feature/x")
		r.ok("-base", "main")
		if gitCmd(t, r.origin, "rev-parse", b) != gitCmd(t, r.w, "rev-parse", "feature/x") {
			t.Fatalf("%s was rewritten", b)
		}
	}
	gitCmd(t, r.w, "commit", "-q", "--amend", "-m", "rewritten")
	r.fails("has commits that are not here")
}

func TestPublishNothingToPublish(t *testing.T) {
	r := newPubRig(t)
	r.ok()
	// The PR is merged and its branch deleted: publishing again must not recreate it.
	gitCmd(t, r.origin, "update-ref", "refs/heads/main", "feature/x")
	gitCmd(t, r.origin, "update-ref", "-d", "refs/heads/feature/x")
	r.fails("already has all the changes")
	gitCmd(t, r.w, "checkout", "-q", "-b", "empty", "main")
	r.fails("no commits over")
	gitCmd(t, r.w, "checkout", "-q", "--detach", "feature/x")
	r.fails("not on a branch")
}

func TestPublishWarnsAboutConflicts(t *testing.T) {
	r := newPubRig(t)
	d := filepath.Join(r.tmp, "gh")
	gitCmd(t, r.tmp, "clone", "-q", r.origin, d)
	r.write(d, "one", "different\n")
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "commit", "-qm", "conflict")
	gitCmd(t, d, "push", "-q", "origin", "main")
	r.ok()
	if !strings.Contains(r.out.String(), "will conflict with origin/main") {
		t.Fatalf("no warning:\n%s", r.out)
	}
}

func TestPublishNeedsAnIdentity(t *testing.T) {
	r := newPubRig(t)
	for _, v := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(v, "") // so it is restored
		os.Unsetenv(v)
	}
	t.Setenv("EMAIL", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitCmd(t, r.w, "config", "user.useConfigOnly", "true")
	r.fails("user.name")
}

func TestPrintable(t *testing.T) {
	if got := printable("a\x1b[31mb\u009bc"); got != "a[31mbc" {
		t.Fatalf("%q", got)
	}
}

// A branch born from development, with main as the default branch: squashing it over main would take development's commits into
// the PR. publish finds the base and remembers it, so the PR's commit stays over the same one.
func TestPublishFindsTheBaseAndRemembersIt(t *testing.T) {
	r := newPubRig(t)
	gitCmd(t, r.w, "checkout", "-q", "-b", "development", "main")
	r.commit(r.w, "devwork", "someone")
	gitCmd(t, r.w, "push", "-q", "origin", "development")
	gitCmd(t, r.w, "checkout", "-q", "-B", "feature/x", "development")
	r.commit(r.w, "one", "agent")
	r.commit(r.w, "two", "agent")
	r.ok()
	if !strings.Contains(r.out.String(), "feature/x -> origin/development") {
		t.Fatalf("it does not say where the PR goes:\n%s", r.out)
	}
	r.commit(r.w, "three", "agent")
	r.ok()
	if got := gitCmd(t, r.origin, "log", "--format=%an|%s", "development..feature/x"); got != "t|one" {
		t.Fatalf("origin has:\n%s", got)
	}
	gitCmd(t, r.w, "push", "-q", "origin", "HEAD~1:refs/heads/release/1") // closer now, but the PR goes to development
	r.commit(r.w, "four", "agent")
	r.ok()
	if !strings.Contains(r.out.String(), "feature/x -> origin/development") {
		t.Fatalf("the base changed:\n%s", r.out)
	}
	gitCmd(t, r.w, "config", baseKey("feature/x"), "origin deleted")
	r.ok()
	r.ok("-base", "main")
	if !strings.Contains(r.out.String(), "feature/x -> origin/main") {
		t.Fatalf("-base must win:\n%s", r.out)
	}
}

// The full case of a PR whose commit was re-authored by hand on the edge: the hub keeps adding to its own version of it.
func TestPublishReplacesAManualSquash(t *testing.T) {
	r := newSyncRig(t)
	gitCmd(t, r.w, "checkout", "-q", "-b", "byhand", "main")
	gitCmd(t, r.w, "cherry-pick", "main..feature/x")
	gitCmd(t, r.w, "commit", "-q", "--amend", "--no-edit", "--reset-author")
	gitCmd(t, r.w, "push", "-q", "origin", "byhand:feature/x")
	gitCmd(t, r.w, "checkout", "-q", "feature/x")
	r.hubCommit("docker")
	r.hubCommit("fix")
	r.must(r.hubSync())
	r.must(r.edgeSync())
	r.t.Chdir(r.w)
	r.yes()
	r.pr("t|t|one")
	r.has("fix")
	r.must(r.edgeSync())
	r.must(r.hubSync())
	r.equal()
}

// A merge on origin that carries changes of its own is work: it stops the publish.
func TestPublishKeepsAMergeWithItsOwnChanges(t *testing.T) {
	r := newPubRig(t)
	r.ok()
	d := filepath.Join(r.tmp, "gh")
	gitCmd(t, r.tmp, "clone", "-q", r.origin, d)
	r.commit(d, "mainwork", "someone")
	gitCmd(t, d, "push", "-q", "origin", "main")
	gitCmd(t, d, "checkout", "-q", "feature/x")
	gitCmd(t, d, "merge", "-q", "--no-commit", "--no-ff", "main")
	r.commit(d, "extra", "someone")
	gitCmd(t, d, "push", "-q", "origin", "feature/x")
	r.fails("git pull --no-rebase")
}
