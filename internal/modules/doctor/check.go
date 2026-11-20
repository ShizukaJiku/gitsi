package doctor

import (
	"context"
	"flag"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// check is the diagnosis, with two levels:
//
//	(nothing) everything that changes nothing: tools, network, login, hub layout, -L tunnel, protocol and version, with a
//	          report to paste (on the edge); on the hub, the hub's diagnosis
//	--active  also tests sending and running (only when the send/exec policy is already enabled)
func check(ctx context.Context, env *module.Env, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	active := fs.Bool("active", false, "also test sending and running when the policy allows it")
	out := fs.String("out", "", "report file (default ~/.gitsi/verify-report.txt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if env.Config.Role == config.RoleHub {
		return report(env.Stdout, HubChecks(ctx, env.Config, systemDeps()), Mode{Quiet: env.Quiet, JSON: env.JSON})
	}
	var a []string
	if *active {
		a = append(a, "-active")
	}
	if *out != "" {
		a = append(a, "-out", *out)
	}
	return verify(ctx, env, a)
}
