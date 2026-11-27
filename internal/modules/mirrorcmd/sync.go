package mirrorcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// The result of a cycle. Its JSON (gitsi --json mirror sync) is what watch reads to show the cycle: keep it stable.
type branchResult struct {
	Branch    string    `json:"branch"`
	Tip       string    `json:"tip,omitempty"`
	Changed   bool      `json:"changed"`
	ChangedAt time.Time `json:"changed_at,omitzero"`
}

type repoResult struct {
	Name     string         `json:"name"`
	Branches []branchResult `json:"branches"`
	Error    string         `json:"error,omitempty"`
}

type syncResult struct {
	CheckedAt time.Time    `json:"checked_at"`
	Repos     []repoResult `json:"repos"`
	Changed   int          `json:"changed"` // repos with at least one branch updated on the bridge
	Failed    int          `json:"failed"`
}

// now is the clock (a var in tests).
var now = time.Now

func syncCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("mirror sync", env.Stderr)
	names, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	m := env.Config.Mirror
	if err := m.Validate(); err != nil {
		return err
	}
	if len(m.Repos) == 0 {
		return errNothing
	}
	repos := m.Repos
	if len(names) > 0 {
		repos = nil
		for _, n := range names {
			i := find(m, n)
			if i < 0 {
				return fmt.Errorf("%s is not mirrored (see gitsi mirror ls)", n)
			}
			repos = append(repos, m.Repos[i])
		}
	}
	release, err := lock()
	if err != nil {
		return err
	}
	defer release()

	st := loadState()
	res := syncResult{CheckedAt: now()}
	url := bridgeFor(env)
	for _, r := range repos {
		rr := syncRepo(ctx, r, url)
		record(env, st, r, rr, res.CheckedAt)
		switch {
		case rr.Error != "":
			res.Failed++
		case slices.ContainsFunc(rr.Branches, func(b branchResult) bool { return b.Changed }):
			res.Changed++
		}
		res.Repos = append(res.Repos, rr)
	}
	if err := st.save(); err != nil {
		return err
	}
	if env.JSON {
		if err := env.PrintJSON(res); err != nil {
			return err
		}
	} else {
		printCycle(env, res)
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d of %d mirrors failed (see gitsi mirror ls)", res.Failed, len(res.Repos))
	}
	return nil
}

func printCycle(env *module.Env, res syncResult) {
	for _, r := range res.Repos {
		var changed []string
		for _, b := range r.Branches {
			if b.Changed {
				changed = append(changed, b.Branch+" "+short(b.Tip))
			}
		}
		switch {
		case r.Error != "":
			fmt.Fprintf(env.Stdout, "%-20s error: %s\n", r.Name, r.Error)
		case len(changed) > 0:
			fmt.Fprintf(env.Stdout, "%-20s updated %s\n", r.Name, strings.Join(changed, ", "))
		case !env.Quiet:
			fmt.Fprintf(env.Stdout, "%-20s up to date\n", r.Name)
		}
	}
	fmt.Fprintf(env.Stdout, "%d repo(s): %d updated, %d failed\n", len(res.Repos), res.Changed, res.Failed)
}

// record keeps the cycle in the state and the audit log (a new tip on the bridge, never content).
func record(env *module.Env, st state, r config.MirrorRepo, rr repoResult, at time.Time) {
	for _, b := range r.Branches {
		s := st.get(r.Name, b)
		s.CheckedAt = at
		s.Error = rr.Error
		for _, res := range rr.Branches {
			if res.Branch != b {
				continue
			}
			s.Tip = res.Tip
			if res.Changed {
				s.ChangedAt = at
				_ = policy.Audit(env.Config.Policy, "mirror", r.Name+" "+b+" "+short(res.Tip))
			}
		}
		st.set(r.Name, b, s)
	}
}

