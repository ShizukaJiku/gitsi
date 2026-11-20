package repo

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// Here summarizes the current folder's repo against the bridge, for `gitsi status`: it fetches from the hub (it only updates the
// remote branches, not the local one) and counts what is left to send or receive. When it cannot fetch, it compares with the
// last fetch and says so.
func Here(ctx context.Context, env *module.Env) (string, error) {
	g := gitx.New("")
	root, err := g.Root(ctx)
	if err != nil {
		return "this folder is not a Git repo", nil
	}
	g.Dir = root
	br := g.CurrentBranch(ctx)
	head := fmt.Sprintf("%s (%s)", filepath.Base(root), br)
	rn := env.Config.RemoteName
	if !g.HasRemote(ctx, rn) {
		return head + ": not on the hub (gitsi share)", nil
	}
	stale := ""
	if _, err := g.Out(ctx, "fetch", "--quiet", rn); err != nil {
		stale = " (not fetched: the hub did not answer)"
	}
	ahead, behind, ok := aheadBehind(ctx, g, br, rn)
	switch {
	case !ok:
		return head + ": the branch is not on the hub (gitsi sync)" + stale, nil
	case ahead == 0 && behind == 0:
		return head + ": up to date with the hub" + stale, nil
	}
	return fmt.Sprintf("%s: %d to send, %d to receive (gitsi sync)%s", head, ahead, behind, stale), nil
}
