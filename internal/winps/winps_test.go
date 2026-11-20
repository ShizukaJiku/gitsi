package winps

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnvDropsPSModulePathAndKeepsTheRest(t *testing.T) {
	got := Env([]string{"PATH=C:\\x", "PSModulePath=C:\\7\\Modules", "psmodulepath=y", "TEMP=C:\\t"})
	if strings.Join(got, ";") != "PATH=C:\\x;TEMP=C:\\t" {
		t.Fatalf("%q", got)
	}
}

// Reproduces running gitsi from PowerShell 7: the inherited PSModulePath points to PowerShell 7's modules and powershell.exe 5.1
// cannot even load Get-FileHash. With Command (no PSModulePath) it works.
func TestCommandRunsWindowsPowerShellUnderAPowerShell7Environment(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell")
	}
	pwsh, err := exec.LookPath("pwsh.exe")
	if err != nil {
		t.Skip("without PowerShell 7 there are no modules to confuse 5.1")
	}
	inherited := "PSModulePath=" + filepath.Join(filepath.Dir(pwsh), "Modules")
	script := "(Get-FileHash -Algorithm SHA256 -InputStream ([IO.MemoryStream]::new([byte[]](97)))).Hash; [string](Get-ExecutionPolicy)"

	cmd := Command(context.Background(), "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(cmd.Environ(), inherited) // how it arrives from PowerShell 7, without the cleanup
	if out, err := cmd.CombinedOutput(); err == nil && strings.Contains(string(out), want) {
		t.Skip("this PowerShell 5.1 tolerates PowerShell 7's PSModulePath; the failure does not reproduce here")
	}

	cmd = Command(context.Background(), "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = Env(append(cmd.Env, inherited))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), want) {
		t.Fatalf("%v %s", err, out)
	}
}

// want is the SHA256 of "a".
const want = "CA978112CA1BBDCAFAC231B39A23DC4DA786EFF8147C4E72B9807785AFEE48BB"