// syncRepo fetches the repo's branches from the origin and pushes to the bridge those whose tip the bridge does not have yet.
// Any failure is the repo's alone: the cycle goes on with the others.
func syncRepo(ctx context.Context, r config.MirrorRepo, bridgeURL func(context.Context, string) (string, error)) repoResult {
	rr := repoResult{Name: r.Name}
	ctx, cancel := context.WithTimeout(ctx, perRepo)
	defer cancel()
	fail := func(err error) repoResult {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("no answer within %s (origin or hub unreachable?)", perRepo)
		}
		rr.Error = oneLine(err.Error())
		return rr
	}
	dir, err := localDir(r.Name)
	if err != nil {
		return fail(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		return fail(fmt.Errorf("its local copy is missing (%s): run gitsi mirror add %s %s -branch %s again", dir, r.Name, r.URL, r.Branches[0]))
	}
	g := newGit(dir)
	args := append(slices.Clone(originConfig), "fetch", "--no-tags", "--quiet", r.URL)
	for _, b := range r.Branches {
		args = append(args, "+refs/heads/"+b+":refs/heads/"+b)
	}
	if _, err := g.Out(ctx, args...); err != nil {
		return fail(err)
	}
	dropSeed(ctx, g)

	tips := map[string]string{}
	var changed []string
	for _, b := range r.Branches {
		tips[b] = ref(ctx, g, "refs/heads/"+b)
		if tips[b] != ref(ctx, g, pushedNS+b) {
			changed = append(changed, b)
		}
	}
	if len(changed) > 0 {
		url, err := bridgeURL(ctx, r.Name)
		if err == nil {
			err = push(ctx, g, url, changed)
		}
		if err != nil {
			return fail(err)
		}
	}
	for _, b := range r.Branches {
		br := branchResult{Branch: b, Tip: tips[b], Changed: slices.Contains(changed, b)}
		rr.Branches = append(rr.Branches, br)
	}
	return rr
}

// push sends the branches to their hidden refs on the bridge. Each one carries an explicit lease (what this edge last saw there):
// the mirror is exact, so a rewritten origin branch is pushed as is, but never over a tip someone else left meanwhile without
// noticing. When the lease fails (another edge, or a local record that fell behind), it reads the bridge's real tips and tries
// once more.
func push(ctx context.Context, g *gitx.Git, url string, branches []string) error {
	try := func() (string, error) {
		args := []string{"push", "--porcelain", "--quiet"}
		for _, b := range branches {
			args = append(args, "--force-with-lease="+bridgeRef(b)+":"+ref(ctx, g, pushedNS+b))
		}
		args = append(args, url)
		for _, b := range branches {
			args = append(args, "refs/heads/"+b+":"+bridgeRef(b))
		}
		return g.Out(ctx, args...)
	}
	out, err := try()
	if err != nil && strings.Contains(out+err.Error(), "stale info") {
		if err := relearn(ctx, g, url, branches); err != nil {
			return err
		}
		_, err = try()
	}
	if err != nil {
		return err
	}
	for _, b := range branches {
		if _, err := g.Out(ctx, "update-ref", pushedNS+b, "refs/heads/"+b); err != nil {
			return err
		}
	}
	return nil
}

// relearn sets the local record of the bridge's tips to what the bridge really has.
func relearn(ctx context.Context, g *gitx.Git, url string, branches []string) error {
	args := []string{"ls-remote", url}
	for _, b := range branches {
		args = append(args, bridgeRef(b))
	}
	out, err := g.Out(ctx, args...)
	if err != nil {
		return err
	}
	real := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			real[f[1]] = f[0]
		}
	}
	for _, b := range branches {
		args := []string{"update-ref", "-d", pushedNS + b}
		if sha := real[bridgeRef(b)]; sha != "" {
			args = []string{"update-ref", pushedNS + b, sha}
		}
		if _, err := g.Out(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

// dropSeed removes the refs that kept the bridge's objects for the first fetch: from now on the fetched branches themselves tell
// the origin what is already here.
func dropSeed(ctx context.Context, g *gitx.Git) {
	out, err := g.Out(ctx, "for-each-ref", "--format=%(refname)", seedNS)
	if err != nil {
		return
	}
	for _, r := range strings.Fields(out) {
		_, _ = g.Out(ctx, "update-ref", "-d", r)
	}
}

// oneLine keeps an error readable in a table: the first non-empty lines joined, without control characters, truncated.
func oneLine(s string) string {
	return watch.Printable(strings.Join(strings.Fields(s), " "), 300)
}
