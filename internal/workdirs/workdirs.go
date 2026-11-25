// Package workdirs remembers where this machine keeps the working folders of the bridge's repos, so the tray can list them,
// show their state and sync them without the user opening a terminal in each one. `repo share`, `repo get` and `repo sync` record
// the folder they work in; a folder that no longer exists is forgotten. Nothing piles up: at most maxDirs folders are kept.
package workdirs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/fsx"
)

// Dir is a working folder and the bridge repo it belongs to.
type Dir struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}

// maxDirs caps the list: the oldest entries go first.
const maxDirs = 50

type file struct {
	Dirs []Dir `json:"dirs"`
}

// Path is ~/.gitsi/workdirs.json.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gitsi", "workdirs.json"), nil
}

// List returns the recorded folders that still exist, sorted by repo and path.
func List() ([]Dir, error) {
	dirs, err := read()
	if err != nil {
		return nil, err
	}
	out := existing(dirs)
	sort.Slice(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Repo, out[j].Repo) {
			return strings.ToLower(out[i].Repo) < strings.ToLower(out[j].Repo)
		}
		return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path)
	})
	return out, nil
}

// Add records a working folder (or refreshes it: the newest entries are kept when the list is full). The path is made absolute;
// folders that no longer exist are dropped on the way.
func Add(repo, path string) error {
	if repo == "" || path == "" {
		return errors.New("a working folder needs its repo and its path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dirs, err := read()
	if err != nil {
		dirs = nil // an unreadable file is replaced: it only holds what the commands record again
	}
	kept := []Dir{}
	for _, d := range existing(dirs) {
		if !strings.EqualFold(filepath.Clean(d.Path), filepath.Clean(abs)) {
			kept = append(kept, d)
		}
	}
	kept = append(kept, Dir{Repo: repo, Path: abs})
	if len(kept) > maxDirs {
		kept = kept[len(kept)-maxDirs:]
	}
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(file{Dirs: kept}, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(p, append(b, '\n'), 0o600)
}

func read() ([]Dir, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Dirs, nil
}

func existing(dirs []Dir) []Dir {
	var out []Dir
	for _, d := range dirs {
		if d.Repo == "" || d.Path == "" {
			continue
		}
		if st, err := os.Stat(d.Path); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return out
}
