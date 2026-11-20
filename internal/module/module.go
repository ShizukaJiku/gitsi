// Package module defines the contract of gitsi's features.
//
// Each feature is a Module that declares its commands and the roles they apply to. Adding a capability = creating a package in
// internal/modules and registering it in internal/app.
package module

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/progress"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// Roles a command is available in.
type Roles uint8

const (
	Edge Roles = 1 << iota
	Hub
	Both = Edge | Hub
)

// Has reports whether the configured role is included.
func (r Roles) Has(role config.Role) bool {
	switch role {
	case config.RoleHub:
		return r&Hub != 0
	default:
		return r&Edge != 0
	}
}

// Env is what a command receives when it runs.
type Env struct {
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Config    *config.Config
	Transport transport.Transport // nil on the hub

	// Output mode, chosen with the global flags (gitsi -v | -q | --json <command>) or the GITSI_VERBOSE / GITSI_QUIET variables.
	// Commands honor them; none changes what it does, only what it reports.
	Verbose bool // detail: ssh commands run, timings
	Quiet   bool // no progress or side information: only results and errors
	JSON    bool // listings and diagnostics as JSON (for scripts)

	// LayoutFn discovers the hub's layout from the edge (set by app). The hub does not need it: it computes it from its config.
	LayoutFn func(ctx context.Context) (layout.Layout, error)
	// RefreshLayoutFn ignores the cache and asks the hub again; it also returns the version of the hub's gitsi. Diagnostics use it.
	RefreshLayoutFn func(ctx context.Context) (layout.Layout, string, error)

	progress *progress.Reporter
}

// Progress returns this command's progress reporter: it writes to stderr (never to stdout), redraws one line on a console and is
// silent with -q. It is created the first time it is asked for, with the global flags already applied.
func (e *Env) Progress() *progress.Reporter {
	if e.progress == nil {
		e.progress = progress.New(progress.Options{Out: e.Stderr, Quiet: e.Quiet, TTY: progress.IsTerminal(e.Stderr)})
	}
	return e.progress
}

// RunOnHub runs a command in the hub's remote shell. It is the common place that says "could not run ssh", and it avoids the
// panic of a nil transport: on an edge without a hub it says how to set one up; on a hub (which connects to nobody) it says the
// command belongs to the edge.
func (e *Env) RunOnHub(ctx context.Context, command string) (transport.Result, error) {
	if e.Transport == nil {
		if e.Config != nil && e.Config.Role == config.RoleHub {
			return transport.Result{}, errors.New("this command runs from the edge: this machine is the hub and connects to no other")
		}
		return transport.Result{}, fault.New(fault.NotConfigured, "this machine has no hub configured", "gitsi setup edge -host <hub-ip> -user <user>")
	}
	res, err := e.Transport.Run(ctx, command)
	if err != nil {
		return res, fmt.Errorf("could not run ssh: %w", err)
	}
	return res, nil
}

// RefreshHubLayout is HubLayout without the cache (for diagnostics): it asks the hub again and returns the version of its gitsi.
func (e *Env) RefreshHubLayout(ctx context.Context) (layout.Layout, string, error) {
	if e.RefreshLayoutFn != nil && e.Config.Role != config.RoleHub {
		return e.RefreshLayoutFn(ctx)
	}
	l, err := e.HubLayout(ctx)
	return l, "", err
}

// HubLayout returns where the repos, the mailbox, the jobs and the binary are ON THE HUB. On the hub it computes it from its own
// configuration; on the edge it asks the hub (with a cache), so the edge never configures someone else's paths.
func (e *Env) HubLayout(ctx context.Context) (layout.Layout, error) {
	if e.Config.Role == config.RoleHub {
		home, _ := os.UserHomeDir()
		return layout.ForHub(e.Config, home), nil
	}
	if e.LayoutFn != nil {
		return e.LayoutFn(ctx)
	}
	return layout.Layout{}, fault.New(fault.NotConfigured, "this machine has no hub configured", "gitsi setup edge -host <hub-ip> -user <user>")
}

// Debugf writes a diagnostic line to stderr, only with -v. It never clutters stdout.
func (e *Env) Debugf(format string, a ...any) {
	if e.Verbose {
		fmt.Fprintln(e.Stderr, "[v] "+fmt.Sprintf(format, a...))
	}
}

// PrintJSON writes v as indented JSON to stdout.
func (e *Env) PrintJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(e.Stdout, string(b))
	return err
}

// Command is a command-line command.
type Command struct {
	// Name is the command's name; when Group is not empty, it is the subcommand inside the group ("repo share" -> Name "share").
	Name    string
	Summary string
	Roles   Roles
	Hidden  bool
	// Group gathers commands of one workflow (setup, repo, svc, job). Empty: a top-level command.
	Group string
	// Shortcut also exposes Name at the top level, visible in the help (e.g. "gitsi sync" besides "gitsi repo sync").
	Shortcut bool
	// Feature, when not empty, requires that feature to be enabled in the policy. The dispatcher enforces it before Run: a new
	// command cannot "forget" the check.
	Feature policy.Feature
	// NeedsHub requires, on the edge, a configured hub (an available transport). It does not apply on the hub.
	NeedsHub bool
	Run      func(ctx context.Context, env *Env, args []string) error
	// Flags are the command's options ("-dir", "-keep"...), for tab completion. A test compares them with its FlagSet.
	Flags []string
	// Complete proposes values for the argument after args (service names, repos, config keys...). Optional; tab completion calls
	// it with a short deadline, so it must not wait longer than a quick query.
	Complete func(ctx context.Context, env *Env, args []string) []string
}

// Module groups related commands.
type Module interface {
	Name() string
	Commands() []Command
}

// ParseInterleaved parses mixed flags and positionals ("send a.txt -name x b.txt"), which flag does not allow. It returns the
// positionals in order. "--" ends the flags.
func ParseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		if strings.HasPrefix(args[0], "-") && len(args[0]) > 1 {
			continue // flag will consume it
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return append(pos, tail...), nil
}

// ExitError asks gitsi to exit with a specific code without printing a message (e.g. the remote command's code).
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return "exit code " + strconv.Itoa(e.Code) }

// Index interprets an argument as a position in a listing ("2" = the second): digits only, 1 to 3 of them, and greater than zero.
// Ids have 8 or 16 hexadecimal characters, so a short number is never mistaken for an id in practice.
func Index(arg string) (int, bool) {
	if len(arg) == 0 || len(arg) > 3 {
		return 0, false
	}
	n := 0
	for _, r := range arg {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, n > 0
}
