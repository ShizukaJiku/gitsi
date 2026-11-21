package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

func statusEnv(tr transport.Transport) (*module.Env, *bytes.Buffer) {
	var out bytes.Buffer
	return &module.Env{Stdout: &out, Stderr: &out, Config: cfg(), Transport: tr}, &out
}

func TestReportModes(t *testing.T) {
	rs := []Result{{Name: "a", Level: Pass, Detail: "ok"}, {Name: "b", Level: Info, Detail: "i"}, {Name: "c", Level: Warn, Detail: "w"}}
	var b bytes.Buffer
	if err := report(&b, rs, Mode{Quiet: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "ok") || !strings.Contains(b.String(), "[") || !strings.Contains(b.String(), "1 warnings") {
		t.Fatalf("-q only shows warnings and failures:\n%s", b.String())
	}
	b.Reset()
	if err := report(&b, append(rs, Result{Name: "d", Level: Fail, Detail: "x"}), Mode{}); err == nil {
		t.Fatal("a FAIL must return an error")
	}
}

// useDeps replaces the machine's tools with the test's while the test lasts.
func useDeps(t *testing.T, d Deps) {
	t.Helper()
	old := systemDeps
	systemDeps = func() Deps { return d }
	t.Cleanup(func() { systemDeps = old })
}

func hubDeps() Deps {
	d := deps(nil, netstatSample)
	d.Output = func(_ context.Context, name string, args ...string) (string, error) {
		if name == "powershell" {
			if strings.Contains(args[len(args)-1], "Get-Service") {
				return "Running\r\n", nil
			}
			return "True\r\n", nil
		}
		return netstatSample, nil
	}
	return d
}

// On the hub, 'check' runs the hub's diagnosis (it needs no transport), also with --json and -q.
func TestCheckOnHub(t *testing.T) {
	ctx := context.Background()
	useDeps(t, hubDeps())
	e, out := statusEnv(nil)
	e.Config.Role = "hub"
	if err := check(ctx, e, nil); err != nil || !strings.Contains(out.String(), "sshd service") {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := check(ctx, e, []string{"-missing"}); err == nil {
		t.Fatal("an unknown flag must fail")
	}
	e, out = statusEnv(nil)
	e.Config.Role = "hub"
	e.JSON = true
	if err := check(ctx, e, nil); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Results []struct{ Level, Name string }
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || len(rep.Results) == 0 {
		t.Fatalf("--json must produce only valid JSON: %v\n%s", err, out)
	}
	e, out = statusEnv(nil)
	e.Config.Role = "hub"
	e.Quiet = true
	if err := check(ctx, e, nil); err != nil || strings.Contains(out.String(), "[OK") {
		t.Fatalf("-q does not show what is fine: %v\n%s", err, out)
	}
}

func TestVerifyCommandWritesTheReportFile(t *testing.T) {
	useDeps(t, deps(nil, netstatSample))
	tr := tunnelHub{newHubTr(t)}
	e, out := statusEnv(tr)
	e.Config = cfg()
	report := filepath.Join(t.TempDir(), "sub", "report.txt")
	if err := verify(context.Background(), e, []string{"-out", report}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b, err := os.ReadFile(report)
	if err != nil || !strings.Contains(string(b), "Summary") {
		t.Fatalf("the report must be saved (creating folders): %v %q", err, b)
	}
	if !strings.Contains(out.String(), "Report saved in "+report) || !strings.Contains(out.String(), "Review the report") {
		t.Fatalf("it must say where it is and to review it before sharing:\n%s", out)
	}

	// with -q only the report remains, without side notes
	e, out = statusEnv(tunnelHub{newHubTr(t)})
	e.Config, e.Quiet = cfg(), true
	if err := verify(context.Background(), e, []string{"-out", report}); err != nil || strings.Contains(out.String(), "Report saved") {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := verify(context.Background(), e, []string{"-missing"}); err == nil {
		t.Fatal("an unknown flag must fail")
	}
}

// tunnelHub is a hubTr whose Forward returns a tunnel that can be closed (verify tests the tunnel).
type tunnelHub struct{ hubTr }

type nopTunnel struct{}

func (nopTunnel) Wait() error  { return nil }
func (nopTunnel) Close() error { return nil }

func (tunnelHub) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nopTunnel{}, nil
}
