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

// prURL returns the URL to open a GitHub PR from the remote's URL, or "" when it is not GitHub.
func prURL(remoteURL, branch string) string {
	m := githubRe.FindStringSubmatch(strings.TrimSpace(remoteURL))
	if m == nil {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/%s/pull/new/%s", m[1], m[2], branch)
}

// publish (edge role) pushes the current branch to 'origin' (the target repo, e.g. GitHub), never to the bridge. When the branch
// has no upstream, or tracks the bridge, it sets it to origin.
func publish(ctx context.Context, env *module.Env, args []string) error {
	fs := newFlags("publish", env.Stderr)
	remote := fs.String("remote", "origin", "target remote (default origin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := safe.Name("remote", *remote); err != nil {
		return err
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
	br := g.CurrentBranch(ctx)
	if !g.HasCommits(ctx) {
		return errors.New("the branch has no commits yet")
	}
	if st, _ := g.Out(ctx, "status", "--porcelain"); st != "" {
		fmt.Fprintln(env.Stderr, "Warning: there are uncommitted changes; they are not included in what is published.")
	}

	up, _ := g.Out(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	pushArgs := []string{"push", *remote, br}
	if up == "" || strings.HasPrefix(up, env.Config.RemoteName+"/") {
		pushArgs = []string{"push", "-u", *remote, br}
	}
	if err := g.Passthrough(ctx, env.Stdout, env.Stderr, pushArgs...); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Published: %s -> %s\n", br, *remote)
	if url, _ := g.Out(ctx, "remote", "get-url", *remote); url != "" {
		if pr := prURL(url, br); pr != "" {
			fmt.Fprintf(env.Stdout, "Open the PR: %s\n", pr)
		}
	}
	return nil
}
