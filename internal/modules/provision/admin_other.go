//go:build !windows

package provision

// IsAdmin: gitsi only provisions Windows hubs; on other systems it is always false.
func IsAdmin() bool { return false }
