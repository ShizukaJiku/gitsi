// Package bridge knows how the edge reaches the hub's bare repos (the bridge). It lives outside the modules so that every module
// that talks to the bridge (repo, mirror) shares one definition instead of depending on another module.
package bridge

import (
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
)

// URL is the URL git uses to reach the hub's bare repo (it uses the ~/.ssh/config alias).
func URL(cfg *config.Config, reposDir, name string) string {
	return cfg.Hub.HostAlias() + ":" + strings.TrimRight(reposDir, "/") + "/" + name + ".git"
}

// EnsureBareScript builds the (PowerShell) command that creates the bare repo on the hub when it does not exist. It leaves HEAD on
// the right branch (avoids "remote HEAD refers to nonexistent ref") and hands ownership to the user (avoids "dubious ownership":
// an administrator SSH session creates things owned by the Administrators group). It prints CREATED or EXISTS. The inputs must
// already be validated (safe.Path, safe.Branch, safe.Name): they go between single quotes.
func EnsureBareScript(bare, branch, user string) string {
	return "if (-not (Test-Path '" + bare + "/HEAD')) { " +
		"git init --bare -b '" + branch + "' '" + bare + "' | Out-Null; " +
		"icacls '" + bare + "' /setowner '" + user + "' /T /C /Q | Out-Null; 'CREATED' } else { 'EXISTS' }"
}
