// Package testhome isolates tests from the user's real profile.
//
// Much of gitsi resolves paths from the profile (os.UserHomeDir, os.UserCacheDir): the config, audit.log, the hub's mailbox and
// jobs, the cleanup marker, Downloads. A test that forgot to set USERPROFILE would write to (or clean) the machine's real data.
// Run makes that impossible per package: the whole test binary runs with a temporary profile.
package testhome

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Run points the profile (USERPROFILE, HOME, LOCALAPPDATA, APPDATA, XDG_*) to a temporary folder, runs the tests, removes the
// folder and exits with their code. Use it from TestMain: func TestMain(m *testing.M) { testhome.Run(m) }.
func Run(m *testing.M) {
	dir, err := os.MkdirTemp("", "gitsi-profile-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		os.Exit(1)
	}
	vars := map[string]string{
		"USERPROFILE":     dir,
		"HOME":            dir,
		"LOCALAPPDATA":    filepath.Join(dir, "AppData", "Local"),
		"APPDATA":         filepath.Join(dir, "AppData", "Roaming"),
		"XDG_CACHE_HOME":  filepath.Join(dir, ".cache"),
		"XDG_CONFIG_HOME": filepath.Join(dir, ".config"),
	}
	for k, v := range vars {
		_ = os.MkdirAll(v, 0o700)
		os.Setenv(k, v)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
