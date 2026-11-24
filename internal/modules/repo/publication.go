package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// longLivedRe recognizes the branches several people work on: rewriting them on origin would break everyone else's copy.
var longLivedRe = regexp.MustCompile(`^(main|master|trunk|dev|devel|develop|development|staging|production|release|release/.+)$`)

// target is the state of the remote, read from it at the start.
type target struct {
	remote, branch string
	def            string // the remote's default branch; "" when unknown
	base           string // branch the PR goes to; "" when the remote has no branches
	baseTip        string // commit of base
	tip            string // commit of the branch on the remote; "" when it is not there yet
}

// remoteOut runs a git command that talks to the target remote. Its failures are not classified as the hub's (gitx.Out does):
// the one that did not answer is the target.
func remoteOut(ctx context.Context, g *gitx.Git, args ...string) (string, error) {
	out, errOut, err := g.Run(ctx, g.Dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errOut))
	}
	return strings.TrimSpace(out), nil
}

// readTarget asks the remote for its branches and fetches the base and the branch. The tracking refs alone would not do: they may
// be old, or still name a branch deleted on the remote when its PR was merged.
func readTarget(ctx context.Context, g *gitx.Git, remote, branch, base string) (target, error) {
	t := target{remote: remote, branch: branch, base: base}
	out, err := remoteOut(ctx, g, "ls-remote", "--symref", remote, "HEAD", "refs/heads/*")
	if err != nil {
		return t, err
	}
	heads := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		val, ref, ok := strings.Cut(strings.TrimSpace(l), "\t")
		if !ok {
			continue
		}
		if sym, isSym := strings.CutPrefix(val, "ref: refs/heads/"); isSym && ref == "HEAD" {
			t.def = sym
		} else if name, isHead := strings.CutPrefix(ref, "refs/heads/"); isHead {
			heads[name] = true
		}
	}
	if !heads[t.def] {
		t.def = ""
		for _, b := range []string{"main", "master"} {
			if heads[b] {
				t.def = b
				break
			}
		}
	}
	guessed := false // nobody chose the base
	if t.base == "" {
		if b := rememberedBase(ctx, g, remote, branch); heads[b] { // e.g. a release branch deleted since then does not count
			t.base = b
		}
	}
	switch {
	case len(heads) == 0:
		t.base = "" // an empty remote: the first push has nothing to compare with
		return t, nil
	case t.base == "" && t.def == "":
		return t, fmt.Errorf("'%s' does not say which its default branch is: choose the PR's with -base <branch>", remote)
	case t.base == "":
		t.base = t.def
		guessed = true
	case !heads[t.base]:
		return t, fmt.Errorf("the branch '%s' does not exist on '%s'", t.base, remote)
	}
	// Explicit refspecs: a single-branch clone does not fetch the rest by default.
	fetch := []string{"fetch", "--quiet", "--no-tags", remote}
	for b := range heads {
		if b == t.base || b == branch || guessed && longLivedRe.MatchString(b) {
			fetch = append(fetch, "+refs/heads/"+b+":refs/remotes/"+remote+"/"+b)
		}
	}
	if _, err := remoteOut(ctx, g, fetch...); err != nil {
		return t, err
	}
	if guessed && branch != t.base && !longLivedRe.MatchString(branch) {
		t.base = closestBase(ctx, g, t, heads)
	}
	if t.baseTip, err = g.Out(ctx, "rev-parse", "--verify", "refs/remotes/"+remote+"/"+t.base); err != nil {
		return t, err
	}
	if heads[branch] {
		if t.tip, err = g.Out(ctx, "rev-parse", "--verify", "refs/remotes/"+remote+"/"+branch); err != nil {
			return t, err
		}
	}
	return t, nil
}

