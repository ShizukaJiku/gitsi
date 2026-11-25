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

// bridgeURL is the URL git uses to reach the hub's bare repo (it uses the ~/.ssh/config alias).
func bridgeURL(cfg *config.Config, reposDir, name string) string {
	return cfg.Hub.HostAlias() + ":" + strings.TrimRight(reposDir, "/") + "/" + name + ".git"
}

// get (edge role) brings to the edge a repo that exists on the hub. The bridge remote is named config.RemoteName ('hub'), and
// 'origin' stays free for the target repo (e.g. GitHub).
func get(ctx context.Context, env *module.Env, args []string) error {
	if env.Config.Role == config.RoleHub {
		return clone(ctx, env, args) // on the hub, the same verb clones from the local bridge
	}
	cfg := env.Config
	nameArg, rest := splitName(args)
	if nameArg == "" {
		return errors.New("usage: gitsi repo get <repo-name> [-dest <path>]   (see the available ones with 'gitsi repo list')")
	}
	fs := newFlags("repo get", env.Stderr)
	dest := fs.String("dest", "", "target folder (default .\\<name>)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	name := strings.TrimSuffix(nameArg, ".git")
	lay, err := env.HubLayout(ctx) // the hub places the repos; the edge does not configure that path
	if err != nil {
		return err
	}
	for _, c := range []error{safe.Name("repo name", name), safe.Path("repos", lay.Repos), safe.Name("remote", cfg.RemoteName)} {
		if c != nil {
			return c
		}
	}
	dst := *dest
	if dst == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		dst = filepath.Join(cwd, name)
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("the target already exists: %s (use -dest for another folder)", dst)
	}

	g := gitx.New("")
	if err := g.Passthrough(ctx, env.Stdout, env.Stderr, "clone", "-o", cfg.RemoteName, bridgeURL(cfg, lay.Repos, name), dst); err != nil {
		return err
	}
	if err := ensureCheckout(ctx, env, gitx.New(dst), cfg.RemoteName); err != nil {
		return err
	}
	remember(ctx, gitx.New(dst), dst, cfg.RemoteName)
	fmt.Fprintf(env.Stdout, "Got it in %s (remote '%s'). Add 'origin' if the repo has one: git remote add origin <url>\n", dst, cfg.RemoteName)
	return nil
}
