// Package winps starts Windows PowerShell (powershell.exe) with an environment that does not inherit PowerShell 7's modules.
//
// When gitsi runs from PowerShell 7 it inherits its PSModulePath, which lists PowerShell 7's modules first. A powershell.exe
// (5.1) started with that environment tries to load them and fails on basic cmdlets (Get-FileHash, Get-ExecutionPolicy,
// Add-Type). PowerShell 7 fixes the variable when it starts powershell.exe itself; an external program like gitsi must do it.
package winps

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Env returns environ without PSModulePath: without the variable, each PowerShell (5.1 or 7) builds its own default. It suits
// any child process; the variable is irrelevant to anything that is not PowerShell.
func Env(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PSModulePath") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Command builds powershell.exe with args and the process environment cleaned (Env).
func Command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell.exe", args...)
	cmd.Env = Env(os.Environ())
	return cmd
}
