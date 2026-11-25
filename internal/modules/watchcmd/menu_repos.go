package watchcmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/safe"
	"github.com/ShizukaJiku/gitsi/internal/watch"
	"github.com/ShizukaJiku/gitsi/internal/workdirs"
)

// repoState is a working folder of a bridge repo, as the Repos submenu shows it.
type repoState struct {
	Repo, Path, Branch string
	Remote             string // the bridge's remote in that folder ("" = the folder has none)
	Ahead, Behind      int    // against the bridge, at the last fetch
	OnBridge           bool   // the branch exists on the bridge
	Changed            int    // files with uncommitted changes
}

// Seams for tests: the recorded folders and how a folder is inspected.
var (
	listWorkdirs = workdirs.List
	inspectRepo  = inspectFolder
)

// fetchTimeout bounds each fetch from the hub while the Repos submenu is refreshed.
var fetchTimeout = 30 * time.Second

// inspectFolder reads a folder's state with git: its branch, what is left to send or receive against the bridge (after a fetch
// when fetch is set; git fetch only updates the remote branches) and its uncommitted changes.
func inspectFolder(ctx context.Context, d workdirs.Dir, remotes []string, fetch bool) repoState {
	g := gitx.New(d.Path)
	st := repoState{Repo: d.Repo, Path: d.Path, Branch: g.CurrentBranch(ctx)}
	for _, rn := range remotes {
		if g.HasRemote(ctx, rn) {
			st.Remote = rn
			break
		}
	}
	if st.Remote != "" {
		if fetch {
			fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
			_, _ = g.Out(fctx, "fetch", "--quiet", st.Remote) // unreachable hub: compare with the last fetch
			cancel()
		}
		st.Ahead, st.Behind, st.OnBridge = g.AheadBehind(ctx, st.Branch, st.Remote)
	}
	if out, err := g.Out(ctx, "status", "--porcelain"); err == nil && strings.TrimSpace(out) != "" {
		st.Changed = len(strings.Split(strings.TrimSpace(out), "\n"))
	}
	return st
}

// bridgeRemotes are the remote names that point to the bridge: the configured one, and on the hub also origin (its working
// clones come from the local bridge).
func (s *session) bridgeRemotes() []string {
	if s.isHub() {
		return []string{s.env.Config.RemoteName, "origin"}
	}
	return []string{s.env.Config.RemoteName}
}

// refreshRepos reads the working folders' state and, with fetch, the bridge's list of repos.
func (s *session) refreshRepos(ctx context.Context, fetch bool) {
	if !s.tray {
		return
	}
	s.repos.load(func() ([]repoState, string) {
		dirs, err := listWorkdirs()
		if err != nil {
			return nil, err.Error()
		}
		var out []repoState
		for _, d := range dirs {
			out = append(out, inspectRepo(ctx, d, s.bridgeRemotes(), fetch))
		}
		return out, ""
	})
	if fetch {
		s.bridge.load(func() ([]string, string) {
			var names, ok []string
			if reason := listJSON(ctx, &names, "repo", "list"); reason != "" {
				return nil, reason
			}
			for _, n := range names {
				if safe.Name("repo name", n) == nil {
					ok = append(ok, n)
				}
			}
			return ok, ""
		})
	}
	s.refresh()
}

// repoStatus is a working folder's state in a few words.
func repoStatus(st repoState) string {
	var parts []string
	switch {
	case st.Remote == "":
		parts = append(parts, "not on the hub")
	case !st.OnBridge:
		parts = append(parts, "branch not on the hub")
	case st.Ahead == 0 && st.Behind == 0:
		parts = append(parts, "up to date")
	default:
		if st.Ahead > 0 {
			parts = append(parts, fmt.Sprintf("%d to send", st.Ahead))
		}
		if st.Behind > 0 {
			parts = append(parts, fmt.Sprintf("%d to receive", st.Behind))
		}
	}
	if st.Changed > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", st.Changed))
	}
	return strings.Join(parts, ", ")
}

func (s *session) reposMenu(ctx context.Context) desktop.MenuItem {
	list, reason, at := s.repos.get()
	var sub []desktop.MenuItem
	pending := 0
	switch {
	case at.IsZero():
		sub = append(sub, desktop.MenuItem{Label: "Reading the folders...", Disabled: true})
	case reason != "":
		sub = append(sub, desktop.MenuItem{Label: "Could not read them: " + menuText(reason, 60), Disabled: true})
	case len(list) == 0:
		sub = append(sub, desktop.MenuItem{Label: "No working folders yet: get a repo from the hub, below", Disabled: true})
	default:
		sub = append(sub, desktop.MenuItem{Label: "Working folders, " + readAt(at), Disabled: true})
		for _, st := range list {
			if st.Ahead > 0 || st.Behind > 0 {
				pending++
			}
			sub = append(sub, s.repoMenu(ctx, st))
		}
	}
	sub = append(sub, desktop.MenuItem{})
	if synced := syncable(list); len(synced) > 1 {
		sub = append(sub, desktop.MenuItem{Label: "Sync all", Icon: desktop.GlyphRefresh, Action: func() { s.syncAll(ctx, synced) }})
	}
	sub = append(sub, s.getRepoMenu(ctx, list),
		desktop.MenuItem{Label: "Refresh", Icon: desktop.GlyphRefresh, Action: func() { s.refreshRepos(ctx, true) }})
	return desktop.MenuItem{Label: countLabel("Repos", pending, "to sync"), Icon: desktop.GlyphRepo, Sub: sub}
}

