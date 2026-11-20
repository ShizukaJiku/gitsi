// Package watchcmd exposes the hub's notices (docs/WATCH.md): `gitsi watch` on the edge (listens in the foreground) and
// `gitsi edges` on the hub (who listens). The logic lives in internal/watch; this only wires it to ssh, the config and the CLI.
package watchcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/install"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/version"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "watch" }

func (mod) Commands() []module.Command {
	return []module.Command{
		{Name: "watch", Summary: "Listen to the hub's notices in the foreground (Ctrl+C quits)", Roles: module.Edge, NeedsHub: true, Run: watchCmd},
		{Name: "edges", Summary: "(hub) Edges listening with 'gitsi watch'", Roles: module.Hub, Run: edgesCmd},
	}
}

// staleAfter: an edge without a heartbeat for longer than this shows as disconnected (the hub beats every 30 s).
const staleAfter = 90 * time.Second

// HubHandler is used by `serve --stdio` on the hub: it watches THIS hub's state (its layout, never one an edge claims).
func HubHandler(env *module.Env) (hub.Router, []string) {
	home, _ := os.UserHomeDir()
	lay := layout.ForHub(env.Config, home)
	return &watch.Handler{
		Source: &watch.FSSource{
			BinaryPath: filepath.FromSlash(lay.Binary),
			Mailbox:    &mailbox.Store{Root: filepath.FromSlash(lay.Mailbox)},
			JobsDir:    filepath.FromSlash(lay.Jobs),
			ReposDir:   filepath.FromSlash(lay.Repos),
		},
		StateDir:   stateDir(env.Config),
		HubVersion: version.Version,
	}, []string{watch.Cap}
}

func stateDir(cfg *config.Config) string {
	home, _ := os.UserHomeDir()
	return filepath.FromSlash(path.Join(layout.ForHub(cfg, home).Home, "watch"))
}

// errNoWatch: the hub's gitsi does not offer watch (it is outdated).
var errNoWatch = fault.New(fault.HubBinary, "the hub's gitsi does not offer watch", "on the hub: gitsi update -publish (with the current version)")

func watchCmd(ctx context.Context, env *module.Env, args []string) error {
	if len(args) > 0 {
		return errors.New("usage: gitsi watch")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	self, _ := os.Executable()
	sum, _ := install.FileSHA256(self)
	id := identity(sum)
	r := &reactor{cfg: env.Config.Watch, self: execSelf, fetch: fetchHere(env.Config.RemoteName, env.Config.Hub.HostAlias()),
		out: env.Stdout, edge: id.User + "@" + id.Edge, now: time.Now}
	id.Reactions = r.names()
	c := &watch.Client{
		Dial: func(ctx context.Context) (io.ReadWriter, func(), error) {
			conn, err := hubclient.Dial(ctx, env.Transport, watch.Cap)
			if err != nil {
				return nil, nil, err
			}
			if !conn.Has(watch.Cap) {
				conn.Close()
				return nil, nil, errNoWatch
			}
			return conn.RW, func() { conn.Close() }, nil
		},
		Fatal:     fatal,
		Identity:  id,
		React:     r.react,
		StatePath: filepath.Join(home, ".gitsi", "watch-state.json"),
		Out:       env.Stdout,
		Audit:     func(action, detail string) { _ = policy.Audit(env.Config.Policy, action, detail) },
	}
	if err := c.Run(ctx); errors.Is(err, watch.ErrRestart) {
		return restartSelf(env) // the new version keeps listening in this same terminal
	} else {
		return err
	}
}

// fatal: errors that retrying does not fix (configuration, key, host key, hub's gitsi without watch). The network and a powered-off hub do get fixed.
func fatal(err error) bool {
	if errors.Is(err, errNoWatch) {
		return true
	}
	if fe, ok := fault.As(err); ok {
		switch fe.Kind {
		case fault.NotConfigured, fault.Auth, fault.HostKey, fault.HubBinary:
			return true
		}
	}
	return false
}

// identity is how this edge presents itself to the hub (shown by 'gitsi edges'). A name with odd characters is replaced.
func identity(sum string) watch.Request {
	clean := func(s, def string) string {
		if watch.ValidIdentity(s) {
			return s
		}
		return def
	}
	return watch.Request{
		Edge:    clean(os.Getenv("COMPUTERNAME"), "edge"),
		User:    clean(os.Getenv("USERNAME"), "user"),
		Version: version.Version,
		SHA256:  sum,
	}
}

type edgeRow struct {
	Edge      string   `json:"edge"`
	User      string   `json:"user"`
	Version   string   `json:"version"`
	Connected bool     `json:"connected"`
	Since     string   `json:"since"`
	LastPing  string   `json:"last_ping"`
	Reactions []string `json:"reactions"`
}

func edgesCmd(_ context.Context, env *module.Env, args []string) error {
	if len(args) > 0 {
		return errors.New("usage: gitsi edges")
	}
	now := time.Now()
	es, err := watch.Edges(stateDir(env.Config), now, staleAfter)
	if err != nil {
		return err
	}
	if env.JSON {
		rows := []edgeRow{}
		for _, e := range es {
			rows = append(rows, edgeRow{e.Edge, e.User, e.Version, e.Connected, e.Since.Format(time.RFC3339), e.LastPing.Format(time.RFC3339), e.Reactions})
		}
		return env.PrintJSON(rows)
	}
	if len(es) == 0 {
		fmt.Fprintln(env.Stdout, "No edge has listened yet. On the edge: gitsi watch")
		return nil
	}
	fmt.Fprintf(env.Stdout, "%-32s %-9s %-14s %-16s %s\n", "EDGE", "VERSION", "STATE", "LAST HEARTBEAT", "REACTIONS")
	for _, e := range es {
		state := "disconnected"
		if e.Connected {
			state = "listening"
		}
		reac := strings.Join(e.Reactions, ", ")
		if reac == "" {
			reac = "-"
		}
		fmt.Fprintf(env.Stdout, "%-32s %-9s %-14s %-16s %s\n", e.User+"@"+e.Edge, e.Version, state, ago(now.Sub(e.LastPing)), reac)
	}
	return nil
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
