package repo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/bridge"
	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

func share(ctx context.Context, env *module.Env, args []string) error {
	cfg := env.Config
	nameArg, rest := splitName(args)
	fs := newFlags("share", env.Stderr)
	name := fs.String("name", nameArg, "repo name on the hub (default: the folder's)")
	branch := fs.String("branch", "", "main branch (default: the current one)")
	remote := fs.String("remote", cfg.RemoteName, "name of the local remote")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	noPush := fs.Bool("no-push", false, "create the repo and the remote without pushing")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	// after the options: -h answers without connecting
	lay, err := env.HubLayout(ctx) // the hub decides where the repos live; the edge asks it
	if err != nil {
		return err
	}

	g := gitx.New("")
	root, err := g.Root(ctx)
	if err != nil {
		return errors.New("this folder is not a Git repository")
	}
	g.Dir = root

	if *name == "" {
		*name = filepath.Base(root)
	}
	*name = strings.TrimSuffix(*name, ".git")
	if *branch == "" {
		*branch = g.CurrentBranch(ctx)
	}
	for _, check := range []error{
		safe.Name("repo name", *name), safe.Branch(*branch), safe.Name("remote", *remote),
		safe.Path("repos", lay.Repos), safe.Name("user", cfg.Hub.User),
	} {
		if check != nil {
			return check
		}
	}
	bare := strings.TrimRight(lay.Repos, "/") + "/" + *name + ".git"

	fmt.Fprintf(env.Stdout, "\nThis WHOLE repo (all its history) will be copied to the hub: %s\n  Local repo: %s\n  Branch:     %s\n", bare, root, *branch)
	if !*yes && !confirm(env.Stdin, env.Stdout, "Continue?") {
		fmt.Fprintln(env.Stdout, "Cancelled.")
		return nil
	}

	fmt.Fprintln(env.Stdout, "Preparing the repo on the hub...")
	res, err := env.RunOnHub(ctx, bridge.EnsureBareScript(bare, *branch, cfg.Hub.User))
	if err != nil {
		return err
	}
	switch {
	case strings.Contains(res.Stdout, "CREATED"):
		fmt.Fprintln(env.Stdout, "Repo created on the hub.")
	case strings.Contains(res.Stdout, "EXISTS"):
		fmt.Fprintln(env.Stdout, "The repo already existed on the hub; it is reused.")
	default:
		return fmt.Errorf("could not prepare the repo on the hub (exit code %d): %s %s", res.ExitCode, strings.TrimSpace(res.Stdout), strings.TrimSpace(res.Stderr))
	}

	url := cfg.Hub.HostAlias() + ":" + bare
	if err := g.SetRemote(ctx, *remote, url); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Remote '%s' -> %s\n", *remote, url)
	remember(ctx, g, root, *remote)
	_ = policy.Audit(cfg.Policy, "share", *name)

	if *noPush {
		fmt.Fprintln(env.Stdout, "Push skipped (-no-push).")
		return nil
	}
	// No -u: branches keep tracking origin and a plain 'git push' does not go to the hub.
	if err := g.Passthrough(ctx, env.Stdout, env.Stderr, "push", *remote, "--all"); err != nil {
		return err
	}
	_ = g.Passthrough(ctx, env.Stdout, env.Stderr, "push", *remote, "--tags")
	fmt.Fprintf(env.Stdout, "\nDone. On the hub run:  gitsi repo get %s\n", *name)
	return nil
}
