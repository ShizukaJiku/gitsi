package services

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

func TestAddPresetAndDefaults(t *testing.T) {
	c := config.Default()
	s, err := Add(c, "postgres", 0, 0)
	if err != nil || s.RemotePort != 5432 || s.LocalPort != 5432 {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = Add(c, "pg2", 5433, 15433)
	if err != nil || s.LocalPort != 15433 {
		t.Fatalf("%+v %v", s, err)
	}
	if len(c.Services) != 2 {
		t.Fatal("they were not added")
	}
}

func TestAddValidation(t *testing.T) {
	c := config.Default()
	if _, err := Add(c, "x", 0, 0); err == nil {
		t.Error("without a preset or a port it must fail")
	}
	if _, err := Add(c, "a;b", 80, 8080); err == nil {
		t.Error("invalid name")
	}
	if _, err := Add(c, "web", 80, 80); err == nil {
		t.Error("privileged local port")
	}
	if _, err := Add(c, "web", 70000, 8080); err == nil {
		t.Error("hub port out of range")
	}
	Add(c, "redis", 0, 0)
	if _, err := Add(c, "redis", 0, 0); err == nil {
		t.Error("duplicate")
	}
	if _, err := Add(c, "cache", 6379, 6379); err == nil {
		t.Error("repeated local port")
	}
}

func TestRemoveAndSelect(t *testing.T) {
	c := config.Default()
	Add(c, "postgres", 0, 0)
	Add(c, "redis", 0, 0)
	if got, err := Select(c, nil); err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := Select(c, []string{"redis"}); err != nil || len(got) != 1 || got[0].Name != "redis" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := Select(c, []string{"nothing"}); err == nil {
		t.Error("it should fail")
	}
	if err := Remove(c, "redis"); err != nil || len(c.Services) != 1 {
		t.Fatal("it was not removed")
	}
	if Remove(c, "redis") == nil {
		t.Error("removing a missing one must fail")
	}
	if _, err := Select(config.Default(), nil); err == nil {
		t.Error("an empty catalog must fail")
	}
}

// Principle (the edge exposes nothing): the generated tunnels are local and point at no other host.
func TestForwardsOnlyLoopbackOfHub(t *testing.T) {
	f := Forwards([]config.Service{{Name: "pg", RemotePort: 5432, LocalPort: 15432}})
	if len(f) != 1 || f[0].LocalPort != 15432 || f[0].RemotePort != 5432 || f[0].RemoteHost != "" {
		t.Fatalf("%+v", f)
	}
}

func TestProbe(t *testing.T) {
	svcs := []config.Service{{Name: "pg", RemotePort: 5432, LocalPort: 5432}, {Name: "r", RemotePort: 6379, LocalPort: 6379}}
	if c := hubProbeCmd(svcs); !strings.Contains(c, "$p=5432") || !strings.Contains(c, "$p=6379") {
		t.Fatal(c)
	}
	m := parseProbe("5432=up\r\n6379=down\r\nnoise\r\n")
	if !m[5432] || m[6379] {
		t.Fatalf("%v", m)
	}
}

type fakeTunnel struct{ closed chan struct{} }

func (f *fakeTunnel) Wait() error  { <-f.closed; return nil }
func (f *fakeTunnel) Close() error { close(f.closed); return nil }

type fakeTr struct {
	got []transport.Forward
	err error
	tun *fakeTunnel
}

func (f *fakeTr) Run(context.Context, string) (transport.Result, error) {
	return transport.Result{}, nil
}
func (f *fakeTr) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (f *fakeTr) Forward(_ context.Context, fw []transport.Forward) (transport.Tunnel, error) {
	f.got = fw
	if f.err != nil {
		return nil, f.err
	}
	f.tun = &fakeTunnel{closed: make(chan struct{})}
	return f.tun, nil
}

func env(t *testing.T, tr transport.Transport) (*module.Env, *bytes.Buffer) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	return &module.Env{Stdout: &out, Stderr: &out, Config: config.Default(), Transport: tr}, &out
}

func freeLocalPort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// The policy is not checked by the command but by the dispatcher, from what the command declares.
func TestUpDeclaresPolicyAndHub(t *testing.T) {
	for _, c := range New().Commands() {
		if c.Name == "up" && (c.Feature != policy.Services || !c.NeedsHub) {
			t.Fatalf("up must declare Feature=services and NeedsHub: %+v", c)
		}
	}
}

func TestUpOpensForwardsAndStopsOnCancel(t *testing.T) {
	tr := &fakeTr{}
	e, out := env(t, tr)
	e.Config.Policy.AllowServices = true
	port := freeLocalPort()
	e.Config.Services = []config.Service{{Name: "postgres", RemotePort: 5432, LocalPort: port}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- upCmd(ctx, e, nil) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("up did not end on cancel")
	}
	if len(tr.got) != 1 || tr.got[0].LocalPort != port || tr.got[0].RemotePort != 5432 {
		t.Fatalf("%+v", tr.got)
	}
	if !strings.Contains(out.String(), "Tunnels closed") {
		t.Fatal(out.String())
	}
}

func TestUpRejectsBusyLocalPort(t *testing.T) {
	e, _ := env(t, &fakeTr{})
	e.Config.Policy.AllowServices = true
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	e.Config.Services = []config.Service{{Name: "pg", RemotePort: 5432, LocalPort: l.Addr().(*net.TCPAddr).Port}}
	if err := upCmd(context.Background(), e, nil); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("%v", err)
	}
}

func TestUpForwardError(t *testing.T) {
	e, _ := env(t, &fakeTr{err: errors.New("boom")})
	e.Config.Policy.AllowServices = true
	e.Config.Services = []config.Service{{Name: "pg", RemotePort: 5432, LocalPort: freeLocalPort()}}
	if err := upCmd(context.Background(), e, nil); err == nil {
		t.Fatal("it should fail")
	}
}

func TestAddRmCommandsPersist(t *testing.T) {
	e, _ := env(t, nil)
	if err := addCmd(context.Background(), e, []string{"redis", "-local", "16379"}); err != nil {
		t.Fatal(err)
	}
	c, _ := config.Load()
	if len(c.Services) != 1 || c.Services[0].LocalPort != 16379 || c.Services[0].RemotePort != 6379 {
		t.Fatalf("%+v", c.Services)
	}
	if err := rmCmd(context.Background(), e, []string{"redis"}); err != nil {
		t.Fatal(err)
	}
	c, _ = config.Load()
	if len(c.Services) != 0 {
		t.Fatal("it was not removed")
	}
}
