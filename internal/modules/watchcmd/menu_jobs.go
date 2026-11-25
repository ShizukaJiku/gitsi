package watchcmd

import (
	"context"
	"fmt"
	"regexp"

	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// jobRow is a hub job as `gitsi --json job ls` lists it.
type jobRow struct {
	ID      string `json:"id"`
	State   string `json:"state"` // running | exited | lost
	Exit    string `json:"exit"`
	Started string `json:"started"`
	Exe     string `json:"exe"`
}

// jobID is the shape of a job id (it goes back as an argument).
var jobID = regexp.MustCompile(`^[0-9a-f]{8}$`)

func (s *session) refreshJobs(ctx context.Context) {
	if !s.tray || s.isHub() || !s.featureOn(policy.Exec) {
		return
	}
	s.jobs.load(func() ([]jobRow, string) {
		var rows, ok []jobRow
		if reason := listJSON(ctx, &rows, "job", "ls"); reason != "" {
			return nil, reason
		}
		for _, r := range rows {
			if jobID.MatchString(r.ID) {
				ok = append(ok, r)
			}
		}
		return ok, ""
	})
	s.refresh()
}

func (s *session) jobsMenu(ctx context.Context) desktop.MenuItem {
	if !s.featureOn(policy.Exec) {
		return desktop.MenuItem{Label: "Jobs", Icon: desktop.GlyphTerminal, Sub: s.offNote(ctx, policy.Exec, "Running commands on the hub")}
	}
	rows, reason, at := s.jobs.get()
	var sub []desktop.MenuItem
	running := 0
	switch {
	case at.IsZero():
		sub = append(sub, desktop.MenuItem{Label: "Reading the jobs...", Disabled: true})
	case reason != "":
		sub = append(sub, desktop.MenuItem{Label: "Could not read them: " + menuText(reason, 60), Disabled: true})
	case len(rows) == 0:
		sub = append(sub, desktop.MenuItem{Label: "No jobs on the hub (" + readAt(at) + ")", Disabled: true})
	default:
		sub = append(sub, desktop.MenuItem{Label: "On the hub, " + readAt(at), Disabled: true})
		for _, r := range rows {
			if r.State == "running" {
				running++
			}
			sub = append(sub, s.jobMenu(ctx, r))
		}
	}
	sub = append(sub, desktop.MenuItem{}, desktop.MenuItem{Label: "Refresh", Icon: desktop.GlyphRefresh, Action: func() { s.refreshJobs(ctx) }})
	return desktop.MenuItem{Label: countLabel("Jobs", running, "running"), Icon: desktop.GlyphTerminal, Sub: sub}
}

func (s *session) jobMenu(ctx context.Context, r jobRow) desktop.MenuItem {
	state := r.State
	if r.State == "exited" {
		state = "exit " + r.Exit
	}
	sub := []desktop.MenuItem{{Label: "Show its output", Icon: desktop.GlyphEye, Action: func() { s.jobOutput(ctx, r) }}}
	if r.State == "running" {
		sub = append(sub, desktop.MenuItem{Label: "Stop...", Icon: desktop.GlyphStop, Action: func() { s.stopJob(ctx, r) }})
	}
	return desktop.MenuItem{Label: menuText(r.Exe, 50) + "\t" + menuText(state, 20), Icon: desktop.GlyphTerminal, Sub: sub}
}

// jobLines is how much of a job's output a dialog shows: its end, where the news is.
const jobLines = "40"

func (s *session) jobOutput(ctx context.Context, r jobRow) {
	actx, cancel := context.WithTimeout(ctx, listTimeout)
	out, errOut, err := runSelf(actx, "", "job", "logs", r.ID, "-tail", jobLines)
	cancel()
	if err != nil {
		s.notify("gitsi: could not read the job's output", lastLine(errOut, err), true)
		return
	}
	if u := s.currentUI(); u != nil {
		u.Message("Job "+r.ID+": "+watch.Printable(r.Exe, 40), cutText(out, "gitsi job logs "+r.ID+" shows all of it"))
	}
}

func (s *session) stopJob(ctx context.Context, r jobRow) {
	if !s.confirm(fmt.Sprintf("Stop the job %s on the hub?\n\n%s", r.ID, watch.Printable(r.Exe, 200))) {
		return
	}
	s.act(ctx, "", "stop the job", "The job "+r.ID+" was stopped", "job", "stop", r.ID)
	s.refreshJobs(ctx)
}
