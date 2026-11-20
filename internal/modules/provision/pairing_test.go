package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

func TestPairingGuidanceGivesTheExactEdgeCommand(t *testing.T) {
	g := pairingGuidance(edFP, "192.0.2.3", "user")
	for _, want := range []string{"This hub's fingerprint: " + edFP, "gitsi setup edge -host 192.0.2.3 -user user -hostkey " + edFP} {
		if !strings.Contains(g, want) {
			t.Errorf("missing %q in:\n%s", want, g)
		}
	}
	// without a readable fingerprint, the command is still valid (without -hostkey)
	g = pairingGuidance("", "10.0.0.2", "u")
	if !strings.Contains(g, "gitsi setup edge -host 10.0.0.2 -user u") || strings.Contains(g, "-hostkey") {
		t.Errorf("%s", g)
	}
}

// edgeEnv prepares an isolated edge; the key is generated with the system's ssh-keygen (as in real use).
func edgeEnv(t *testing.T) (*module.Env, *bytes.Buffer, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	var out bytes.Buffer
	return &module.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out, Config: config.Default()}, &out, home
}

func withScanner(t *testing.T, s Scanner) {
	old := hostScanner
	hostScanner = s
	t.Cleanup(func() { hostScanner = old })
}

// A fingerprint that does not match stops BEFORE saving anything: neither configuration nor known_hosts.
func TestSetupEdgeRejectsWrongHostKeyBeforeSavingAnything(t *testing.T) {
	env, _, home := edgeEnv(t)
	line := "127.0.0.1 " + strings.Fields(edPub)[0] + " " + strings.Fields(edPub)[1]
	withScanner(t, scanner(line, nil))

	err := initCmd(context.Background(), env, []string{"-host", "127.0.0.1", "-port", "1", "-user", "u", "-hostkey", rsaFP})
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.HostKey {
		t.Fatalf("it should be HostKey: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gitsi", "config.json")); err == nil {
		t.Error("the configuration must not be saved when the fingerprint does not match")
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "known_hosts")); err == nil {
		t.Error("known_hosts must not be written")
	}
}

// With the right fingerprint it is pinned in known_hosts and it goes on; a network failure is reported with its category (not hidden).
func TestSetupEdgePinsHostKeyAndReportsUnreachableHub(t *testing.T) {
	env, out, home := edgeEnv(t)
	line := "127.0.0.1 " + strings.Fields(edPub)[0] + " " + strings.Fields(edPub)[1]
	withScanner(t, scanner(line, nil))

	err := initCmd(context.Background(), env, []string{"-host", "127.0.0.1", "-port", "1", "-user", "u", "-hostkey", edFP})
	if !strings.Contains(out.String(), "Hub fingerprint verified and pinned: "+edFP) {
		t.Fatalf("output:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts")); !strings.Contains(string(b), line) {
		t.Errorf("known_hosts: %q", b)
	}
	if _, serr := os.Stat(filepath.Join(home, ".gitsi", "config.json")); serr != nil {
		t.Error("with the fingerprint verified the configuration is saved")
	}
	// port 1: nobody listens -> the network failure comes out categorized and with its action, not as a pairing problem
	if fe, ok := fault.As(err); !ok || fe.Kind != fault.Unreachable {
		t.Fatalf("an unreachable hub must be reported as Unreachable, not as 'key not registered': %v\n%s", err, out)
	}
	if strings.Contains(out.String(), "key still has to be registered") {
		t.Errorf("a network failure must not be mistaken for an unregistered key:\n%s", out)
	}
}

// Without -hostkey the fingerprint is shown to compare it, and nothing is pinned.
func TestSetupEdgeWithoutHostKeyOnlyShowsFingerprint(t *testing.T) {
	env, out, home := edgeEnv(t)
	line := "127.0.0.1 " + strings.Fields(edPub)[0] + " " + strings.Fields(edPub)[1]
	withScanner(t, scanner(line, nil))
	_ = initCmd(context.Background(), env, []string{"-host", "127.0.0.1", "-port", "1", "-user", "u"})
	if !strings.Contains(out.String(), "Hub fingerprint: "+edFP) || !strings.Contains(out.String(), "-hostkey") {
		t.Fatalf("it must show the fingerprint and how to pin it:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "known_hosts")); err == nil {
		t.Error("without -hostkey nothing is pinned")
	}
}

func TestLocalIPToward(t *testing.T) {
	if ip := localIPToward("127.0.0.1", 22); ip != "127.0.0.1" {
		t.Fatalf("%q", ip)
	}
	if ip := localIPToward("host.invalid.local.", 22); !strings.HasPrefix(ip, "<") {
		t.Fatalf("without a route it must leave a placeholder, not invent: %q", ip)
	}
}
