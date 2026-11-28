package mirrorcmd

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/mirror"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// hubSession replaces dialHub with an in-memory session served by the hub's real handler; has says whether the hub announces
// the capability. It returns the hub's store.
func hubSession(t *testing.T, has bool) mirror.Store {
	t.Helper()
	st := mirror.Store{Dir: filepath.Join(t.TempDir(), "mirror")}
	h := &mirror.Handler{Store: st}
	prev := dialHub
	dialHub = func(context.Context, *module.Env) (io.ReadWriter, func(), bool, error) {
		edgeR, hubW := io.Pipe()
		hubR, edgeW := io.Pipe()
		go func() {
			for {
				f, err := proto.ReadFrame(hubR)
				if err != nil {
					return
				}
				if err := h.Handle(context.Background(), f, hubW); err != nil {
					_ = proto.WriteError(hubW, err.Error())
				}
			}
		}()
		return struct {
			io.Reader
			io.Writer
		}{edgeR, edgeW}, func() { edgeW.Close() }, has, nil
	}
	t.Cleanup(func() { dialHub = prev })
	return st
}

// After a cycle the hub has the edge's whole state: tips, times, errors and the origin's host (not its URL).
func TestSyncReportsToTheHub(t *testing.T) {
	r := newRig(t)
	hubStore := hubSession(t, true)
	r.origin("app", "main")
	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.do(syncCmd); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	got, err := hubStore.List()
	if err != nil || len(got) != 1 || len(got[0].Repos) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	repo := got[0].Repos[0]
	if repo.Name != "app" || repo.Host != "origin.test" || len(repo.Branches) != 1 || repo.Branches[0].Tip == "" || repo.Branches[0].ChangedAt.IsZero() {
		t.Fatalf("%+v", repo)
	}
	if strings.Contains(r.out.String(), "warning") {
		t.Fatalf("no warning expected: %s", r.out)
	}
	// stopping the last mirror empties the edge's report at once
	if err := r.do(rm, "app"); err != nil {
		t.Fatal(err)
	}
	if got, _ := hubStore.List(); len(got) != 1 || len(got[0].Repos) != 0 {
		t.Fatalf("%+v", got)
	}
}

// A hub without the capability (older gitsi) only gets a warning: the mirrors were updated anyway and the cycle does not fail.
func TestOldHubOnlyWarns(t *testing.T) {
	r := newRig(t)
	hubSession(t, false)
	r.origin("app", "main")
	if err := r.do(add, "app", "https://origin.test/app.git", "-branch", "main"); err != nil {
		t.Fatal(err)
	}
	r.env.JSON = true
	if err := r.do(syncCmd); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	var res syncResult
	if err := json.Unmarshal([]byte(r.out.String()), &res); err != nil || res.Warning != mirror.ErrNoCap.Error() || res.Changed != 1 {
		t.Fatalf("%+v %v\n%s", res, err, r.out)
	}
}

// On the hub, mirror ls shows each edge's report and calls out an edge that stopped checking.
func TestHubLs(t *testing.T) {
	r := newRig(t)
	r.env.Config.Role = config.RoleHub
	st := hubStore(r.env.Config)
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	tip := strings.Repeat("ab", 20)
	for edge, checked := range map[string]time.Time{"me@laptop": at.Add(-5 * time.Minute), "me@old": at.Add(-2 * time.Hour)} {
		rep := mirror.Report{Edge: edge, CheckedAt: checked, Repos: []mirror.RepoState{{Name: "app", Branches: []mirror.BranchState{
			{Branch: "development", Tip: tip, CheckedAt: checked, ChangedAt: checked, Error: "fatal: Authentication failed"}}}}}
		if err := st.Put(rep); err != nil {
			t.Fatal(err)
		}
	}
	prev := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = prev })

	if err := r.do(ls); err != nil {
		t.Fatal(err)
	}
	out := r.out.String()
	if !strings.Contains(out, "me@old: last cycle 2 h ago (STALE") || strings.Contains(out, "me@laptop: last cycle 5 min ago (STALE") ||
		!strings.Contains(out, "Authentication failed") || !strings.Contains(out, tip[:10]) {
		t.Fatal(out)
	}
	r.env.JSON = true
	if err := r.do(ls); err != nil {
		t.Fatal(err)
	}
	var rows []hubRow
	if err := json.Unmarshal([]byte(r.out.String()), &rows); err != nil || len(rows) != 2 || rows[0].Stale || !rows[1].Stale {
		t.Fatalf("%v %s", err, r.out)
	}
}

func TestOriginHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/app.git": "github.com", "ssh://git@host.example:2222/a/b.git": "host.example",
		"git@github.com:acme/app.git": "github.com",
	} {
		if got := originHost(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}
