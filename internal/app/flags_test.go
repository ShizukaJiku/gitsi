package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// refuse is a transport that goes nowhere: asking a command for -h must never leave the machine.
type refuse struct{}

var errRefused = errors.New("test transport: no network")

func (refuse) Run(context.Context, string) (transport.Result, error) {
	return transport.Result{}, errRefused
}
func (refuse) Stream(context.Context, string) (transport.Stream, error) { return nil, errRefused }
func (refuse) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, errRefused
}

var flagLine = regexp.MustCompile(`(?m)^\s+-([A-Za-z][A-Za-z0-9-]*)`)

// helpFlags runs a command with -h in an isolated environment (temporary profile, temporary folder, no network) and returns its
// options. ok is false when the command does not reach its FlagSet (it has no options or fails earlier in this environment).
func helpFlags(t *testing.T, c module.Command) (flags []string, ok bool) {
	t.Helper()
	role := config.RoleEdge
	if !c.Roles.Has(config.RoleEdge) {
		role = config.RoleHub
	}
	cfg := config.Default()
	cfg.Role = role
	cfg.Hub.Host, cfg.Hub.User = "hub.invalid", "nobody"
	dir := t.TempDir()
	var out, errb bytes.Buffer
	env := &module.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, Config: cfg, Quiet: true}
	if role == config.RoleEdge {
		env.Transport = refuse{}
		lay := layout.ForHub(cfg, dir)
		env.LayoutFn = func(context.Context) (layout.Layout, error) { return lay, nil }
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // nothing that waits may start
	// -h alone, or after a name for the commands that require it first (repo get <name> -dest ...)
	err := c.Run(ctx, env, []string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		errb.Reset()
		if err = c.Run(ctx, env, []string{"x", "-h"}); !errors.Is(err, flag.ErrHelp) {
			return nil, false
		}
	}
	for _, m := range flagLine.FindAllStringSubmatch(errb.String(), -1) {
		flags = append(flags, "-"+m[1])
	}
	sort.Strings(flags)
	return flags, true
}

// The options each command declares for tab completion are those of its FlagSet: none missing, none extra. A command that
// declares options must be able to show them with -h in an environment without a network.
func TestDeclaredFlagsMatchTheFlagSet(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, m := range Modules() {
		for _, c := range m.Commands() {
			name := strings.TrimSpace(c.Group + " " + c.Name)
			got, ok := helpFlags(t, c)
			want := append([]string(nil), c.Flags...)
			sort.Strings(want)
			switch {
			case !ok && len(want) > 0:
				t.Errorf("%s declares %v but -h does not reach its FlagSet", name, want)
			case ok && strings.Join(got, " ") != strings.Join(want, " "):
				t.Errorf("%s: the FlagSet has %v and Flags declares %v", name, got, want)
			}
		}
	}
}
