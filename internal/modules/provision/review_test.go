package provision

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// Defects found while reviewing provisioning. Each test reproduces the failure the fix addresses.

// 1. An impossible IP must not pass validation: the failure would arrive after sshd_config was changed and sshd stopped.
func TestEdgeIPMustBeARealAddress(t *testing.T) {
	good := []string{"192.0.2.7", "10.0.0.0/8", "192.0.2.0/24", "1.2.3.4/32"}
	bad := []string{"999.1.1.1", "300.300.300.300", "1.2.3.4/33", "1.2.3.4/99", "1.2.3", "1.2.3.4.5", "", "a.b.c.d", "192.0.2.7/"}
	for _, ip := range good {
		o := opts()
		o.EdgeIP = ip
		if err := o.Validate(); err != nil {
			t.Errorf("%q must be accepted: %v", ip, err)
		}
	}
	for _, ip := range bad {
		o := opts()
		o.EdgeIP = ip
		if err := o.Validate(); err == nil {
			t.Errorf("%q is not a valid IP or range and must be rejected before touching anything", ip)
		}
	}
}

// 2. When sshd rejects the new configuration, it must go back to the one there was BEFORE this run (the working one), not the
// factory one: on a second run the backup is the unhardened original (with password authentication enabled).
func TestRejectedConfigRestoresThePreviousOneNotTheFactoryOne(t *testing.T) {
	onlyWindows(t)
	cfg := filepath.Join(`C:\ProgramData`, "ssh", "sshd_config")
	f := &fakeSystem{files: map[string]string{cfg: stockSSHD}, adminResp: "False"}
	if err := SetupHub(context.Background(), f, opts()); err != nil {
		t.Fatal(err)
	}
	working := f.files[cfg] // the hardened config that already worked
	if working == stockSSHD || !strings.Contains(working, "PasswordAuthentication no") {
		t.Fatal("the first run should harden")
	}
	f.sshdErr = context.DeadlineExceeded
	o := opts()
	o.Port = 2222
	if err := SetupHub(context.Background(), f, o); err == nil {
		t.Fatal("sshd -t failed: there must be an error")
	}
	if f.files[cfg] != working {
		t.Fatalf("it must go back to the working configuration, not the factory one:\n%s", f.files[cfg])
	}
	if strings.Contains(f.files[cfg], "PasswordAuthentication yes") {
		t.Fatal("password authentication must never be left enabled")
	}
}

// 3. A managed block missing its closing marker must not swallow EVERYTHING after it (including the administrators' Match),
// which would leave sshd without its key configuration.
func TestHardenSSHDWithAnUnterminatedBlockKeepsTheRestOfTheFile(t *testing.T) {
	in := []string{
		"# earlier conf",
		sshdStart,
		"Port 2200",
		// sshdEnd is missing (someone removed it by hand)
		"Subsystem sftp sftp-server.exe",
		"Match Group administrators",
		"       AuthorizedKeysFile __PROGRAMDATA__/ssh/administrators_authorized_keys",
	}
	out := strings.Join(HardenSSHD(in, "user", 22), "\n")
	for _, want := range []string{"Subsystem sftp sftp-server.exe", "Match Group administrators", "administrators_authorized_keys", "Port 22\n", "# earlier conf"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q was lost:\n%s", want, out)
		}
	}
	if strings.Count(out, sshdStart) != 1 || strings.Count(out, sshdEnd) != 1 {
		t.Errorf("there must be exactly one managed block:\n%s", out)
	}
	// and the orphan block's global directives must not stay duplicated
	if strings.Contains(out, "Port 2200") {
		t.Errorf("the orphan block's directive must go away (only the new one stays in effect):\n%s", out)
	}
}

