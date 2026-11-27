package app

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/version"
)

// Command groups: each one is a workflow. A command belongs to a group through module.Command.Group.
var (
	groupOrder   = []string{"setup", "repo", "mirror", "svc", "job"}
	groupSummary = map[string]string{
		"setup":  "Set this machine up as an edge or a hub and see its role",
		"repo":   "Git repos through the hub's bridge",
		"mirror": "Origin branches the edge mirrors on the bridge",
		"svc":    "Hub services through local tunnels (-L)",
		"job":    "Long-running background jobs on the hub",
	}
)

type entry struct {
	cmd   module.Command
	owner string
}

type group struct {
	name string
	subs map[string]entry
}

// registry indexes the commands: top level (shortcuts and hidden aliases included) and per group.
type registry struct {
	top    map[string]entry
	groups map[string]*group
}

func newRegistry(mods []module.Module) (*registry, error) {
	r := &registry{top: map[string]entry{}, groups: map[string]*group{}}
	addTop := func(name string, e entry) error {
		if prev, dup := r.top[name]; dup {
			return fmt.Errorf("the name %q is repeated in %s and %s", name, prev.owner, e.owner)
		}
		r.top[name] = e
		return nil
	}
	for _, m := range mods {
		for _, c := range m.Commands() {
			e := entry{cmd: c, owner: m.Name()}
			if c.Group == "" {
				if err := addTop(c.Name, e); err != nil {
					return nil, err
				}
			} else {
				if _, ok := groupSummary[c.Group]; !ok {
					return nil, fmt.Errorf("%s: the group %q is not declared", m.Name(), c.Group)
				}
				g := r.groups[c.Group]
				if g == nil {
					g = &group{name: c.Group, subs: map[string]entry{}}
					r.groups[c.Group] = g
				}
				if prev, dup := g.subs[c.Name]; dup {
					return nil, fmt.Errorf("%q repeated in the group %s (%s and %s)", c.Name, c.Group, prev.owner, m.Name())
				}
				g.subs[c.Name] = e
				if c.Shortcut {
					if err := addTop(c.Name, e); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	for name, g := range r.groups {
		if prev, clash := r.top[name]; clash {
			return nil, fmt.Errorf("the group %q clashes with the command of %s", g.name, prev.owner)
		}
	}
	return r, nil
}

type resolveKind int

const (
	found resolveKind = iota
	showGroup
	unknownSub
	unknownCmd
)

type resolution struct {
	kind    resolveKind
	cmd     module.Command
	rest    []string
	display string // "repo share" or "share": for messages
	group   string
}

func isHelp(s string) bool { return s == "-h" || s == "--help" || s == "help" }

// resolve interprets the command line: a shortcut/alias/top-level command, or "group subcommand".
func (r *registry) resolve(args []string) resolution {
	name := args[0]
	if e, ok := r.top[name]; ok {
		return resolution{kind: found, cmd: e.cmd, rest: args[1:], display: name}
	}
	if g, ok := r.groups[name]; ok {
		if len(args) == 1 || isHelp(args[1]) {
			return resolution{kind: showGroup, group: name}
		}
		if e, ok := g.subs[args[1]]; ok {
			return resolution{kind: found, cmd: e.cmd, rest: args[2:], display: name + " " + args[1]}
		}
		return resolution{kind: unknownSub, group: name, display: name + " " + args[1]}
	}
	return resolution{kind: unknownCmd, display: name}
}

func roleLine(role config.Role, configured bool) string {
	what := "EDGE (only consumes: starts every connection)"
	if role == config.RoleHub {
		what = "HUB (serves: keeps repos, mailbox and services)"
	}
	s := "this machine is a " + what
	if !configured {
		s = "this machine is not set up yet (default role: " + what + ")\n  -> gitsi setup edge   (the one that consumes)   |   gitsi setup hub   (the one that serves)"
	}
	return s
}

// visibleSubs returns, sorted, a group's subcommands valid for the role.
func (g *group) visibleSubs(role config.Role) []entry {
	var out []entry
	for _, e := range g.subs {
		if !e.cmd.Hidden && e.cmd.Roles.Has(role) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].cmd.Name < out[j].cmd.Name })
	return out
}

func (r *registry) help(w io.Writer, role config.Role, configured bool) {
	fmt.Fprintf(w, "gitsi %s\n%s\n\nUsage: gitsi <group> <command> [options]   or a shortcut   (gitsi <group> lists its commands)\n\nGroups:\n", version.Version, roleLine(role, configured))
	for _, name := range groupOrder {
		g := r.groups[name]
		if g == nil {
			continue
		}
		subs := g.visibleSubs(role)
		if len(subs) == 0 {
			continue
		}
		var names []string
		for _, e := range subs {
			names = append(names, e.cmd.Name)
		}
		fmt.Fprintf(w, "  %-7s %-52s %s\n", name, groupSummary[name], strings.Join(names, " "))
	}

	var shortcuts []string
	var others []entry
	for name, e := range r.top {
		if e.cmd.Hidden || !e.cmd.Roles.Has(role) || name != e.cmd.Name {
			continue
		}
		switch {
		case e.cmd.Shortcut: // daily use: a group shortcut or a top-level command marked as such (send, take)
			shortcuts = append(shortcuts, name)
		case e.cmd.Group == "":
			others = append(others, e)
		}
	}
	sort.Strings(shortcuts)
	sort.Slice(others, func(i, j int) bool { return others[i].cmd.Name < others[j].cmd.Name })
	if len(shortcuts) > 0 {
		fmt.Fprintf(w, "\nDaily use: %s\n", strings.Join(shortcuts, ", "))
	}
	if len(others) > 0 {
		fmt.Fprintln(w, "\nOther:")
		for _, e := range others {
			fmt.Fprintf(w, "  %-9s %s\n", e.cmd.Name, e.cmd.Summary)
		}
	}
}

func (r *registry) groupHelp(w io.Writer, name string, role config.Role, configured bool) {
	fmt.Fprintf(w, "gitsi %s: %s\n%s\n\n", name, groupSummary[name], roleLine(role, configured))
	subs := r.groups[name].visibleSubs(role)
	if len(subs) == 0 {
		fmt.Fprintln(w, "  (no command of this group is available for this role)")
		return
	}
	width := 9 // the names column, aligned with the longest in the group
	for _, e := range subs {
		width = max(width, len(e.cmd.Name))
	}
	for _, e := range subs {
		short := ""
		if e.cmd.Shortcut {
			short = "  [shortcut: gitsi " + e.cmd.Name + "]"
		}
		fmt.Fprintf(w, "  gitsi %s %-*s %s%s\n", name, width, e.cmd.Name, e.cmd.Summary, short)
	}
}
