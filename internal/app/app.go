// Package app assembles the modules and dispatches the command line.
package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/hubview"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/modules/cleancmd"
	"github.com/ShizukaJiku/gitsi/internal/modules/completion"
	"github.com/ShizukaJiku/gitsi/internal/modules/dockercmd"
	"github.com/ShizukaJiku/gitsi/internal/modules/doctor"
	"github.com/ShizukaJiku/gitsi/internal/modules/drop"
	"github.com/ShizukaJiku/gitsi/internal/modules/mirrorcmd"
	"github.com/ShizukaJiku/gitsi/internal/modules/policycmd"
	"github.com/ShizukaJiku/gitsi/internal/modules/provision"
	"github.com/ShizukaJiku/gitsi/internal/modules/remote"
	"github.com/ShizukaJiku/gitsi/internal/modules/repo"
	"github.com/ShizukaJiku/gitsi/internal/modules/servecmd"
	"github.com/ShizukaJiku/gitsi/internal/modules/services"
	"github.com/ShizukaJiku/gitsi/internal/modules/statuscmd"
	"github.com/ShizukaJiku/gitsi/internal/modules/update"
	"github.com/ShizukaJiku/gitsi/internal/modules/watchcmd"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/transport"
	"github.com/ShizukaJiku/gitsi/internal/version"
)

// Modules returns the registered modules. To add a feature, add it here.
func Modules() []module.Module {
	return []module.Module{
		provision.New(), repo.New(), mirrorcmd.New(), services.New(), policycmd.New(), servecmd.New(drop.HubHandler, watchcmd.HubHandler), drop.New(), remote.New(drop.TakeByID), doctor.New(), update.New(), watchcmd.New(), cleancmd.New(), dockercmd.New(), completion.New(),
		statuscmd.New(
			statuscmd.Probe{Label: "mailbox", Roles: module.Both, NeedsHub: true, Run: drop.Pending},
			statuscmd.Probe{Label: "outbox", Roles: module.Hub, Run: drop.Waiting},
			statuscmd.Probe{Label: "jobs", Roles: module.Edge, NeedsHub: true, Run: remote.Jobs},
			statuscmd.Probe{Label: "watch", Roles: module.Hub, Run: watchcmd.Listening},
			statuscmd.Probe{Label: "repo", Roles: module.Both, Run: repo.Here},
		),
	}
}

// Run runs gitsi and returns the process exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	reg, err := newRegistry(Modules())
	if err != nil {
		fmt.Fprintln(stderr, "internal error:", err)
		return 1
	}
	configured := config.Exists()

	args, mode, err := parseGlobal(args)
	if err != nil {
		fmt.Fprintln(stderr, "gitsi:", err)
		return 2
	}

	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || (args[0] == "help" && len(args) == 1) {
		reg.help(stdout, cfg.Role, configured)
		return 0
	}
	if args[0] == "help" { // gitsi help <group> | <command...>  =  gitsi <...> -h: it shows help, it never runs the command
		args = append(append([]string(nil), args[1:]...), "-h")
	}
	if args[0] == "__complete" { // called by PowerShell tab completion (gitsi setup completion)
		quiet := outputMode{quiet: true}
		return completeCmd(ctx, reg, cfg, args[1:], func() *module.Env { return newEnv(cfg, stdin, io.Discard, io.Discard, quiet) }, stdout)
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(stdout, "gitsi", version.String())
		return 0
	}

	res := reg.resolve(args)
	switch res.kind {
	case showGroup:
		reg.groupHelp(stdout, res.group, cfg.Role, configured)
		return 0
	case unknownSub:
		fmt.Fprintf(stderr, "there is no command %q\n", "gitsi "+res.display)
		if s := module.DidYouMean("gitsi "+res.group+" ", module.Suggest(args[1], reg.subNames(res.group, cfg.Role))); s != "" {
			fmt.Fprintf(stderr, "  %s   (gitsi %s lists its commands)\n", s, res.group)
			return 2
		}
		fmt.Fprintln(stderr)
		reg.groupHelp(stderr, res.group, cfg.Role, configured)
		return 2
	case unknownCmd:
		fmt.Fprintf(stderr, "unknown command: %s\n", res.display)
		// a likely typo gets a one-line answer; only without a close match is the whole help worth printing
		if s := module.DidYouMean("gitsi ", module.Suggest(args[0], append(reg.topNames(cfg.Role), "help", "version"))); s != "" {
			fmt.Fprintf(stderr, "  %s   (gitsi help lists the commands)\n", s)
			return 2
		}
		fmt.Fprintln(stderr)
		reg.help(stderr, cfg.Role, configured)
		return 2
	}
	cmd := res.cmd
	if !cmd.Roles.Has(cfg.Role) {
		fmt.Fprintf(stderr, "%s: not available for this machine's %s role (%s)\n", res.display, cfg.Role, roleLine(cfg.Role, configured))
		return 2
	}

	if isHelpRequest(res.rest) {
		return commandHelp(cmd, res.display, cfg, stdin, stdout)
	}

	env := newEnv(cfg, stdin, stdout, stderr, mode)
	env.Debugf("gitsi %s, role %s, command %q", version.Version, cfg.Role, res.display)
	if err := authorize(cmd, env); err != nil {
		if !offerToEnable(cmd, env, err, mode.json) {
			return exitCode(res.display, err, stderr)
		}
		if err := authorize(cmd, env); err != nil {
			return exitCode(res.display, err, stderr)
		}
	}
	if err := cmd.Run(ctx, env, res.rest); err != nil {
		return exitCode(res.display, err, stderr)
	}
	return 0
}

