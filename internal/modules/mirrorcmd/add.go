package mirrorcmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/bridge"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

const addUsage = "usage: gitsi mirror add <name> <origin-url> -branch <branch> [-branch <branch>...]   or   gitsi mirror add -list <file>"

func add(ctx context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("mirror add", env.Stderr)
	var branches branchList
	fs.Var(&branches, "branch", "origin branch to follow (repeat it for several)")
	list := fs.String("list", "", "file with one mirror per line: <name> <origin-url> <branch> [<branch>...]")
	yes := fs.Bool("yes", false, "with -list, do not ask for confirmation")
	pos, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	var want []config.MirrorRepo
	switch {
	case *list != "" && len(pos) == 0 && len(branches) == 0:
		if want, err = readList(*list); err != nil {
			return err
		}
	case *list == "" && len(pos) == 2 && len(branches) > 0:
		want = []config.MirrorRepo{{Name: strings.TrimSuffix(pos[0], ".git"), URL: pos[1], Branches: branches}}
	default:
		return errors.New(addUsage)
	}
	// Everything is checked before anything is touched: one bad line in a list adds nothing.
	if _, err := merge(env.Config.Mirror, want); err != nil {
		return err
	}
	if err := safe.Name("user", env.Config.Hub.User); err != nil {
		return err
	}
	if *list != "" {
		fmt.Fprintln(env.Stdout, "These origin branches will be mirrored on the hub's bridge (refs/gitsi/mirror/...):")
		for _, r := range want {
			fmt.Fprintf(env.Stdout, "  %-20s %-50s %s\n", r.Name, r.URL, strings.Join(r.Branches, " "))
		}
		if !*yes && !confirm(env.Stdin, env.Stdout, "Continue?") {
			fmt.Fprintln(env.Stdout, "Cancelled.")
			return nil
		}
	}

	release, err := lock()
	if err != nil {
		return err
	}
	defer release()
	lay, err := env.HubLayout(ctx)
	if err != nil {
		return err
	}
	if err := safe.Path("repos", lay.Repos); err != nil {
		return err
	}
	for i, r := range want {
		if err := prepare(ctx, env, lay.Repos, r); err != nil {
			if i > 0 { // keep what is already prepared: running the same command again goes on from here
				env.Config.Mirror, _ = merge(env.Config.Mirror, want[:i])
				_ = env.Config.Save()
			}
			return fmt.Errorf("%s: %w (the %d before it were added; running the same command again goes on)", r.Name, err, i)
		}
	}
	env.Config.Mirror, _ = merge(env.Config.Mirror, want)
	if err := env.Config.Save(); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Added %d mirror(s). First cycle: gitsi mirror sync. To keep them up to date while watch runs: gitsi config set watch.mirror on\n", len(want))
	return nil
}

// merge adds repos to the list: a repo already followed from the same origin gains the new branches; from another origin it is
// an error (a bridge repo mirrors one origin). The result is validated.
func merge(m config.Mirror, add []config.MirrorRepo) (config.Mirror, error) {
	out := config.Mirror{Repos: slices.Clone(m.Repos)}
	for _, r := range add {
		i := find(out, r.Name)
		if i < 0 {
			out.Repos = append(out.Repos, config.MirrorRepo{Name: r.Name, URL: r.URL, Branches: slices.Clone(r.Branches)})
			continue
		}
		cur := out.Repos[i]
		if cur.URL != r.URL {
			return m, fmt.Errorf("mirror %s already follows %s: to change its origin, first gitsi mirror rm %s", cur.Name, cur.URL, cur.Name)
		}
		cur.Branches = slices.Clone(cur.Branches)
		for _, b := range r.Branches {
			if !slices.ContainsFunc(cur.Branches, func(c string) bool { return strings.EqualFold(c, b) }) {
				cur.Branches = append(cur.Branches, b)
			}
		}
		out.Repos[i] = cur
	}
	if err := out.Validate(); err != nil {
		return m, err
	}
	return out, nil
}

// prepare makes sure the bridge repo exists (creating it like share does) and seeds the local bare with the bridge's objects, so
// the first fetch from the origin only brings what the bridge lacks. A tip the bridge already mirrors becomes the local record,
// so the first push already has the right lease.
func prepare(ctx context.Context, env *module.Env, reposDir string, r config.MirrorRepo) error {
	bare := strings.TrimRight(reposDir, "/") + "/" + r.Name + ".git"
	res, err := env.RunOnHub(ctx, bridge.EnsureBareScript(bare, r.Branches[0], env.Config.Hub.User))
	if err != nil {
		return err
	}
	what := "reused"
	switch {
	case strings.Contains(res.Stdout, "CREATED"):
		what = "created"
	case strings.Contains(res.Stdout, "EXISTS"):
	default:
		return fmt.Errorf("could not prepare the repo on the hub (exit code %d): %s %s", res.ExitCode, strings.TrimSpace(res.Stdout), strings.TrimSpace(res.Stderr))
	}
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
	url := bridge.URL(env.Config, reposDir, r.Name)
	if _, err := g.Out(ctx, "fetch", "--no-tags", "--quiet", url, "+refs/heads/*:"+seedNS+"heads/*", "+"+mirrorNS+"*:"+seedNS+"mirror/*"); err != nil {
		return fmt.Errorf("could not bring the bridge's copy: %w", err)
	}
	for _, b := range r.Branches {
		if sha := ref(ctx, g, seedNS+"mirror/"+b); sha != "" {
			if _, err := g.Out(ctx, "update-ref", pushedNS+b, sha); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(env.Stdout, "%-20s %s  [%s]  (bridge repo %s)\n", r.Name, r.URL, strings.Join(r.Branches, " "), what)
	return nil
}

// readList reads "<name> <origin-url> <branch> [<branch>...]" lines; blank lines and lines starting with # are skipped.
func readList(path string) ([]config.MirrorRepo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []config.MirrorRepo
	for i, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if len(f) < 3 {
			return nil, fmt.Errorf("%s:%d: expected <name> <origin-url> <branch> [<branch>...]", path, i+1)
		}
		out = append(out, config.MirrorRepo{Name: strings.TrimSuffix(f[0], ".git"), URL: f[1], Branches: f[2:]})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no mirror in the file", path)
	}
	if _, err := merge(config.Mirror{}, out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// confirm asks y/N on stdin; no answer (no terminal) is a no.
func confirm(in io.Reader, out io.Writer, question string) bool {
	if in == nil {
		return false
	}
	fmt.Fprintf(out, "%s (y/N) ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
