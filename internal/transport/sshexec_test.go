package transport

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Invariant (the edge exposes nothing): tunnels always bind to 127.0.0.1 and never use -R or GatewayPorts.
func TestForwardArgsLoopbackOnly(t *testing.T) {
	s := &SSHExec{Host: "192.0.2.10", User: "user", KeyFile: "k"}
	args, err := s.forwardArgs([]Forward{{LocalPort: 5432, RemotePort: 5432}, {LocalPort: 6379, RemoteHost: "127.0.0.1", RemotePort: 6379}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, bad := range []string{" -R", "-g", "GatewayPorts", "0.0.0.0", "-D"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("forbidden argument %q in: %s", bad, joined)
		}
	}
	if !strings.Contains(joined, "-L 127.0.0.1:5432:127.0.0.1:5432") || !strings.Contains(joined, "-L 127.0.0.1:6379:127.0.0.1:6379") {
		t.Fatalf("the expected -L are missing: %s", joined)
	}
	if args[len(args)-1] != "user@192.0.2.10" {
		t.Fatalf("the target must go last: %v", args)
	}
}

func TestForwardArgsValidation(t *testing.T) {
	s := &SSHExec{Host: "h", User: "u"}
	if _, err := s.forwardArgs(nil); err == nil {
		t.Fatal("no tunnels must fail")
	}
	if _, err := s.forwardArgs([]Forward{{LocalPort: 0, RemotePort: 1}}); err == nil {
		t.Fatal("an invalid local port must fail")
	}
}

func TestRunArgs(t *testing.T) {
	s := &SSHExec{Host: "h", User: "u", Port: 2222, KeyFile: "key"}
	a := strings.Join(s.runArgs("echo hi"), " ")
	for _, want := range []string{"BatchMode=yes", "-i key", "-p 2222", "u@h", "echo hi"} {
		if !strings.Contains(a, want) {
			t.Fatalf("missing %q in %s", want, a)
		}
	}
}

func TestFetchArgsShareBaseOptionsAndUseScpPortFlag(t *testing.T) {
	s := &SSHExec{Host: "h", User: "u", Port: 2200, KeyFile: "k"}
	got := strings.Join(s.fetchArgs("C:/x/gitsi.exe", "dst", false), " ")
	for _, want := range []string{"BatchMode=yes", "IdentitiesOnly=yes", "-i k", "-P 2200", "u@h:C:/x/gitsi.exe dst"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, " -p ") {
		t.Error("scp uses -P, not -p")
	}
}

// Second barrier against a user or host starting with a dash: "--" right before the target, in ssh, tunnels and scp.
func TestTargetAlwaysFollowsEndOfOptions(t *testing.T) {
	s := &SSHExec{Host: "h", User: "-oProxyCommand", KeyFile: "k"}
	fw, err := s.forwardArgs([]Forward{{LocalPort: 1, RemotePort: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		args   []string
		target string
	}{
		"run":     {s.runArgs("echo hi"), "-oProxyCommand@h"},
		"forward": {fw, "-oProxyCommand@h"},
		"fetch":   {s.fetchArgs("C:/x", "dst", false), "-oProxyCommand@h:C:/x"},
	} {
		i := indexOf(c.args, c.target)
		if i < 1 || c.args[i-1] != "--" {
			t.Errorf("%s: the target must come right after --: %q", name, c.args)
		}
		if j := indexOf(c.args, "--"); j != i-1 {
			t.Errorf("%s: a single -- and no option after it: %q", name, c.args)
		}
	}
}

func indexOf(a []string, v string) int {
	for i, x := range a {
		if x == v {
			return i
		}
	}
	return -1
}

func TestLegacySCP(t *testing.T) {
	for v, want := range map[string]bool{
		"OpenSSH_for_Windows_8.1p1, LibreSSL 3.0.2": true,
		"OpenSSH_for_Windows_7.7p1, LibreSSL 2.6.5": true,
		"OpenSSH_8.9p1 Ubuntu-3, OpenSSL 3.0.2":     true,
		"OpenSSH_for_Windows_9.5p2, LibreSSL 3.8.2": false,
		"OpenSSH_10.0p2, OpenSSL 3.5.0":             false,
		"":                                          false, // unknown: modern (current behavior)
		"some other ssh 1.0":                        false,
	} {
		if got := legacySCP(v); got != want {
			t.Errorf("%q => %v, want %v", v, got, want)
		}
	}
}

// Old scp sends the remote path through the hub's shell: a path with spaces must be quoted there, and only there (in SFTP mode
// the quotes would become part of the name).
func TestFetchArgsQuotesPathsWithSpacesOnlyForLegacySCP(t *testing.T) {
	s := &SSHExec{Host: "h", User: "u"}
	remote := "C:/Users/Juan Perez/gitsi/gitsi.exe"
	if a := s.fetchArgs(remote, "dst", true); indexOf(a, `u@h:"`+remote+`"`) < 0 {
		t.Errorf("legacy: not quoted: %q", a)
	}
	if a := s.fetchArgs(remote, "dst", false); indexOf(a, "u@h:"+remote) < 0 {
		t.Errorf("sftp: it must go as is: %q", a)
	}
	if a := s.fetchArgs("C:/x/gitsi.exe", "dst", true); indexOf(a, "u@h:C:/x/gitsi.exe") < 0 {
		t.Errorf("legacy without spaces: unchanged: %q", a)
	}
}

// End to end with a fake scp that records its arguments: the version is only asked when the path has spaces, and it decides the
// quoting.
func TestFetchAsksTheVersionOnlyForPathsWithSpaces(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses a .cmd")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	scp := filepath.Join(dir, "fake-scp.cmd")
	if err := os.WriteFile(scp, []byte("@echo off\r\necho %*> \""+argsFile+"\"\r\nexit /b 0\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	version, asked := "", 0
	old := sshVersion
	sshVersion = func(context.Context, string) string { asked++; return version }
	t.Cleanup(func() { sshVersion = old })

	s := &SSHExec{Host: "h", User: "u", SCPBinary: scp}
	fetch := func(remote string) string {
		t.Helper()
		if err := s.Fetch(context.Background(), remote, "dst"); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	if got := fetch("C:/x/gitsi.exe"); asked != 0 || !strings.Contains(got, "-- u@h:C:/x/gitsi.exe dst") {
		t.Fatalf("without spaces: asked=%d args=%s", asked, got)
	}
	// The .cmd sees the raw command line, as Go escapes it for Windows: scp's C runtime turns `"u@h:\"C:/a b/x\""` back into the
	// single argument u@h:"C:/a b/x".
	version = "OpenSSH_for_Windows_8.1p1, LibreSSL 3.0.2"
	if got := fetch("C:/a b/gitsi.exe"); asked != 1 || !strings.Contains(got, `-- "u@h:\"C:/a b/gitsi.exe\"" dst`) {
		t.Fatalf("legacy: asked=%d args=%s", asked, got)
	}
	version = "OpenSSH_for_Windows_9.5p2, LibreSSL 3.8.2"
	if got := fetch("C:/a b/gitsi.exe"); asked != 2 || !strings.Contains(got, `-- "u@h:C:/a b/gitsi.exe" dst`) {
		t.Fatalf("sftp: asked=%d args=%s", asked, got)
	}
}
