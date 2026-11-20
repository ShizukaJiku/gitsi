package services

import (
	"context"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// 'svc up pg pg' (a repeated name) would ask for two tunnels on the same local port: ssh fails with ExitOnForwardFailure.
func TestSelectIgnoresRepeatedNames(t *testing.T) {
	c := config.Default()
	Add(c, "postgres", 0, 0)
	Add(c, "redis", 0, 0)
	got, err := Select(c, []string{"postgres", "redis", "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("a repeated name must not open two tunnels: %+v", got)
	}
}

// probeTr answers the hub's port query and records what it was asked.
type probeTr struct {
	fakeTr
	out  string
	code int
	err  error
	asks []string
}

func (p *probeTr) Run(_ context.Context, cmd string) (transport.Result, error) {
	p.asks = append(p.asks, cmd)
	return transport.Result{Stdout: p.out, ExitCode: p.code}, p.err
}

func TestLsShowsTunnelAndHubState(t *testing.T) {
	tr := &probeTr{out: "5432=up\r\n6379=down\r\n"}
	e, out := env(t, tr)
	e.Config.Policy.AllowServices = true
	busy := freeLocalPort()
	Add(e.Config, "postgres", 5432, busy)
	Add(e.Config, "redis", 0, freeLocalPort())
	if err := lsCmd(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"Services policy: enabled", "postgres", "answers", "redis", "no answer", "closed"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	if len(tr.asks) != 1 || !strings.Contains(tr.asks[0], "$p=5432") || !strings.Contains(tr.asks[0], "$p=6379") {
		t.Fatalf("it must ask the hub about both ports in a single call: %q", tr.asks)
	}
}

func TestLsWithoutHubAnswerShowsUnknownAndJSON(t *testing.T) {
	tr := &probeTr{code: 1}
	e, out := env(t, tr)
	Add(e.Config, "postgres", 0, freeLocalPort())
	if err := lsCmd(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "policy: off") || !strings.Contains(out.String(), "?") {
		t.Fatalf("without an answer from the hub the state is unknown (?):\n%s", out)
	}
	out.Reset()
	e.JSON = true
	if err := lsCmd(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"name": "postgres"`) || !strings.Contains(out.String(), `"hub": "?"`) {
		t.Fatalf("%s", out)
	}
}

func TestLsEmptyCatalog(t *testing.T) {
	e, out := env(t, &probeTr{})
	if err := lsCmd(context.Background(), e, nil); err != nil || !strings.Contains(out.String(), "gitsi svc add postgres") {
		t.Fatalf("%v %s", err, out)
	}
	out.Reset()
	e.JSON = true
	if err := lsCmd(context.Background(), e, nil); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("%v %q", err, out)
	}
}

func TestAddRmCommandValidation(t *testing.T) {
	e, _ := env(t, &probeTr{})
	for _, args := range [][]string{{}, {"a", "1", "2"}, {"api", "xx"}} {
		if err := addCmd(context.Background(), e, args); err == nil {
			t.Errorf("add %q must fail", args)
		}
	}
	for _, args := range [][]string{{}, {"a", "b"}, {"missing"}} {
		if err := rmCmd(context.Background(), e, args); err == nil {
			t.Errorf("rm %q must fail", args)
		}
	}
	// the messages suggest the current commands
	Add(e.Config, "redis", 0, 0)
	_, err := Add(e.Config, "redis", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "gitsi svc rm redis") {
		t.Fatalf("%v", err)
	}
	if _, err := Add(e.Config, "api", 0, 0); err == nil || !strings.Contains(err.Error(), "gitsi svc add api <port>") {
		t.Fatalf("%v", err)
	}
}

// 'svc up postgres' adds on its own a known service that was not in the catalog; an unknown one still needs 'svc add'.
func TestUpAddsKnownServicesOnTheFly(t *testing.T) {
	tr := &fakeTr{}
	e, out := env(t, tr)
	e.Config.Policy.AllowServices = true
	if err := addKnown(e, []string{"redis"}); err != nil {
		t.Fatal(err)
	}
	if len(e.Config.Services) != 1 || e.Config.Services[0].RemotePort != 6379 || !strings.Contains(out.String(), "Added to the catalog: redis") {
		t.Fatalf("%+v %s", e.Config.Services, out)
	}
	if err := addKnown(e, []string{"redis"}); err != nil || len(e.Config.Services) != 1 {
		t.Fatal("when it is already there it is not duplicated")
	}
	if err := upCmd(context.Background(), e, []string{"myapi"}); err == nil || !strings.Contains(err.Error(), "there is no service") {
		t.Fatalf("an unknown one still needs 'svc add': %v", err)
	}
}
