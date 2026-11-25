package watchcmd

import (
	"context"
	"fmt"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/version"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// refreshEdges reads which edges listen (the hub's own state files: no child, no network).
func (s *session) refreshEdges() {
	if !s.tray || !s.isHub() {
		return
	}
	s.edges.load(func() ([]watch.EdgeState, string) {
		es, err := watch.Edges(stateDir(s.env.Config), time.Now(), staleAfter)
		if err != nil {
			return nil, err.Error()
		}
		return es, ""
	})
	s.refresh()
}

func (s *session) edgesMenu(ctx context.Context) desktop.MenuItem {
	es, reason, _ := s.edges.get()
	sub := []desktop.MenuItem{{Label: "Listening with gitsi watch", Disabled: true}}
	listening := 0
	switch {
	case reason != "":
		sub = append(sub, desktop.MenuItem{Label: "Could not read them: " + menuText(reason, 60), Disabled: true})
	case len(es) == 0:
		sub = append(sub, desktop.MenuItem{Label: "No edge has listened yet", Disabled: true})
	}
	for _, e := range es {
		state := "disconnected"
		if e.Connected {
			state = "listening since " + e.Since.Local().Format("15:04")
			listening++
		}
		sub = append(sub, desktop.MenuItem{Label: menuText(e.User+"@"+e.Edge, 50) + "\t" + menuText(state+", "+e.Version, 40),
			Icon: desktop.GlyphMonitor, Disabled: true})
	}
	sub = append(sub, desktop.MenuItem{},
		desktop.MenuItem{Label: "Publish this version to the edges...", Icon: desktop.GlyphUpload, Action: func() { s.publishVersion(ctx) }})
	return desktop.MenuItem{Label: countLabel("Edges", listening, "listening"), Icon: desktop.GlyphMonitor, Sub: sub}
}

// publishVersion publishes this gitsi for the edges (`gitsi update -publish`), after asking.
func (s *session) publishVersion(ctx context.Context) {
	if !s.confirm(fmt.Sprintf("Publish gitsi %s for the edges?\n\nEdges with \"Update automatically\" install it on their own; the "+
		"others when they run gitsi update.", version.Version)) {
		return
	}
	s.act(ctx, "", "publish this version", "gitsi "+version.Version+" is published for the edges", "update", "-publish")
}