// 4. A non-administrator user's key must go to the real profile folder, not to C:\Users\<name>: the profile folder is not always
// named like the user (Microsoft accounts, domain accounts, profiles on another drive).
func TestNonAdminKeyGoesToTheRealProfileFolder(t *testing.T) {
	onlyWindows(t)
	cfg := filepath.Join(`C:\ProgramData`, "ssh", "sshd_config")
	f := &fakeSystem{files: map[string]string{cfg: stockSSHD}, adminResp: "False"}
	o := opts()
	o.UserProfile = `D:\Profiles\Jane Doe`
	if err := SetupHub(context.Background(), f, o); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(o.UserProfile, ".ssh", "authorized_keys")
	if !strings.Contains(f.files[want], "ssh-ed25519 AAAAC3 edge") {
		t.Fatalf("the key must go to %s; files: %v", want, keys(f.files))
	}
	if _, wrong := f.files[`C:\Users\user\.ssh\authorized_keys`]; wrong {
		t.Error("nothing may be written to C:\\Users\\<user> when the profile is elsewhere")
	}
}

func keys(m map[string]string) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	return k
}

// 5. A key path with spaces (C:\Users\Jane Doe\.ssh\...) must be quoted in ~/.ssh/config, or OpenSSH splits it.
func TestHostBlockQuotesAKeyPathWithSpaces(t *testing.T) {
	b := strings.Join(HostBlock("gitsi-hub", "192.0.2.3", "user", 0, `C:\Users\Jane Doe\.ssh\gitsi_ed25519`), "\n")
	if !strings.Contains(b, `IdentityFile "C:/Users/Jane Doe/.ssh/gitsi_ed25519"`) {
		t.Fatalf("the path with spaces must be quoted:\n%s", b)
	}
	b = strings.Join(HostBlock("gitsi-hub", "h", "u", 0, `C:\Users\user\.ssh\k`), "\n")
	if !strings.Contains(b, "IdentityFile C:/Users/user/.ssh/k\n") {
		t.Fatalf("without spaces no quotes are needed:\n%s", b)
	}
}

// 6. An out-of-range -port must be rejected before creating keys or touching anything (it would end up in ~/.ssh/config).
func TestInitRejectsAPortOutOfRangeBeforeTouchingAnything(t *testing.T) {
	for _, port := range []string{"99999", "-5", "70000"} {
		env, _, home := cfgEnv(t, config.RoleEdge)
		err := initCmd(context.Background(), env, []string{"-host", "192.0.2.3", "-user", "user", "-port", port})
		if err == nil || !strings.Contains(err.Error(), "port") {
			t.Errorf("-port %s must be rejected: %v", port, err)
		}
		if matches, _ := filepath.Glob(filepath.Join(home, ".ssh", "*")); len(matches) != 0 {
			t.Errorf("it must create nothing in ~/.ssh when validation fails: %v", matches)
		}
	}
}

// ---- setup hub: validation and -dry-run (nothing is written or run) ----

func dryEnv(t *testing.T) (*module.Env, *bytes.Buffer, string) {
	t.Helper()
	env, out, _ := cfgEnv(t, config.RoleEdge)
	pd := t.TempDir()
	t.Setenv("ProgramData", pd)
	cfgPath := filepath.Join(pd, "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(stockSSHD), 0o644); err != nil {
		t.Fatal(err)
	}
	return env, out, cfgPath
}

func TestSetupHubDryRunShowsThePlanAndChangesNothing(t *testing.T) {
	env, out, cfgPath := dryEnv(t)
	err := setupHubCmd(context.Background(), env, []string{"-dry-run", "-edge-ip", "192.0.2.7", "-public-key", "ssh-ed25519 AAAAC3 edge", "-user", "other"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"-dry-run", "[dry-run] PowerShell", "[dry-run] write", "192.0.2.7", "icacls"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in the plan:\n%s", want, out)
		}
	}
	if b, _ := os.ReadFile(cfgPath); string(b) != stockSSHD {
		t.Fatal("-dry-run must not modify sshd_config")
	}
	if _, serr := os.Stat(cfgPath + ".gitsi.bak"); serr == nil {
		t.Fatal("-dry-run must not create the backup")
	}
	if env.Config.Role == config.RoleHub {
		t.Fatal("-dry-run must not change the role")
	}
}

