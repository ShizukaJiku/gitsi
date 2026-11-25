// Package policycmd shows and changes the policy of sensitive features.
package policycmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "policy" }

func (mod) Commands() []module.Command {
	return []module.Command{{
		Name:    "policy",
		Summary: "Optional features: show them, or policy set <feature> on|off",
		Roles:   module.Both,
		Run:     run,
		Complete: func(_ context.Context, _ *module.Env, args []string) []string {
			switch len(args) {
			case 0:
				return []string{"set"}
			case 1:
				var out []string
				for _, f := range policy.All {
					out = append(out, string(f))
				}
				return out
			case 2:
				return []string{"on", "off"}
			}
			return nil
		},
	}}
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "off"
}

func run(_ context.Context, env *module.Env, args []string) error {
	p := &env.Config.Policy
	if len(args) == 0 {
		for _, f := range policy.All {
			v, _ := policy.Field(p, f)
			fmt.Fprintf(env.Stdout, "%-10s %s\n", f, onOff(*v))
		}
		fmt.Fprintf(env.Stdout, "%-10s %v\n", "audit", p.Audit)
		return nil
	}
	if args[0] != "set" || len(args) < 3 {
		return errors.New("usage: gitsi policy set <send|take|services|exec|clip> <on|off> [-yes]")
	}
	v, err := policy.Field(p, policy.Feature(args[1]))
	if err != nil {
		return err
	}
	var want bool
	switch args[2] {
	case "on":
		want = true
	case "off":
		want = false
	default:
		return errors.New("the value must be on or off")
	}
	yes := len(args) > 3 && args[3] == "-yes"
	if want && !*v && !yes {
		fmt.Fprintf(env.Stdout, "Enable %q: it %s\nContinue? (y/N): ", args[1], policy.Effects[policy.Feature(args[1])])
		line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("cancelled")
		}
	}
	if err := policy.Set(env.Config, policy.Feature(args[1]), want); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "%s: %s\n", args[1], onOff(want))
	return nil
}
