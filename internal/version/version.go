// Package version exposes the binary's version. Build data is set with -ldflags (see scripts/build.ps1):
//
//	-X github.com/ShizukaJiku/gitsi/internal/version.Version=1.2.3
//	-X github.com/ShizukaJiku/gitsi/internal/version.Commit=abc1234
//	-X github.com/ShizukaJiku/gitsi/internal/version.Date=2026-11-17
package version

import "strings"

// Version is gitsi's semantic version.
var Version = "1.4.0"

// Commit is the commit it was built from (empty when not set).
var Commit = ""

// Date is the build date (empty when not set).
var Date = ""

// String returns the version with its build data: "1.0.0 (commit abc1234, built 2026-11-17)". It tells which of two
// machines runs the newer binary.
func String() string {
	var extra []string
	if Commit != "" {
		extra = append(extra, "commit "+Commit)
	}
	if Date != "" {
		extra = append(extra, "built "+Date)
	}
	if len(extra) == 0 {
		return Version
	}
	return Version + " (" + strings.Join(extra, ", ") + ")"
}
