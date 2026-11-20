// Package dockercmd exposes `gitsi docker` (edge): it sets up the official Docker client to drive the hub's Docker over SSH. The
// logic lives in internal/dockercli.
package dockercmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ShizukaJiku/gitsi/internal/dockercli"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

type mod struct {
	pathKey string           // key of the account's PATH (empty: the real one); tests use another
	run     dockercli.Runner // runs docker.exe (nil: the real one)
}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "docker" }

func (m mod) Commands() []module.Command {
	return []module.Command{{
		Name:    "docker",
		Summary: "Set up this machine's docker client to use the hub's Docker",
		Roles:   module.Edge, Feature: policy.Exec, NeedsHub: true, Run: m.cmd,
	}}
}

func (m mod) cmd(ctx context.Context, env *module.Env, args []string) error {
	if len(args) > 0 {
		return errors.New("usage: gitsi docker")
	}
	f, ok := env.Transport.(transport.Fetcher)
	if !ok {
		return errors.New("the transport cannot download files")
	}
	lay, err := env.HubLayout(ctx)
	if err != nil {
		return err
	}
	localApp, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	clientDir := filepath.Join(localApp, "gitsi", "docker", dockercli.Dir)
	dockerHome := filepath.Join(home, ".docker")

	// 1. client and plugins, verified
	endAsk := env.Progress().Phase("Asking the hub")
	res, err := env.RunOnHub(ctx, dockercli.HashScript(lay.Home))
	endAsk()
	if err != nil {
		return err
	}
	endDl := env.Progress().Phase("Downloading the docker client")
	lines, err := dockercli.Sync(ctx, lay.Home, clientDir, dockerHome, dockercli.ParseHashes(res.Stdout), f.Fetch)
	endDl()
	for _, l := range lines {
		fmt.Fprintln(env.Stdout, "  "+l)
	}
	if err != nil {
		return err
	}
	docker := filepath.Join(clientDir, "docker.exe")

	// 2. the account's PATH
	key := m.pathKey
	if key == "" {
		key = dockercli.UserPathKey
	}
	added, err := dockercli.AddToUserPath(ctx, key, clientDir)
	if err != nil {
		return err
	}
	if added {
		fmt.Fprintf(env.Stdout, "Account PATH: added %s (new terminals find 'docker')\n", clientDir)
	} else {
		fmt.Fprintf(env.Stdout, "Account PATH: already included %s\n", clientDir)
	}

	// 3. context and a check against the hub's engine
	run := m.run
	if run == nil {
		run = dockercli.ExecRunner
	}
	server, err := dockercli.SetupContext(ctx, run, docker, env.Config.Hub.HostAlias())
	if err != nil {
		return err
	}
	_ = policy.Audit(env.Config.Policy, "docker.setup", "context="+dockercli.ContextName+" server="+server)
	fmt.Fprintf(env.Stdout, "Context: %s -> ssh://%s (active). Hub engine: %s\n", dockercli.ContextName, env.Config.Hub.HostAlias(), server)
	fmt.Fprintln(env.Stdout, "Done: 'docker ps', 'docker compose up', 'docker build' go to the hub's Docker.")
	if added {
		fmt.Fprintf(env.Stdout, "Not in THIS terminal yet: open a new one, or run  $env:Path += ';%s'\n", clientDir)
	}
	return nil
}
