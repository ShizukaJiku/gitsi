package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

type fakeEx struct {
	cmd  string
	code int
	err  error
}

func (f *fakeEx) Run(context.Context, string) (transport.Result, error) {
	return transport.Result{}, nil
}
func (f *fakeEx) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (f *fakeEx) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}
func (f *fakeEx) Exec(_ context.Context, c string, out, _ io.Writer) (int, error) {
	f.cmd = c
	out.Write([]byte("live output\n"))
	return f.code, f.err
}

func env(t *testing.T, tr transport.Transport) (*module.Env, *bytes.Buffer) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	return &module.Env{Stdout: &out, Stderr: &out, Config: config.Default(), Transport: tr}, &out
}

func TestRunWrapsCommandAndStreamsOutput(t *testing.T) {
	f := &fakeEx{}
	e, out := env(t, f)
	if err := run(context.Background(), e, []string{"-cwd", "C:/Users/user/app", "docker", "compose", "up", "-d"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Set-Location -LiteralPath 'C:/Users/user/app'", "docker compose up -d", "$LASTEXITCODE"} {
		if !strings.Contains(f.cmd, want) {
			t.Errorf("missing %q in %q", want, f.cmd)
		}
	}
	if !strings.Contains(out.String(), "live output") {
		t.Fatal("the remote output must reach stdout")
	}
}

func TestRunPropagatesExitCode(t *testing.T) {
	e, _ := env(t, &fakeEx{code: 7})
	err := run(context.Background(), e, []string{"exit", "7"})
	var xe *module.ExitError
	if !errors.As(err, &xe) || xe.Code != 7 {
		t.Fatalf("%v", err)
	}
}

func TestRunValidation(t *testing.T) {
	e, _ := env(t, &fakeEx{})
	if err := run(context.Background(), e, nil); err == nil {
		t.Error("without a command")
	}
	if err := run(context.Background(), e, []string{"-cwd", "C:/x'; calc; '", "ls"}); err == nil {
		t.Error("a cwd with quotes must not reach the remote shell")
	}
	e2, _ := env(t, &fakeEx{err: errors.New("ssh broken")})
	if err := run(context.Background(), e2, []string{"ls"}); err == nil || !strings.Contains(err.Error(), "ssh") {
		t.Errorf("%v", err)
	}
}

func TestRunDeclaresExecPolicy(t *testing.T) {
	for _, c := range New(nil).Commands() {
		if c.Name == "run" && (c.Feature != policy.Exec || !c.NeedsHub || c.Roles != module.Edge) {
			t.Fatalf("%+v", c)
		}
	}
}

func TestAuditDoesNotRecordArguments(t *testing.T) {
	f := &fakeEx{}
	e, _ := env(t, f)
	e.Config.Policy.Audit = true
	if err := run(context.Background(), e, []string{"curl", "-H", "Authorization: Bearer SECRET", "http://x"}); err != nil {
		t.Fatal(err)
	}
	home, _ := config.Path()
	b, _ := readAll(home[:len(home)-len("config.json")] + "audit.log")
	if !strings.Contains(b, "exe=curl") || strings.Contains(b, "SECRET") {
		t.Fatalf("the audit only records the program, not the arguments: %q", b)
	}
}

func readAll(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}