// Without OpenSSH Server installed there is no sshd_config (sshd creates it on its first start): -dry-run must show the plan
// anyway, starting from sshd_config_default or, when that does not exist either, from the factory configuration.
func TestSetupHubDryRunWithoutOpenSSHServerShowsThePlan(t *testing.T) {
	for _, withDefault := range []bool{true, false} {
		env, out, cfgPath := dryEnv(t)
		if err := os.Remove(cfgPath); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		t.Setenv("SystemRoot", root)
		want := "factory configuration"
		if withDefault {
			def := filepath.Join(root, "System32", "OpenSSH", "sshd_config_default")
			if err := os.MkdirAll(filepath.Dir(def), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(def, []byte(stockSSHD), 0o644); err != nil {
				t.Fatal(err)
			}
			want = def
		}
		err := setupHubCmd(context.Background(), env, []string{"-dry-run", "-edge-ip", "192.0.2.7", "-public-key", "ssh-ed25519 AAAAC3 edge", "-user", "other"})
		if err != nil {
			t.Fatalf("with default=%v: %v\n%s", withDefault, err, out)
		}
		for _, w := range []string{"does not exist yet", want, "[dry-run] write " + cfgPath, "NetFirewallRule", "sshd.exe -t"} {
			if !strings.Contains(out.String(), w) {
				t.Errorf("with default=%v missing %q in the plan:\n%s", withDefault, w, out)
			}
		}
		if _, serr := os.Stat(cfgPath); serr == nil {
			t.Fatal("-dry-run must not create sshd_config")
		}
	}
}

// A read error other than "does not exist" (e.g. permissions) is not covered up with the factory configuration.
func TestDryRunOnlyFallsBackWhenSSHDConfigIsMissing(t *testing.T) {
	dir := t.TempDir()
	d := dryRun{realSystem: realSystem{out: io.Discard}, sshdConfig: dir, sshdDefault: filepath.Join(dir, "x")}
	if _, err := d.ReadFile(dir); err == nil { // reading a folder fails, but not with "does not exist"
		t.Fatal("an error other than 'does not exist' must arrive as is")
	}
	if _, err := d.ReadFile(filepath.Join(dir, "other")); err == nil {
		t.Fatal("only sshd_config has a substitute")
	}
}

func TestSetupHubReadsThePublicKeyFromAFile(t *testing.T) {
	env, out, _ := dryEnv(t)
	kf := filepath.Join(t.TempDir(), "edge.pub")
	os.WriteFile(kf, []byte("\r\n\n  ssh-ed25519 AAAAC3 from-file  \n# other\n"), 0o644)
	if err := setupHubCmd(context.Background(), env, []string{"-dry-run", "-edge-ip", "10.0.0.5", "-public-key-file", kf, "-user", "other"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "write") {
		t.Fatalf("it should reach the plan:\n%s", out)
	}
	if err := setupHubCmd(context.Background(), env, []string{"-dry-run", "-edge-ip", "10.0.0.5", "-public-key-file", filepath.Join(t.TempDir(), "missing.pub")}); err == nil {
		t.Fatal("a missing key file must be an error")
	}
}

func TestSetupHubRejectsBadInputsWithUsageAndTouchesNothing(t *testing.T) {
	for name, args := range map[string][]string{
		"impossible ip": {"-dry-run", "-edge-ip", "999.1.1.1", "-public-key", "ssh-ed25519 AAAAC3 e", "-user", "u"},
		"private key":   {"-dry-run", "-edge-ip", "10.0.0.5", "-public-key", "-----BEGIN OPENSSH PRIVATE KEY-----", "-user", "u"},
		"no key":        {"-dry-run", "-edge-ip", "10.0.0.5", "-user", "u"},
		"port":          {"-dry-run", "-edge-ip", "10.0.0.5", "-public-key", "ssh-ed25519 AAAAC3 e", "-user", "u", "-port", "70000"},
	} {
		env, out, _ := dryEnv(t)
		err := setupHubCmd(context.Background(), env, args)
		if err == nil || !strings.Contains(err.Error(), "usage: gitsi setup hub") {
			t.Errorf("%s: it must be rejected with the usage: %v", name, err)
		}
		if strings.Contains(out.String(), "[dry-run]") {
			t.Errorf("%s: it must not get to plan anything:\n%s", name, out)
		}
	}
}

// ---- setup edge: role and arguments ----

func TestSetupEdgeRejectsMissingHost(t *testing.T) {
	env, _, home := cfgEnv(t, config.RoleEdge)
	if err := initCmd(context.Background(), env, []string{"-user", "user"}); err == nil || !strings.Contains(err.Error(), "usage: gitsi setup edge") {
		t.Fatalf("-host is missing: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(home, ".ssh", "*")); len(matches) != 0 {
		t.Fatalf("it must create nothing: %v", matches)
	}
}

func TestLocalIPTowardLoopbackAndInvalidHost(t *testing.T) {
	if got := localIPToward("127.0.0.1", 0); got != "127.0.0.1" {
		t.Errorf("toward loopback the local IP is 127.0.0.1: %q", got)
	}
	if got := localIPToward("invalid host with spaces", 22); !strings.HasPrefix(got, "<") {
		t.Errorf("an unreachable target returns the placeholder, not an invented IP: %q", got)
	}
}

// ---- SetupHub: each failing step stops the process with its cause (and stops touching the machine) ----

// failingSystem is a fakeSystem that can be made to fail at a given step.
type failingSystem struct {
	*fakeSystem
	failPS    string // fragment of the PowerShell script that fails
	failRun   string // name of the program that fails
	failWrite string // path (fragment) whose write fails
}

func (f *failingSystem) PowerShell(ctx context.Context, s string) (string, error) {
	if f.failPS != "" && strings.Contains(s, f.failPS) {
		f.scripts = append(f.scripts, s)
		return "", errors.New("simulated failure")
	}
	return f.fakeSystem.PowerShell(ctx, s)
}
func (f *failingSystem) Run(ctx context.Context, name string, args ...string) (string, error) {
	if f.failRun != "" && strings.Contains(name, f.failRun) {
		f.runs = append(f.runs, name)
		return "", errors.New("simulated failure")
	}
	return f.fakeSystem.Run(ctx, name, args...)
}
func (f *failingSystem) WriteFile(p string, d []byte) error {
	if f.failWrite != "" && strings.Contains(p, f.failWrite) {
		return errors.New("simulated failure")
	}
	return f.fakeSystem.WriteFile(p, d)
}

func TestSetupHubStepFailuresStopWithTheirCause(t *testing.T) {
	cfg := filepath.Join(`C:\ProgramData`, "ssh", "sshd_config")
	cases := []struct {
		name    string
		sys     func() *failingSystem
		want    string
		notWant string // a later step that must NOT have run
	}{
		{"install OpenSSH", func() *failingSystem { return &failingSystem{failPS: "Get-WindowsCapability"} }, "installing OpenSSH Server", "DefaultShell"},
		{"write the backup", func() *failingSystem { return &failingSystem{failWrite: ".gitsi.bak"} }, "simulated failure", "DefaultShell"},
		{"write sshd_config", func() *failingSystem { return &failingSystem{failWrite: `sshd_config`} }, "simulated failure", "DefaultShell"},
		{"default shell", func() *failingSystem { return &failingSystem{failPS: "DefaultShell"} }, "simulated failure", "Get-LocalGroupMember"},
		{"read the Administrators group", func() *failingSystem { return &failingSystem{failPS: "Get-LocalGroupMember"} }, "could not read the Administrators group", "icacls"},
		{"write authorized_keys", func() *failingSystem { return &failingSystem{failWrite: "authorized_keys"} }, "simulated failure", "icacls"},
		{"icacls permissions", func() *failingSystem { return &failingSystem{failRun: "icacls"} }, "permissions of", "New-NetFirewallRule"},
		{"firewall", func() *failingSystem { return &failingSystem{failPS: "New-NetFirewallRule"} }, "simulated failure", "Start-Service sshd\n"},
		{"start sshd", func() *failingSystem { return &failingSystem{failPS: "Start-Service sshd"} }, "simulated failure", ""},
	}
	for _, c := range cases {
		fs := &fakeSystem{files: map[string]string{cfg: stockSSHD}, adminResp: "False"}
		sys := c.sys()
		sys.fakeSystem = fs
		o := opts()
		o.UserProfile = `C:\Users\user`
		err := SetupHub(context.Background(), sys, o)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, expected %q", c.name, err, c.want)
			continue
		}
		all := strings.Join(fs.scripts, "\n") + "\n" + strings.Join(fs.runs, "\n")
		if c.notWant != "" && strings.Contains(all, c.notWant) {
			t.Errorf("%s: it should not go on until %q:\n%s", c.name, c.notWant, all)
		}
	}
}

func TestSetupHubRefusesInvalidOptionsBeforeTouchingAnything(t *testing.T) {
	fs := &fakeSystem{files: map[string]string{}}
	o := opts()
	o.EdgeIP = "999.9.9.9"
	if err := SetupHub(context.Background(), fs, o); err == nil || len(fs.scripts)+len(fs.runs) != 0 {
		t.Fatalf("invalid input must run nothing: %v %v %v", err, fs.scripts, fs.runs)
	}
}

func TestSetupHubWithoutSSHDConfigFailsBeforeChangingAnything(t *testing.T) {
	fs := &fakeSystem{files: map[string]string{}}
	err := SetupHub(context.Background(), fs, opts())
	if err == nil || !strings.Contains(err.Error(), "could not read") {
		t.Fatalf("%v", err)
	}
	for _, s := range fs.scripts {
		if strings.Contains(s, "DefaultShell") || strings.Contains(s, "NetFirewallRule") {
			t.Fatalf("without sshd_config it must not go on: %s", s)
		}
	}
}

// ---- the real system (touching nothing on the machine) ----

func TestRealSystemRunReadWriteAndDryRun(t *testing.T) {
	var out bytes.Buffer
	rs := realSystem{out: &out}
	if _, err := rs.Run(context.Background(), os.Args[0], "-test.run=^$"); err != nil {
		t.Fatalf("a program that ends well: %v", err)
	}
	if _, err := rs.Run(context.Background(), "gitsi-program-that-does-not-exist"); err == nil {
		t.Fatal("a missing program is an error")
	}
	p := filepath.Join(t.TempDir(), "sub", "dir", "f.txt")
	if err := rs.WriteFile(p, []byte("hello")); err != nil { // creates the missing folders
		t.Fatal(err)
	}
	if b, err := rs.ReadFile(p); err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	rs.Logf("step %d", 1)
	if !strings.Contains(out.String(), "step 1") {
		t.Fatalf("Logf: %q", out.String())
	}

	d := dryRun{realSystem: rs}
	out.Reset()
	d.PowerShell(context.Background(), "uno\ndos")
	d.Run(context.Background(), "icacls", "x", "/grant")
	d.WriteFile(filepath.Join(t.TempDir(), "not-created.txt"), []byte("abc"))
	for _, want := range []string{"[dry-run] PowerShell", "    dos", "[dry-run] run: icacls x /grant", "[dry-run] write", "(3 bytes)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
}
