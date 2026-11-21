package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// clone (hub role) clones a shared repo into the current folder, like 'git clone'.
func clone(ctx context.Context, env *module.Env, args []string) error {
	nameArg, rest := splitName(args)
	if nameArg == "" {
		return errors.New("usage: gitsi repo get <repo-name> [-dest <path>]")
	}
	fs := newFlags("repo get", env.Stderr)
	dest := fs.String("dest", "", "target folder (default .\\<name>)")
	reposDir := fs.String("repos-dir", "", "folder of the bridge repos (default: the hub's layout)")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	name := strings.TrimSuffix(nameArg, ".git")
	if err := safe.Name("repo name", name); err != nil {
		return err
	}
	if *reposDir == "" {
		// the same folder 'share' writes to: the hub's layout (computed from its configuration, it does not fail on a hub)
		lay, err := env.HubLayout(ctx)
		if err != nil {
			return err
		}
		*reposDir = filepath.FromSlash(lay.Repos)
	}
	bare := filepath.Join(*reposDir, name+".git")
	if _, err := os.Stat(bare); err != nil {
		return fmt.Errorf("%s does not exist: share the repo from the edge first ('gitsi share')", bare)
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
	// When another account created the repo (e.g. an elevated SSH session), Git treats it as 'dubious ownership'.
	if _, err := g.Out(ctx, "ls-remote", bare); err != nil && strings.Contains(err.Error(), "dubious ownership") {
		fwd := filepath.ToSlash(bare)
		if _, err := g.Out(ctx, "config", "--global", "--add", "safe.directory", fwd); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Marked as safe (safe.directory): %s\n", fwd)
	}

	if _, err := g.Out(ctx, "clone", bare, dst); err != nil {
		return err
	}

	if err := ensureCheckout(ctx, env, gitx.New(dst), "origin"); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Cloned in %s\n", dst)
	return nil
}

// ensureCheckout: when the repo's HEAD pointed to a missing branch there is no checkout; it picks a real branch of the remote.
func ensureCheckout(ctx context.Context, env *module.Env, c *gitx.Git, remote string) error {
	if c.HasCommits(ctx) {
		return nil
	}
	branches, err := c.RemoteBranches(ctx)
	if err != nil {
		return err
	}
	if len(branches) == 0 {
		fmt.Fprintln(env.Stdout, "The repo is empty (no branches yet).")
		return nil
	}
	b := strings.TrimPrefix(branches[0], remote+"/")
	if _, err := c.Out(ctx, "checkout", b); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Default branch set to '%s'.\n", b)
	return nil
}
