// Package safe validates values that end up inside remote shell commands or git commands. Names are restricted to a safe
// alphabet so nothing dangerous ever needs escaping.
package safe

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	// A name never starts with a dash: user and host end up as ssh's target argument, where "-x" would be read as an option.
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]*$`)
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9._:][A-Za-z0-9._:-]{0,252}$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	pathRe   = regexp.MustCompile(`^[A-Za-z0-9:/._ -][A-Za-z0-9:/._ ~-]*$`)

	badFileRe  = regexp.MustCompile(`[\x00-\x1f<>:"/\\|?*]`)
	reservedRe = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$`)
)

// FileName checks that a name is safe as a Windows file name (no paths, no reserved characters or names).
func FileName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("invalid name: %q", name)
	case len(name) > 120:
		return errors.New("name too long (at most 120 characters)")
	case badFileRe.MatchString(name):
		return fmt.Errorf("name with characters that are not allowed: %q", name)
	case reservedRe.MatchString(name):
		return fmt.Errorf("reserved Windows name: %q", name)
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return fmt.Errorf("the name cannot end with a dot or a space: %q", name)
	}
	return nil
}

// Name validates repo, user, host and alias names.
func Name(what, v string) error {
	if !nameRe.MatchString(v) || v == "." || v == ".." {
		return fmt.Errorf("invalid %s: %q (only letters, digits, dot, dash and underscore; not starting with a dash)", what, v)
	}
	return nil
}

// Host validates the hub's address: a name, an IPv4 or an IPv6 (with colons), never starting with a dash.
func Host(v string) error {
	if !hostRe.MatchString(v) {
		return fmt.Errorf("invalid host: %q (letters, digits, dot, dash or colon; not starting with a dash)", v)
	}
	return nil
}

// Branch validates branch names.
func Branch(v string) error {
	if !branchRe.MatchString(v) || strings.Contains(v, "..") {
		return fmt.Errorf("invalid branch: %q", v)
	}
	return nil
}

// Path validates paths passed to the remote shell (no quotes, no metacharacters). A tilde is accepted inside the path, for
// 8.3 short names (C:/Users/RUNNER~1), but not first: there PowerShell would read it as the home folder.
func Path(what, v string) error {
	if !pathRe.MatchString(v) || strings.Contains(v, "..") {
		return fmt.Errorf("invalid %s: %q", what, v)
	}
	return nil
}
