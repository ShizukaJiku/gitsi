package repo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// listCmd builds the (PowerShell) command that lists the bridge's bare repos.
func listCmd(reposDir string) string {
	// the hub creates the folder with the first 'share': if it does not exist yet, the list is empty (not an error)
	return "if (Test-Path -LiteralPath '" + reposDir + "') { Get-ChildItem '" + reposDir + "' -Directory -Filter *.git | ForEach-Object { $_.Name -replace '\\.git$','' } }"
}

func list(ctx context.Context, env *module.Env, _ []string) error {
	names, err := repoNames(ctx, env)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		if env.Config.Role == config.RoleHub {
			fmt.Fprintln(env.Stdout, "No repos on the bridge yet (the edge creates them with 'gitsi share').")
		} else {
			fmt.Fprintln(env.Stdout, "No repos on the hub yet.")
		}
		return nil
	}
	for _, n := range names {
		fmt.Fprintln(env.Stdout, n)
	}
	return nil
}

// repoNames are the bridge's repos: on the hub, its own folders; on the edge, it asks the hub.
func repoNames(ctx context.Context, env *module.Env) ([]string, error) {
	lay, err := env.HubLayout(ctx)
	if err != nil {
		return nil, err
	}
	if err := safe.Path("repos", lay.Repos); err != nil {
		return nil, err
	}
	if env.Config.Role == config.RoleHub {
		return localNames(lay.Repos)
	}
	res, err := env.RunOnHub(ctx, listCmd(lay.Repos))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("could not list (exit code %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return strings.Fields(res.Stdout), nil
}

// completeRepos proposes the bridge's repos for 'repo get'. Without a reachable hub (or when it is slow), it proposes nothing.
func completeRepos(ctx context.Context, env *module.Env, args []string) []string {
	if len(args) > 0 {
		return nil
	}
	names, _ := repoNames(ctx, env)
	return names
}

// localNames lists this hub's bare bridge repos.
func localNames(reposDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.FromSlash(reposDir))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".git") {
			names = append(names, strings.TrimSuffix(e.Name(), ".git"))
		}
	}
	return names, nil
}
