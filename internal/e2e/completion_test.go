package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The block `gitsi setup completion` leaves in the profile works in the real PowerShell: TabExpansion2 (what Tab does) asks the
// real binary for the candidates. All with a temporary profile: the user's profile and configuration are not touched.
func TestTabCompletionInRealPowerShell(t *testing.T) {
	if testing.Short() || runtime.GOOS != "windows" {
		t.Skip("builds the binary and uses powershell.exe")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "gitsi.exe")
	buildGitsi(t, bin)
	home := filepath.Join(dir, "profile")
	os.MkdirAll(filepath.Join(home, ".gitsi"), 0o700)
	cfg := `{"version": 1, "role": "edge", "services": [{"name": "api", "remote_port": 3000, "local_port": 13000}]}`
	if err := os.WriteFile(filepath.Join(home, ".gitsi", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// each case: what was typed up to the cursor -> expected candidates
	cases := map[string]string{
		"gitsi se":          "send,setup",
		"gitsi st":          "status",
		"gitsi job ":        "get,logs,ls,start,stop",
		"gitsi take ":       "-clip,-dir,-keep,-last,-list,-rm,-show",
		"gitsi svc rm a":    "api",
		"gitsi -q svc up r": "rabbitmq,redis",
	}
	var script strings.Builder
	script.WriteString("$ErrorActionPreference = 'Stop'\n")
	script.WriteString("$env:PATH = '" + filepath.Dir(bin) + ";' + $env:PATH\n")
	script.WriteString("Invoke-Expression (& gitsi setup completion -print | Out-String)\n")
	for line := range cases {
		script.WriteString("$m = (TabExpansion2 -inputScript '" + line + "' -cursorColumn " + itoa(len(line)) + ").CompletionMatches\n")
		script.WriteString("'" + line + "=' + (($m | ForEach-Object { $_.CompletionText }) -join ',')\n")
	}
	// PowerShell 7, when present, also calls the completer with a word starting with "-" (Windows PowerShell 5.1 does not)
	script.WriteString("$m = (TabExpansion2 -inputScript 'gitsi take -l' -cursorColumn 13).CompletionMatches\n")
	script.WriteString("'gitsi take -l=' + (($m | ForEach-Object { $_.CompletionText }) -join ',')\n")
	shells := []string{"powershell.exe"}
	if _, err := exec.LookPath("pwsh.exe"); err == nil {
		shells = append(shells, "pwsh.exe")
	}
	for _, sh := range shells {
		want := map[string]string{"gitsi take -l": ""}
		if sh == "pwsh.exe" {
			want["gitsi take -l"] = "-last,-list"
		}
		for k, v := range cases {
			want[k] = v
		}
		cmd := exec.Command(sh, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script.String())
		cmd.Env = append(os.Environ(), "USERPROFILE="+home, "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", sh, err, out)
		}
		got := map[string]string{}
		for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
			if k, v, ok := strings.Cut(l, "="); ok {
				got[k] = v
			}
		}
		for line, w := range want {
			if got[line] != w {
				t.Errorf("%s, %q: candidates %q, want %q\n%s", sh, line, got[line], w, out)
			}
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
