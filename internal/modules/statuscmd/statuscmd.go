// Package statuscmd exposes `gitsi status`: the state at a glance (the hub and its version, the mailbox, the jobs, the listening
// edges and the repo of the current folder). Each piece comes from a read-only probe of the module that knows it; app gathers them
// here (statuscmd.New(...)) so no module depends on another.
package statuscmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/version"
)

// Probe is one line of `gitsi status`: a read-only query that returns a short summary.
type Probe struct {
	Label string
	Roles module.Roles
	// NeedsHub: on the edge, it queries the hub. When the hub does not answer, it is not attempted (each attempt would wait for
	// its own deadline).
	NeedsHub bool
	Run      func(ctx context.Context, env *module.Env) (string, error)
}

// probeTimeout bounds each query: status is for a quick look, not for waiting.
const probeTimeout = 20 * time.Second

type mod struct{ probes []Probe }

// New returns the module with the probes app passes, in display order.
func New(probes ...Probe) module.Module { return mod{probes: probes} }

func (mod) Name() string { return "status" }

func (m mod) Commands() []module.Command {
	return []module.Command{{
		Name:     "status",
		Shortcut: true,
		Summary:  "State at a glance: hub, mailbox, jobs and current repo",
		Roles:    module.Both,
		NeedsHub: true,
		Run:      m.run,
	}}
}

// line is one row of the result (also its JSON form).
type line struct {
	Label string `json:"label"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

func (m mod) run(ctx context.Context, env *module.Env, args []string) error {
	if len(args) > 0 {
		return errors.New("usage: gitsi status")
	}
	role := env.Config.Role
	var lines []line
	var hubErr error
	if role == config.RoleEdge {
		l := line{Label: "hub"}
		if text, err := hubLine(ctx, env); err != nil {
			hubErr, l.Error = err, err.Error()
		} else {
			l.Text = text
		}
		lines = append(lines, l)
	} else {
		lines = append(lines, line{Label: "machine", Text: "hub, gitsi " + version.Version})
	}
	for _, p := range m.probes {
		if !p.Roles.Has(role) {
			continue
		}
		l := line{Label: p.Label}
		if p.NeedsHub && role == config.RoleEdge && hubErr != nil {
			l.Error = "no hub"
		} else {
			pctx, cancel := context.WithTimeout(ctx, probeTimeout)
			text, err := p.Run(pctx, env)
			cancel()
			if err != nil {
				l.Error = err.Error()
			} else {
				l.Text = text
			}
		}
		lines = append(lines, l)
	}
	if env.JSON {
		if err := env.PrintJSON(lines); err != nil {
			return err
		}
	} else {
		for _, l := range lines {
			if l.Error != "" {
				fmt.Fprintf(env.Stdout, "%-9s error: %s\n", l.Label, l.Error)
			} else {
				fmt.Fprintf(env.Stdout, "%-9s %s\n", l.Label, l.Text)
			}
		}
	}
	return hubErr // a hub that does not answer is a failure (network or authentication exit code); the rest is information
}

// hubLine asks the hub (without the cache) and compares its version with this machine's.
func hubLine(ctx context.Context, env *module.Env) (string, error) {
	if env.RefreshLayoutFn == nil {
		return "", errors.New("this machine has no hub configured")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	_, v, err := env.RefreshLayoutFn(ctx)
	if err != nil {
		return "", err
	}
	// the hub announces its full version ("1.0.0 (commit ..., built ...)"): only the number is shown and compared
	if f := strings.Fields(v); len(f) > 0 {
		v = f[0]
	}
	text := fmt.Sprintf("%s answers, gitsi %s", env.Config.Hub.Host, v)
	switch {
	case v == version.Version:
		text += " (same as this machine)"
	case v != "":
		text += fmt.Sprintf(" and this machine %s (gitsi update)", version.Version)
	}
	return text, nil
}
