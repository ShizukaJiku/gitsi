//go:build !windows

package layout

// shortPath: 8.3 names only exist on Windows.
func shortPath(string) string { return "" }
