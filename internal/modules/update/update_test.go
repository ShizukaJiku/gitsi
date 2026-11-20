package update

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/install"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// Where the published binary is is decided by the hub (its Home), not by the edge: here the hub announces another folder.
func TestEdgeFetchesTheBinaryFromWhereTheHubSaysIt(t *testing.T) {
	env, _, self, ft := setup(t, "stale", "fresh")
	var asked string
	env.LayoutFn = func(context.Context) (layout.Layout, error) {
		return layout.Layout{Binary: "D:/other/place/gitsi.exe", Home: "D:/other/place", Repos: "D:/r", Mailbox: "D:/m", Jobs: "D:/j"}, nil
	}
	ft.onRun = func(c string) { asked = c }
	if err := apply(context.Background(), env, ft, self, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "D:/other/place/gitsi.exe") {
		t.Fatalf("it must ask for the hash where the hub says: %q", asked)
	}
}

type fakeT struct {
	hash    string
	content string
	code    int
	onRun   func(string)
	size    int64 // size the hub reports; 0 = content's
}

func (f *fakeT) Run(_ context.Context, c string) (transport.Result, error) {
	if f.onRun != nil {
		f.onRun(c)
	}
	out := strings.ToUpper(f.hash)
	if f.hash != "" {
		size := f.size
		if size == 0 {
			size = int64(len(f.content))
		}
		out += "|" + itoa64(size)
	}
	return transport.Result{Stdout: out + "\r\n", ExitCode: f.code}, nil
}
func (f *fakeT) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (f *fakeT) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}
func (f *fakeT) Fetch(_ context.Context, _, local string) error {
	return os.WriteFile(local, []byte(f.content), 0o755)
}

func setup(t *testing.T, cur, remote string) (*module.Env, *bytes.Buffer, string, *fakeT) {
	dir := t.TempDir()
	self := filepath.Join(dir, "gitsi.exe")
	if err := os.WriteFile(self, []byte(cur), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "x")
	if err := os.WriteFile(p, []byte(remote), 0o644); err != nil {
		t.Fatal(err)
	}
	h, _ := install.FileSHA256(p)
	ft := &fakeT{hash: h, content: remote}
	var out bytes.Buffer
	cfg := config.Default()
	env := &module.Env{Stdout: &out, Stderr: &out, Config: cfg, Transport: ft}
	env.LayoutFn = func(context.Context) (layout.Layout, error) {
		return layout.ForHub(&config.Config{ReposDir: "C:/Users/user/repos"}, "C:/Users/user"), nil
	}
	return env, &out, self, ft
}

func TestApplyReplaces(t *testing.T) {
	env, _, self, ft := setup(t, "stale", "fresh")
	if err := apply(context.Background(), env, ft, self, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(self); string(b) != "fresh" {
		t.Fatalf("it was not replaced: %q", b)
	}
	if _, err := os.Stat(self + ".new"); err == nil {
		t.Fatal("the temporary file remained")
	}
}

func TestApplyUpToDateAndCheck(t *testing.T) {
	env, out, self, ft := setup(t, "same", "same")
	if err := apply(context.Background(), env, ft, self, false); err != nil || !strings.Contains(out.String(), "up to date") {
		t.Fatalf("%v %s", err, out)
	}
	env, out, self, ft = setup(t, "stale", "fresh")
	if err := apply(context.Background(), env, ft, self, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(self); string(b) != "stale" || !strings.Contains(out.String(), "different version") {
		t.Fatal("-check must change nothing")
	}
}

func TestApplyRejectsHashMismatch(t *testing.T) {
	env, _, self, ft := setup(t, "stale", "fresh")
	ft.content = "tampered"
	if err := apply(context.Background(), env, ft, self, false); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("it should reject: %v", err)
	}
	if b, _ := os.ReadFile(self); string(b) != "stale" {
		t.Fatal("it was replaced with an unverified binary")
	}
}

func TestApplyNoPublishedBinary(t *testing.T) {
	env, _, self, ft := setup(t, "stale", "fresh")
	ft.code, ft.hash = 1, ""
	if err := apply(context.Background(), env, ft, self, false); err == nil {
		t.Fatal("it should fail")
	}
}

func TestRunRoleRules(t *testing.T) {
	ctx := context.Background()
	env, _, _, _ := setup(t, "stale", "fresh")

	env.Config.Role = config.RoleHub
	if err := run(ctx, env, nil); err == nil || !strings.Contains(err.Error(), "-publish") {
		t.Fatalf("on the hub, without -publish it must say how to publish: %v", err)
	}
	env.Config.Role = config.RoleEdge
	if err := run(ctx, env, []string{"-publish"}); err == nil || !strings.Contains(err.Error(), "only applies on the hub") {
		t.Fatalf("-publish on the edge must be rejected: %v", err)
	}
	if err := run(ctx, env, []string{"-missing"}); err == nil {
		t.Fatal("an unknown flag must fail")
	}
	env.Transport = noFetch{}
	if err := run(ctx, env, nil); err == nil || !strings.Contains(err.Error(), "download") {
		t.Fatalf("a transport without Fetch must be explained: %v", err)
	}
}

// noFetch is a Transport that cannot download files (it does not implement transport.Fetcher).
type noFetch struct{}

func (noFetch) Run(context.Context, string) (transport.Result, error)    { return transport.Result{}, nil }
func (noFetch) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (noFetch) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}

func TestPublishSelfInHub(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	env, out, _, _ := setup(t, "x", "x")
	env.Config.Role = config.RoleHub
	if err := run(context.Background(), env, []string{"-publish"}); err != nil {
		t.Fatal(err)
	}
	pub := filepath.Join(home, "gitsi", "gitsi.exe")
	if _, err := os.Stat(pub); err != nil {
		t.Fatalf("it was not published in the hub's home: %v\n%s", err, out)
	}
	self, _ := os.Executable()
	want, _ := install.FileSHA256(self)
	if got, _ := install.FileSHA256(pub); got != want || !strings.Contains(out.String(), want) {
		t.Fatalf("the published binary must be identical and announce its hash: %s", out)
	}
}

type failFetch struct{ *fakeT }

func (failFetch) Fetch(context.Context, string, string) error { return os.ErrPermission }

func TestApplyFetchFailureLeavesNothingBehind(t *testing.T) {
	env, _, self, ft := setup(t, "stale", "fresh")
	err := apply(context.Background(), env, failFetch{ft}, self, false)
	if err == nil {
		t.Fatal("a download failure must be returned")
	}
	if b, _ := os.ReadFile(self); string(b) != "stale" {
		t.Fatal("the executable must not change")
	}
	if _, err := os.Stat(self + ".new"); err == nil {
		t.Fatal("the temporary file must not remain")
	}
}

func TestReplaceExeRestoresOnFailure(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "gitsi.exe")
	if err := os.WriteFile(self, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	// the new one does not exist: the second rename fails and the first must be undone
	err := replaceExe(self, filepath.Join(dir, "missing.new"))
	if err == nil || !strings.Contains(err.Error(), "was restored") {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(self); string(b) != "current" {
		t.Fatalf("the executable should be restored: %q", b)
	}
	if _, err := os.Stat(self + ".old"); err == nil {
		t.Fatal("no .old may remain after restoring")
	}
}
