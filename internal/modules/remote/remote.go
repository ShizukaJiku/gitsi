// Package remote runs work on the hub (docker compose, builds, tests) started from the edge. It requires Policy.AllowExec; each
// run is audited (only the program and the folder, not the arguments).
package remote

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/safe"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

type mod struct{ take Taker }

// New returns the module.
func New(take Taker) module.Module { return mod{take: take} }

// Taker brings item id from the hub's outbox to dir and returns where it ended up (drop.TakeByID, passed in by app).
type Taker func(ctx context.Context, env *module.Env, id, dir string) (string, error)

func (mod) Name() string { return "remote" }

func (m mod) Commands() []module.Command {
	return []module.Command{
		{Name: "run", Summary: "Run a command on the hub with live output",
			Roles: module.Edge, Feature: policy.Exec, NeedsHub: true, Run: m.run, Flags: []string{"-cwd", "-get", "-repo"}},
		{Group: "job", Name: "start", Summary: "Start a command in the background on the hub", Roles: module.Edge, Feature: policy.Exec, NeedsHub: true, Run: startCmd, Flags: []string{"-cwd", "-repo"}},
		{Group: "job", Name: "ls", Summary: "List the hub's jobs", Roles: module.Edge, Feature: policy.Exec, NeedsHub: true, Run: jobsCmd},
		{Group: "job", Name: "logs", Summary: "A job's output: <n|id> [-f] [-tail N]", Roles: module.Edge, Feature: policy.Exec, NeedsHub: true, Run: logsCmd, Flags: []string{"-f", "-tail"}},
		{Group: "job", Name: "stop", Summary: "Stop a job: <n|id>", Roles: module.Edge, Feature: policy.Exec, NeedsHub: true, Run: stopCmd},
		{Group: "job", Name: "get", Summary: "Get a path from a job's folder: <n|id> <path>", Roles: module.Edge, Feature: policy.Exec, NeedsHub: true, Run: m.jobGet},
	}
}

// wrap builds the remote PowerShell command: it enters cwd (when given), runs and keeps the exit code. The user's text goes to
// PowerShell as is (that is what `run` is for); the policy and the audit log protect it.
func wrap(cwd, command string) string {
	var b strings.Builder
	if cwd != "" {
		b.WriteString("Set-Location -LiteralPath '" + cwd + "'; ")
	}
	b.WriteString(command)
	// $? is false when the last command failed; $LASTEXITCODE keeps a native executable's code.
	b.WriteString("; if ($LASTEXITCODE) { exit $LASTEXITCODE } elseif (-not $?) { exit 1 }")
	return b.String()
}

func (m mod) run(ctx context.Context, env *module.Env, args []string) error {
	return runWith(ctx, env, args, m.take)
}

// run is `gitsi run` without the -get option (there is nothing to bring from the outbox with).
func run(ctx context.Context, env *module.Env, args []string) error {
	return runWith(ctx, env, args, nil)
}

func runWith(ctx context.Context, env *module.Env, args []string, take Taker) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	cwd := fs.String("cwd", "", "working folder on the hub")
	get := fs.String("get", "", "with -repo: when done, bring this path of the working clone (relative) to Downloads\\gitsi")
	repo := fs.Bool("repo", false, "take the current folder as is (uncommitted changes included) to the repo's working clone on the hub and run there")
	// gitsi's flags go before the command; everything else is the remote command as is
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("usage: gitsi run [-cwd <path-on-the-hub> | -repo] <command...>")
	}
	if *get != "" {
		if !*repo {
			return errors.New("-get brings something from the working clone: it goes with -repo")
		}
		if take == nil {
			return errors.New("-get is not available here")
		}
		if err := checkGetPath(*get); err != nil {
			return err
		}
		if err := policy.Check(env.Config.Policy, policy.Take); err != nil {
			return err // brings content to this machine: it requires take, checked before running anything
		}
	}
	if *repo {
		work, err := repoCwd(ctx, env, *cwd)
		if err != nil {
			return err
		}
		*cwd = work
	}
	if *cwd != "" {
		if err := safe.Path("cwd", *cwd); err != nil {
			return err
		}
	}
	ex, ok := env.Transport.(transport.Executor)
	if !ok {
		return errors.New("the transport does not support running with live output")
	}
	command := strings.Join(rest, " ")
	_ = policy.Audit(env.Config.Policy, "run", fmt.Sprintf("exe=%s cwd=%s", strings.Fields(command)[0], *cwd))

	code, err := ex.Exec(ctx, wrap(*cwd, command), env.Stdout, env.Stderr)
	if err != nil {
		return fmt.Errorf("could not run ssh: %w", err)
	}
	if *get != "" {
		// also when the command failed: a report of failed tests is exactly what one wants to see
		if err := getResult(ctx, env, *cwd, *get, take); err != nil {
			fmt.Fprintf(env.Stderr, "could not bring %s: %v\n", *get, err)
			if code == 0 {
				return err
			}
		}
	}
	if code != 0 {
		return &module.ExitError{Code: code}
	}
	return nil
}

// startCmd is `job start`: it starts the command in the background on the hub (in the foreground, with live output, it is `run`).
func startCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := flag.NewFlagSet("job start", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	cwd := fs.String("cwd", "", "working folder on the hub")
	repo := fs.Bool("repo", false, "take the current folder as is (uncommitted changes included) to the repo's working clone on the hub and start there")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("usage: gitsi job start [-cwd <path-on-the-hub> | -repo] <command...>")
	}
	if *repo {
		work, err := repoCwd(ctx, env, *cwd)
		if err != nil {
			return err
		}
		*cwd = work
	}
	if *cwd != "" {
		if err := safe.Path("cwd", *cwd); err != nil {
			return err
		}
	}
	return runBG(ctx, env, *cwd, strings.Join(rest, " "))
}
