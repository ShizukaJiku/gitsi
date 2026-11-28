package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// syncCmd pulls and pushes the current branch against the bridge. It fast-forwards; when the branches diverge it goes on only if
// nobody loses work unseen: the edge replaces on the bridge what it had already seen (it asks when the hub has new commits), and
// the hub adopts a branch the edge replaced.
func syncCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := newFlags("sync", env.Stderr)
	remote := fs.String("remote", "", "remote to use (default: the bridge's, or origin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	isHub := env.Config.Role == config.RoleHub

	g := gitx.New("")
	root, err := g.Root(ctx)
	if err != nil {
		return errors.New("this folder is not a Git repository")
	}
	g.Dir = root

	rn := *remote
	if rn == "" {
		switch {
		case g.HasRemote(ctx, env.Config.RemoteName):
			rn = env.Config.RemoteName
		case g.HasRemote(ctx, "origin"):
			rn = "origin"
		default:
			return fmt.Errorf("there is no '%s' or 'origin' remote: use 'gitsi share' (edge) or 'gitsi repo get' (hub)", env.Config.RemoteName)
		}
	}
	if err := safe.Name("remote", rn); err != nil {
		return err
	}
	if rn == env.Config.RemoteName || (isHub && rn == "origin") {
		remember(ctx, g, root, rn) // a folder synced with the bridge shows in the tray's Repos
	}
	br, err := g.Out(ctx, "branch", "--show-current")
	if err != nil {
		return err
	}
	if br == "" { // CurrentBranch would answer "main"
		return errors.New("HEAD is not on a branch (detached, or a rebase in progress): finish the rebase or switch to the branch to sync")
	}
	if st, _ := g.Out(ctx, "status", "--porcelain"); st != "" {
		fmt.Fprintln(env.Stderr, "Warning: there are uncommitted changes; they do not travel until you commit them.")
	}

	if isHub && rn == "origin" {
		ensureMirrorRefspec(ctx, g, rn) // a clone made before the mirrors existed gains it here
	}
	track := "refs/remotes/" + rn + "/" + br
	before, _ := g.Out(ctx, "rev-parse", "--verify", "--quiet", track)
	if _, err := g.Out(ctx, "fetch", rn); err != nil {
		return err
	}
	if g.HasRef(ctx, track) && g.HasCommits(ctx) {
		tip, err := g.Out(ctx, "rev-parse", "--verify", track)
		if err != nil {
			return err
		}
		inHere, inThere := isAncestor(ctx, g, tip, "HEAD"), isAncestor(ctx, g, "HEAD", tip)
		dropped, own := false, []string(nil)
		if isHub && !inThere {
			if dropped, own, err = droppedByEdge(ctx, g, tip, append(reflog(ctx, g, track), before)); err != nil {
				return err
			}
		}
		switch {
		case dropped && len(own) > 0:
			return fmt.Errorf("the edge replaced the branch, and this clone has %d commit(s) that never reached the bridge. To keep them over the edge's version: 'git reset --keep %s/%s', then 'git cherry-pick %s', and run sync again",
				len(own), rn, br, strings.Join(own, " "))
		case dropped:
			// Everything here had been on the bridge: there is nothing of the hub's own to lose.
			if _, err := g.Out(ctx, "reset", "--keep", tip); err != nil { // --keep refuses to overwrite uncommitted changes
				return fmt.Errorf("the edge replaced the branch, but there are uncommitted changes in the same files: commit or stash them and run sync again: %w", err)
			}
			fmt.Fprintln(env.Stdout, "The edge replaced the branch: this clone now has its version.")
		case inHere: // up to date, or only to send
		case inThere:
			if _, err := g.Out(ctx, "merge", "--ff-only", track); err != nil {
				return fmt.Errorf("could not bring the changes from %s (uncommitted changes in the same files? commit or stash them): %w", rn, err)
			}
		case !isHub && rn == env.Config.RemoteName && allIn(ctx, g, tip, reflog(ctx, g, "refs/heads/"+br)):
			// The local branch contained everything the hub has and was rewritten afterwards (amend, rebase).
			return replaceBridge(ctx, env, g, rn, br, tip, false)
		default:
			if !isHub && rn == env.Config.RemoteName {
				return replaceBridge(ctx, env, g, rn, br, tip, true)
			}
			return divergence(ctx, g, rn, br, tip)
		}
	}
	// No -u: the branch's upstream does not change.
	if _, err := g.Out(ctx, "push", rn, "HEAD:refs/heads/"+br); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Synced: %s  <->  %s\n", br, rn)
	return nil
}

