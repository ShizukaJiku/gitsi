package module

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

func TestRolesHas(t *testing.T) {
	cases := []struct {
		r    Roles
		role config.Role
		want bool
	}{
		{Edge, config.RoleEdge, true}, {Edge, config.RoleHub, false},
		{Hub, config.RoleHub, true}, {Hub, config.RoleEdge, false},
		{Both, config.RoleEdge, true}, {Both, config.RoleHub, true},
		{Edge, "", true}, // without a configured role it behaves as an edge
	}
	for _, c := range cases {
		if got := c.r.Has(c.role); got != c.want {
			t.Errorf("Roles(%d).Has(%q) = %v", c.r, c.role, got)
		}
	}
}

func envFor(role config.Role) *Env {
	cfg := config.Default()
	cfg.Role = role
	return &Env{Config: cfg, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
}

func TestHubLayoutComesFromOwnConfigInHub(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	e := envFor(config.RoleHub)
	e.LayoutFn = func(context.Context) (layout.Layout, error) {
		t.Fatal("the hub must ask nobody")
		return layout.Layout{}, nil
	}
	l, err := e.HubLayout(context.Background())
	if err != nil || !strings.HasSuffix(l.Home, "/gitsi") || !strings.HasSuffix(l.Repos, "/gitsi/repos") {
		t.Fatalf("%+v %v", l, err)
	}
	// RefreshHubLayout on the hub does not ask either: it computes the same, without a remote version
	e.RefreshLayoutFn = func(context.Context) (layout.Layout, string, error) {
		t.Fatal("it must not ask")
		return layout.Layout{}, "", nil
	}
	if l2, v, err := e.RefreshHubLayout(context.Background()); err != nil || l2 != l || v != "" {
		t.Fatalf("%+v %q %v", l2, v, err)
	}
}

func TestHubLayoutInEdgeAsksTheHub(t *testing.T) {
	ctx := context.Background()
	e := envFor(config.RoleEdge)

	// without a resolver: an actionable error (the edge does not deduce the hub's paths)
	_, err := e.HubLayout(ctx)
	if fe, ok := fault.As(err); !ok || fe.Kind != fault.NotConfigured || !strings.Contains(fe.Hint, "setup edge") {
		t.Fatalf("it must say how to set up the hub: %v", err)
	}

	// a repos_dir in the edge's config is not used
	e.Config.ReposDir = "C:/Users/x/repos"
	if _, err := e.HubLayout(ctx); err == nil {
		t.Fatal("repos_dir on the edge must not serve as a layout")
	}

	// with a resolver, the hub decides
	want := layout.Layout{Repos: "D:/r", Binary: "D:/b/gitsi.exe"}
	e.LayoutFn = func(context.Context) (layout.Layout, error) { return want, nil }
	if l, err := e.HubLayout(ctx); err != nil || l != want {
		t.Fatalf("%+v %v", l, err)
	}
	e.LayoutFn = func(context.Context) (layout.Layout, error) { return layout.Layout{}, errors.New("no network") }
	if _, err := e.HubLayout(ctx); err == nil {
		t.Fatal("the hub's error must reach the command")
	}

	// Refresh uses its resolver (and brings the hub's version)
	e.RefreshLayoutFn = func(context.Context) (layout.Layout, string, error) { return want, "0.9.0", nil }
	if l, v, err := e.RefreshHubLayout(ctx); err != nil || l != want || v != "0.9.0" {
		t.Fatalf("%+v %q %v", l, v, err)
	}
	// without a refresh resolver it falls back to HubLayout, without a version
	e.RefreshLayoutFn = nil
	e.LayoutFn = func(context.Context) (layout.Layout, error) { return want, nil }
	if _, v, err := e.RefreshHubLayout(ctx); err != nil || v != "" {
		t.Fatalf("%q %v", v, err)
	}
}

func TestDebugfOnlyWhenVerbose(t *testing.T) {
	e := envFor(config.RoleEdge)
	e.Debugf("detail %d", 1)
	if e.Stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatal("without -v it must write nothing")
	}
	e.Verbose = true
	e.Debugf("detail %d", 2)
	if got := e.Stderr.(*bytes.Buffer).String(); got != "[v] detail 2\n" {
		t.Fatalf("%q", got)
	}
	if e.Stdout.(*bytes.Buffer).Len() != 0 {
		t.Fatal("diagnostics go to stderr, never to stdout")
	}
}

func TestPrintJSONIsIndentedAndReportsErrors(t *testing.T) {
	e := envFor(config.RoleEdge)
	if err := e.PrintJSON(map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	out := e.Stdout.(*bytes.Buffer).String()
	var back map[string]int
	if json.Unmarshal([]byte(out), &back) != nil || back["a"] != 1 || !strings.Contains(out, "\n  ") {
		t.Fatalf("%q", out)
	}
	if err := e.PrintJSON(make(chan int)); err == nil {
		t.Fatal("a non-serializable value must return an error")
	}
}

func TestExitErrorMessage(t *testing.T) {
	if got := (&ExitError{Code: 42}).Error(); !strings.Contains(got, "42") {
		t.Fatal(got)
	}
}

type runFake struct {
	res transport.Result
	err error
}

func (r runFake) Run(context.Context, string) (transport.Result, error)  { return r.res, r.err }
func (runFake) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (runFake) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}

func TestRunOnHub(t *testing.T) {
	ctx := context.Background()
	e := envFor(config.RoleEdge)

	// without a transport there is no panic: there is an error that says how to set up
	if _, err := e.RunOnHub(ctx, "x"); err == nil || !strings.Contains(err.Error(), "hub") {
		t.Fatalf("%v", err)
	} else if fe, ok := fault.As(err); !ok || fe.Kind != fault.NotConfigured {
		t.Fatalf("it must be a NotConfigured fault: %v", err)
	}

	e.Transport = runFake{res: transport.Result{Stdout: "hello", ExitCode: 3}}
	if res, err := e.RunOnHub(ctx, "x"); err != nil || res.Stdout != "hello" || res.ExitCode != 3 {
		t.Fatalf("the result passes through as is: %+v %v", res, err)
	}

	cause := errors.New("ssh.exe not found")
	e.Transport = runFake{err: cause}
	if _, err := e.RunOnHub(ctx, "x"); !errors.Is(err, cause) || !strings.Contains(err.Error(), "could not run ssh") {
		t.Fatalf("it must wrap the cause without losing it: %v", err)
	}
}

func TestRunOnHubInAHubSaysItIsAnEdgeCommand(t *testing.T) {
	e := envFor(config.RoleHub)
	_, err := e.RunOnHub(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), "setup edge") || !strings.Contains(err.Error(), "edge") {
		t.Fatalf("on a hub 'setup edge' must not be suggested: %v", err)
	}
}
