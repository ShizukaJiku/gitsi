package mirrorcmd

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mirror"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// staleAfter: an edge whose last cycle is older than three intervals is no longer checking (watch closed, or failing to start).
const staleAfter = 30 * time.Minute

// HubHandler is the handler that keeps the edges' reports on the hub (app hands it to serve).
func HubHandler(env *module.Env) (hub.Router, []string) {
	return &mirror.Handler{Store: hubStore(env.Config)}, []string{mirror.Cap}
}

// hubStore is THIS machine's record (the hub's), in the home the hub decides.
func hubStore(cfg *config.Config) mirror.Store {
	home, _ := os.UserHomeDir()
	return mirror.Store{Dir: mirror.Dir(layout.ForHub(cfg, home).Home)}
}

// dialHub opens a session with the hub and says whether it records mirror reports (a var in tests).
var dialHub = func(ctx context.Context, env *module.Env) (io.ReadWriter, func(), bool, error) {
	conn, err := hubclient.Dial(ctx, env.Transport, mirror.Cap)
	if err != nil {
		return nil, nil, false, err
	}
	return conn.RW, func() { conn.Close() }, conn.Has(mirror.Cap), nil
}

// report sends the hub this edge's whole mirror state. It returns a warning instead of an error: the cycle already did its job,
// and a hub that cannot record it does not make the mirrors fail.
func report(ctx context.Context, env *module.Env, st state, at time.Time) string {
	r := mirror.Report{Edge: edgeName(), CheckedAt: at}
	for _, repo := range env.Config.Mirror.Repos {
		rs := mirror.RepoState{Name: repo.Name, Host: originHost(repo.URL)}
		for _, b := range repo.Branches {
			s := st.get(repo.Name, b)
			rs.Branches = append(rs.Branches, mirror.BranchState{Branch: b, Tip: s.Tip, CheckedAt: s.CheckedAt, ChangedAt: s.ChangedAt, Error: s.Error})
		}
		r.Repos = append(r.Repos, rs)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	rw, closeFn, has, err := dialHub(ctx, env)
	if err != nil {
		return "the hub was not told about this cycle: " + oneLine(err.Error())
	}
	defer closeFn()
	if !has {
		return mirror.ErrNoCap.Error()
	}
	if err := mirror.Send(rw, r); err != nil {
		return "the hub was not told about this cycle: " + oneLine(err.Error())
	}
	return ""
}

// edgeName is user@machine, the same name watch gives the edge on the hub.
func edgeName() string {
	clean := func(s, def string) string {
		if watch.ValidIdentity(s) && !strings.Contains(s, "@") {
			return s
		}
		return def
	}
	return clean(os.Getenv("USERNAME"), "user") + "@" + clean(os.Getenv("COMPUTERNAME"), "edge")
}

var scpHostRe = regexp.MustCompile(`^[^@/]+@([^:/]+):`)

// originHost is the host part of an origin URL (the hub sees where a mirror comes from, not the rest of its URL).
func originHost(u string) string {
	if m := scpHostRe.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	if p, err := url.Parse(u); err == nil {
		return p.Hostname()
	}
	return ""
}

type hubRow struct {
	mirror.Report
	Stale bool `json:"stale"`
}

// hubLs shows on the hub what each edge mirrors and whether it is still checking.
func hubLs(env *module.Env) error {
	reports, err := hubStore(env.Config).List()
	if err != nil {
		return err
	}
	t := now()
	rows := []hubRow{}
	for _, r := range reports {
		rows = append(rows, hubRow{Report: r, Stale: t.Sub(r.CheckedAt) > staleAfter})
	}
	if env.JSON {
		return env.PrintJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(env.Stdout, "No edge has reported mirrors yet. On the edge: gitsi mirror add <name> <origin-url> -branch <branch>")
		return nil
	}
	for i, r := range rows {
		if i > 0 {
			fmt.Fprintln(env.Stdout)
		}
		line := fmt.Sprintf("%s: last cycle %s", r.Edge, ago(t, r.CheckedAt))
		if r.Stale {
			line += " (STALE: is gitsi watch running there with watch.mirror on?)"
		}
		fmt.Fprintln(env.Stdout, line)
		fmt.Fprintf(env.Stdout, "  %-20s %-24s %-10s %-12s %-12s %s\n", "REPO", "BRANCH", "TIP", "CHANGED", "CHECKED", "ERROR")
		for _, repo := range r.Repos {
			for _, b := range repo.Branches {
				fmt.Fprintf(env.Stdout, "  %-20s %-24s %-10s %-12s %-12s %s\n", repo.Name, b.Branch, orDash(short(b.Tip)), ago(t, b.ChangedAt), ago(t, b.CheckedAt), b.Error)
			}
		}
	}
	return nil
}
