package transport

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/fault"
)

// fakeSSH creates a fake executable that writes `stderr` and exits with `code`: it tests classification on real processes without
// depending on the network. Windows only (it uses a .cmd).
func fakeSSH(t *testing.T, stderr string, code int) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("uses a .cmd")
	}
	p := filepath.Join(t.TempDir(), "fake-ssh.cmd")
	script := "@echo off\r\necho " + stderr + " 1>&2\r\nexit /b " + itoa(code) + "\r\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func newSSH(bin string) *SSHExec {
	return &SSHExec{Host: "192.0.2.10", User: "u", KeyFile: "KEY", Binary: bin}
}

func TestConnectTimeoutIsAlwaysSet(t *testing.T) {
	s := newSSH("ssh")
	for name, args := range map[string][]string{"run": s.runArgs("x"), "fetch": s.fetchArgs("r", "l")} {
		if !strings.Contains(strings.Join(args, " "), "ConnectTimeout=10") {
			t.Errorf("%s: ConnectTimeout is missing (an unreachable hub would hang the command): %v", name, args)
		}
	}
	fw, _ := s.forwardArgs([]Forward{{LocalPort: 1, RemotePort: 2}})
	if !strings.Contains(strings.Join(fw, " "), "ConnectTimeout=10") {
		t.Error("tunnels too")
	}
}

func TestRunClassifiesSSHFailures(t *testing.T) {
	cases := map[string]fault.Kind{
		"Permission denied (publickey).":                                Auth(),
		"ssh: connect to host 192.0.2.10 port 22: Connection timed out": fault.Unreachable,
		"Host key verification failed.":                                 fault.HostKey,
	}
	for stderr, want := range cases {
		res, err := newSSH(fakeSSH(t, stderr, 255)).Run(context.Background(), "x")
		fe, ok := fault.As(err)
		if !ok || fe.Kind != want {
			t.Errorf("%q => %v (want %v)", stderr, err, want)
		}
		if res.ExitCode != 255 || !strings.Contains(res.Stderr, stderr[:10]) {
			t.Errorf("the raw output must be kept: %+v", res)
		}
	}
}

// Auth returns the category (a function to keep the table readable).
func Auth() fault.Kind { return fault.Auth }

// The exit code of the remote COMMAND is not an ssh failure: it is not classified even if its text looks alike.
func TestRunDoesNotClassifyRemoteCommandFailures(t *testing.T) {
	res, err := newSSH(fakeSSH(t, "Permission denied (publickey).", 1)).Run(context.Background(), "x")
	if err != nil || res.ExitCode != 1 {
		t.Fatalf("exit code 1 belongs to the remote command, not to ssh: %v %+v", err, res)
	}
	// and a 255 without recognizable text is returned as is (no invented cause)
	res, err = newSSH(fakeSSH(t, "something unknown", 255)).Run(context.Background(), "x")
	if err != nil || res.ExitCode != 255 {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestExecClassifiesButStillStreamsOutput(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := newSSH(fakeSSH(t, "Permission denied (publickey).", 255)).Exec(context.Background(), "x", &out, &errb)
	if fe, ok := fault.As(err); !ok || fe.Kind != fault.Auth || code != 255 {
		t.Fatalf("%v %d", err, code)
	}
	if !strings.Contains(errb.String(), "Permission denied") {
		t.Fatal("live stderr must reach the user")
	}
}

// Stream captures stderr: without it, a greeting that never arrives would only say "no answer".
func TestStreamExposesStderrAndFault(t *testing.T) {
	st, err := newSSH(fakeSSH(t, "Permission denied (publickey).", 255)).Stream(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d, ok := st.(StreamDiagnostics)
	if !ok {
		t.Fatal("the ssh stream must be a StreamDiagnostics")
	}
	deadline := time.Now().Add(3 * time.Second)
	for d.Stderr() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(d.Stderr(), "Permission denied") {
		t.Fatalf("stderr: %q", d.Stderr())
	}
	if fe := d.Fault(); fe == nil || fe.Kind != fault.Auth || !strings.Contains(fe.Hint, "KEY") {
		t.Fatalf("%+v", fe)
	}
}

func TestFetchClassifiesScpFailures(t *testing.T) {
	s := newSSH("ssh")
	s.SCPBinary = fakeSSH(t, "Permission denied (publickey).", 1)
	err := s.Fetch(context.Background(), "r", "l")
	if fe, ok := fault.As(err); !ok || fe.Kind != fault.Auth {
		t.Fatalf("%v", err)
	}
}

// The Trace hook receives one line per ssh/scp run, with the command shortened and the duration.
func TestTraceReportsCommandsAndTruncatesLongPayloads(t *testing.T) {
	var lines []string
	s := newSSH(fakeSSH(t, "", 0))
	s.Trace = func(l string) { lines = append(lines, l) }
	if _, err := s.Run(context.Background(), "echo hello"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("QUJD", 200) // a long base64 payload must not flood the output
	s.Run(context.Background(), "Set-Content "+long)
	if len(lines) != 2 || !strings.Contains(lines[0], "u@192.0.2.10: echo hello") || !strings.Contains(lines[0], "exit 0") || !strings.Contains(lines[0], " ms]") {
		t.Fatalf("%q", lines)
	}
	if len(lines[1]) > 260 || !strings.Contains(lines[1], "...") {
		t.Fatalf("the long command must be shortened (%d): %s", len(lines[1]), lines[1])
	}
	// without Trace nothing happens
	s.Trace = nil
	if _, err := s.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
}