// newEnv builds a command's environment: on an edge with a configured hub, the ssh transport and the hub's layout (the hub
// decides it: the edge asks for it and caches it).
func newEnv(cfg *config.Config, stdin io.Reader, stdout, stderr io.Writer, mode outputMode) *module.Env {
	env := &module.Env{Stdin: stdin, Stdout: stdout, Stderr: stderr, Config: cfg, Verbose: mode.verbose, Quiet: mode.quiet, JSON: mode.json}
	// a hand-edited config.json with an invalid value gets no transport: authorize explains it (and 'gitsi config set' fixes it)
	if cfg.Role == config.RoleEdge && cfg.Hub.Host != "" && cfg.Hub.Validate() == nil {
		ssh := &transport.SSHExec{Host: cfg.Hub.Host, User: cfg.Hub.User, Port: cfg.Hub.Port, KeyFile: cfg.Hub.KeyFile}
		if env.Verbose {
			ssh.Trace = func(line string) { fmt.Fprintln(stderr, "[ssh] "+line) }
		}
		env.Transport = ssh
		res := &hubview.Resolver{Transport: ssh, Config: cfg, Debugf: env.Debugf}
		env.LayoutFn = res.Layout
		env.RefreshLayoutFn = res.Refresh
	}
	return env
}

// exitCode turns a command's error into the process exit code and prints the message when appropriate: an ExitError carries its
// own code and prints nothing (e.g. the remote command's code for `run`); any other error gives 1.
func exitCode(name string, err error, stderr io.Writer) int {
	var xe *module.ExitError
	if errors.As(err, &xe) {
		return xe.Code
	}
	if isHelpError(err) {
		return 0 // the FlagSet already printed the usage
	}
	if module.IsUsageError(err) {
		return 2 // wrong usage (README); the FlagSet already explained it, with a suggestion
	}
	if fe, ok := fault.As(err); ok {
		fmt.Fprintf(stderr, "%s: %s\n", name, fault.Format(fe))
		return fe.Kind.ExitCode()
	}
	fmt.Fprintln(stderr, name+":", err)
	return 1
}

// authorize enforces the preconditions the command declares: policy and a configured hub.
func authorize(cmd module.Command, env *module.Env) error {
	// The policy protects the edge (what goes in and out of it, what it runs on the hub). On the hub, 'send' and 'take' only move
	// files within its own mailbox: it is not required.
	if cmd.Feature != "" && env.Config.Role != config.RoleHub {
		if err := policy.Check(env.Config.Policy, cmd.Feature); err != nil {
			return err // already a fault.FeatureOff with its hint
		}
	}
	if cmd.NeedsHub && env.Config.Role == config.RoleEdge && env.Transport == nil {
		if h := env.Config.Hub; h.Host != "" {
			if err := h.Validate(); err != nil {
				return fault.New(fault.NotConfigured, "invalid hub configuration: "+err.Error(), "fix it with: gitsi config set <key> <value> (gitsi config shows the keys)")
			}
		}
		return fault.New(fault.NotConfigured, "this machine has no hub configured", "gitsi setup edge -host <hub-ip> -user <user>")
	}
	return nil
}

// interactive reports whether the user can be asked: the input is a terminal (not a script or a pipe). A variable for tests.
var interactive = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// offerToEnable: when the command fails because its optional feature is off and the user is at a terminal, it explains what it
// enables and asks whether to turn it on now, instead of forcing 'gitsi policy set ... on' and repeating the command. It is still
// a conscious act (y/N, no by default) and it is audited. It returns true when the feature was enabled.
func offerToEnable(cmd module.Command, env *module.Env, err error, jsonMode bool) bool {
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.FeatureOff || cmd.Feature == "" || jsonMode || !interactive(env.Stdin) {
		return false
	}
	fmt.Fprintf(env.Stderr, "The %q feature is disabled: it %s\nEnable it now? (y/N): ", cmd.Feature, policy.Effects[cmd.Feature])
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
	default:
		return false
	}
	if err := policy.Set(env.Config, cmd.Feature, true); err != nil {
		fmt.Fprintln(env.Stderr, "could not enable it:", err)
		return false
	}
	fmt.Fprintf(env.Stderr, "%s: enabled (turn it off with: gitsi policy set %s off)\n", cmd.Feature, cmd.Feature)
	return true
}
