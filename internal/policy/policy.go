// Package policy enforces the feature policy and records what is done (audit). It never inspects content: it only records the
// action, the item or port, and the size.
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
)

// Feature is a sensitive feature the policy can enable.
type Feature string

const (
	Send     Feature = "send"
	Take     Feature = "take"
	Services Feature = "services"
	Exec     Feature = "exec"
	Clip     Feature = "clip"
)

// All lists the optional features, in the order they are shown.
var All = []Feature{Send, Take, Services, Exec, Clip}

// Check returns an error when the feature is not enabled in the configuration.
func Check(p config.Policy, f Feature) error {
	ok := map[Feature]bool{Send: p.AllowSend, Take: p.AllowTake, Services: p.AllowServices, Exec: p.AllowExec, Clip: p.AllowClip}[f]
	if !ok {
		return fault.New(fault.FeatureOff, fmt.Sprintf("the %q feature is disabled: it is optional and must be enabled explicitly", f),
			"gitsi policy set "+string(f)+" on")
	}
	return nil
}

// Audit appends a line to ~/.gitsi/audit.log when auditing is on.
func Audit(p config.Policy, action, detail string) error {
	if !p.Audit {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".gitsi")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "audit.log")
	rotate(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\n", time.Now().Format(time.RFC3339), oneField(action), oneField(detail))
	return err
}

// oneField keeps a value with tabs or newlines from forging another log entry (one line = one fact).
func oneField(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// AuditMaxSize is the size past which audit.log becomes audit.log.1 (replacing the previous one) and a new one starts. The record
// never takes more than about twice this size.
var AuditMaxSize int64 = 1 << 20

func rotate(path string) {
	if st, err := os.Stat(path); err != nil || st.Size() < AuditMaxSize {
		return
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}

// Field returns the configuration switch that belongs to a feature.
func Field(p *config.Policy, f Feature) (*bool, error) {
	switch f {
	case Send:
		return &p.AllowSend, nil
	case Take:
		return &p.AllowTake, nil
	case Services:
		return &p.AllowServices, nil
	case Exec:
		return &p.AllowExec, nil
	case Clip:
		return &p.AllowClip, nil
	}
	return nil, fmt.Errorf("unknown feature %q (send, take, services, exec, clip)", f)
}

// Effects describes, in one line, what each optional feature enables: confirming is a conscious act, not a formality.
var Effects = map[Feature]string{
	Send:     "lets this machine send files, folders and text to the hub's mailbox.",
	Take:     "lets this machine receive what the hub leaves in its outbox.",
	Services: "lets this machine open local tunnels (127.0.0.1) to the hub's services.",
	Exec:     "lets this machine run commands on the hub and start background jobs.",
	Clip: "lets 'gitsi watch' share this machine's clipboard with the other one: a hotkey (or every copy, with clip.auto) sends " +
		"what is copied, and what the other machine sends is put on the clipboard.",
}

// Set turns a feature on or off, saves the configuration and records it in the audit log.
func Set(cfg *config.Config, f Feature, on bool) error {
	v, err := Field(&cfg.Policy, f)
	if err != nil {
		return err
	}
	*v = on
	if err := cfg.Save(); err != nil {
		return err
	}
	state := "off"
	if on {
		state = "on"
	}
	_ = Audit(cfg.Policy, "policy.set", string(f)+"="+state)
	return nil
}