func syncable(list []repoState) []repoState {
	var out []repoState
	for _, st := range list {
		if st.Remote != "" {
			out = append(out, st)
		}
	}
	return out
}

// repoMenu is one working folder: its state and what can be done with it.
func (s *session) repoMenu(ctx context.Context, st repoState) desktop.MenuItem {
	var sub []desktop.MenuItem
	if st.Remote != "" {
		sub = append(sub, desktop.MenuItem{Label: "Sync with the hub", Icon: desktop.GlyphRefresh, Action: func() { s.syncRepo(ctx, st, true) }})
		if !s.isHub() {
			sub = append(sub, desktop.MenuItem{Label: "Publish to origin...", Icon: desktop.GlyphUpload, Action: func() { s.publishRepo(ctx, st) }})
		}
		sub = append(sub, desktop.MenuItem{})
	}
	sub = append(sub,
		desktop.MenuItem{Label: "Open the folder", Icon: desktop.GlyphFolder, Action: func() {
			if err := openFolder(st.Path); err != nil {
				s.notify("gitsi", "Could not open the folder: "+err.Error(), true)
			}
		}},
		desktop.MenuItem{Label: "Open a terminal here", Icon: desktop.GlyphTerminal, Action: func() {
			if err := openTerminal(st.Path); err != nil {
				s.notify("gitsi", "Could not open a terminal: "+err.Error(), true)
			}
		}})
	label := menuText(fmt.Sprintf("%s (%s)", st.Repo, st.Branch), 50) + "\t" + repoStatus(st)
	return desktop.MenuItem{Label: label, Icon: desktop.GlyphRepo, Sub: sub}
}

// syncRepo runs `gitsi repo sync` in the folder; refresh reads the folders again afterwards.
func (s *session) syncRepo(ctx context.Context, st repoState, refresh bool) bool {
	ok := s.act(ctx, st.Path, "sync "+st.Repo, st.Repo+" is in sync with the hub", "repo", "sync")
	if refresh {
		s.refreshRepos(ctx, false)
	}
	return ok
}

func (s *session) syncAll(ctx context.Context, list []repoState) {
	for _, st := range list {
		s.syncRepo(ctx, st, false)
	}
	s.refreshRepos(ctx, false)
}

func (s *session) publishRepo(ctx context.Context, st repoState) {
	if !s.confirm(fmt.Sprintf("Publish %s of %s to origin?\n\nThe branch goes to origin as one commit of yours, ready for a pull request.",
		watch.Printable(st.Branch, 80), watch.Printable(st.Repo, 80))) {
		return
	}
	s.act(ctx, st.Path, "publish "+st.Repo, st.Repo+" was published", "repo", "publish")
	s.refreshRepos(ctx, false)
}

// getRepoMenu offers the bridge's repos that have no working folder here.
func (s *session) getRepoMenu(ctx context.Context, have []repoState) desktop.MenuItem {
	names, reason, at := s.bridge.get()
	here := map[string]bool{}
	for _, st := range have {
		here[strings.ToLower(st.Repo)] = true
	}
	var sub []desktop.MenuItem
	switch {
	case at.IsZero():
		sub = append(sub, desktop.MenuItem{Label: "Reading the hub's list...", Disabled: true})
	case reason != "":
		sub = append(sub, desktop.MenuItem{Label: "Could not list them: " + menuText(reason, 60), Disabled: true})
	}
	for _, n := range names {
		if here[strings.ToLower(n)] {
			continue
		}
		sub = append(sub, desktop.MenuItem{Label: menuText(n, 50) + "...", Icon: desktop.GlyphTake, Action: func() { s.getRepo(ctx, n) }})
	}
	if len(sub) == 0 {
		sub = append(sub, desktop.MenuItem{Label: "Every repo on the hub has a folder here", Disabled: true})
	}
	return desktop.MenuItem{Label: "Get a repo from the hub", Icon: desktop.GlyphTake, Sub: sub}
}

// getRepo asks where to put a repo and gets it there (`gitsi repo get`, which records the folder).
func (s *session) getRepo(ctx context.Context, name string) {
	u := s.currentUI()
	if u == nil {
		return
	}
	parent, ok := u.PickFolder("Where should " + name + " go? A folder named " + name + " is created inside the one you choose.")
	if !ok {
		return
	}
	dest := filepath.Join(parent, name)
	s.act(ctx, parent, "get "+name, name+" is in "+dest, "repo", "get", name, "-dest", dest)
	s.refreshRepos(ctx, false)
}
