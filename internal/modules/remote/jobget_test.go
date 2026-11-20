package remote

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// jobGetHub answers the jobs listing and the script that leaves a path in the outbox.
type jobGetHub struct {
	runTr
	listing string
	getRan  string
}

func (h *jobGetHub) Run(ctx context.Context, script string) (transport.Result, error) {
	switch {
	case strings.Contains(script, "' send $p"):
		h.getRan = script
		return transport.Result{Stdout: "Left for the edge: TestResults (dir, 2 KiB)  id 0123456789abcdef\r\n"}, nil
	case strings.Contains(script, `"JOB|$id`):
		return transport.Result{Stdout: h.listing}, nil
	}
	return h.runTr.Run(ctx, script)
}

const jobsListing = "JOB|aaaaaaa1|exited|0|2026-11-19T10:00:00-03:00|dotnet|C:/Users/user/gitsi/work/proy\r\n" +
	"JOB|bbbbbbb2|running||2026-11-19T11:00:00-03:00|npm|C:/Users/user/gitsi/work/web\r\n" +
	"JOB|ccccccc3|exited|1|2026-11-19T12:00:00-03:00|make|\r\n"

func jobGetRig(t *testing.T) (*module.Env, *jobGetHub, *[]string) {
	hub := &jobGetHub{listing: jobsListing}
	e := envWith(t, hub)
	e.Config.Policy.AllowExec, e.Config.Policy.AllowTake = true, true
	e.Stdout, e.Stderr = &strings.Builder{}, &strings.Builder{}
	return e, hub, &[]string{}
}

// jobGet runs 'job get' writing down in took what it asks to bring (id and folder).
func jobGet(e *module.Env, took *[]string, args ...string) error {
	m := mod{take: func(_ context.Context, _ *module.Env, id, dir string) (string, error) {
		*took = append(*took, id, dir)
		return filepath.Join(dir, "TestResults"), nil
	}}
	return m.jobGet(context.Background(), e, args)
}

// What a long job leaves in its folder (e.g. the dotnet test report of 'job start -repo') reaches Downloads\gitsi, as with
// 'run -repo -get'; by 'job ls' number or by id.
func TestJobGetBringsAPathFromTheJobFolder(t *testing.T) {
	for _, ref := range []string{"1", "aaaaaaa1"} {
		e, hub, took := jobGetRig(t)
		if err := jobGet(e, took, ref, "TestResults"); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if !strings.Contains(hub.getRan, "'C:/Users/user/gitsi/work/proy/TestResults'") {
			t.Fatalf("%s: it leaves in the outbox the path inside the job's folder: %q", ref, hub.getRan)
		}
		if len(*took) != 2 || (*took)[0] != "0123456789abcdef" || !strings.HasSuffix((*took)[1], filepath.Join("Downloads", "gitsi")) {
			t.Fatalf("%s: it brings it to Downloads\\gitsi: %q", ref, *took)
		}
		if out := e.Stdout.(*strings.Builder).String(); !strings.Contains(out, "Received:") {
			t.Fatalf("%s: %s", ref, out)
		}
	}
}

func TestJobGetRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"usage", []string{"1"}, "usage:"},
		{"path outside", []string{"1", "../out"}, "relative"},
		{"running", []string{"2", "TestResults"}, "still running"},
		{"no folder", []string{"3", "TestResults"}, "-cwd or -repo"},
		{"missing", []string{"9", "TestResults"}, "there is no job number 9"},
		{"unknown id", []string{"ddddddd4", "TestResults"}, "there is no job ddddddd4"},
	}
	for _, c := range cases {
		e, hub, took := jobGetRig(t)
		err := jobGet(e, took, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (expected %q)", c.name, err, c.want)
		}
		if hub.getRan != "" || len(*took) > 0 {
			t.Errorf("%s: it leaves and brings nothing", c.name)
		}
	}
}

// Bringing content to this machine requires 'take', checked before asking the hub anything.
func TestJobGetNeedsTakePolicy(t *testing.T) {
	e, hub, took := jobGetRig(t)
	e.Config.Policy.AllowTake = false
	if err := jobGet(e, took, "1", "TestResults"); err == nil || !strings.Contains(err.Error(), "take") {
		t.Fatalf("%v", err)
	}
	if len(hub.cmds) > 0 || hub.getRan != "" {
		t.Fatalf("it does not query the hub: %q", hub.cmds)
	}
}
