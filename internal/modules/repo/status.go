package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// aheadBehind returns (ahead, behind) of the local branch against remote/branch; ok=false when the remote lacks the branch.
func aheadBehind(ctx context.Context, g *gitx.Git, branch, remote string) (ahead, behind int, ok bool) {
	ref := "refs/remotes/" + remote + "/" + branch
	if !g.HasRef(ctx, ref) {
		return 0, 0, false
	}
	out, err := g.Out(ctx, "rev-list", "--left-right", "--count", "HEAD..."+ref)
	if err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(strings.Join(strings.Fields(out), " "), "%d %d", &ahead, &behind); err != nil {
		return 0, 0, false
	}
	return ahead, behind, true
}

// repoStatus shows, without touching the network by default, what is left to send or receive on the bridge and on origin.
func repoStatus(ctx context.Context, env *module.Env, args []string) error {
	fs := newFlags("repo status", env.Stderr)
	fetch := fs.Bool("fetch", false, "update the remotes before comparing (uses the network)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g := gitx.New("")
	root, err := g.Root(ctx)
	if err != nil {
		return errors.New("this folder is not a Git repository")
	}
	g.Dir = root
	br := g.CurrentBranch(ctx)
	fmt.Fprintf(env.Stdout, "Repo:   %s\nBranch: %s\n", root, br)

	if st, _ := g.Out(ctx, "status", "--porcelain"); st != "" {
		fmt.Fprintf(env.Stdout, "Uncommitted changes: %d files\n", len(strings.Split(st, "\n")))
	} else {
		fmt.Fprintln(env.Stdout, "Uncommitted changes: none")
	}

	for _, r := range []struct{ name, label string }{{env.Config.RemoteName, "bridge (hub)"}, {"origin", "origin (target)"}} {
		if !g.HasRemote(ctx, r.name) {
			fmt.Fprintf(env.Stdout, "%-18s no '%s' remote\n", r.label, r.name)
			continue
		}
		if *fetch {
			if _, err := g.Out(ctx, "fetch", r.name); err != nil {
				fmt.Fprintf(env.Stdout, "%-18s could not update: %v\n", r.label, err)
				continue
			}
		}
		ahead, behind, ok := aheadBehind(ctx, g, br, r.name)
		switch {
		case !ok:
			fmt.Fprintf(env.Stdout, "%-18s branch '%s' does not exist there (or 'gitsi repo status -fetch' is needed)\n", r.label, br)
		case ahead == 0 && behind == 0:
			fmt.Fprintf(env.Stdout, "%-18s up to date\n", r.label)
		default:
			fmt.Fprintf(env.Stdout, "%-18s %d to send, %d to receive\n", r.label, ahead, behind)
		}
	}
	return nil
}
