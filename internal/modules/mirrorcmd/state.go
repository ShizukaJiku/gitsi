package mirrorcmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/fsx"
)

// branchState is what the edge knows of one mirrored branch after its last cycle.
type branchState struct {
	Tip       string    `json:"tip,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	ChangedAt time.Time `json:"changed_at,omitzero"` // when the bridge last received a new tip
	Error     string    `json:"error,omitempty"`
}

// state is ~/.gitsi/mirror-state.json: repo -> branch -> state. Only mirror commands write it, under the lock.
type state map[string]map[string]branchState

func statePath() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".gitsi", "mirror-state.json"), err
}

// loadState reads the state; a missing or damaged file is an empty state (it only describes past cycles).
func loadState() state {
	s := state{}
	if p, err := statePath(); err == nil {
		if b, err := fsx.ReadFileRetry(p); err == nil {
			_ = json.Unmarshal(b, &s)
		}
	}
	return s
}

func (s state) save() error {
	p, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(p, b, 0o600)
}

func (s state) get(repo, branch string) branchState { return s[repo][branch] }

func (s state) set(repo, branch string, b branchState) {
	if s[repo] == nil {
		s[repo] = map[string]branchState{}
	}
	s[repo][branch] = b
}

var errNothing = errors.New("nothing is mirrored yet: gitsi mirror add <name> <origin-url> -branch <branch>")
