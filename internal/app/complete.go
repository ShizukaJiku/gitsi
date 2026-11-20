package app

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// completeTimeout bounds how long Tab waits: a value that must be asked of the hub (repos) and does not arrive is simply not proposed.
const completeTimeout = 4 * time.Second

// completeCmd serves `gitsi __complete cur:<word> <previous words...>` (called by PowerShell tab completion, gitsi setup completion): it
// writes one candidate per line. The current word carries the "cur:" prefix because PowerShell 5.1 drops empty arguments.
func completeCmd(ctx context.Context, reg *registry, cfg *config.Config, args []string, newEnv func() *module.Env, out io.Writer) int {
	cur := ""
	if len(args) > 0 && strings.HasPrefix(args[0], "cur:") {
		cur, args = strings.TrimPrefix(args[0], "cur:"), args[1:]
	}
	ctx, cancel := context.WithTimeout(ctx, completeTimeout)
	defer cancel()
	for _, c := range reg.complete(ctx, cfg.Role, args, cur, newEnv) {
		fmt.Fprintln(out, c)
	}
	return 0
}

// complete returns the candidates for the current word: commands and groups visible for the role, subcommands, the command's
// options (when the word starts with -) or the values the command proposes (Command.Complete).
func (r *registry) complete(ctx context.Context, role config.Role, prev []string, cur string, newEnv func() *module.Env) []string {
	var words []string
	for _, w := range prev {
		if w != "-v" && w != "-q" && w != "--json" {
			words = append(words, w)
		}
	}
	var cands []string
	switch {
	case len(words) == 0:
		cands = append(r.topNames(role), "help", "version")
	case words[0] == "help" && len(words) == 1:
		cands = r.groupNames(role)
	default:
		var cmd module.Command
		var rest []string
		if e, ok := r.top[words[0]]; ok {
			cmd, rest = e.cmd, words[1:]
		} else if g, ok := r.groups[words[0]]; ok {
			if len(words) == 1 {
				for _, e := range g.visibleSubs(role) {
					cands = append(cands, e.cmd.Name)
				}
				break
			}
			e, ok := g.subs[words[1]]
			if !ok {
				return nil
			}
			cmd, rest = e.cmd, words[2:]
		} else {
			return nil
		}
		if cmd.Hidden || !cmd.Roles.Has(role) {
			return nil
		}
		// With an empty word the options are included too: Windows PowerShell 5.1 does not call native completion when the word
		// already starts with "-", so "gitsi take <Tab>" is the way to browse them there.
		if !strings.HasPrefix(cur, "-") && cmd.Complete != nil {
			cands = cmd.Complete(ctx, newEnv(), rest)
		}
		if cur == "" || strings.HasPrefix(cur, "-") {
			cands = append(cands, cmd.Flags...)
		}
	}
	return filterPrefix(cands, cur)
}

// topNames are the top-level commands and the groups the role can use (no hidden ones).
func (r *registry) topNames(role config.Role) []string {
	var names []string
	for name, e := range r.top {
		if !e.cmd.Hidden && e.cmd.Roles.Has(role) {
			names = append(names, name)
		}
	}
	return append(names, r.groupNames(role)...)
}

func (r *registry) groupNames(role config.Role) []string {
	var names []string
	for name, g := range r.groups {
		if len(g.visibleSubs(role)) > 0 {
			names = append(names, name)
		}
	}
	return names
}

// filterPrefix keeps the candidates that start with cur (case-insensitive), sorted and without repeats.
func filterPrefix(cands []string, cur string) []string {
	seen := map[string]bool{}
	var out []string
	low := strings.ToLower(cur)
	for _, c := range cands {
		if c == "" || seen[c] || !strings.HasPrefix(strings.ToLower(c), low) {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
