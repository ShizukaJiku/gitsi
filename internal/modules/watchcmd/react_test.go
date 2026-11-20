package watchcmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// fakeSelf records the calls to gitsi itself and answers whatever it is told.
type fakeSelf struct {
	calls [][]string
	fail  map[string]bool // first argument -> fails
}

func (f *fakeSelf) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if f.fail[args[0]] {
		return "error: an antivirus removed the file", errors.New("exit 1")
	}
	return "Updated. SHA256 abc", nil
}

func newReactor(cfg config.Watch, f *fakeSelf) (*reactor, *bytes.Buffer) {
	var out bytes.Buffer
	return &reactor{cfg: cfg, self: f.run, out: &out, edge: "u@WORKSTATION", now: func() time.Time { return time.Date(2026, 11, 18, 22, 0, 0, 0, time.UTC) }}, &out
}

var upd = watch.Event{Type: watch.EvUpdate, Version: "0.12.0", SHA256: strings.Repeat("a", 64)}
var item = watch.Event{Type: watch.EvOutbox, ID: "1111111111111111", Kind: "file", Name: "a.zip"}

func TestNothingHappensWithoutReactions(t *testing.T) {
	f := &fakeSelf{}
	r, _ := newReactor(config.Watch{}, f)
	if r.react(context.Background(), upd) || r.react(context.Background(), item) || len(f.calls) != 0 || len(r.names()) != 0 {
		t.Fatalf("by default it only notifies: %v", f.calls)
	}
}

func TestAutoUpdateInstallsReportsAndAsksToRestart(t *testing.T) {
	f := &fakeSelf{}
	r, out := newReactor(config.Watch{AutoUpdate: true, Report: true}, f)
	if !r.react(context.Background(), upd) {
		t.Fatal("with the new version installed, watch must restart")
	}
	if len(f.calls) != 2 || f.calls[0][0] != "update" || f.calls[1][0] != "send" {
		t.Fatalf("update and then the report: %v", f.calls)
	}
	send := f.calls[1]
	if send[3] != "-name" || send[4] != "watch-update.txt" || !strings.Contains(send[2], "install version 0.12.0: OK") || !strings.Contains(send[2], "u@WORKSTATION") {
		t.Fatalf("report: %q", send)
	}
	if !strings.Contains(out.String(), "restarting watch") {
		t.Fatal(out.String())
	}
	if got := strings.Join(r.names(), ","); got != "update,report" {
		t.Fatal(got)
	}
}

func TestFailedUpdateDoesNotRestartAndIsReported(t *testing.T) {
	f := &fakeSelf{fail: map[string]bool{"update": true}}
	r, out := newReactor(config.Watch{AutoUpdate: true, Report: true}, f)
	if r.react(context.Background(), upd) {
		t.Fatal("when it fails there is no restart")
	}
	if !strings.Contains(f.calls[1][2], "FAILED") || !strings.Contains(out.String(), "this version is not retried") {
		t.Fatalf("%v %s", f.calls, out)
	}
}

func TestAutoTakeBringsNewItemsWithoutReportUnlessAsked(t *testing.T) {
	f := &fakeSelf{}
	r, out := newReactor(config.Watch{AutoTake: `C:\Users\u\Downloads\gitsi`}, f)
	if r.react(context.Background(), item) {
		t.Fatal("receiving something does not restart")
	}
	if len(f.calls) != 1 || strings.Join(f.calls[0], " ") != `take 1111111111111111 -dir C:\Users\u\Downloads\gitsi` {
		t.Fatalf("%v", f.calls)
	}
	if !strings.Contains(out.String(), "received a.zip") {
		t.Fatal(out.String())
	}
}