// closestBase returns the shared branch the current one comes from: the one it has fewest commits over. With the default branch
// alone, a squash would take the commits of e.g. 'development' into the PR. A branch that already has everything does not count.
func closestBase(ctx context.Context, g *gitx.Git, t target, heads map[string]bool) string {
	count := func(b string) (n int) {
		out, err := g.Out(ctx, "rev-list", "--count", "HEAD", "^refs/remotes/"+t.remote+"/"+b)
		if err == nil {
			_, _ = fmt.Sscan(out, &n)
		}
		return n
	}
	best, least := t.base, count(t.base)
	for b := range heads {
		if b == t.base || !longLivedRe.MatchString(b) {
			continue
		}
		// On a tie the default branch wins, and then the first by name: map order must not decide.
		if n := count(b); n > 0 && (n < least || n == least && best != t.base && b < best) {
			best, least = b, n
		}
	}
	return best
}

// baseKey is the configuration key with the base of a branch.
func baseKey(branch string) string { return "branch." + branch + ".gitsi-base" }

// rememberedBase returns the base of the last publish of the branch, or "".
func rememberedBase(ctx context.Context, g *gitx.Git, remote, branch string) string {
	out, err := g.Out(ctx, "config", "--get", baseKey(branch))
	if f := strings.Fields(out); err == nil && len(f) == 2 && f[0] == remote && safe.Branch(f[1]) == nil {
		return f[1]
	}
	return ""
}

// publication is what is about to be pushed.
type publication struct {
	g *gitx.Git
	target
	head     string
	commit   string // what the remote branch will point to
	forced   bool   // the push replaces the remote branch instead of moving it forward
	replaces bool   // ...and what it replaces was not put there by the last publish of this clone
}

// prepare decides what is pushed, checks that it loses nothing on the remote and shows it. It changes neither the branch nor the
// remote (at most it adds an object to the local repository).
func (p *publication) prepare(ctx context.Context, out, warn io.Writer) error {
	g := p.g
	var err error
	if p.head, err = g.Out(ctx, "rev-parse", "HEAD"); err != nil {
		return err
	}
	p.commit = p.head
	if p.base == "" {
		fmt.Fprintf(out, "'%s' is empty: the branch is pushed as it is.\n", p.remote)
		return nil
	}
	if p.branch == p.base || p.branch == p.def || longLivedRe.MatchString(p.branch) {
		// Shared by everyone: its history is never rewritten, and the push is a plain one.
		fmt.Fprintf(out, "'%s' is a shared branch: it is pushed as it is, without rewriting anything.\n", p.branch)
		from := p.baseTip
		if p.tip != "" {
			if !isAncestor(ctx, g, p.tip, p.head) {
				return fmt.Errorf("%s/%s has commits that are not here: bring them with 'git pull %s %s' and publish again", p.remote, p.branch, p.remote, p.branch)
			}
			from = p.tip
		}
		return p.summary(ctx, out, "To push", from, p.head)
	}

	mergeBase, err := g.Out(ctx, "merge-base", p.baseTip, p.head)
	if err != nil {
		return fmt.Errorf("the branch has no common history with %s/%s (in a shallow clone: git fetch --unshallow)", p.remote, p.base)
	}
	commits, err := p.lines(ctx, "rev-list", p.head, "^"+p.baseTip)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("nothing to publish: the branch has no commits over %s/%s", p.remote, p.base)
	}
	baseTree, err := g.Out(ctx, "rev-parse", p.baseTip+"^{tree}")
	if err != nil {
		return err
	}
	merged, state := p.mergeTree(ctx, p.baseTip, p.head)
	switch {
	case state == mergeClean && merged == baseTree:
		return fmt.Errorf("nothing to publish: %s/%s already has all the changes of the branch (was its PR merged?)", p.remote, p.base)
	case state == mergeConflict:
		fmt.Fprintf(warn, "Warning: the PR will conflict with %s/%s. To resolve it here: git pull --no-rebase %s %s\n", p.remote, p.base, p.remote, p.base)
	}
	if err := p.checkRemote(ctx); err != nil {
		return err
	}
	if err := p.summary(ctx, out, "Branch", p.baseTip, p.head); err != nil {
		return err
	}
	if err := p.rewrite(ctx, mergeBase); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s -> %s/%s\n", p.branch, p.remote, p.base)
	if err := p.summary(ctx, out, "PR", p.baseTip, p.commit); err != nil {
		return err
	}
	if p.tip != "" && !isAncestor(ctx, g, p.tip, p.commit) {
		p.forced = true
		pushed, _ := publishedAs(ctx, g, p.remote, p.branch)
		p.replaces = p.tip != pushed
	}
	return nil
}

