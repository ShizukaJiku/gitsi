package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// offline is the transport of a help request: it goes nowhere. Asking a command for -h must never leave the machine.
type offline struct{}

var errOffline = errors.New("help: no network")

func (offline) Run(context.Context, string) (transport.Result, error) {
	return transport.Result{}, errOffline
}
func (offline) Stream(context.Context, string) (transport.Stream, error) { return nil, errOffline }
func (offline) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, errOffline
}

// isHelpRequest reports whether a command's arguments ask for its help (gitsi <cmd> -h, or gitsi help <cmd>).
func isHelpRequest(rest []string) bool {
	return len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help")
}

// helpUsage runs cmd with -h in an offline environment (a transport that refuses, a context already cancelled, a copy of the
// configuration) and returns what its FlagSet printed. It tries -h alone and then after a placeholder argument, for the commands
// that read a name first (repo get <name> -dest ...). ok is false when the command has no FlagSet or does not reach it.
func helpUsage(cmd module.Command, cfg *config.Config, stdin io.Reader) (usage string, ok bool) {
	c := *cfg // nothing a help request does may change the real configuration
	var errb bytes.Buffer
	env := &module.Env{Stdin: stdin, Stdout: io.Discard, Stderr: &errb, Config: &c, Quiet: true}
	if c.Role == config.RoleEdge {
		env.Transport = offline{}
		env.LayoutFn = func(context.Context) (layout.Layout, error) { return layout.Layout{}, errOffline }
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // nothing that waits may start
	for _, args := range [][]string{{"-h"}, {"x", "-h"}} {
		errb.Reset()
		if err := cmd.Run(ctx, env, args); errors.Is(err, flag.ErrHelp) {
			return errb.String(), true
		}
	}
	return "", false
}

// commandHelp answers a help request without the command's preconditions (an enabled feature, a configured hub): learning how
// something works must not require enabling it first. A command without options is not run at all: its summary is the help.
func commandHelp(cmd module.Command, display string, cfg *config.Config, stdin io.Reader, stdout io.Writer) int {
	fmt.Fprintf(stdout, "gitsi %s: %s\n", display, cmd.Summary)
	if cmd.Feature != "" {
		fmt.Fprintf(stdout, "Optional feature %q (gitsi policy set %s on).\n", cmd.Feature, cmd.Feature)
	}
	if len(cmd.Flags) == 0 {
		return 0
	}
	if usage, ok := helpUsage(cmd, cfg, stdin); ok {
		// the FlagSet's first line ("Usage of take:") repeats what the summary already says
		if i := strings.IndexByte(usage, '\n'); i >= 0 && strings.HasPrefix(usage, "Usage of ") {
			usage = usage[i+1:]
		}
		fmt.Fprint(stdout, "Options:\n"+usage)
	}
	return 0
}

// isHelpError reports a -h that reached a command through its normal path (after its arguments, e.g. take 2 -h): asking for help
// is not a failure, so it exits 0.
func isHelpError(err error) bool { return errors.Is(err, flag.ErrHelp) }
