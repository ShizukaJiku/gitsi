package doctor

import (
	"context"
	"strings"
	"testing"
)

// hubWithSSHD simulates the hub: the sshd service running, the firewall rule and, for "Get-Process", the given sshd PIDs.
func hubWithSSHD(netstat, sshdPIDs string) Deps {
	d := deps(nil, netstat)
	d.Output = func(_ context.Context, name string, args ...string) (string, error) {
		if name != "powershell" {
			return netstat, nil
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "Get-Service"):
			return "Running\r\n", nil
		case strings.Contains(script, "Get-Process"):
			return sshdPIDs, nil
		}
		return "True\r\n", nil
	}
	return d
}

// `setup hub` accepts -port, so the hub's diagnosis must not require port 22: a hub on another port must not give a false FAIL.
func TestHubSSHOnAnotherPortIsNotAFailure(t *testing.T) {
	netstat := `
  TCP    0.0.0.0:2222           0.0.0.0:0              LISTENING       4321
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1100
`
	rs := HubChecks(context.Background(), cfg(), hubWithSSHD(netstat, "4321\r\n"))
	var row Result
	for _, r := range rs {
		if r.Name == "sshd" {
			row = r
		}
	}
	if row.Level != Pass || !strings.Contains(row.Detail, "2222") {
		t.Fatalf("sshd listens on 2222: it must be OK and say the port: %+v", rs)
	}
}

// Another program on 22 is not sshd: when the sshd service runs but its process does not listen, it is a failure.
func TestHubPort22HeldBySomethingElseIsNotSSHD(t *testing.T) {
	netstat := `
  TCP    0.0.0.0:22             0.0.0.0:0              LISTENING       999
`
	rs := HubChecks(context.Background(), cfg(), hubWithSSHD(netstat, "4321\r\n"))
	if levels(rs)["sshd"] != Fail {
		t.Fatalf("port 22 belongs to PID 999, not to sshd (4321): %+v", rs)
	}
}

// Any 127.x.x.x is loopback (not only 127.0.0.1): it is not an exposed port.
func TestAny127AddressIsLoopback(t *testing.T) {
	ls := ParseListeners("  TCP    127.0.0.2:9000         0.0.0.0:0              LISTENING       5\n  TCP    0.0.0.0:9001           0.0.0.0:0              LISTENING       5\n")
	if len(ls) != 2 || !ls[0].Loopback() || ls[1].Loopback() {
		t.Fatalf("%+v", ls)
	}
}

func TestParseListenersIgnoresNoiseAndBadLines(t *testing.T) {
	in := "TCP 1.2.3.4\nUDP 0.0.0.0:53 *:* 5\nTCP    0.0.0.0:abc 0.0.0.0:0 LISTENING 5\nTCP    nocolon 0.0.0.0:0 LISTENING 5\n\n"
	if ls := ParseListeners(in); len(ls) != 0 {
		t.Fatalf("invalid lines are not listening sockets: %+v", ls)
	}
}

func TestToolCheckMissingTool(t *testing.T) {
	d := deps(nil, "")
	d.LookPath = func(string) (string, error) { return "", context.DeadlineExceeded }
	var rs []Result
	toolChecks(context.Background(), d, &rs, "git")
	if levels(rs)["tool git"] != Fail {
		t.Fatalf("%+v", rs)
	}
}

func TestListenerChecksWhenNetstatCannotRun(t *testing.T) {
	d := deps(nil, "")
	d.Output = func(context.Context, string, ...string) (string, error) { return "", context.Canceled }
	var rs []Result
	listenerChecks(context.Background(), d, &rs, nil, false)
	if len(rs) != 1 || rs[0].Level != Warn {
		t.Fatalf("without netstat it warns and claims nothing: %+v", rs)
	}
}

func TestSystemDepsAreWired(t *testing.T) {
	d := SystemDeps()
	if d.LookPath == nil || d.Output == nil || d.Dial == nil || d.Interfaces == nil || d.FileExists == nil {
		t.Fatalf("dependencies are missing: %+v", d)
	}
	if d.FileExists("Z:\\does\\not\\exist") {
		t.Error("a missing path does not exist")
	}
	if err := d.Dial("127.0.0.1:1", 200_000_000); err == nil {
		t.Error("nothing listens on 127.0.0.1:1")
	}
}

func TestFreePortGivesAUsablePort(t *testing.T) {
	p, err := freePort()
	if err != nil || p < 1 || p > 65535 {
		t.Fatalf("%d %v", p, err)
	}
}

func TestFieldExtractsKeyValue(t *testing.T) {
	if field("ps=5.1 git=True repos=False", "git") != "True" || field("ps=5.1", "git") != "" || field("xgit=1", "git") != "" {
		t.Fatal("field")
	}
}
