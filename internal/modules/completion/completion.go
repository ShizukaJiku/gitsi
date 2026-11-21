// Package completion exposes `gitsi setup completion`: it registers gitsi's Tab completion in the user's PowerShell profile
// (Windows PowerShell and, when present, PowerShell 7). The completion itself is resolved by the binary (`gitsi __complete`, in
// app), so the profile only carries a fixed block that does not change between versions.
package completion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/fsx"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "completion" }

func (mod) Commands() []module.Command {
	return []module.Command{{
		Group:   "setup",
		Name:    "completion",
		Summary: "Tab completion in PowerShell (-print, -off)",
		Roles:   module.Both,
		Run:     run,
		Flags:   []string{"-off", "-print"},
	}}
}

const (
	begin = "# >>> gitsi completion (remove with: gitsi setup completion -off)"
	end   = "# <<< gitsi completion"
)

// Script is the block that goes into the profile (gitsi setup completion). It asks `gitsi __complete` for the candidates; the
// current word goes as "cur:<word>" because PowerShell 5.1 drops empty arguments when calling a program.
const Script = begin + `
Register-ArgumentCompleter -Native -CommandName 'gitsi', 'gitsi.exe' -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $prev = @($commandAst.CommandElements | Select-Object -Skip 1 | Where-Object { $_.Extent.EndOffset -le $cursorPosition } | ForEach-Object { $_.Extent.Text })
    if ($wordToComplete -ne '' -and $prev.Count -gt 0) { $prev = @($prev | Select-Object -First ($prev.Count - 1)) }
    & gitsi __complete "cur:$wordToComplete" @prev 2>$null | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
` + end + "\n"

// Shell is an installed PowerShell: where its user profile is and which execution policy it has.
type Shell struct {
	Name    string // "Windows PowerShell" or "PowerShell 7"
	Profile string // $PROFILE.CurrentUserAllHosts
	Policy  string // Get-ExecutionPolicy
}

// shells finds the machine's PowerShells. A variable for tests.
var shells = func(ctx context.Context) []Shell {
	var out []Shell
	for _, s := range []struct{ name, exe string }{{"Windows PowerShell", "powershell.exe"}, {"PowerShell 7", "pwsh.exe"}} {
		if _, err := exec.LookPath(s.exe); err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, s.exe, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command",
			"$PROFILE.CurrentUserAllHosts; [string](Get-ExecutionPolicy)")
		cmd.Env = winps.Env(os.Environ()) // each PowerShell with its own modules, even when gitsi runs from the other one
		b, err := cmd.Output()
		// two lines: the profile path (it may have spaces) and the policy
		text := strings.TrimSpace(strings.ReplaceAll(string(b), "\r", ""))
		i := strings.LastIndex(text, "\n")
		if err != nil || i < 0 {
			continue
		}
		out = append(out, Shell{Name: s.name, Profile: strings.TrimSpace(text[:i]), Policy: strings.TrimSpace(text[i+1:])})
	}
	return out
}

func run(ctx context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("setup completion", env.Stderr)
	print := fs.Bool("print", false, "only print the block (to use it in this session: gitsi setup completion -print | Out-String | Invoke-Expression)")
	off := fs.Bool("off", false, "remove tab completion from the profiles")
	if _, err := module.ParseInterleaved(fs, args); err != nil {
		return err
	}
	if *print {
		fmt.Fprint(env.Stdout, Script)
		return nil
	}
	found := shells(ctx)
	if len(found) == 0 {
		return errors.New("no PowerShell found on this machine; tab completion is for PowerShell")
	}
	for _, sh := range found {
		changed, err := Apply(sh.Profile, !*off)
		if err != nil {
			return fmt.Errorf("%s: %w", sh.Name, err)
		}
		switch {
		case *off && changed:
			fmt.Fprintf(env.Stdout, "%s: tab completion removed from %s\n", sh.Name, sh.Profile)
		case *off:
			fmt.Fprintf(env.Stdout, "%s: it was not enabled\n", sh.Name)
		default:
			fmt.Fprintf(env.Stdout, "%s: tab completion in %s (active in new consoles)\n", sh.Name, sh.Profile)
			if !loadsProfiles(sh.Policy) {
				fmt.Fprintf(env.Stderr, "  warning: with the %s execution policy, PowerShell does not load the profile. Either allow it: "+
					"Set-ExecutionPolicy -Scope CurrentUser RemoteSigned, or run in each console: gitsi setup completion -print | Out-String | Invoke-Expression\n", sh.Policy)
			}
		}
	}
	return nil
}

// loadsProfiles reports whether PowerShell runs the profile (a local script) with that policy.
func loadsProfiles(policy string) bool {
	switch strings.ToLower(policy) {
	case "remotesigned", "unrestricted", "bypass":
		return true
	}
	return false
}

// Apply puts (on) or removes (off) gitsi's block in the profile, without touching the rest of the file. It writes only when
// something changes and reports whether it did. It is the only place that writes the profile.
func Apply(profile string, on bool) (bool, error) {
	b, err := os.ReadFile(profile)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	old := string(b)
	text := strip(old)
	if on {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\r\n"
		}
		text += strings.ReplaceAll(Script, "\n", "\r\n")
	}
	if text == old {
		return false, nil
	}
	if text == "" && os.IsNotExist(err) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(profile), 0o755); err != nil {
		return false, err
	}
	return true, fsx.WriteFileAtomic(profile, []byte(text), 0o644)
}

// strip removes gitsi's block (markers included) from the profile's text.
func strip(s string) string {
	i := strings.Index(s, begin)
	if i < 0 {
		return s
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		return s[:i] // block cut by hand: removed to the end
	}
	rest := s[i+j+len(end):]
	rest = strings.TrimPrefix(strings.TrimPrefix(rest, "\r"), "\n")
	return s[:i] + rest
}
