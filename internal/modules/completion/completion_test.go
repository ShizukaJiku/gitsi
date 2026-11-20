package completion

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

func TestApplyAddsOnceKeepsTheRestAndRemoves(t *testing.T) {
	p := filepath.Join(t.TempDir(), "WindowsPowerShell", "profile.ps1")
	mine := "Set-Alias ll Get-ChildItem\r\n"
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := Apply(p, true); err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	if changed, err := Apply(p, true); err != nil || changed {
		t.Fatalf("the second time nothing changes: %v %v", changed, err)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), mine) || strings.Count(string(b), begin) != 1 || !strings.Contains(string(b), "Register-ArgumentCompleter") {
		t.Fatalf("%q", b)
	}
	if changed, err := Apply(p, false); err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	if b, _ := os.ReadFile(p); string(b) != mine {
		t.Fatalf("-off leaves the profile as it was: %q", b)
	}
}

func TestApplyOffWithoutProfileCreatesNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "PowerShell", "profile.ps1")
	if changed, err := Apply(p, false); err != nil || changed {
		t.Fatalf("%v %v", changed, err)
	}
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Fatal("it must not create the profile folder")
	}
}

func TestApplyRemovesABlockCutByHand(t *testing.T) {
	p := filepath.Join(t.TempDir(), "profile.ps1")
	os.WriteFile(p, []byte("before\r\n"+begin+"\r\nRegister-ArgumentCompleter ..."), 0o644)
	if _, err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "before\r\n" {
		t.Fatalf("%q", b)
	}
}

func TestRunWarnsWhenThePolicyDoesNotLoadProfiles(t *testing.T) {
	dir := t.TempDir()
	old := shells
	t.Cleanup(func() { shells = old })
	shells = func(context.Context) []Shell {
		return []Shell{
			{Name: "Windows PowerShell", Profile: filepath.Join(dir, "a", "profile.ps1"), Policy: "Restricted"},
			{Name: "PowerShell 7", Profile: filepath.Join(dir, "b", "profile.ps1"), Policy: "RemoteSigned"},
		}
	}
	var out, errb bytes.Buffer
	env := &module.Env{Stdout: &out, Stderr: &errb, Config: config.Default()}
	if err := run(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"a", "b"} {
		if b, _ := os.ReadFile(filepath.Join(dir, sub, "profile.ps1")); !strings.Contains(string(b), begin) {
			t.Errorf("profile %s without the block", sub)
		}
	}
	if strings.Count(errb.String(), "warning") != 1 || !strings.Contains(errb.String(), "Restricted") {
		t.Fatalf("it only warns about the policy that does not load profiles: %s", errb.String())
	}
	out.Reset()
	if err := run(context.Background(), env, []string{"-off"}); err != nil || strings.Count(out.String(), "removed") != 2 {
		t.Fatalf("%v %s", err, out.String())
	}
}

func TestPrintShowsTheBlockAndTouchesNothing(t *testing.T) {
	old := shells
	t.Cleanup(func() { shells = old })
	shells = func(context.Context) []Shell { t.Fatal("-print does not look for profiles"); return nil }
	var out bytes.Buffer
	env := &module.Env{Stdout: &out, Stderr: &out, Config: config.Default()}
	if err := run(context.Background(), env, []string{"-print"}); err != nil || out.String() != Script {
		t.Fatalf("%v %q", err, out.String())
	}
}

// shells asks the real PowerShell where its profile is and which policy it has (read only: it writes nothing).
func TestShellsReadsTheRealProfilePathAndPolicy(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell")
	}
	found := shells(context.Background())
	if len(found) == 0 || found[0].Name != "Windows PowerShell" {
		// An account without a resolvable Documents folder (e.g. a service account) gets an empty $PROFILE: there is nowhere to
		// install completion and shells() rightly leaves that PowerShell out. Anything else is a defect.
		cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$PROFILE.CurrentUserAllHosts")
		cmd.Env = winps.Env(os.Environ())
		out, err := cmd.CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "" {
			t.Skip("Windows PowerShell has no profile path for this account")
		}
		t.Fatalf("%+v\npowershell.exe: %v\n%s", found, err, out)
	}
	for _, sh := range found {
		if !strings.EqualFold(filepath.Base(sh.Profile), "profile.ps1") || !filepath.IsAbs(sh.Profile) || sh.Policy == "" || strings.Contains(sh.Policy, "\n") {
			t.Errorf("%+v", sh)
		}
	}
}
