package doctor

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

const netstatSample = `
Conexiones activas

  Proto  Direccion local         Direccion remota       Estado           PID
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1100
  TCP    0.0.0.0:22             0.0.0.0:0              ESCUCHANDO      900
  TCP    127.0.0.1:5432         0.0.0.0:0              LISTENING       2200
  TCP    [::]:445               [::]:0                 LISTENING       4
  TCP    [::1]:8080             [::]:0                 LISTENING       3300
  TCP    192.168.1.7:50000     52.1.2.3:443           ESTABLISHED     777
`

func TestParseListeners(t *testing.T) {
	ls := ParseListeners(netstatSample)
	if len(ls) != 5 {
		t.Fatalf("expected 5 listening (not the established connection), got %d: %+v", len(ls), ls)
	}
	if !ls[2].Loopback() || ls[2].Port != 5432 || ls[0].Loopback() {
		t.Fatalf("loopback misdetected: %+v", ls)
	}
	if !ls[4].Loopback() || ls[4].Addr != "::1" {
		t.Fatalf("ipv6 loopback: %+v", ls[4])
	}
}

func TestVPNAdapters(t *testing.T) {
	v := VPNAdapters([]string{"Ethernet", "Example VPN Client Connection", "Wi-Fi"})
	if len(v) != 1 {
		t.Fatalf("%v", v)
	}
}

type fakeTr struct {
	out  map[string]string
	fwdE error
}

func (f fakeTr) Run(_ context.Context, c string) (transport.Result, error) {
	if c == "echo gitsi-ok" {
		return transport.Result{Stdout: "gitsi-ok\r\n"}, nil
	}
	return transport.Result{Stdout: f.out["probe"]}, nil
}
func (f fakeTr) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (f fakeTr) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, f.fwdE
}

