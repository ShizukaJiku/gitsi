package provision

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

const stockSSHD = `#Port 22
#PasswordAuthentication yes
AuthorizedKeysFile	.ssh/authorized_keys
#AllowTcpForwarding yes
Subsystem	sftp	sftp-server.exe
Match Group administrators
       AuthorizedKeysFile __PROGRAMDATA__/ssh/administrators_authorized_keys`

func TestHardenSSHD(t *testing.T) {
	got := strings.Join(HardenSSHD(strings.Split(stockSSHD, "\n"), "user", 22), "\n")
	for _, want := range []string{"PasswordAuthentication no", "PubkeyAuthentication yes", "AllowUsers user",
		"AllowTcpForwarding local", "GatewayPorts no", "Subsystem\tsftp", "Match Group administrators"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// what is managed goes before any Match
	if strings.Index(got, "AllowUsers user") > strings.Index(got, "Match Group") {
		t.Error("the directives must go before the first Match")
	}
	// commented defaults are removed so ours is the only one
	if strings.Contains(got, "#PasswordAuthentication yes") || strings.Contains(got, "#AllowTcpForwarding yes") {
		t.Errorf("equivalent directives remained outside the block:\n%s", got)
	}
	// reverse forwarding is never enabled
	for _, bad := range []string{"AllowTcpForwarding yes", "AllowTcpForwarding all", "GatewayPorts yes", "GatewayPorts clientspecified"} {
		if strings.Contains(got, bad) {
			t.Errorf("it must not contain %q", bad)
		}
	}
}

func TestHardenSSHDIsIdempotent(t *testing.T) {
	once := HardenSSHD(strings.Split(stockSSHD, "\n"), "user", 22)
	twice := HardenSSHD(once, "user", 22)
	if strings.Join(once, "\n") != strings.Join(twice, "\n") {
		t.Fatal("it is not idempotent")
	}
	if j := strings.Join(once, "\n"); strings.Count(j, "AllowUsers") != 1 || strings.Count(j, sshdStart) != 1 {
		t.Fatalf("a single managed block must remain:\n%s", j)
	}
}

func TestAuthorizedKeysPathAndAddKeyLine(t *testing.T) {
	if p := AuthorizedKeysPath(true, `C:\ProgramData`, "user"); p != `C:\ProgramData\ssh\administrators_authorized_keys` {
		t.Fatal(p)
	}
	if p := AuthorizedKeysPath(false, `C:\ProgramData`, "user"); p != `C:\Users\user\.ssh\authorized_keys` {
		t.Fatal(p)
	}
	lines, added := AddKeyLine([]string{"ssh-ed25519 AAA other", ""}, "ssh-ed25519 BBB edge")
	if !added || len(lines) != 2 || lines[1] != "ssh-ed25519 BBB edge" {
		t.Fatalf("%v %v", lines, added)
	}
	if _, added := AddKeyLine(lines, "ssh-ed25519 BBB edge"); added {
		t.Fatal("it must not duplicate")
	}
}

func TestValidate(t *testing.T) {
	ok := HubOptions{EdgeIP: "192.0.2.7", PublicKey: "ssh-ed25519 AAAAC3 edge", User: "user", Port: 22}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*HubOptions){
		"bad ip":           func(o *HubOptions) { o.EdgeIP = "not-an-ip" },
		"ip with a quote":  func(o *HubOptions) { o.EdgeIP = "1.2.3.4'; calc" },
		"private key":      func(o *HubOptions) { o.PublicKey = "-----BEGIN OPENSSH PRIVATE KEY-----" },
		"odd key":          func(o *HubOptions) { o.PublicKey = "hello" },
		"key with newline": func(o *HubOptions) { o.PublicKey = "ssh-ed25519 AAA\nssh-ed25519 BBB" },
		"bad user":         func(o *HubOptions) { o.User = "a;b" },
		"bad port":         func(o *HubOptions) { o.Port = 0 },
	}
	for name, mut := range cases {
		o := ok
		mut(&o)
		if o.Validate() == nil {
			t.Errorf("%s: should fail", name)
		}
	}
}

// fakeSystem records what would be done and serves files from memory.
type fakeSystem struct {
	files     map[string]string
	scripts   []string
	runs      []string
	adminResp string
	sshdErr   error
}