// replaceBridge makes the bridge's branch equal to the local one. What it replaces stays on the bridge in a hidden ref (one per
// branch), and the push is conditioned to the commit that was shown: newer work from the hub is never overwritten unseen.
func replaceBridge(ctx context.Context, env *module.Env, g *gitx.Git, rn, br, tip string, ask bool) error {
	own, err := newThere(ctx, g, tip)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "%s/%s has %d commit(s) that are not in this branch", rn, br, len(own))
	fmt.Fprintf(env.Stdout, " (%s)\n", strings.Join((&publication{g: g}).authors(ctx, own), ", "))
	for i, c := range own {
		if i == 10 {
			fmt.Fprintf(env.Stdout, "  ... and %d more\n", len(own)-i)
			break
		}
		if l, err := g.Out(ctx, "show", "-s", "--no-show-signature", "--format=%h %s", c, "--"); err == nil {
			fmt.Fprintln(env.Stdout, "  "+printable(l))
		}
	}
	if ask && !confirm(env.Stdin, env.Stdout, "The hub has new work. Replace the hub's branch with this one?") {
		return divergence(ctx, g, rn, br, tip)
	}
	ref := "refs/heads/" + br
	backup := "refs/gitsi/replaced/" + br + "/tip" // "/tip": branches "a" and, later, "a/b" can both have one
	if _, err := g.Out(ctx, "push", "--atomic", "--force-with-lease="+ref+":"+tip, rn, "+"+tip+":"+backup, "HEAD:"+ref); err != nil {
		return fmt.Errorf("the bridge did not change (if the hub pushed in the meantime, run sync again): %w", err)
	}
	fmt.Fprintf(env.Stdout, "Synced: %s  ->  %s (the previous version can be recovered with: git fetch %s %s)\n", br, rn, rn, backup)
	fmt.Fprintln(env.Stdout, "On the hub run:  gitsi sync")
	return nil
}

// divergence explains why sync stopped and the ways out.
func divergence(ctx context.Context, g *gitx.Git, rn, br, tip string) error {
	if _, ok := sincePublish(ctx, g, br, rn); ok {
		return fmt.Errorf("'%s' was published on %s as one commit: use 'gitsi publish' to send what is new", br, rn)
	}
	own, err := newThere(ctx, g, tip)
	if err != nil {
		return err
	}
	who := strings.Join((&publication{g: g}).authors(ctx, own), ", ")
	return fmt.Errorf("the branches diverge: %s has %d commit(s) that are not here (%s), and this branch has others. Combine them with 'git pull --no-rebase %s %s' and run sync again",
		rn, len(own), who, rn, br)
}

// newThere lists the commits of tip that the current branch does not have.
func newThere(ctx context.Context, g *gitx.Git, tip string) ([]string, error) {
	return (&publication{g: g}).lines(ctx, "rev-list", tip, "^HEAD")
}

// droppedByEdge reports whether this branch still has commits that were on the bridge and are not there anymore (pushing would
// bring back what the edge replaced), and lists the commits here that never were on the bridge, oldest first. seen are the commits
// the bridge's branch has had.
func droppedByEdge(ctx context.Context, g *gitx.Git, tip string, seen []string) (dropped bool, own []string, err error) {
	git := &publication{g: g}
	all, err := git.lines(ctx, "rev-list", "HEAD", "^"+tip)
	if err != nil {
		return false, nil, err
	}
	args := []string{"rev-list", "--ignore-missing", "--reverse", "--topo-order", "--abbrev-commit", "HEAD", "^" + tip}
	for _, s := range seen {
		if s != "" {
			args = append(args, "^"+s)
		}
	}
	own, err = git.lines(ctx, args...)
	return len(own) < len(all), own, err
}

// reflog returns the last commits the reference has pointed to on this machine. It reads the file: 'git log -g' hides what the
// reference pointed to before its first entry, which after a clone is exactly the bridge's version the clone started from.
func reflog(ctx context.Context, g *gitx.Git, ref string) []string {
	path, err := g.Out(ctx, "rev-parse", "--git-path", "logs/"+ref)
	if err != nil {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.Dir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	entries := strings.Split(strings.TrimSpace(string(b)), "\n")
	entries = entries[max(0, len(entries)-200):] // they go in a command line
	var ids []string
	seen := map[string]bool{}
	for _, e := range entries {
		f := strings.Fields(e) // "<old> <new> name <email> ..."
		for _, id := range f[:min(2, len(f))] {
			if !seen[id] && strings.Trim(id, "0") != "" {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// allIn reports whether everything in commit is contained in some of the others. Without proof the answer is no.
func allIn(ctx context.Context, g *gitx.Git, commit string, others []string) bool {
	args := []string{"rev-list", "--ignore-missing", "--count", commit}
	for _, o := range others {
		if o != "" {
			args = append(args, "^"+o)
		}
	}
	if len(args) == 4 {
		return false
	}
	out, err := g.Out(ctx, args...)
	return err == nil && out == "0"
}