func deps(dialErr error, netstat string) Deps {
	return Deps{
		LookPath: func(s string) (string, error) {
			if s == "git" {
				return `C:\bin\git.exe`, nil
			}
			return `C:\Windows\System32\OpenSSH\` + s + ".exe", nil
		},
		Output:     func(context.Context, string, ...string) (string, error) { return netstat, nil },
		Dial:       func(string, time.Duration) error { return dialErr },
		Interfaces: func() []string { return []string{"Ethernet"} },
		FileExists: func(string) bool { return true },
	}
}

func cfg() *config.Config {
	c := config.Default()
	c.Hub = config.Hub{Host: "192.0.2.10", User: "user"}
	return c
}

// annLayout is the layout the hub announces in the tests (the edge does not deduce the hub's paths).
func annLayout(context.Context) (layout.Layout, error) {
	return layout.ForHub(&config.Config{ReposDir: "C:/Users/user/repos"}, "C:/Users/user"), nil
}

func levels(rs []Result) map[string]Level {
	m := map[string]Level{}
	for _, r := range rs {
		m[r.Name] = r.Level
	}
	return m
}

func TestEdgeAllGood(t *testing.T) {
	tr := fakeTr{out: map[string]string{"probe": "ps=5.1.26100 git=True repos=True"}}
	rs := EdgeChecks(context.Background(), cfg(), tr, deps(nil, netstatSample), Options{Layout: annLayout})
	for _, r := range rs {
		if r.Level == Fail || r.Level == Warn {
			t.Errorf("it should not fail: %+v", r)
		}
	}
	var out bytes.Buffer
	if err := report(&out, rs, Mode{}); err != nil || !strings.Contains(out.String(), "0 failures") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestEdgeUnreachableStopsEarly(t *testing.T) {
	rs := EdgeChecks(context.Background(), cfg(), fakeTr{}, deps(errors.New("timeout"), netstatSample), Options{Layout: annLayout})
	if levels(rs)["TCP to the hub"] != Fail {
		t.Fatalf("%+v", rs)
	}
	if _, ok := levels(rs)["SSH login"]; ok {
		t.Fatal("it must not go on with SSH when TCP fails")
	}
	if report(&bytes.Buffer{}, rs, Mode{}) == nil {
		t.Fatal("report must return an error with FAIL")
	}
}

func TestEdgeCmdShellAndMissingGit(t *testing.T) {
	tr := fakeTr{out: map[string]string{"probe": "'ps' is not recognized"}}
	if levels(EdgeChecks(context.Background(), cfg(), tr, deps(nil, ""), Options{Layout: annLayout}))["hub shell"] != Fail {
		t.Fatal("it should detect a shell that is not PowerShell")
	}
	tr = fakeTr{out: map[string]string{"probe": "ps=5.1 git=False repos=False"}}
	l := levels(EdgeChecks(context.Background(), cfg(), tr, deps(nil, ""), Options{Layout: annLayout}))
	if l["git on the hub"] != Fail || l["repos folder"] != Info {
		t.Fatalf("%v", l)
	}
}

// Principle (the edge exposes nothing): a local tunnel port exposed outside loopback is a failure.
func TestServicePortMustBeLoopback(t *testing.T) {
	c := cfg()
	c.Services = []config.Service{{Name: "pg", RemotePort: 5432, LocalPort: 135}}
	tr := fakeTr{out: map[string]string{"probe": "ps=5.1 git=True repos=True"}}
	rs := EdgeChecks(context.Background(), c, tr, deps(nil, netstatSample), Options{Layout: annLayout})
	if levels(rs)["local tunnel :135"] != Fail {
		t.Fatalf("%+v", rs)
	}
}

func TestWarnsWhenSSHIsNotWindowsOpenSSH(t *testing.T) {
	d := deps(nil, "")
	d.LookPath = func(s string) (string, error) { return `C:\Program Files\Git\usr\bin\` + s + ".exe", nil }
	var rs []Result
	toolChecks(context.Background(), d, &rs, "ssh", "git")
	l := levels(rs)
	if l["tool ssh"] != Warn || l["tool git"] != Pass {
		t.Fatalf("%+v", rs)
	}
}

func TestEdgeNoConfig(t *testing.T) {
	rs := EdgeChecks(context.Background(), config.Default(), nil, deps(nil, ""), Options{Layout: annLayout})
	if levels(rs)["configuration"] != Fail {
		t.Fatalf("%+v", rs)
	}
}

// Enabling an optional feature is a legitimate user decision: it is reported, not flagged as a problem.
func TestOptionalFeaturesAreReportedAsInfoNotWarning(t *testing.T) {
	tr := fakeTr{out: map[string]string{"probe": "ps=5.1 git=True repos=True"}}
	if got := levels(EdgeChecks(context.Background(), cfg(), tr, deps(nil, ""), Options{Layout: annLayout}))["optional features"]; got != Pass {
		t.Fatalf("all off: it must be OK, it was %v", got)
	}
	c := cfg()
	c.Policy.AllowExec = true
	rs := EdgeChecks(context.Background(), c, tr, deps(nil, ""), Options{Layout: annLayout})
	if levels(rs)["optional features"] != Info {
		t.Fatalf("enabled: it must be INFO, not a warning: %+v", rs)
	}
	for _, r := range rs {
		if strings.Contains(strings.ToLower(r.Detail), "approval") || strings.Contains(strings.ToLower(r.Detail), "authoriz") {
			t.Errorf("the diagnosis must not ask for outside permission: %+v", r)
		}
	}
}

func TestHubChecks(t *testing.T) {
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
	rs := HubChecks(context.Background(), cfg(), d)
	l := levels(rs)
	if l["sshd service"] != Pass || l["firewall"] != Pass || l["sshd"] != Pass {
		t.Fatalf("%+v", rs)
	}
}

type streamTr struct {
	fakeTr
	works bool
}

func (s streamTr) Stream(_ context.Context, _ string) (transport.Stream, error) {
	return nil, errors.New("no process")
}

func TestServeCheckReportsFailureWithAdvice(t *testing.T) {
	var rs []Result
	serveCheck(context.Background(), cfg(), streamTr{}, &rs)
	if len(rs) != 1 || rs[0].Level != Fail || rs[0].Name != "hub protocol" {
		t.Fatalf("%+v", rs)
	}
}

// The diagnosis reports the layout the hub ANNOUNCES (not the edge's), and uses that path to check the repos folder.
func TestEdgeReportsTheLayoutTheHubAnnouncesAndProbesThatPath(t *testing.T) {
	c := cfg()
	c.ReposDir = "" // the edge configures no hub path
	var probed string
	tr := &probeTr{fakeTr: fakeTr{out: map[string]string{"probe": "ps=5.1 git=True repos=True"}}, onRun: func(s string) {
		if strings.Contains(s, "Test-Path") {
			probed = s
		}
	}}
	lay := layout.Layout{Home: "D:/h", Repos: "D:/my/repos", Binary: "D:/h/gitsi.exe", Mailbox: "D:/h/mailbox", Jobs: "D:/h/jobs"}
	rs := EdgeChecks(context.Background(), c, tr, deps(nil, ""), Options{Layout: func(context.Context) (layout.Layout, error) { return lay, nil }})
	var row Result
	for _, r := range rs {
		if r.Name == "hub layout" {
			row = r
		}
	}
	if row.Level != Pass || !strings.Contains(row.Detail, "D:/my/repos") || !strings.Contains(row.Detail, "D:/h/mailbox") {
		t.Fatalf("%+v", row)
	}
	if !strings.Contains(probed, "Test-Path 'D:/my/repos'") {
		t.Fatalf("the repos folder is checked at the announced path: %q", probed)
	}
	if levels(rs)["repos folder"] != Pass {
		t.Fatalf("%+v", rs)
	}
}

// When the layout cannot be learned, the diagnosis says so with the cause and action, without inventing paths.
func TestEdgeReportsWhenTheLayoutCannotBeKnown(t *testing.T) {
	c := cfg()
	c.ReposDir = ""
	tr := fakeTr{out: map[string]string{"probe": "ps=5.1 git=True repos=?"}}
	rs := EdgeChecks(context.Background(), c, tr, deps(nil, ""), Options{Layout: func(context.Context) (layout.Layout, error) {
		return layout.Layout{}, fault.New(fault.HubBinary, "the hub's gitsi is older", "on the hub: gitsi update -publish")
	}})
	for _, r := range rs {
		if r.Name == "hub layout" {
			if r.Level != Warn || !strings.Contains(r.Detail, "gitsi update -publish") {
				t.Fatalf("%+v", r)
			}
			return
		}
	}
	t.Fatal("the layout row is missing")
}

type probeTr struct {
	fakeTr
	onRun func(string)
}

func (p *probeTr) Run(ctx context.Context, c string) (transport.Result, error) {
	p.onRun(c)
	return p.fakeTr.Run(ctx, c)
}
