package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/cleanup"
	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// runRef is the bridge's hidden ref that carries the snapshot of the edge's folder for `run -repo`: it is not a branch, so it
// does not show up in clones or mix with anything; each run replaces it.
const runRef = "refs/gitsi/run"

// snapshotRunner builds the snapshot with its own environment variables (temporary index, fixed author). A variable for tests.
var snapshotRunner = gitx.EnvRunner

// prepareRepo takes the edge's folder as is (uncommitted changes and new files included, honoring .gitignore) to the repo's
// working clone on the hub, and returns that clone's path. It touches neither the user's index or branch nor the bridge's
// branches.
func prepareRepo(ctx context.Context, env *module.Env) (work, name string, err error) {
	g := gitx.New("")
	root, err := g.Root(ctx)
	if err != nil {
		return "", "", errors.New("-repo is used inside a Git repo shared with the hub (gitsi share)")
	}
	g.Dir = root
	rn := env.Config.RemoteName
	url, err := g.Out(ctx, "config", "--get", "remote."+rn+".url") // as share left it (without insteadOf rewrites)
	if err != nil {
		return "", "", fmt.Errorf("this repo is not on the hub (it has no '%s' remote): gitsi share", rn)
	}
	prefix := env.Config.Hub.HostAlias() + ":"
	if !strings.HasPrefix(url, prefix) {
		return "", "", fmt.Errorf("the '%s' remote (%s) does not point to gitsi's bridge (%s...)", rn, url, prefix)
	}
	bridge := strings.TrimPrefix(url, prefix)
	name = strings.TrimSuffix(path.Base(bridge), ".git")
	lay, err := env.HubLayout(ctx)
	if err != nil {
		return "", "", err
	}
	work = path.Join(lay.Home, "work", name)
	if err := errors.Join(safe.Name("repo name", name), safe.Path("bridge", bridge), safe.Path("working clone", work)); err != nil {
		return "", "", err
	}
	// preparing the clone makes it equal to the edge's folder: if a job is still running there, its files would change under it
	if id, err := runningIn(ctx, env, work); err != nil {
		return "", "", err
	} else if id != "" {
		return "", "", fmt.Errorf("job %s is still running in %s's working clone: wait for it to end (gitsi job logs %s -f) or stop it (gitsi job stop %s)", id, name, id, id)
	}
	sha, err := snapshot(ctx, root)
	if err != nil {
		return "", "", err
	}
	if _, err := g.Out(ctx, "push", "--quiet", "--force", rn, sha+":"+runRef); err != nil {
		return "", "", fmt.Errorf("could not send the folder to the hub: %w", err)
	}
	res, err := env.RunOnHub(ctx, prepareScript(bridge, work))
	if err != nil {
		return "", "", err
	}
	if !strings.Contains(res.Stdout, "READY") {
		return "", "", fmt.Errorf("the hub could not prepare the working clone %s: %s", work, strings.TrimSpace(res.Stdout+" "+res.Stderr))
	}
	return work, name, nil
}

// snapshot returns a commit with the folder's current state (what `git add -A` + commit would do) without touching the user's
// index: it uses a temporary index removed at the end. The author is fixed so it does not depend on git's configuration.
func snapshot(ctx context.Context, root string) (string, error) {
	f, err := os.CreateTemp("", "gitsi-index-*")
	if err != nil {
		return "", err
	}
	idx := f.Name()
	f.Close()
	os.Remove(idx) // git creates the index; an empty file is not a valid index
	defer os.Remove(idx)
	g := &gitx.Git{Dir: root, Run: snapshotRunner("GIT_INDEX_FILE="+idx,
		"GIT_AUTHOR_NAME=gitsi", "GIT_AUTHOR_EMAIL=gitsi@localhost", "GIT_COMMITTER_NAME=gitsi", "GIT_COMMITTER_EMAIL=gitsi@localhost")}
	parent := []string{}
	if head, err := g.Out(ctx, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil && head != "" {
		if _, err := g.Out(ctx, "read-tree", "HEAD"); err != nil {
			return "", err
		}
		parent = []string{"-p", head}
	}
	if _, err := g.Out(ctx, "add", "--all"); err != nil {
		return "", fmt.Errorf("could not build the folder's snapshot: %w", err)
	}
	tree, err := g.Out(ctx, "write-tree")
	if err != nil {
		return "", err
	}
	return g.Out(ctx, append(append([]string{"commit-tree", tree}, parent...), "-m", "gitsi run -repo")...)
}

// runningIn returns the id of a job still running in the hub folder dir ("" when there is none).
func runningIn(ctx context.Context, env *module.Env, dir string) (string, error) {
	jobs, err := jobsDirOf(ctx, env)
	if err != nil {
		return "", err
	}
	res, err := env.RunOnHub(ctx, listScript(jobs))
	if err != nil {
		return "", err
	}
	for _, j := range parseJobs(res.Stdout) {
		if j.State == "running" && strings.EqualFold(j.Cwd, dir) {
			return j.ID, nil
		}
	}
	return "", nil
}

// repoCwd serves the -repo option of run and job start: it prepares the working clone and returns its folder as cwd.
func repoCwd(ctx context.Context, env *module.Env, cwd string) (string, error) {
	if cwd != "" {
		return "", errors.New("-repo already chooses the folder on the hub (the repo's working clone): it does not combine with -cwd")
	}
	work, name, err := prepareRepo(ctx, env)
	if err != nil {
		return "", err
	}
	if !env.Quiet {
		fmt.Fprintf(env.Stderr, "%s: folder sent; on the hub: %s\n", name, work)
	}
	return work, nil
}

// checkGetPath validates the -get path: relative to the working clone and never leaving it.
func checkGetPath(rel string) error {
	clean := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if path.IsAbs(clean) || strings.Contains(clean, ":") || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%q: it must be a relative path inside the hub's working folder (e.g. TestResults)", rel)
	}
	return nil
}

var getIDRe = regexp.MustCompile(`id ([0-9a-f]{16})`)

// getResult leaves <work>/<rel> in the hub's outbox (with its own gitsi) and brings it to Downloads\gitsi with take. work is the
// folder where the command ran: run -repo's working clone or a job's folder (job get).
func getResult(ctx context.Context, env *module.Env, work, rel string, take Taker) error {
	lay, err := env.HubLayout(ctx)
	if err != nil {
		return err
	}
	target := path.Join(work, path.Clean(strings.ReplaceAll(rel, "\\", "/")))
	if err := errors.Join(safe.Path("path to bring", target), safe.Path("hub's gitsi", lay.Binary)); err != nil {
		return err
	}
	res, err := env.RunOnHub(ctx, getScript(lay.Binary, target))
	if err != nil {
		return err
	}
	if strings.Contains(res.Stdout, "NOPATH") {
		return fmt.Errorf("there is no %s in the hub's working folder (%s)", rel, work)
	}
	m := getIDRe.FindStringSubmatch(res.Stdout)
	if m == nil {
		return fmt.Errorf("the hub could not leave it in its outbox: %s", strings.TrimSpace(res.Stdout+" "+res.Stderr))
	}
	dest, err := take(ctx, env, m[1], cleanup.ReceiveDir())
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Received: %s\n", dest)
	return nil
}
