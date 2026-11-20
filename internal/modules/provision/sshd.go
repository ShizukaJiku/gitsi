package provision

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	sshdStart = "# --- gitsi (managed) ---"
	sshdEnd   = "# --- end gitsi ---"
)

// managedDirectives are the global directives gitsi controls. Earlier equivalent ones (before the first Match) are removed so
// ours is the only effective one.
var managedDirectives = regexp.MustCompile(`^\s*#?\s*(Port|PasswordAuthentication|KbdInteractiveAuthentication|PubkeyAuthentication|PermitRootLogin|AllowUsers|AllowTcpForwarding|GatewayPorts|PermitTunnel|X11Forwarding|ClientAliveInterval|ClientAliveCountMax)\b`)

var matchLine = regexp.MustCompile(`^\s*Match\s`)

// HardenSSHD returns sshd_config hardened for the hub role:
//   - public key only (no password), only the given user;
//   - local port forwarding only (-L): -R and GatewayPorts are forbidden (the edge exposes nothing).
//
// It is idempotent: it replaces its own block.
func HardenSSHD(lines []string, user string, port int) []string {
	managed := []string{
		sshdStart,
		fmt.Sprintf("Port %d", port),
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"PubkeyAuthentication yes",
		"PermitRootLogin no",
		"AllowUsers " + user,
		"AllowTcpForwarding local",
		"GatewayPorts no",
		"PermitTunnel no",
		"X11Forwarding no",
		"ClientAliveInterval 30",
		"ClientAliveCountMax 4",
		sshdEnd,
	}
	var rest []string
	inManaged, seenMatch := false, false
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, sshdStart) {
			// Without a closing marker further on (removed by hand) it does not discard "to the end": the rest of the file would be
			// lost, including the administrators' Match. Only the marker and its global directives are removed.
			inManaged = hasEndMarker(lines[i+1:])
			continue
		}
		if inManaged {
			if strings.HasPrefix(l, sshdEnd) {
				inManaged = false
			}
			continue
		}
		if matchLine.MatchString(l) {
			seenMatch = true
		}
		if !seenMatch && managedDirectives.MatchString(l) {
			continue
		}
		rest = append(rest, l)
	}
	// The block goes BEFORE any Match to be global.
	return append(managed, rest...)
}

func hasEndMarker(lines []string) bool {
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, sshdEnd) {
			return true
		}
	}
	return false
}

// AuthorizedKeysPath decides where the public key goes. Members of the Administrators group use administrators_authorized_keys
// (a Win32-OpenSSH rule); everyone else, ~/.ssh/authorized_keys.
func AuthorizedKeysPath(isAdmin bool, programData, user string) string {
	if isAdmin {
		return programData + `\ssh\administrators_authorized_keys`
	}
	return `C:\Users\` + user + `\.ssh\authorized_keys`
}

// AddKeyLine adds the key when it is not there yet (compares the whole line, without trailing spaces).
func AddKeyLine(existing []string, key string) ([]string, bool) {
	key = strings.TrimSpace(key)
	for _, l := range existing {
		if strings.TrimSpace(l) == key {
			return existing, false
		}
	}
	var out []string
	for _, l := range existing {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return append(out, key), true
}
