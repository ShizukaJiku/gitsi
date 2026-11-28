// Package mirrorcmd exposes `gitsi mirror`: the edge keeps branches of origin repos (GitHub, GitLab...) mirrored on the bridge, so
// the hub, which does not reach the origin, stays up to date with them. They travel to hidden refs (refs/gitsi/mirror/<branch>)
// that never collide with the branches shared with share and sync. Only the edge decides what is followed: the hub never supplies
// a URL.
package mirrorcmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/bridge"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/mirror"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "mirror" }

func (mod) Commands() []module.Command {
	return []module.Command{
		{Group: "mirror", Name: "add", Summary: "Follow origin branches into a bridge repo (-list: several at once)", Roles: module.Edge, NeedsHub: true, Run: add, Flags: []string{"-branch", "-list", "-yes"}},
		{Group: "mirror", Name: "sync", Summary: "Bring the followed origin branches to the bridge now", Roles: module.Edge, NeedsHub: true, Run: syncCmd, Complete: completeNames},
		{Group: "mirror", Name: "ls", Summary: "What is mirrored: last check and error (on the hub, of each edge)", Roles: module.Both, Run: ls},
		{Group: "mirror", Name: "rm", Summary: "Stop following a repo, or one of its branches", Roles: module.Edge, NeedsHub: true, Run: rm, Flags: []string{"-branch"}, Complete: completeNames},
	}
}

// Refs. On the bridge each branch lives in mirrorNS<branch>, mapped one to one from the origin's refs/heads: like there, "a" and
// "a/b" cannot both exist, and the origin cannot have both either. In the local bare, refs/heads/<branch> is the last tip fetched from the origin and pushedNS<branch> the
// one the bridge last confirmed: comparing them tells whether to push without asking the hub, and it is the push's lease.
const (
	mirrorNS = "refs/gitsi/mirror/"
	pushedNS = "refs/gitsi/pushed/"
	seedNS   = "refs/seed/" // objects brought from the bridge when a mirror is added, until the first fetch from the origin
)

func bridgeRef(branch string) string { return mirrorNS + branch }

// perRepo bounds one repo's fetch and push: an origin that does not answer cannot hold up the others (a var in tests).
var perRepo = 2 * time.Minute

// newGit runs git without ever waiting for a person: no terminal prompt and no credential manager window, since a watch in the
// tray has nobody to answer them. Captured output already starts git without a console, so ssh cannot ask either: an expired
// credential is an error, never a hang. A var in tests.
var newGit = func(dir string) *gitx.Git {
	return &gitx.Git{Dir: dir, Run: gitx.EnvRunner("GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")}
}

// originConfig limits git toward an origin to https and ssh. The URL was validated (safe.GitURL), but the user's own git
// configuration (insteadOf) or a redirect could still turn it into another transport. A var for the tests, whose origin is a
// local folder.
var originConfig = []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always"}

// mirrorsDir holds the local bare repos, one per mirror.
func mirrorsDir() (string, error) {
	home, err := os.UserHomeDir()
	return mirror.LocalDir(home), err
}

func localDir(name string) (string, error) {
	d, err := mirrorsDir()
	return filepath.Join(d, name+".git"), err
}

// lock lets one mirror command at a time touch the local bares and the state (a manual sync and watch's cycle could otherwise
// push the same branch twice). It is a named mutex, released by Windows when the process ends: a crash leaves no stale lock.
// It includes the configuration's path, so another profile (the end-to-end tests) does not block the user's. A var in tests.
var lock = func() (func(), error) {
	p, _ := config.Path()
	sum := sha256.Sum256([]byte(strings.ToLower(p)))
	release, err := desktop.Lock("gitsi-mirror-" + hex.EncodeToString(sum[:8]))
	if errors.Is(err, desktop.ErrRunning) {
		return nil, errors.New("another mirror add, sync or rm is running with this profile: wait for it to end and try again")
	}
	return release, err
}

// bridgeFor returns the bridge URL of a repo, asking the hub for its layout only when it is needed: a cycle with no news does not
// connect to the hub at all.
func bridgeFor(env *module.Env) func(ctx context.Context, name string) (string, error) {
	return func(ctx context.Context, name string) (string, error) {
		lay, err := env.HubLayout(ctx)
		if err != nil {
			return "", err
		}
		if err := safe.Path("repos", lay.Repos); err != nil {
			return "", err
		}
		return bridge.URL(env.Config, lay.Repos, name), nil
	}
}

// find returns the index of a mirrored repo, without case (see config.Mirror.Validate), or -1.
func find(m config.Mirror, name string) int {
	for i, r := range m.Repos {
		if strings.EqualFold(r.Name, name) {
			return i
		}
	}
	return -1
}

// ref returns a ref's commit, or "" when it does not exist.
func ref(ctx context.Context, g *gitx.Git, name string) string {
	out, err := g.Out(ctx, "rev-parse", "--verify", "--quiet", name+"^{commit}")
	if err != nil {
		return ""
	}
	return out
}

func completeNames(_ context.Context, env *module.Env, _ []string) []string {
	var n []string
	for _, r := range env.Config.Mirror.Repos {
		n = append(n, r.Name)
	}
	return n
}

// branchList is a repeatable -branch option.
type branchList []string

func (b *branchList) String() string     { return strings.Join(*b, ",") }
func (b *branchList) Set(v string) error { *b = append(*b, v); return nil }

var _ flag.Value = (*branchList)(nil)

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
