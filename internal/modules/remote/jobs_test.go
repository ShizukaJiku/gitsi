package remote

import (
	"context"
	"encoding/base64"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

func decodePS(t *testing.T, enc string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func TestEncodePSRoundTrip(t *testing.T) {
	in := "Write-Output 'ñandú'; exit 3"
	if got := decodePS(t, encodePS(in)); got != in {
		t.Fatalf("%q", got)
	}
}

func TestStartScriptIsSafeAndAbortsOnError(t *testing.T) {
	cmd := "echo 'hello'; $x = \"with quotes\""
	s := startScript("C:/Users/user/gitsi/jobs", "abcd1234", "C:/work", cmd)
	// 1) on any error it must abort before reporting STARTED (in a real test, New-Item failed silently)
	if !strings.HasPrefix(s, "$ErrorActionPreference = 'Stop'") {
		t.Error("the script must abort on the first error")
	}
	// 2) New-Item does not accept -LiteralPath in PowerShell 5.1
	if strings.Contains(s, "New-Item") {
		t.Error("do not use New-Item -LiteralPath (it does not exist in PowerShell 5.1)")
	}
	// 3) the user's command never appears in clear on the command line: it goes encoded
	if strings.Contains(s, "with quotes") || strings.Contains(s, "echo 'hello'") {
		t.Error("the command must travel encoded, not in clear")
	}
	// 4) it is started through WMI (outside the SSH session's process tree)
	if !strings.Contains(s, "Win32_Process") || !strings.Contains(s, "STARTED|abcd1234|") {
		t.Error("it must be started with WMI and report the id")
	}
	// 5) the encoded command includes cwd and the exit code logic
	i := strings.Index(s, "cmd.enc\" -Value '") + len("cmd.enc\" -Value '")
	j := strings.Index(s[i:], "'")
	inner := decodePS(t, s[i:i+j])
	for _, want := range []string{"Set-Location -LiteralPath 'C:/work'", cmd, "$LASTEXITCODE"} {
		if !strings.Contains(inner, want) {
			t.Errorf("missing %q in the encoded command: %q", want, inner)
		}
	}
	if strings.Contains(s, "\n") {
		t.Error("a single line: it travels as an ssh argument")
	}
}

func TestExeName(t *testing.T) {
	cases := map[string]string{"docker compose up": "docker", "C:\\x\\a.exe -v": "C:\\x\\a.exe", "$(evil) x": "evil", "": "?", ";;; x": "?"}
	for in, want := range cases {
		if got := exeName(in); got != want {
			t.Errorf("exeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseJobsAndLogs(t *testing.T) {
	jobs := parseJobs("JOB|aaaaaaaa|running||2026-11-16T22:00:00-05:00|docker\r\nJOB|bbbbbbbb|exited|5|2026-11-16T21:00:00-05:00|make\r\nnoise\r\n")
	// sorted by start: make (21:00) before docker (22:00)
	if len(jobs) != 2 || jobs[1].State != "running" || jobs[0].Exit != "5" || jobs[0].Exe != "make" {
		t.Fatalf("%+v", jobs)
	}
	c, err := parseLogs("STATE|exited|5|11\r\nDATA|" + base64.StdEncoding.EncodeToString([]byte("hello\nend")) + "\r\n")
	if err != nil || c.State != "exited" || c.Exit != "5" || c.Offset != 11 || string(c.Data) != "hello\nend" {
		t.Fatalf("%+v %v", c, err)
	}
	if c, _ := parseLogs("NOJOB\r\n"); !c.NoJob {
		t.Error("NOJOB")
	}
	if _, err := parseLogs("garbage"); err == nil {
		t.Error("an unexpected answer must fail")
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\n", 2); got != "b\nc\n" {
		t.Fatalf("%q", got)
	}
	if got := lastLines("a\nb", 5); got != "a\nb" {
		t.Fatalf("%q", got)
	}
}

func TestIDValidation(t *testing.T) {
	for _, ok := range []string{"abcd1234", "00000000"} {
		if checkID(ok) != nil {
			t.Errorf("%s valid", ok)
		}
	}
	for _, bad := range []string{"", "abc", "ABCD1234", "abcd123g", "../../x", "abcd1234; calc"} {
		if checkID(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

type runTr struct {
	cmds []string
	out  string
}

func (r *runTr) Run(_ context.Context, c string) (transport.Result, error) {
	r.cmds = append(r.cmds, c)
	return transport.Result{Stdout: r.out}, nil
}
func (r *runTr) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (r *runTr) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}

func envWith(t *testing.T, tr transport.Transport) *module.Env {
	e, _ := env(t, tr)
	e.LayoutFn = func(context.Context) (layout.Layout, error) {
		return layout.ForHub(&config.Config{ReposDir: "C:/Users/user/repos"}, "C:/Users/user"), nil
	}
	return e
}

func TestRunBGReportsFailureWhenHubDoesNotConfirm(t *testing.T) {
	tr := &runTr{out: "Exception: something failed"}
	e := envWith(t, tr)
	if err := runBG(context.Background(), e, "", "echo hola"); err == nil || !strings.Contains(err.Error(), "could not start") {
		t.Fatalf("without STARTED it must fail: %v", err)
	}
}

func TestRunBGAndLimits(t *testing.T) {
	tr := &runTr{}
	e := envWith(t, tr)
	tr.out = "" // filled with the real id after generating the script
	if err := runBG(context.Background(), e, "", strings.Repeat("x", maxCommandLen+1)); err == nil {
		t.Error("a command that is too long must be rejected")
	}
	if len(tr.cmds) != 0 {
		t.Error("it must not contact the hub")
	}
	e.LayoutFn = nil
	if err := runBG(context.Background(), e, "", "x"); err == nil {
		t.Error("without the hub's layout")
	}
}

func TestJobsCommandsDeclarePolicy(t *testing.T) {
	for _, c := range New(nil).Commands() {
		switch c.Name {
		case "run", "jobs", "logs", "stop":
			if c.Feature != policy.Exec || !c.NeedsHub || c.Roles != module.Edge || c.Hidden {
				t.Errorf("%s: %+v", c.Name, c)
			}
		}
	}
}

// The jobs folder is announced by the hub (its layout), not deduced from anything the edge configures.
func TestJobsDirComesFromTheHubLayout(t *testing.T) {
	e, _ := env(t, &runTr{})
	e.LayoutFn = func(context.Context) (layout.Layout, error) {
		return layout.Layout{Home: "D:/h", Repos: "D:/r", Binary: "D:/h/gitsi.exe", Mailbox: "D:/h/mailbox", Jobs: "D:/h/my-jobs"}, nil
	}
	got, err := jobsDirOf(context.Background(), e)
	if err != nil || got != "D:/h/my-jobs" {
		t.Fatalf("%q %v", got, err)
	}
}
