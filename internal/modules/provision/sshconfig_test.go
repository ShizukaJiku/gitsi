package provision

import (
	"strings"
	"testing"
)

func TestMergeSSHConfigKeepsOthersAndReplacesAManualBlock(t *testing.T) {
	existing := strings.Split("Host other\n    HostName x.y\n\nHost gitsi-hub\n    HostName stale\n    User stale\n\nHost final\n    HostName z", "\n")
	block := HostBlock("gitsi-hub", "192.0.2.3", "user", 0, `C:\Users\u\.ssh\k`)
	got := strings.Join(MergeSSHConfig(existing, "gitsi-hub", block), "\n")

	for _, want := range []string{"Host other", "HostName x.y", "Host final", "HostName z", "HostName 192.0.2.3", "IdentityFile C:/Users/u/.ssh/k"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "stale") {
		t.Errorf("the hand-written block was not removed:\n%s", got)
	}
	if strings.Count(got, "Host gitsi-hub") != 1 {
		t.Errorf("there must be a single gitsi-hub block:\n%s", got)
	}
}

// The block declares only the configured alias.
func TestHostBlockDeclaresOnlyTheConfiguredAlias(t *testing.T) {
	b := HostBlock("gitsi-hub", "h", "u", 0, "k")
	if b[1] != "Host gitsi-hub" {
		t.Fatalf("%q", b[1])
	}
}

func TestMergeSSHConfigIdempotent(t *testing.T) {
	block := HostBlock("gitsi-hub", "h", "u", 2222, "k")
	once := MergeSSHConfig(nil, "gitsi-hub", block)
	twice := MergeSSHConfig(once, "gitsi-hub", block)
	if strings.Join(once, "\n") != strings.Join(twice, "\n") {
		t.Fatalf("it is not idempotent:\n%v\n---\n%v", once, twice)
	}
	if !strings.Contains(strings.Join(once, "\n"), "Port 2222") {
		t.Fatal("the port is missing")
	}
}

func TestHostBlockHasNoReverseForwarding(t *testing.T) {
	b := strings.Join(HostBlock("a", "h", "u", 0, "k"), "\n")
	for _, bad := range []string{"RemoteForward", "ForwardAgent yes", "GatewayPorts"} {
		if strings.Contains(b, bad) {
			t.Fatalf("the block must not contain %q", bad)
		}
	}
}
