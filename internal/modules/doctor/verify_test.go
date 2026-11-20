package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/proto"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// hubTr simulates the hub: Stream serves the real protocol with an on-disk Store; Exec answers like 'hostname'.
type hubTr struct {
	fakeTr
	store *mailbox.Store
	exec  int
}

func (h hubTr) Stream(_ context.Context, _ string) (transport.Stream, error) {
	cl, sv := net.Pipe()
	go func() {
		hub.Serve(context.Background(), proto.NewArmorReader(sv), proto.NewArmorWriter(sv),
			hub.Config{Agent: "t", Caps: []string{mailbox.CapDrop}, Handler: &mailbox.Handler{Store: h.store}, Info: layout.ForHub(cfg(), "C:/Users/user").Info()})
		sv.Close()
	}()
	return pipeStream{cl}, nil
}

type pipeStream struct{ net.Conn }

func (h hubTr) Exec(_ context.Context, cmd string, out, _ io.Writer) (int, error) {
	out.Write([]byte("HUB-PC\r\n"))
	return h.exec, nil
}

func newHubTr(t *testing.T) hubTr {
	return hubTr{
		fakeTr: fakeTr{out: map[string]string{"probe": "ps=5.1 git=True repos=True"}},
		store:  &mailbox.Store{Root: t.TempDir()},
	}
}

func TestVerifyDefaultDoesNotModifyAnything(t *testing.T) {
	tr := newHubTr(t)
	var out bytes.Buffer
	if err := runVerify(context.Background(), &out, cfg(), tr, deps(nil, netstatSample), VerifyOptions{Layout: annLayout}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	r := out.String()
	for _, want := range []string{"=== gitsi check ===", "hub protocol", "Summary:"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in:\n%s", want, r)
		}
	}
	if strings.Contains(r, "send test") || strings.Contains(r, "run test") {
		t.Error("without -active nothing that changes anything is tested")
	}
	if items, _ := tr.store.List(mailbox.Inbox); len(items) != 0 {
		t.Fatalf("without -active nothing may reach the mailbox: %+v", items)
	}
}

func TestVerifyActiveRespectsPolicy(t *testing.T) {
	// policy off: skipped, never enabled on its own
	tr := newHubTr(t)
	var out bytes.Buffer
	runVerify(context.Background(), &out, cfg(), tr, deps(nil, netstatSample), VerifyOptions{Active: true, Layout: annLayout})
	if !strings.Contains(out.String(), "skipped: 'send' is off") || !strings.Contains(out.String(), "skipped: 'exec' is off") {
		t.Fatalf("it must skip with the policy off:\n%s", out.String())
	}
	if items, _ := tr.store.List(mailbox.Inbox); len(items) != 0 {
		t.Fatal("it must not send with the policy off")
	}

	// policy on: it really sends (the hub verifies the hash) and runs hostname
	c := cfg()
	c.Policy.AllowSend, c.Policy.AllowExec = true, true
	out.Reset()
	if err := runVerify(context.Background(), &out, c, tr, deps(nil, netstatSample), VerifyOptions{Active: true, Layout: annLayout}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "the hub received and verified") || !strings.Contains(out.String(), "HUB-PC") {
		t.Fatalf("\n%s", out.String())
	}
	items, _ := tr.store.List(mailbox.Inbox)
	if len(items) != 1 || items[0].Kind != mailbox.Text || items[0].Name != "verify.txt" {
		t.Fatalf("%+v", items)
	}
}

func TestVerifyReportsRemoteExecFailure(t *testing.T) {
	tr := newHubTr(t)
	tr.exec = 5
	c := cfg()
	c.Policy.AllowExec = true
	var out bytes.Buffer
	if err := runVerify(context.Background(), &out, c, tr, deps(nil, netstatSample), VerifyOptions{Active: true, Layout: annLayout}); err == nil {
		t.Fatalf("a failed 'hostname' must give an error:\n%s", out.String())
	}
}

func TestCheckDeclaredWithoutPreconditions(t *testing.T) {
	for _, c := range (mod{}).Commands() {
		if c.Name == "check" {
			// it must run without a configured hub: precisely to say it needs configuring
			if c.NeedsHub || c.Feature != "" {
				t.Fatalf("check must require neither a hub nor a policy: %+v", c)
			}
			return
		}
	}
	t.Fatal("the check command is missing")
}

// When the hub's binary differs, the report says which version is on each side.
type verTr struct{ hubTr }

func (v verTr) Run(_ context.Context, c string) (transport.Result, error) {
	switch {
	case strings.Contains(c, "Get-FileHash"):
		return transport.Result{Stdout: strings.Repeat("a", 64)}, nil
	case strings.HasSuffix(c, "' version"):
		return transport.Result{Stdout: "gitsi 9.9.9 (commit zzz9999)\r\n"}, nil
	}
	return v.hubTr.Run(context.Background(), c)
}

func TestUpdateCheckNamesBothVersionsWhenDifferent(t *testing.T) {
	tr := verTr{newHubTr(t)}
	r := updateCheck(context.Background(), cfg(), tr, VerifyOptions{Layout: annLayout})
	if r.Level != Warn || !strings.Contains(r.Detail, "hub 9.9.9 (commit zzz9999)") || !strings.Contains(r.Detail, "this machine") {
		t.Fatalf("%+v", r)
	}
}

func TestReportQuietAndJSON(t *testing.T) {
	rs := []Result{{Pass, "a", "fine"}, {Info, "b", "datum"}, {Warn, "c", "careful"}, {Fail, "d", "broken"}}

	var q bytes.Buffer
	if err := report(&q, rs, Mode{Quiet: true}); err == nil {
		t.Fatal("there is a failure: it must return an error")
	}
	if strings.Contains(q.String(), "fine") || strings.Contains(q.String(), "datum") || !strings.Contains(q.String(), "careful") || !strings.Contains(q.String(), "broken") || !strings.Contains(q.String(), "Summary") {
		t.Fatalf("-q: only warnings, failures and the summary:\n%s", q.String())
	}

	var j bytes.Buffer
	report(&j, rs, Mode{JSON: true})
	var got struct {
		Results []struct{ Level, Name, Detail string }
		Summary map[string]int
	}
	if err := json.Unmarshal(j.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, j.String())
	}
	if len(got.Results) != 4 || got.Results[3].Level != "fail" || got.Summary["ok"] != 1 || got.Summary["fail"] != 1 || got.Summary["warn"] != 1 || got.Summary["info"] != 1 {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(j.String(), "Summary:") {
		t.Error("in JSON there is no loose text")
	}
}
