// Package cleancmd exposes `gitsi clean`: it applies right away the retention gitsi applies on its own while in use, and with
// -dry-run it shows what it would remove. On both roles: partial downloads and 'take -clip' copies; on the hub, also the mailbox
// and the jobs.
package cleancmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/cleanup"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/progress"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "clean" }

func (mod) Commands() []module.Command {
	return []module.Command{{
		Name:    "clean",
		Summary: "Remove expired data now (gitsi also does it on use); -dry-run shows it",
		Roles:   module.Both,
		Run:     run,
		Flags:   []string{"-dry-run"},
	}}
}

func run(_ context.Context, env *module.Env, args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	dry := fs.Bool("dry-run", false, "only show what would be removed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: gitsi clean [-dry-run]")
	}
	now := time.Now()
	var removed []cleanup.Removed
	if env.Config.Role == config.RoleHub {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		removed = cleanup.Hub(layout.ForHub(env.Config, home), now, *dry)
	}
	// 'take' exists on both roles: partial downloads and 'take -clip' copies stay on this machine whatever its role.
	removed = append(removed, cleanup.Parts(cleanup.ReceiveDir(), now, *dry)...)
	if root, err := cleanup.ClipRoot(); err == nil {
		removed = append(removed, cleanup.Clips(root, now, *dry)...)
	}
	if env.JSON {
		if removed == nil {
			removed = []cleanup.Removed{}
		}
		return env.PrintJSON(removed)
	}
	verb := "Removed"
	if *dry {
		verb = "Would remove"
	}
	var total int64
	for _, r := range removed {
		total += r.Size
		fmt.Fprintf(env.Stdout, "%s: %-12s %-40s %9s  (%d days old)\n", verb, r.What, r.Name, progress.Bytes(r.Size), int(r.Age.Hours()/24))
	}
	switch {
	case len(removed) == 0:
		fmt.Fprintln(env.Stdout, "Nothing to clean.")
	case *dry:
		fmt.Fprintf(env.Stdout, "%d item(s), %s. Without -dry-run they are removed.\n", len(removed), progress.Bytes(total))
	default:
		fmt.Fprintf(env.Stdout, "%d item(s), %s freed.\n", len(removed), progress.Bytes(total))
	}
	return nil
}
