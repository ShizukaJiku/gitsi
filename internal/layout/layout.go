// Package layout describes where gitsi lives on the hub: repos, mailbox, jobs and the binary.
//
// Principle: **the hub decides its layout and announces it; the edge discovers it and never configures it.**
package layout

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// Layout is gitsi's layout on the hub.
type Layout struct {
	Home    string // gitsi's root on the hub; holds the binary, the mailbox and the jobs
	Repos   string // folder of the bridge repos
	Binary  string // gitsi.exe published for the edges
	Mailbox string // mailbox (inbox/outbox)
	Jobs    string // background jobs
}

func slash(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// BinaryName is the name of the executable published on the hub.
const BinaryName = "gitsi.exe"

// ForHub computes the layout from the hub's configuration and its user's home folder.
//
//	Home   = config.home       or  <home folder>/gitsi
//	Repos  = config.repos_dir  or  <Home>/repos
//	Binary, Mailbox and Jobs always live under Home.
//
// That gives the hub owner ONE choice (Home) and a sensible default for everything else.
//
// The paths end up in PowerShell commands, so they must pass safe.Path. A profile folder with an accented letter would not: then
// the default uses its 8.3 short name (C:/Users/JOSPRE~1), the same folder spelled with safe characters. When that does not help
// (8.3 names disabled on the volume, or an apostrophe, which 8.3 names keep) the layout stays invalid and Check says what to do.
func ForHub(cfg *config.Config, userHome string) Layout {
	home := slash(cfg.Home)
	if home == "" {
		uh := slash(userHome)
		if safe.Path("home", uh) != nil {
			if s := slash(ShortPath(userHome)); s != "" && safe.Path("home", s) == nil {
				uh = s
			}
		}
		home = path.Join(uh, "gitsi")
	}
	repos := slash(cfg.ReposDir)
	if repos == "" {
		repos = path.Join(home, "repos")
	}
	return Layout{
		Home:    path.Clean(home),
		Repos:   path.Clean(repos),
		Binary:  path.Join(home, BinaryName),
		Mailbox: path.Join(home, "mailbox"),
		Jobs:    path.Join(home, "jobs"),
	}
}

// ShortPath returns the 8.3 short name of an existing folder, or "" when it has none. A variable so tests do not depend on the
// volume's settings.
var ShortPath = shortPath

// Check validates every path of the layout as FromInfo does on the edge: the hub can say what is wrong before an edge rejects it.
func (l Layout) Check() error {
	for _, f := range []struct{ key, v string }{{keyHome, l.Home}, {keyRepos, l.Repos}, {keyBinary, l.Binary}, {keyMailbox, l.Mailbox}, {keyJobs, l.Jobs}} {
		if err := safe.Path(f.key, f.v); err != nil {
			return err
		}
	}
	return nil
}

// FixHint is the action for a layout that fails Check.
const FixHint = "on the hub: gitsi config set home <folder with letters, digits, spaces, '.', '_' or '-'> (e.g. C:/gitsi)"

// Keys of the Info map in the protocol greeting.
const (
	keyHome    = "layout.home"
	keyRepos   = "layout.repos"
	keyBinary  = "layout.binary"
	keyMailbox = "layout.mailbox"
	keyJobs    = "layout.jobs"
)

// Info turns the layout into the map the hub announces in the greeting.
func (l Layout) Info() map[string]string {
	return map[string]string{keyHome: l.Home, keyRepos: l.Repos, keyBinary: l.Binary, keyMailbox: l.Mailbox, keyJobs: l.Jobs}
}

// FromInfo rebuilds the layout announced by a hub. It returns ErrNotAnnounced when the greeting does not carry one. Every path is
// validated: they end up inside PowerShell commands, so what arrives is never trusted blindly.
func FromInfo(info map[string]string) (Layout, error) {
	if _, ok := info[keyRepos]; !ok {
		return Layout{}, ErrNotAnnounced
	}
	get := func(k string) (string, error) {
		v := info[k]
		if v == "" {
			return "", fmt.Errorf("the hub announced an incomplete layout (%s is missing)", k)
		}
		if err := safe.Path(k, v); err != nil {
			return "", fmt.Errorf("the hub announced an invalid path: %w", err)
		}
		return v, nil
	}
	var l Layout
	var err error
	for _, f := range []struct {
		key string
		dst *string
	}{{keyHome, &l.Home}, {keyRepos, &l.Repos}, {keyBinary, &l.Binary}, {keyMailbox, &l.Mailbox}, {keyJobs, &l.Jobs}} {
		if *f.dst, err = get(f.key); err != nil {
			return Layout{}, err
		}
	}
	return l, nil
}

// ErrNotAnnounced means the hub's greeting does not carry its layout (incomplete answer or an outdated gitsi on the hub).
var ErrNotAnnounced = errors.New("the hub does not announce its layout")
