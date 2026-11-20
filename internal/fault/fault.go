// Package fault defines errors that carry a category, a cause and a suggested action, and recognizes the usual ssh/scp/git
// failures from their stderr. It is a leaf package (it depends on nothing else in the project) so any layer can use it.
//
// A plain error says WHAT happened; a fault.Error also says WHY and WHAT TO DO, and carries an exit code that is stable per
// category so a script can tell "network down" apart from "key rejected".
package fault

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Kind is the category of a failure.
type Kind int

const (
	Other         Kind = iota // uncategorized
	NotConfigured             // this machine's configuration is missing or invalid
	Unreachable               // the hub does not answer (network, firewall, powered off)
	Auth                      // the hub rejected the key
	HostKey                   // the hub's host key does not match the known one
	HubBinary                 // gitsi is not published on the hub
	FeatureOff                // optional feature disabled (opt-in)
	Integrity                 // the content does not match its hash
	Timeout                   // the operation exceeded its deadline
)

var names = map[Kind]string{
	Other: "error", NotConfigured: "configuration", Unreachable: "network", Auth: "authentication", HostKey: "hub host key",
	HubBinary: "gitsi on the hub", FeatureOff: "feature disabled", Integrity: "integrity", Timeout: "timeout",
}

func (k Kind) String() string { return names[k] }

// ExitCode is the process exit code for this category. The codes are part of the CLI contract (see docs/ARCHITECTURE.md).
func (k Kind) ExitCode() int {
	switch k {
	case NotConfigured:
		return 10
	case Unreachable:
		return 11
	case Auth:
		return 12
	case HostKey:
		return 13
	case HubBinary:
		return 14
	case FeatureOff:
		return 15
	case Integrity:
		return 16
	case Timeout:
		return 17
	}
	return 1
}

// Error is a failure with a category, a message, a suggested action and the original cause.
type Error struct {
	Kind Kind
	Msg  string
	Hint string // what to do; may be empty
	Err  error  // original cause (stderr, system error...)
}

func (e *Error) Error() string {
	if e.Msg == "" && e.Err != nil {
		return e.Err.Error()
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// New creates a categorized failure.
func New(kind Kind, msg, hint string) *Error { return &Error{Kind: kind, Msg: msg, Hint: hint} }

// As extracts an *Error from the error chain.
func As(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// Where describes what was being connected to, so hints can be specific.
type Where struct {
	Host    string
	KeyFile string
}

type rule struct {
	re   *regexp.Regexp
	make func(w Where, line string) *Error
}

func rx(p string) *regexp.Regexp { return regexp.MustCompile("(?i)" + p) }

func orUnknown(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// Rules are evaluated in order: the first match wins. The texts are OpenSSH's (always in English) and PowerShell's when the hub
// lacks the binary (PowerShell localizes that one, so the Spanish form is matched too).
var rules = []rule{
	{rx(`remote host identification has changed|host key verification failed|someone could be eavesdropping`), func(w Where, _ string) *Error {
		return New(HostKey, "the hub's host key does not match the one this machine knew",
			"if you reinstalled the hub, remove the old key with: ssh-keygen -R "+orUnknown(w.Host, "<host>")+" . If you did NOT reinstall it, stop: it may not be your hub")
	}},
	// a missing key is the most specific cause: ssh also prints "Permission denied" and must not win over it
	{rx(`identity file .* not accessible|no such identity|load key .*: no such file|could not open a connection to your authentication agent`), func(w Where, _ string) *Error {
		return New(NotConfigured, "the configured SSH key cannot be found", "check 'gitsi config show' or run again: gitsi setup edge -host <hub-ip> -user <user>")
	}},
	{rx(`permission denied \((publickey|password|keyboard)|permission denied, please try again|too many authentication failures`), func(w Where, _ string) *Error {
		return New(Auth, "the hub rejected this machine's key",
			"the key ("+orUnknown(w.KeyFile, "the configured one")+") is not registered on the hub. On the hub: gitsi setup hub -edge-ip <this-machine-ip> -public-key-file <key.pub> -user <user>")
	}},
	{rx(`bad owner or permissions on|unprotected private key file|bad permissions`), func(Where, string) *Error {
		return New(NotConfigured, "ssh refused a file because of unsafe permissions", "only your user may read your key and your ~/.ssh/config")
	}},
	{rx(`could not resolve hostname|name or service not known|no such host is known|nodename nor servname|temporary failure in name resolution`), func(w Where, _ string) *Error {
		return New(NotConfigured, "the hub's name or alias does not resolve",
			"check the saved host with 'gitsi config show' (use the hub's IP) or run again: gitsi setup edge -host <hub-ip> -user <user>")
	}},
	{rx(`connection timed out|operation timed out|connection refused|no route to host|network is unreachable|connection reset|connection closed by|kex_exchange_identification|banner exchange|ssh: connect to host`), func(w Where, _ string) *Error {
		return New(Unreachable, "could not connect to the hub ("+orUnknown(w.Host, "?")+")",
			"check that the hub is on, reachable from this network (a VPN may hide the local network) and that its firewall admits this machine. Details: gitsi check")
	}},
	{rx(`(is not recognized as the name of a cmdlet|no se reconoce como nombre de un cmdlet|is not recognized as an internal or external command).*|cannot find (the )?path .*gitsi`), func(Where, string) *Error {
		return New(HubBinary, "the hub has no gitsi published at the expected path", "on the hub: gitsi update -publish (and gitsi setup hub if it was never set up)")
	}},
}

// Classify recognizes an ssh/scp/git failure from its stderr. It returns nil when it does not recognize the text, so the caller
// keeps its original message (information is never hidden).
func Classify(stderr string, w Where) *Error {
	text := strings.TrimSpace(stderr)
	if text == "" {
		return nil
	}
	for _, r := range rules {
		if r.re.MatchString(text) {
			e := r.make(w, text)
			e.Err = errors.New(lastLines(text, 3))
			return e
		}
	}
	return nil
}

// lastLines keeps the end of stderr (the most informative part) to attach it as the cause.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	var keep []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "Warning: Permanently added") { // usual ssh noise, never the cause
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " | ")
}

// Format returns the message ready to print: the cause and, when there is one, the suggested action.
func Format(e *Error) string {
	s := e.Error()
	if e.Err != nil && e.Msg != "" {
		s += fmt.Sprintf(" [%s]", e.Err)
	}
	if e.Hint != "" {
		s += "\n  -> " + e.Hint
	}
	return s
}
