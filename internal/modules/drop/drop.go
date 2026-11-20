// Package drop is the mailbox that moves files, folders and text between the machines.
//
// Model (see docs/ARCHITECTURE.md, section 4): the hub never pushes to the edge. Both directions go through a store on the hub;
// the edge sends to the inbox (`send`) and receives from the outbox (`take`). On the hub the same verbs work the other way: `take`
// receives from the inbox and `send` leaves things in the outbox for the edge. The edge's features require policy (send / take)
// and are audited.
package drop

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ShizukaJiku/gitsi/internal/clipboard"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/progress"
)

// session is an open connection to the hub.
type session struct {
	RW    io.ReadWriter
	Close func()
}

// opener opens a session with the hub. It is injected to test without SSH.
type opener func(ctx context.Context, env *module.Env) (*session, error)

type mod struct {
	open    opener
	clip    clipboard.Reader // clipboard (injectable)
	toClip  clipboard.Writer // take -clip (inyectable)
	clipDir string           // where take -clip leaves files and folders (empty: %LOCALAPPDATA%\gitsi\clipboard)
}

// New returns the module with the real connection (SSH).
func New() module.Module { return mod{open: sshOpen, clip: clipboard.Read, toClip: clipboard.Write} }

func (mod) Name() string { return "drop" }

func (m mod) Commands() []module.Command {
	return []module.Command{
		{Name: "send", Shortcut: true, Summary: "Send what is copied (by default), paths or -text to the other side",
			Roles: module.Both, Feature: policy.Send, NeedsHub: true, Run: m.send, Flags: []string{"-clip", "-exclude", "-name", "-text"}},
		{Name: "take", Shortcut: true, Summary: "Receive what arrived (by default: the latest, to the clipboard)",
			Roles: module.Both, Feature: policy.Take, NeedsHub: true, Run: m.take, Flags: []string{"-clip", "-dir", "-keep", "-last", "-list", "-rm", "-show"}},
	}
}

// sshOpen opens the real session: `gitsi serve --stdio` on the hub over SSH.
func sshOpen(ctx context.Context, env *module.Env) (*session, error) {
	c, err := hubclient.Dial(ctx, env.Transport, mailbox.CapDrop) // the hub knows where its gitsi and its mailbox are
	if err != nil {
		return nil, err
	}
	if !c.Has(mailbox.CapDrop) {
		c.Close()
		return nil, errors.New("the hub does not offer the mailbox: on the hub: gitsi update -publish (and, if it was never set up, gitsi setup hub)")
	}
	return &session{RW: c.RW, Close: func() { c.Close() }}, nil
}

// HubHandler is used by `serve --stdio` on the hub: it serves the mailbox with the configured folder.
func HubHandler(env *module.Env) (hub.Router, []string) {
	return &mailbox.Handler{Store: store(env.Config)}, []string{mailbox.CapDrop}
}

// store is THIS machine's mailbox (the hub's): it lives where the hub decides (layout.ForHub), never where an edge asks.
func store(cfg *config.Config) *mailbox.Store {
	home, _ := os.UserHomeDir()
	return &mailbox.Store{Root: filepath.FromSlash(layout.ForHub(cfg, home).Mailbox)}
}

// transfer returns a transfer's progress callback (nil when small or with -q). after is what follows the transfer ("verifying
// on the hub"). The presentation (percentage, speed, remaining time, console) lives in internal/progress.
func transfer(env *module.Env, label string, total int64, after string) mailbox.Progress {
	return env.Progress().Transfer(label, total, after)
}

// human gives a readable size (the same format progress uses).
func human(n int64) string { return progress.Bytes(n) }
