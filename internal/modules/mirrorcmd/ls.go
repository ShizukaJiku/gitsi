package mirrorcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

type lsRow struct {
	Repo      string    `json:"repo"`
	URL       string    `json:"url"`
	Branch    string    `json:"branch"`
	Tip       string    `json:"tip,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	ChangedAt time.Time `json:"changed_at,omitzero"`
	Error     string    `json:"error,omitempty"`
}

func ls(_ context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("mirror ls", env.Stderr)
	pos, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return errors.New("usage: gitsi mirror ls")
	}
	if env.Config.Role == config.RoleHub {
		return hubLs(env)
	}
	st := loadState()
	rows := []lsRow{}
	for _, r := range env.Config.Mirror.Repos {
		for _, b := range r.Branches {
			s := st.get(r.Name, b)
			rows = append(rows, lsRow{Repo: r.Name, URL: r.URL, Branch: b, Tip: s.Tip, CheckedAt: s.CheckedAt, ChangedAt: s.ChangedAt, Error: s.Error})
		}
	}
	if env.JSON {
		return env.PrintJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(env.Stdout, "Nothing is mirrored. Follow an origin branch: gitsi mirror add <name> <origin-url> -branch <branch>")
		return nil
	}
	t := now()
	fmt.Fprintf(env.Stdout, "%-20s %-24s %-10s %-12s %-12s %s\n", "REPO", "BRANCH", "TIP", "CHANGED", "CHECKED", "ERROR")
	for _, r := range rows {
		fmt.Fprintf(env.Stdout, "%-20s %-24s %-10s %-12s %-12s %s\n", r.Repo, r.Branch, orDash(short(r.Tip)), ago(t, r.ChangedAt), ago(t, r.CheckedAt), r.Error)
	}
	return nil
}

func rm(ctx context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("mirror rm", env.Stderr)
	var only branchList
	fs.Var(&only, "branch", "stop following only this branch (repeat it for several)")
	pos, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: gitsi mirror rm <name> [-branch <branch>...]")
	}
	m := env.Config.Mirror
	i := find(m, strings.TrimSuffix(pos[0], ".git"))
	if i < 0 {
		return fmt.Errorf("%s is not mirrored (see gitsi mirror ls)", pos[0])
	}
	r := m.Repos[i]
	drop := r.Branches
	if len(only) > 0 {
		drop = nil
		for _, b := range only {
			j := slices.IndexFunc(r.Branches, func(c string) bool { return strings.EqualFold(c, b) })
			if j < 0 {
				return fmt.Errorf("%s does not follow the branch %s (it follows %s)", r.Name, b, strings.Join(r.Branches, ", "))
			}
			drop = append(drop, r.Branches[j])
		}
	}
	release, err := lock()
	if err != nil {
		return err
	}
	defer release()

	// The bridge first: if the hub does not answer, nothing local is forgotten, so no mirror ref is left behind unknowingly.
	dir, err := localDir(r.Name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		if _, err := newGit("").Out(ctx, "init", "--bare", "--quiet", dir); err != nil {
			return err
		}
	}
	g := newGit(dir)
	url, err := bridgeFor(env)(ctx, r.Name)
	if err != nil {
		return err
	}
	if err := relearn(ctx, g, url, drop); err != nil {
		return err
	}
	args = []string{"push", "--quiet", url}
	for _, b := range drop {
		if ref(ctx, g, pushedNS+b) != "" { // relearn left the record only where the bridge has the ref
			args = append(args, ":"+bridgeRef(b))
		}
	}
	if len(args) > 3 {
		if _, err := g.Out(ctx, args...); err != nil {
			return fmt.Errorf("could not remove the mirror refs from the bridge: %w", err)
		}
	}

	st := loadState()
	keep := slices.DeleteFunc(slices.Clone(r.Branches), func(b string) bool { return slices.Contains(drop, b) })
	for _, b := range drop {
		delete(st[r.Name], b)
	}
	if len(keep) == 0 {
		m.Repos = slices.Delete(slices.Clone(m.Repos), i, i+1)
		delete(st, r.Name)
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("could not remove the local copy %s: %w", dir, err)
		}
	} else {
		m.Repos = slices.Clone(m.Repos)
		m.Repos[i].Branches = keep
		for _, b := range drop {
			_, _ = g.Out(ctx, "update-ref", "-d", "refs/heads/"+b)
			_, _ = g.Out(ctx, "update-ref", "-d", pushedNS+b)
		}
	}
	env.Config.Mirror = m
	if err := env.Config.Save(); err != nil {
		return err
	}
	if err := st.save(); err != nil {
		return err
	}
	// The hub learns at once: otherwise the last mirror removed would show there as stale for ever.
	if w := report(ctx, env, st, now()); w != "" {
		fmt.Fprintln(env.Stderr, "warning: "+w)
	}
	if len(keep) == 0 {
		fmt.Fprintf(env.Stdout, "Stopped mirroring %s: its local copy and the bridge's mirror refs are gone (the bridge repo stays).\n", r.Name)
	} else {
		fmt.Fprintf(env.Stdout, "%s no longer follows %s; it still follows %s.\n", r.Name, strings.Join(drop, ", "), strings.Join(keep, ", "))
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ago says how long ago something happened, in the unit that reads best.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d d ago", int(d.Hours()/24))
}
