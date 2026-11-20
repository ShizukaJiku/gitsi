package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/fault"
)

// Keys and fingerprints generated with the real ssh-keygen: the computation is pinned against the official tool, not itself.
const (
	edPub  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ7yOY3adbu0KAPWo9Cnl/WVvQUl3/JeGOG/8AT/vIuQ test@gitsi"
	edFP   = "SHA256:8vbHZVfRNk2idCaeicvHIUMYoO6czo4BaVpGtrEXvFI"
	rsaFP  = "SHA256:gMk+IWnqQ7yJ6aH6N8TFlpuFkPrxKUhEWu9ZUe0CX3w"
	rsaPub = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDNWvO59H8RWndV67SGLBz/55uldsA+Gpx/8iujtXl2PzN1BvKw8Cua221OBUJfJoglVgQaoBckHtW2lo4zPwHcZcrKkTHcDvgIMnXh2fZFYiy+RPcINbaYVI0mNucAvt3PnOBEN0rJNJvxGoDBf8mSNvOUCo2iQzvnlOklEZ/U0RoOF3XDbXWvZ+X/ap0CNEQvryGOjBoQVhmQslD27jb0p3D+ckXok2kHKZN05B77kfhTEkegsV4q42S6PrvvAkBz4v1OvKAgwfPkehL51nsl36nKPELpBD5rv3EZXDtHp8jcua1g4XoNYdGU/AxhD4l51wSyFqmC1DXQBtmmpt59 rsa@gitsi"
)

func TestFingerprintMatchesSSHKeygen(t *testing.T) {
	for name, c := range map[string]struct{ line, want string }{
		"ed25519":            {edPub, edFP},
		"rsa":                {rsaPub, rsaFP},
		"ssh-keyscan format": {"192.0.2.3 " + strings.Fields(edPub)[0] + " " + strings.Fields(edPub)[1], edFP},
		"[host]:port format": {"[10.0.0.1]:2222 " + strings.Fields(edPub)[0] + " " + strings.Fields(edPub)[1], edFP},
		"no comment":         {strings.Fields(edPub)[0] + " " + strings.Fields(edPub)[1], edFP},
	} {
		got, err := Fingerprint(c.line)
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v (want %s)", name, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "hola", "ssh-ed25519", "ssh-ed25519 !!!no-base64!!!", "ssh-ed25519 "} {
		if _, err := Fingerprint(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	for in, want := range map[string]string{"SHA256:abc": "SHA256:abc", "abc": "SHA256:abc", "  abc  ": "SHA256:abc", "sha256:abc": "SHA256:abc", "": ""} {
		if got := NormalizeFingerprint(in); got != want {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
}

func scanner(line string, err error) Scanner {
	return func(context.Context, string, int) (string, error) { return line, err }
}

func TestVerifyHostKeyPinsWhenFingerprintMatches(t *testing.T) {
	kh := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	line := "192.0.2.3 " + strings.Fields(edPub)[0] + " " + strings.Fields(edPub)[1]
	res, err := VerifyHostKey(context.Background(), scanner(line, nil), "192.0.2.3", 0, edFP, kh)
	if err != nil || !res.Pinned || res.Fingerprint != edFP {
		t.Fatalf("%+v %v", res, err)
	}
	if b, _ := os.ReadFile(kh); !strings.Contains(string(b), line) {
		t.Fatalf("known_hosts: %q", b)
	}
	// repeating is idempotent
	if _, err := VerifyHostKey(context.Background(), scanner(line, nil), "192.0.2.3", 0, edFP, kh); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(kh); strings.Count(string(b), line) != 1 {
		t.Fatalf("it must not duplicate: %q", b)
	}
	// it accepts the fingerprint without the prefix
	if _, err := VerifyHostKey(context.Background(), scanner(line, nil), "192.0.2.3", 0, strings.TrimPrefix(edFP, "SHA256:"), kh); err != nil {
		t.Fatal(err)
	}
}

// A different fingerprint is a HostKey failure and nothing is written: precisely the "it is not the hub" case.
func TestVerifyHostKeyRejectsMismatchAndWritesNothing(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	line := "h " + strings.Fields(edPub)[0] + " " + strings.Fields(edPub)[1]
	res, err := VerifyHostKey(context.Background(), scanner(line, nil), "h", 0, rsaFP, kh)
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.HostKey || res.Pinned {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(fe.Msg, edFP) || !strings.Contains(fe.Msg, rsaFP) {
		t.Errorf("the message must show both fingerprints to compare them: %s", fe.Msg)
	}
	if _, err := os.Stat(kh); err == nil {
		t.Fatal("known_hosts must not be written on a different fingerprint")
	}
}

func TestVerifyHostKeyWithoutExpectedOnlyReports(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	line := "h " + strings.Fields(edPub)[0] + " " + strings.Fields(edPub)[1]
	res, err := VerifyHostKey(context.Background(), scanner(line, nil), "h", 0, "", kh)
	if err != nil || res.Pinned || res.Fingerprint != edFP {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(kh); err == nil {
		t.Fatal("without an expected fingerprint nothing is pinned")
	}
	if _, err := VerifyHostKey(context.Background(), scanner("", errors.New("no network")), "h", 0, edFP, kh); err == nil {
		t.Fatal("when the key cannot be obtained it must fail")
	}
}

func TestPinKnownHostsRefusesToOverwriteAnotherKey(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(kh, []byte("h ssh-ed25519 AAAAOTRAOTRAOTRA\n"), 0o600)
	err := PinKnownHosts(kh, "h "+strings.Fields(edPub)[0]+" "+strings.Fields(edPub)[1])
	if fe, ok := fault.As(err); !ok || fe.Kind != fault.HostKey || !strings.Contains(fe.Hint, "ssh-keygen -R h") {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(kh); !strings.Contains(string(b), "AAAAOTRAOTRAOTRA") {
		t.Fatal("the existing entry must be kept")
	}
	// another host is added without touching the previous one (and honors a file without a trailing newline)
	os.WriteFile(kh, []byte("other ssh-rsa AAAA"), 0o600)
	if err := PinKnownHosts(kh, "h "+strings.Fields(edPub)[0]+" "+strings.Fields(edPub)[1]); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(kh); !strings.HasPrefix(string(b), "other ssh-rsa AAAA\nh ssh-ed25519") {
		t.Fatalf("%q", b)
	}
}

func TestPickIPPrefersTheEdgeNetwork(t *testing.T) {
	ips := []string{"172.17.208.1", "192.0.2.3", "10.0.0.5"}
	if got := pickIP(ips, "192.0.2.7"); got != "192.0.2.3" {
		t.Fatal(got)
	}
	if got := pickIP(ips, "8.8.8.8"); got != "172.17.208.1" {
		t.Fatal(got)
	}
	if got := pickIP(nil, "1.2.3.4"); got != "<hub-ip>" {
		t.Fatal(got)
	}
}

func TestHubHostFingerprintReadsTheHostKeyFile(t *testing.T) {
	onlyWindows(t)
	f := &fakeSystem{files: map[string]string{`C:\ProgramData\ssh\ssh_host_ed25519_key.pub`: edPub + "\r\n"}}
	got, err := hubHostFingerprint(f, `C:\ProgramData`)
	if err != nil || got != edFP {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := hubHostFingerprint(&fakeSystem{files: map[string]string{}}, `C:\ProgramData`); err == nil {
		t.Fatal("without a file it must fail")
	}
}
