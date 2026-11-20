package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// syncCmd pulls and pushes the current branch against the bridge. Fast-forward only: when the branches diverge, it stops.
func syncCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := newFlags("sync", env.Stderr)
	remote := fs.String("remote", "", "remote to use (default: the bridge's, or origin)")
	if err := fs.Parse(args); err != nil {
		return err
	}

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
	br := g.CurrentBranch(ctx)

	if _, err := g.Out(ctx, "fetch", rn); err != nil {
		return err
	}
	if g.HasRef(ctx, "refs/remotes/"+rn+"/"+br) {
		if _, err := g.Out(ctx, "merge", "--ff-only", rn+"/"+br); err != nil {
			return fmt.Errorf("the branches diverge (there are changes on both sides). Resolve it by hand with 'git pull %s %s' and run sync again: %w", rn, br, err)
		}
	}
	// No -u: the branch's upstream does not change.
	if _, err := g.Out(ctx, "push", rn, br); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Synced: %s  <->  %s\n", br, rn)
	return nil
}