func (f *fakeSystem) PowerShell(_ context.Context, s string) (string, error) {
	f.scripts = append(f.scripts, s)
	if strings.Contains(s, "Get-LocalGroupMember") {
		return f.adminResp, nil
	}
	return "", nil
}
func (f *fakeSystem) Run(_ context.Context, name string, args ...string) (string, error) {
	f.runs = append(f.runs, name+" "+strings.Join(args, " "))
	if strings.HasSuffix(name, "sshd.exe") {
		return "", f.sshdErr
	}
	return "", nil
}
func (f *fakeSystem) ReadFile(p string) ([]byte, error) {
	if v, ok := f.files[p]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("does not exist")
}
func (f *fakeSystem) WriteFile(p string, d []byte) error { f.files[p] = string(d); return nil }
func (f *fakeSystem) Logf(string, ...any)                {}

func opts() HubOptions {
	return HubOptions{EdgeIP: "192.0.2.7", PublicKey: "ssh-ed25519 AAAAC3 edge", User: "user", Port: 22,
		ProgramData: `C:\ProgramData`, SSHDBinary: `C:\Windows\System32\OpenSSH\sshd.exe`}
}

func TestSetupHubAdminUser(t *testing.T) {
	onlyWindows(t)
	f := &fakeSystem{files: map[string]string{`C:\ProgramData\ssh\sshd_config`: stockSSHD}, adminResp: "True\r\n"}
	if err := SetupHub(context.Background(), f, opts()); err != nil {
		t.Fatal(err)
	}
	// backup of the original and hardened config
	if f.files[`C:\ProgramData\ssh\sshd_config.gitsi.bak`] != stockSSHD {
		t.Error("the backup of the original sshd_config is missing")
	}
	if !strings.Contains(f.files[`C:\ProgramData\ssh\sshd_config`], "AllowUsers user") {
		t.Error("sshd_config was not hardened")
	}
	// key in the administrators file (the localized group name case)
	if ak := f.files[`C:\ProgramData\ssh\administrators_authorized_keys`]; !strings.Contains(ak, "ssh-ed25519 AAAAC3 edge") {
		t.Errorf("the key is not in administrators_authorized_keys: %v", f.files)
	}
	all := strings.Join(f.scripts, "\n") + "\n" + strings.Join(f.runs, "\n")
	for _, want := range []string{"S-1-5-32-544", "-RemoteAddress '192.0.2.7'", "-LocalPort 22", "DefaultShell", "icacls", "*S-1-5-32-544:F", "sshd.exe -t", "Start-Service sshd"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in what ran:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Administrators'") {
		t.Error("it must not depend on the English group name")
	}
}

func TestSetupHubNonAdminUserAndIdempotent(t *testing.T) {
	onlyWindows(t)
	f := &fakeSystem{files: map[string]string{`C:\ProgramData\ssh\sshd_config`: stockSSHD}, adminResp: "False"}
	for i := 0; i < 2; i++ {
		if err := SetupHub(context.Background(), f, opts()); err != nil {
			t.Fatal(err)
		}
	}
	ak := f.files[`C:\Users\user\.ssh\authorized_keys`]
	if strings.Count(ak, "ssh-ed25519 AAAAC3 edge") != 1 {
		t.Fatalf("the key must appear only once: %q", ak)
	}
}

func TestSetupHubRestoresConfigWhenSSHDRejectsIt(t *testing.T) {
	onlyWindows(t)
	f := &fakeSystem{files: map[string]string{`C:\ProgramData\ssh\sshd_config`: stockSSHD}, sshdErr: fmt.Errorf("bad config")}
	err := SetupHub(context.Background(), f, opts())
	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("it should fail and restore: %v", err)
	}
	if f.files[`C:\ProgramData\ssh\sshd_config`] != stockSSHD {
		t.Fatal("the original sshd_config was not restored")
	}
}

// onlyWindows skips the tests that use Windows paths and separators (C:\ProgramData...): the hub only runs on Windows, and on
// Linux (e.g. the scripts/race.ps1 container) filepath.Join uses "/", so they would fail for that and not for a defect.
func onlyWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("uses Windows paths (ProgramData, USERPROFILE); the hub only runs on Windows")
	}
}
