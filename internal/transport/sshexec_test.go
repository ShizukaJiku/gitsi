package transport

import (
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
	got := strings.Join(s.fetchArgs("C:/x/gitsi.exe", "dst"), " ")
	for _, want := range []string{"BatchMode=yes", "IdentitiesOnly=yes", "-i k", "-P 2200", "u@h:C:/x/gitsi.exe dst"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, " -p ") {
		t.Error("scp uses -P, not -p")
	}
}
