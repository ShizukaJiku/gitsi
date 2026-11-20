package provision

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	blockStart = "# >>> gitsi >>>"
	blockEnd   = "# <<< gitsi <<<"
)

// quoteIfSpaces quotes a path with spaces: ssh_config splits arguments on spaces (C:/Users/Jane Doe/...).
func quoteIfSpaces(p string) string {
	if strings.ContainsAny(p, " \t") && !strings.Contains(p, `"`) {
		return `"` + p + `"`
	}
	return p
}

// HostBlock generates the ~/.ssh/config block to reach the hub (used by git through the alias). Client only: no ForwardAgent, no
// RemoteForward.
func HostBlock(alias, host, user string, port int, keyFile string) []string {
	b := []string{
		blockStart,
		"Host " + alias,
		"    HostName " + host,
		"    User " + user,
	}
	if port != 0 {
		b = append(b, fmt.Sprintf("    Port %d", port))
	}
	b = append(b,
		"    IdentityFile "+quoteIfSpaces(strings.ReplaceAll(keyFile, `\`, "/")),
		"    IdentitiesOnly yes",
		"    StrictHostKeyChecking accept-new",
		"    ServerAliveInterval 30",
		"    ForwardAgent no",
		"    ForwardX11 no",
		blockEnd,
	)
	return b
}

// MergeSSHConfig keeps everything that is not gitsi's: it removes the marked block and also a hand-written "Host <alias>" block
// (up to the next unindented directive; if it stayed, ssh would use the first one), and appends the new block at the end.
func MergeSSHConfig(existing []string, alias string, block []string) []string {
	hostRe := regexp.MustCompile(`^\s*Host\s+` + regexp.QuoteMeta(alias) + `\s*$`)
	var kept []string
	mode := ""
	for _, l := range existing {
		switch mode {
		case "marked":
			if strings.HasPrefix(l, blockEnd) {
				mode = ""
			}
			continue
		case "manual":
			if len(l) > 0 && l[0] != ' ' && l[0] != '\t' {
				mode = ""
			} else {
				continue
			}
		}
		if strings.HasPrefix(l, blockStart) {
			mode = "marked"
			continue
		}
		if hostRe.MatchString(l) {
			mode = "manual"
			continue
		}
		kept = append(kept, l)
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(kept) > 0 {
		kept = append(kept, "")
	}
	return append(kept, block...)
}
