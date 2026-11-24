package repo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

var githubRe = regexp.MustCompile(`github\.com[:/]+([^/]+)/(.+?)(?:\.git)?$`)

// prURL returns the URL to open a GitHub PR from the remote's URL, or "" when it is not GitHub. An empty base is the
// repository's default branch.
func prURL(remoteURL, base, branch string) string {
	m := githubRe.FindStringSubmatch(strings.TrimSpace(remoteURL))
	if m == nil {
		return ""
	}
	if base != "" {
		return fmt.Sprintf("https://github.com/%s/%s/compare/%s...%s?expand=1", m[1], m[2], base, branch)
	}
	return fmt.Sprintf("https://github.com/%s/%s/pull/new/%s", m[1], m[2], branch)
}

// publish (edge role) pushes the current branch to 'origin' (the target repo, e.g. GitHub), never to the bridge, as one commit
// authored by this machine's Git user. The commit is built aside and only origin receives it: the local branch, the index and
// the bridge keep their history, so 'sync' with the hub never diverges because of a publish.
func publish(ctx context.Context, env *module.Env, args []string) error {
	fs := newFlags("publish", env.Stderr)
	remote := fs.String("remote", "origin", "target remote")
	baseFlag := fs.String("base", "", "branch the PR goes to (default: the shared branch this one comes from)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := safe.Name("remote", *remote); err != nil {
		return err
	}
	if *baseFlag != "" {
		if err := safe.Branch(*baseFlag); err != nil {
			return err
		}
	}
	if *remote == env.Config.RemoteName {
		return fmt.Errorf("'%s' is the bridge to the hub, not the target repo: publish pushes to origin", *remote)
	}

	g := gitx.New("")
	root, err := g.Root(ctx)
	if err != nil {
		return errors.New("this folder is not a Git repository")
	}
	g.Dir = root
	if !g.HasRemote(ctx, *remote) {
		return fmt.Errorf("there is no remote '%s'. Add it with: git remote add %s <repo-url>", *remote, *remote)
	}
	br, err := g.Out(ctx, "branch", "--show-current")
	if err != nil {
		return err
	}
	if br == "" { // CurrentBranch would answer "main": a detached HEAD (or a rebase in progress) must not end up there
		return errors.New("HEAD is not on a branch (detached, or a rebase in progress): finish the rebase or switch to the branch to publish")
	}
	if !g.HasCommits(ctx) {
		return errors.New("the branch has no commits yet")
	}
	if st, _ := g.Out(ctx, "status", "--porcelain"); st != "" {
		fmt.Fprintln(env.Stderr, "Warning: there are uncommitted changes; they are not included in what is published.")
	}
	if _, behind, ok := aheadBehind(ctx, g, br, env.Config.RemoteName); ok && behind > 0 {
		fmt.Fprintf(env.Stderr, "Warning: the bridge has %d commit(s) that are not here; 'gitsi sync' brings them.\n", behind)
	}

	t, err := readTarget(ctx, g, *remote, br, *baseFlag)
	if err != nil {
		return err
	}
	p := &publication{g: g, target: t}
	if err := p.prepare(ctx, env.Stdout, env.Stderr); err != nil {
		return err
	}
	if p.commit == t.tip {
		fmt.Fprintf(env.Stdout, "Already published: %s has nothing new for %s.\n", br, t.remote)
	} else {
		if p.replaces {
			q := fmt.Sprintf("%s/%s has commits that this clone did not publish; replace them with the above?", t.remote, br)
			if !confirm(env.Stdin, env.Stdout, q) {
				return errors.New("cancelled: nothing was pushed")
			}
		}
		if err := p.push(ctx, env.Stdout, env.Stderr); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Published: %s -> %s\n", br, t.remote)
	}
	p.record(ctx)
	up, _ := g.Out(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if up == "" || strings.HasPrefix(up, env.Config.RemoteName+"/") {
		if _, err := g.Out(ctx, "branch", "--set-upstream-to="+t.remote+"/"+br, br); err != nil {
			fmt.Fprintf(env.Stderr, "Warning: could not set the branch's upstream: %v\n", err)
		}
	}
	if br == t.base {
		return nil // a branch has no PR to itself
	}
	if url, _ := g.Out(ctx, "remote", "get-url", t.remote); url != "" {
		base := t.base
		if base == t.def {
			base = ""
		}
		if pr := prURL(url, base, br); pr != "" {
			fmt.Fprintf(env.Stdout, "Open the PR: %s\n", pr)
		}
	}
	return nil
}
