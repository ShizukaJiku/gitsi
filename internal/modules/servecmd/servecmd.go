// Package servecmd exposes `gitsi serve --stdio`: the hub side of the protocol, which the edge runs over SSH. It is not a
// network server: it only reads stdin and writes stdout, so it opens no port.
package servecmd

import (
	"context"
	"errors"
	"flag"
	"os"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/cleanup"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/proto"
	"github.com/ShizukaJiku/gitsi/internal/version"
)

// Factory creates a session's handler and the capabilities it announces. It receives the environment because it needs the
// configuration (mailbox folder), which does not exist yet when the modules are registered.
type Factory func(env *module.Env) (hub.Router, []string)

type mod struct{ factories []Factory }

// New returns the module. Each factory contributes a handler (mailbox, watch...) that shares the session; with none, greeting only.
func New(fs ...Factory) module.Module { return mod{factories: fs} }

func (mod) Name() string { return "serve" }

func (m mod) Commands() []module.Command {
	return []module.Command{{
		Name:    "serve",
		Summary: "(hub) serve the protocol over stdin/stdout; the edge runs it over SSH",
		Roles:   module.Both, // a hub with no saved role still shows as an edge; serve must answer anyway
		Hidden:  true,
		Run:     m.run,
		Flags:   []string{"-stdio"},
	}}
}

func (m mod) run(ctx context.Context, env *module.Env, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(env.Stderr) // stdout belongs to the protocol only
	stdio := fs.Bool("stdio", false, "serve over stdin/stdout (the only mode)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*stdio {
		return errors.New("use 'gitsi serve --stdio' (there is no network mode: the hub opens no ports)")
	}
	home, _ := os.UserHomeDir()
	cleanup.HubAuto(layout.ForHub(env.Config, home), time.Now()) // the hub's retention; no background processes
	cfg := hub.Config{Agent: "gitsi/" + version.Version, Info: hubInfo(env.Config)}
	if len(m.factories) > 0 {
		var mux hub.Mux
		for _, f := range m.factories {
			h, caps := f(env)
			mux = append(mux, h)
			cfg.Caps = append(cfg.Caps, caps...)
		}
		cfg.Handler = mux
	}
	// the wire is base64 text (proto.Armor): the remote shell may reinterpret binary bytes
	return hub.Serve(ctx, proto.NewArmorReader(env.Stdin), proto.NewArmorWriter(env.Stdout), cfg)
}

// hubInfo is what this hub announces to the edge: its folder layout (decided here, not on the edge) and its version.
func hubInfo(cfg *config.Config) map[string]string {
	home, _ := os.UserHomeDir()
	info := layout.ForHub(cfg, home).Info()
	info["version"] = version.String()
	return info
}