// rewrite leaves in p.commit the branch as one commit of this machine's user over mergeBase: the equivalent of a squash plus
// 'git commit --amend --no-edit --reset-author', without moving the branch.
func (p *publication) rewrite(ctx context.Context, mergeBase string) error {
	g := p.g
	author, committer, err := identity(ctx, g)
	if err != nil {
		return err
	}
	tree, err := g.Out(ctx, "rev-parse", p.head+"^{tree}")
	if err != nil {
		return err
	}
	// A commit already as it should be is not made again: publishing twice does not change the PR's hashes.
	want := strings.Join([]string{tree, mergeBase, author, committer}, "\n")
	for _, c := range []string{p.tip, p.head} {
		if c == "" {
			continue
		}
		got, err := g.Out(ctx, "show", "-s", "--no-show-signature", "--format=%T%n%P%n%an <%ae>%n%cn <%ce>", c, "--")
		if err == nil && got == want {
			p.commit = c
			return nil
		}
	}
	msg, err := p.squashMessage(ctx)
	if err != nil {
		return err
	}
	// By file: a long message does not fit in a Windows command line.
	f, err := os.CreateTemp("", "gitsi-publish-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, werr := f.WriteString(strings.TrimSpace(msg) + "\n")
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	args := []string{"commit-tree", tree, "-p", mergeBase, "-F", f.Name()}
	if sign, _ := g.Out(ctx, "config", "--type=bool", "commit.gpgsign"); sign == "true" {
		args = append(args, "-S") // commit-tree does not look at commit.gpgsign
	}
	p.commit, err = g.Out(ctx, args...)
	return err
}

// squashMessage keeps a single commit's message; with several, the first one's subject is the title and every message follows.
func (p *publication) squashMessage(ctx context.Context) (string, error) {
	out, err := p.g.Out(ctx, "log", "--no-show-signature", "--no-merges", "--reverse", "--topo-order", "--format=%B%x00", p.head, "^"+p.baseTip, "--")
	if err != nil {
		return "", err
	}
	var msgs []string
	for _, m := range strings.Split(out, "\x00") {
		if m = strings.TrimSpace(m); m != "" {
			msgs = append(msgs, m)
		}
	}
	switch len(msgs) {
	case 0:
		return p.branch, nil
	case 1:
		return msgs[0], nil
	}
	title, _, _ := strings.Cut(msgs[0], "\n")
	return strings.TrimSpace(title) + "\n\n* " + strings.Join(msgs, "\n\n* "), nil
}

// identity is the Git user of this machine, as "name <email>", in the two roles a commit records.
func identity(ctx context.Context, g *gitx.Git) (author, committer string, err error) {
	var ids []string
	for _, v := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		out, err := g.Out(ctx, "var", v)
		i := strings.LastIndex(out, ">")
		if err != nil || i < 0 {
			return "", "", errors.New("git does not know who publishes: set it with 'git config user.name <name>' and 'git config user.email <email>'")
		}
		ids = append(ids, out[:i+1])
	}
	return ids[0], ids[1], nil
}

