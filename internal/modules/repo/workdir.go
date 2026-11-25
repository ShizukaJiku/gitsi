package repo

import (
	"context"
	"path"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/safe"
	"github.com/ShizukaJiku/gitsi/internal/workdirs"
)

// remember records a working folder for the tray (internal/workdirs): the repo is named after the remote's URL. It is best
// effort: a folder that cannot be recorded only goes missing from the tray's list.
func remember(ctx context.Context, g *gitx.Git, root, remote string) {
	url, err := g.Out(ctx, "config", "--get", "remote."+remote+".url")
	if err != nil {
		return
	}
	name := strings.TrimSuffix(path.Base(filepath.ToSlash(strings.TrimSpace(url))), ".git")
	if safe.Name("repo name", name) != nil {
		return
	}
	_ = workdirs.Add(name, root)
}
