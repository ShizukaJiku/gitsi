// Package mirror is the hub's record of the origin mirrors each edge keeps: the report an edge sends after each cycle, its
// validation, where the hub keeps it and the handler that receives it. It knows neither the CLI nor ssh. The report only
// describes what the edge did: the hub never answers with a URL or a branch to follow.
package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ShizukaJiku/gitsi/internal/fsx"
	"github.com/ShizukaJiku/gitsi/internal/proto"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// Cap is the capability a hub announces when it records mirror reports.
const Cap = "mirror"

// Report is an edge's whole mirror state after a cycle: every repo it follows, not only the ones the cycle checked, so a repo
// the edge stopped following disappears from the hub.
type Report struct {
	Edge      string      `json:"edge"` // user@machine, as watch names the edge
	CheckedAt time.Time   `json:"checked_at"`
	Repos     []RepoState `json:"repos"`
}

// RepoState is one followed repo.
type RepoState struct {
	Name     string        `json:"name"`           // the bridge repo
	Host     string        `json:"host,omitempty"` // the origin's host (the rest of the URL is the edge's business)
	Branches []BranchState `json:"branches"`
}

// BranchState is one followed branch.
type BranchState struct {
	Branch    string    `json:"branch"`
	Tip       string    `json:"tip,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	ChangedAt time.Time `json:"changed_at,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// Limits of a report: it is small, and a larger one is not a mirror state.
const (
	maxRepos    = 1000
	maxBranches = 100
	maxError    = 500
)

var (
	edgeRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}@[A-Za-z0-9._-]{1,64}$`)
	tipRe  = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// Validate checks a report before the hub keeps or shows it: the edge's name becomes a file name, and every text ends up on the
// hub's terminal.
func (r Report) Validate() error {
	if !edgeRe.MatchString(r.Edge) {
		return fmt.Errorf("invalid edge name: %q", r.Edge)
	}
	if len(r.Repos) > maxRepos {
		return fmt.Errorf("too many repos in the report: %d", len(r.Repos))
	}
	for _, repo := range r.Repos {
		if err := safe.Name("repo", repo.Name); err != nil {
			return err
		}
		if repo.Host != "" {
			if err := safe.Host(repo.Host); err != nil {
				return err
			}
		}
		if len(repo.Branches) > maxBranches {
			return fmt.Errorf("%s: too many branches: %d", repo.Name, len(repo.Branches))
		}
		for _, b := range repo.Branches {
			if err := safe.Branch(b.Branch); err != nil {
				return fmt.Errorf("%s: %w", repo.Name, err)
			}
			if b.Tip != "" && !tipRe.MatchString(b.Tip) {
				return fmt.Errorf("%s %s: invalid tip: %q", repo.Name, b.Branch, b.Tip)
			}
			if utf8.RuneCountInString(b.Error) > maxError || strings.ContainsFunc(b.Error, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
				return fmt.Errorf("%s %s: invalid error text", repo.Name, b.Branch)
			}
		}
	}
	return nil
}

// LocalDir is where an edge keeps its local mirror copies, one bare repo per mirror: <profile>/.gitsi/mirrors.
func LocalDir(profile string) string { return filepath.Join(profile, ".gitsi", "mirrors") }

// Dir is where the hub keeps the reports, inside its home (layout.Layout.Home).
func Dir(home string) string { return filepath.Join(filepath.FromSlash(home), "mirror") }

// Store keeps one report per edge, <Dir>/<edge>.json; each new report replaces the previous one.
type Store struct{ Dir string }

// Put saves a valid report.
func (s Store) Put(r Report) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(s.Dir, r.Edge+".json"), b, 0o600)
}

// List returns the saved reports, by edge. A damaged or invalid file is skipped: it shows nothing rather than something wrong.
func (s Store) List() ([]Report, error) {
	files, err := filepath.Glob(filepath.Join(s.Dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []Report
	for _, f := range files {
		b, err := fsx.ReadFileRetry(f)
		if err != nil {
			continue
		}
		var r Report
		if json.Unmarshal(b, &r) != nil || r.Validate() != nil || r.Edge+".json" != filepath.Base(f) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Edge < out[j].Edge })
	return out, nil
}

// Handler receives the edges' reports on the hub.
type Handler struct{ Store Store }

// Handles implements hub.Router.
func (h *Handler) Handles(k proto.Kind) bool { return k == proto.KindMirror }

// Handle implements hub.Handler: it keeps a valid report and acknowledges it.
func (h *Handler) Handle(_ context.Context, f proto.Frame, w io.Writer) error {
	var r Report
	if err := json.Unmarshal(f.Payload, &r); err != nil {
		return fmt.Errorf("invalid mirror report: %w", err)
	}
	if err := h.Store.Put(r); err != nil {
		return err
	}
	return proto.WriteJSON(w, proto.KindOK, struct{}{})
}

// Send delivers a report over a negotiated session and waits for the hub's acknowledgement.
func Send(rw io.ReadWriter, r Report) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := proto.WriteJSON(rw, proto.KindMirror, r); err != nil {
		return err
	}
	_, err := proto.Expect(rw, proto.KindOK)
	return err
}

// ErrNoCap means the hub's gitsi does not record mirror reports (it is older).
var ErrNoCap = errors.New("the hub's gitsi does not record mirror state (it is older than 1.5.0): on the hub, gitsi update -publish")