// checkRemote makes sure that replacing the remote branch loses no work: everything in it must be in the local branch, or be what
// this clone published.
func (p *publication) checkRemote(ctx context.Context) error {
	if p.tip == "" || isAncestor(ctx, p.g, p.tip, p.head) {
		return nil
	}
	pushed, _ := publishedAs(ctx, p.g, p.remote, p.branch)
	if p.tip == pushed || pushed == "" && p.squashedHere(ctx) {
		return nil
	}
	args := []string{"rev-list", "--reverse", "--topo-order", "--cherry-pick", "--right-only", p.head + "..." + p.tip, "^" + p.baseTip}
	if pushed != "" && p.g.HasRef(ctx, pushed+"^{commit}") {
		args = append(args, "^"+pushed)
	}
	list, err := p.lines(ctx, args...)
	if err != nil {
		return err
	}
	var foreign []string
	hasMerge := false
	for _, c := range list {
		parents, err := p.g.Out(ctx, "show", "-s", "--no-show-signature", "--format=%P", c, "--")
		if err != nil {
			return err
		}
		if ps := strings.Fields(parents); len(ps) > 1 {
			// GitHub's "Update branch": a merge with nothing of its own is not work to preserve.
			tree, state := p.mergeTree(ctx, ps[0], ps[1])
			own, _ := p.g.Out(ctx, "rev-parse", c+"^{tree}")
			if len(ps) == 2 && state == mergeClean && tree == own {
				continue
			}
			hasMerge = true
		}
		foreign = append(foreign, c)
	}
	if len(foreign) == 0 {
		return nil
	}
	fix := "git pull --no-rebase " + p.remote + " " + p.branch
	if !hasMerge && len(foreign) <= 5 {
		fix = "git cherry-pick"
		for _, c := range foreign {
			fix += " " + c[:min(10, len(c))]
		}
	}
	return fmt.Errorf("%s/%s has %d commit(s) that are not here and would be lost (%s). Bring them with '%s' and publish again",
		p.remote, p.branch, len(foreign), strings.Join(p.authors(ctx, foreign), ", "), fix)
}

// squashedHere recognizes a squash of this branch published from another clone: a single commit whose content is exactly that of
// a commit of the local branch.
func (p *publication) squashedHere(ctx context.Context) bool {
	if n, err := p.g.Out(ctx, "rev-list", "--count", p.tip, "^"+p.baseTip); err != nil || n != "1" {
		return false
	}
	tree, err := p.g.Out(ctx, "rev-parse", p.tip+"^{tree}")
	if err != nil {
		return false
	}
	trees, err := p.lines(ctx, "log", "--no-show-signature", "--format=%T", p.head, "^"+p.baseTip, "--")
	if err != nil {
		return false
	}
	for _, t := range trees {
		if t == tree {
			return true
		}
	}
	return false
}

// push updates the remote branch; when it replaces commits, only if it is still where readTarget saw it: someone else's push in
// between is rejected. The lease names the commit, because a background fetch would defeat the implicit one.
func (p *publication) push(ctx context.Context, stdout, stderr io.Writer) error {
	ref := "refs/heads/" + p.branch
	args := []string{"push"}
	if p.forced {
		args = append(args, "--force-with-lease="+ref+":"+p.tip)
	}
	if err := p.g.Passthrough(ctx, stdout, stderr, append(args, p.remote, p.commit+":"+ref)...); err != nil {
		return fmt.Errorf("the push was not accepted (the reason is above; if '%s' changed in the meantime, publish again): %w", p.remote, err)
	}
	return nil
}

// publishedKey is the configuration key with the last publish. In the branch's section, 'git branch -d' deletes it.
func publishedKey(branch string) string { return "branch." + branch + ".gitsi-published" }

// record notes what was published, from which commit and over which base. Without it the next publish only asks more, so failing is not an error.
func (p *publication) record(ctx context.Context) {
	_, _ = p.g.Out(ctx, "config", publishedKey(p.branch), p.remote+" "+p.commit+" "+p.head)
	if p.base != "" && p.base != p.branch {
		_, _ = p.g.Out(ctx, "config", baseKey(p.branch), p.remote+" "+p.base) // the next publish goes to the same PR
	}
}

