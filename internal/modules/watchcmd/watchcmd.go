// Package watchcmd exposes the hub's notices (docs/WATCH.md) and the shared clipboard (docs/CLIP.md): `gitsi watch` on the edge
// (listens to the hub and shares the clipboard, in a terminal or in the tray), `gitsi watch` on the hub (shares its clipboard) and
// `gitsi edges` on the hub (who listens). The logic lives in internal/watch, internal/clip and internal/desktop; this only wires
// them to ssh, the config and the CLI.
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
	"sync"
	"sync/atomic"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/clip"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/install"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/version"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "watch" }

func (mod) Commands() []module.Command {
	return []module.Command{
		{Name: "watch", Summary: "Hub notices and the shared clipboard, in a terminal or the tray", Roles: module.Both, NeedsHub: true,
			Run: watchCmd, Flags: []string{"-autostart", "-tray"}},
		{Name: "edges", Summary: "(hub) Edges listening with 'gitsi watch'", Roles: module.Hub, Run: edgesCmd},
	}
}

// staleAfter: an edge without a heartbeat for longer than this shows as disconnected (the hub beats every 30 s).
const staleAfter = 90 * time.Second

// HubHandler is used by `serve --stdio` on the hub: it watches THIS hub's state (its layout, never one an edge claims). The
// clipboard is always offered; whether the hub's clip policy allows it is checked live, so turning it on or off applies to the
// sessions already open.
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
		StateDir:    stateDir(env.Config),
		HubVersion:  version.Version,
		Clip:        hubSlot(env.Config),
		ClipAllowed: livePolicy(env.Config.Policy.AllowClip, time.Now),
		ClipMax:     env.Config.Clip.MaxBytes(),
	}, []string{watch.Cap, clip.Cap}
}

// loadConfig reads the configuration again (a variable in tests).
var loadConfig = config.Load

// livePolicy reports whether the clip policy is on, reading the configuration again at most once a second: a watch session
// lasts days and the policy may change meanwhile, while the slot is checked every 100 ms. When the file cannot be read, the last
// known value stands.
func livePolicy(initial bool, now func() time.Time) func() bool {
	var mu sync.Mutex
	on, checked := initial, now()
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		if t := now(); t.Sub(checked) >= time.Second {
			checked = t
			if cfg, err := loadConfig(); err == nil {
				on = cfg.Policy.AllowClip
			}
		}
		return on
	}
}

// hubSlot is the hub's clipboard slot: <hub home>/clip.
func hubSlot(cfg *config.Config) *clip.Slot {
	home, _ := os.UserHomeDir()
	return &clip.Slot{Dir: filepath.FromSlash(path.Join(layout.ForHub(cfg, home).Home, "clip"))}
}

func stateDir(cfg *config.Config) string {
	home, _ := os.UserHomeDir()
	return filepath.FromSlash(path.Join(layout.ForHub(cfg, home).Home, "watch"))
}

// errNoWatch: the hub's gitsi does not offer watch (it is outdated).
var errNoWatch = fault.New(fault.HubBinary, "the hub's gitsi does not offer watch", "on the hub: gitsi update -publish (with the current version)")

func watchCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("watch", env.Stderr)
	tray := fs.Bool("tray", false, "run in the tray: the terminal is released and watch is quit from the icon's menu")
	autostart := fs.String("autostart", "", "(hub) start 'gitsi watch -tray' when you log on: on | off")
	rest, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return errors.New("usage: gitsi watch [-tray]   (on the hub also: gitsi watch -autostart on|off)")
	}
	if *autostart != "" {
		return autostartCmd(env, *autostart)
	}
	if *tray && os.Getenv(childEnv) != "1" {
		return detach(env)
	}
	release, err := lock()
	if errors.Is(err, desktop.ErrRunning) {
		return errors.New("watch is already running on this machine (in the tray or in another terminal): quit it first")
	} else if err != nil {
		return err
	}
	defer func() { release() }()
	s, err := newSession(env, *tray)
	if err != nil {
		return err
	}
	defer s.close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cancel = cancel
	if env.Config.Role == config.RoleHub {
		err = s.runHub(ctx)
	} else {
		err = s.runEdge(ctx)
	}
	if errors.Is(err, watch.ErrRestart) {
		s.close()
		release() // the new binary takes the lock
		return restartSelf(env, *tray)
	}
	if err != nil {
		s.printf("watch stopped: %v", err)
	}
	return err
}

// runEdge listens to the hub's notices and, when enabled, shares the clipboard inside the same session.
func (s *session) runEdge(ctx context.Context) error {
	env := s.env
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	self, _ := os.Executable()
	sum, _ := install.FileSHA256(self)
	id := identity(sum)
	r := &reactor{cfg: env.Config.Watch, self: execSelf, fetch: fetchHere(env.Config.RemoteName, env.Config.Hub.HostAlias()),
		out: s.out, edge: id.User + "@" + id.Edge, now: time.Now}
	id.Reactions = r.names()
	var hubClip atomic.Bool // the hub of the current session shares its clipboard
	var told atomic.Bool    // the user was already told it does not
	c := &watch.Client{
		Dial: func(ctx context.Context) (io.ReadWriter, func(), error) {
			conn, err := hubclient.Dial(ctx, env.Transport, watch.Cap, clip.Cap)
			if err != nil {
				return nil, nil, err
			}
			if !conn.Has(watch.Cap) {
				conn.Close()
				return nil, nil, errNoWatch
			}
			hubClip.Store(conn.Has(clip.Cap))
			if s.clipOn && !conn.Has(clip.Cap) && !told.Swap(true) {
				s.printf("the hub's gitsi does not offer the shared clipboard (it is older than 1.3.0): on the hub, gitsi update -publish")
			}
			return conn.RW, func() { conn.Close() }, nil
		},
		Fatal:     fatal,
		Identity:  id,
		React:     r.react,
		StatePath: filepath.Join(home, ".gitsi", "watch-state.json"),
		Out:       s.out,
		Audit:     s.audit,
		Quit:      s.quitHint(),
		State:     s.setConnected,
	}
	if s.clipOn {
		c.Clip = func() bool { return hubClip.Load() }
		c.ClipMax = env.Config.Clip.MaxBytes()
		c.OnClip = func(ctx context.Context, it clip.Item, data []byte) { _ = s.sharer.Receive(ctx, it, data) }
		s.sharer.Send = c.SendClip
	}
	if err := s.startUI(ctx); err != nil {
		return err
	}
	return c.Run(ctx)
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
