// Package cleanup gathers gitsi's retention rules: what is removed automatically, when and where. There are no background
// processes: cleanup is triggered by using gitsi (when a session opens on the hub, when the mailbox is used, when receiving), at
// most once per Interval, and `gitsi clean` runs it on demand (with -dry-run it only shows it).
package cleanup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/mirror"
)

// Retention periods.
const (
	MailboxMaxAge = 14 * 24 * time.Hour // hub mailbox items (inbox and outbox)
	JobsMaxAge    = 14 * 24 * time.Hour // FINISHED jobs (running ones are never touched)
	PartMaxAge    = 7 * 24 * time.Hour  // abandoned partial downloads
	ClipMaxAge    = 7 * 24 * time.Hour  // copies left by 'take -clip'
	// MirrorReportMaxAge: an edge's mirror report on the hub that no cycle renewed (the edge stopped mirroring, or is gone).
	MirrorReportMaxAge = 14 * 24 * time.Hour
	// MirrorOrphanMinAge: a local mirror copy of a repo no longer followed is left alone this long (it may be a mirror add in
	// progress, whose configuration is saved at the end).
	MirrorOrphanMinAge = 24 * time.Hour
	Interval           = time.Hour // automatic cleanup never runs more often than this
)

// Removed is something that was removed (or would be, with dry-run).
type Removed struct {
	What string        `json:"what"` // "inbox", "outbox", "job", "partial", "clipboard", "mirror report", "mirror copy"
	Name string        `json:"name"`
	Size int64         `json:"size"`
	Age  time.Duration `json:"age_ns"`
}

// Due reports whether cleanup is due (the marker is older than Interval) and, if so, renews it. That way several sessions in a row
// do not walk the folders every time.
func Due(marker string, now time.Time) bool {
	if st, err := os.Stat(marker); err == nil && now.Sub(st.ModTime()) < Interval {
		return false
	}
	_ = os.MkdirAll(filepath.Dir(marker), 0o700)
	if f, err := os.Create(marker); err == nil {
		f.Close()
		_ = os.Chtimes(marker, now, now)
	}
	return true
}

// Mailbox removes the mailbox items created before now-MailboxMaxAge.
func Mailbox(st *mailbox.Store, now time.Time, dry bool) []Removed {
	var out []Removed
	for _, b := range []mailbox.Box{mailbox.Inbox, mailbox.Outbox} {
		items, err := st.List(b)
		if err != nil {
			continue
		}
		for _, it := range items {
			if age := now.Sub(it.Created); age > MailboxMaxAge {
				if dry || st.Remove(b, it.ID) == nil {
					out = append(out, Removed{What: string(b), Name: it.Name + " (" + it.ID[:8] + ")", Size: it.Size, Age: age})
				}
			}
		}
	}
	return out
}

// Jobs removes the folders of jobs that finished (they have an 'exit' file) more than JobsMaxAge ago. A job without 'exit'
// (running, or lost) is never touched.
func Jobs(dir string, now time.Time, dry bool) []Removed {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Removed
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		jd := filepath.Join(dir, e.Name())
		st, err := os.Stat(filepath.Join(jd, "exit"))
		if err != nil {
			continue
		}
		if age := now.Sub(st.ModTime()); age > JobsMaxAge {
			size := dirSize(jd)
			if dry || os.RemoveAll(jd) == nil {
				out = append(out, Removed{What: "job", Name: e.Name(), Size: size, Age: age})
			}
		}
	}
	return out
}

// Parts removes the partial downloads of 'take' (.gitsi-<id>.part) abandoned in dir more than PartMaxAge ago. Recent ones are
// kept: 'take' resumes from them.
func Parts(dir string, now time.Time, dry bool) []Removed {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Removed
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, ".gitsi-") || !strings.HasSuffix(n, ".part") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if age := now.Sub(info.ModTime()); age > PartMaxAge {
			if dry || os.Remove(filepath.Join(dir, n)) == nil {
				out = append(out, Removed{What: "partial", Name: n, Size: info.Size(), Age: age})
			}
		}
	}
	return out
}

// Clips removes the copies left by 'take -clip' (one folder per item under root) older than ClipMaxAge.
func Clips(root string, now time.Time, dry bool) []Removed {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Removed
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() {
			continue
		}
		if age := now.Sub(info.ModTime()); age > ClipMaxAge {
			p := filepath.Join(root, e.Name())
			size := dirSize(p)
			if dry || os.RemoveAll(p) == nil {
				out = append(out, Removed{What: "clipboard", Name: e.Name(), Size: size, Age: age})
			}
		}
	}
	return out
}

func dirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// Hub applies the hub's retention: mailbox and jobs, in the folders the hub itself decides.
func Hub(lay layout.Layout, now time.Time, dry bool) []Removed {
	st := &mailbox.Store{Root: filepath.FromSlash(lay.Mailbox)}
	out := append(Mailbox(st, now, dry), Jobs(filepath.FromSlash(lay.Jobs), now, dry)...)
	return append(out, MirrorReports(mirror.Dir(lay.Home), now, dry)...)
}

// MirrorReports removes the edges' mirror reports (internal/mirror) older than MirrorReportMaxAge: each cycle renews its edge's,
// so an old one belongs to an edge that no longer mirrors, and would otherwise show as stale for ever.
func MirrorReports(dir string, now time.Time, dry bool) []Removed {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []Removed
	for _, p := range files {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if age := now.Sub(info.ModTime()); age > MirrorReportMaxAge {
			if dry || os.Remove(p) == nil {
				out = append(out, Removed{What: "mirror report", Name: strings.TrimSuffix(filepath.Base(p), ".json"), Size: info.Size(), Age: age})
			}
		}
	}
	return out
}

// Mirrors removes the edge's local mirror copies (<dir>/<name>.git) of repos it no longer follows (keep: the followed names, without
// case, as config.Mirror compares them). They are left behind when a mirror is removed from config.json by hand, or when gitsi
// mirror rm could not finish.
func Mirrors(dir string, keep []string, now time.Time, dry bool) []Removed {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Removed
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".git")
		if !e.IsDir() || !ok || slices.ContainsFunc(keep, func(k string) bool { return strings.EqualFold(k, name) }) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if age := now.Sub(info.ModTime()); age > MirrorOrphanMinAge {
			p := filepath.Join(dir, e.Name())
			size := dirSize(p)
			if dry || os.RemoveAll(p) == nil {
				out = append(out, Removed{What: "mirror copy", Name: name, Size: size, Age: age})
			}
		}
	}
	return out
}

// HubAuto is the hub's automatic cleanup: at most once per Interval (marker in <home>/.cleanup).
func HubAuto(lay layout.Layout, now time.Time) []Removed {
	if !Due(filepath.Join(filepath.FromSlash(lay.Home), ".cleanup"), now) {
		return nil
	}
	return Hub(lay, now, false)
}

// ClipRoot is where 'take -clip' leaves files and folders to paste: %LOCALAPPDATA%\gitsi\clipboard.
func ClipRoot() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "gitsi", "clipboard"), nil
}

// ReceiveDir is where 'take' leaves what it receives when -dir is not given: %USERPROFILE%\Downloads\gitsi.
func ReceiveDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads", "gitsi")
}