// publishedAs returns the commit this clone last published on the remote for the branch and the local commit it came from ("" when
// there is none).
func publishedAs(ctx context.Context, g *gitx.Git, remote, branch string) (pushed, source string) {
	out, err := g.Out(ctx, "config", "--get", publishedKey(branch))
	if f := strings.Fields(out); err == nil && len(f) == 3 && f[0] == remote {
		return f[1], f[2]
	}
	return "", ""
}

// sincePublish returns how many local commits came after the last publish; ok=false when the remote branch is not what this clone
// published. A squashed publish shares no commits with the branch: comparing histories would report work to receive.
func sincePublish(ctx context.Context, g *gitx.Git, branch, remote string) (n int, ok bool) {
	pushed, source := publishedAs(ctx, g, remote, branch)
	if pushed == "" {
		return 0, false
	}
	tip, err := g.Out(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+branch)
	if err != nil || tip != pushed || !isAncestor(ctx, g, source, "HEAD") {
		return 0, false
	}
	out, err := g.Out(ctx, "rev-list", "--count", source+"..HEAD")
	if err != nil {
		return 0, false
	}
	if _, err := fmt.Sscan(out, &n); err != nil {
		return 0, false
	}
	return n, true
}

// summary shows how many commits go from one commit to another, whose they are and how much they change.
func (p *publication) summary(ctx context.Context, out io.Writer, label, from, to string) error {
	commits, err := p.lines(ctx, "rev-list", to, "^"+from)
	if err != nil {
		return err
	}
	stat, err := p.g.Out(ctx, "diff", "--shortstat", from+"..."+to, "--")
	if err != nil {
		return err
	}
	if stat == "" {
		stat = "no changes"
	}
	fmt.Fprintf(out, "%-8s %d commit(s), %s\n", label+":", len(commits), stat)
	for _, a := range p.authors(ctx, commits) {
		fmt.Fprintf(out, "         %s\n", a)
	}
	return nil
}

// authors returns the authors of the commits with how many are theirs, in order of appearance.
func (p *publication) authors(ctx context.Context, commits []string) []string {
	count := map[string]int{}
	var order []string
	// In batches: a branch with many commits does not fit in a Windows command line.
	for len(commits) > 0 {
		n := min(200, len(commits))
		args := append([]string{"show", "-s", "--no-show-signature", "--format=%an <%ae>"}, commits[:n]...)
		commits = commits[n:]
		list, err := p.lines(ctx, append(args, "--")...)
		if err != nil {
			continue
		}
		for _, a := range list {
			if a = printable(a); count[a] == 0 {
				order = append(order, a)
			}
			count[a]++
		}
	}
	for i, a := range order {
		order[i] = fmt.Sprintf("%s: %d", a, count[a])
	}
	return order
}

// printable removes control characters (an author name comes from outside and ends up on the terminal).
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// lines runs git and returns its output lines.
func (p *publication) lines(ctx context.Context, args ...string) ([]string, error) {
	out, err := p.g.Out(ctx, args...)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// isAncestor reports whether commit b contains commit a.
func isAncestor(ctx context.Context, g *gitx.Git, a, b string) bool {
	_, err := g.Out(ctx, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

type mergeState int

const (
	mergeUnknown  mergeState = iota // git older than 2.38, or it failed
	mergeClean                      // it merges without conflicts
	mergeConflict                   // it conflicts
)

// mergeTree merges two commits without touching the working folder and returns the resulting tree.
func (p *publication) mergeTree(ctx context.Context, a, b string) (string, mergeState) {
	out, _, err := p.g.Run(ctx, p.g.Dir, "merge-tree", "--write-tree", "--no-messages", a, b)
	tree, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if err == nil {
		return tree, mergeClean
	}
	var ee interface{ ExitCode() int }
	if errors.As(err, &ee) && ee.ExitCode() == 1 && tree != "" {
		return tree, mergeConflict
	}
	return "", mergeUnknown
}
