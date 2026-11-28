package mirror

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/proto"
)

const tip = "0123456789abcdef0123456789abcdef01234567"

func valid() Report {
	return Report{Edge: "user@laptop", CheckedAt: time.Now().UTC().Truncate(time.Second), Repos: []RepoState{
		{Name: "app", Host: "github.com", Branches: []BranchState{{Branch: "development", Tip: tip}, {Branch: "release/1", Error: "no answer"}}},
	}}
}

func TestValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	for what, mutate := range map[string]func(r *Report){
		"edge as a path":    func(r *Report) { r.Edge = `..\x@y` },
		"edge without user": func(r *Report) { r.Edge = "laptop" },
		"repo name":         func(r *Report) { r.Repos[0].Name = "-x" },
		"host":              func(r *Report) { r.Repos[0].Host = "-oProxy" },
		"branch":            func(r *Report) { r.Repos[0].Branches[0].Branch = "a;b" },
		"tip":               func(r *Report) { r.Repos[0].Branches[0].Tip = "HEAD" },
		"control chars":     func(r *Report) { r.Repos[0].Branches[1].Error = "red \x1b[31m" },
		"long error":        func(r *Report) { r.Repos[0].Branches[1].Error = strings.Repeat("x", 501) },
	} {
		r := valid()
		r.Repos = []RepoState{{Name: r.Repos[0].Name, Host: r.Repos[0].Host, Branches: append([]BranchState(nil), r.Repos[0].Branches...)}}
		mutate(&r)
		if r.Validate() == nil {
			t.Errorf("%s: should be rejected", what)
		}
	}
}

// The edge's report travels over the session, the hub keeps it (one file per edge, replaced by the next) and lists it.
func TestSendHandleList(t *testing.T) {
	st := Store{Dir: filepath.Join(t.TempDir(), "mirror")}
	h := &Handler{Store: st}
	edgeR, hubW := io.Pipe()
	hubR, edgeW := io.Pipe()
	go func() {
		for {
			f, err := proto.ReadFrame(hubR)
			if err != nil {
				return
			}
			if !h.Handles(f.Kind) {
				t.Errorf("kind %d", f.Kind)
				return
			}
			if err := h.Handle(context.Background(), f, hubW); err != nil {
				_ = proto.WriteError(hubW, err.Error())
			}
		}
	}()
	rw := struct {
		io.Reader
		io.Writer
	}{edgeR, edgeW}
	if err := Send(rw, valid()); err != nil {
		t.Fatal(err)
	}
	next := valid()
	next.Repos = next.Repos[:0] // the edge stopped following everything
	if err := Send(rw, next); err != nil {
		t.Fatal(err)
	}
	got, err := st.List()
	if err != nil || len(got) != 1 || got[0].Edge != "user@laptop" || len(got[0].Repos) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	// an invalid report is refused by the hub too (the edge's own check could be skipped by another client)
	if err := proto.WriteJSON(edgeW, proto.KindMirror, Report{Edge: "x/../y@z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := proto.Expect(edgeR, proto.KindOK); err == nil || !strings.Contains(err.Error(), "invalid edge") {
		t.Fatalf("%v", err)
	}
	edgeW.Close()
}

// A damaged or foreign file in the folder is not shown.
func TestListSkipsBadFiles(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	if err := st.Put(valid()); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(st.Dir, "broken@x.json"), []byte("{"), 0o600)
	b, _ := os.ReadFile(filepath.Join(st.Dir, "user@laptop.json"))
	os.WriteFile(filepath.Join(st.Dir, "renamed@pc.json"), b, 0o600) // its content names another edge
	got, err := st.List()
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}
