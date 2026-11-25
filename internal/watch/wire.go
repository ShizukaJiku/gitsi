// Package watch implements the hub's notices to the edge (docs/WATCH.md). The edge opens the session and says what it knows; the
// hub watches its own state and sends TYPED notices of what changes. Commands never travel: the edge decides what to do with each
// known type and ignores the rest. It is pure domain: it knows neither the CLI nor ssh (it receives an already negotiated
// io.ReadWriter).
package watch

import (
	"regexp"
	"strings"
	"time"
)

// Cap is the capability a hub that can serve watch announces in its greeting.
const Cap = "watch"

// Notice types.
const (
	EvHello    = "hello"            // on connect: the hub's version
	EvBaseline = "baseline"         // first connection: what already exists, so it is not shown as new
	EvUpdate   = "update.available" // another binary is published on the hub
	EvOutbox   = "outbox.item"      // an item appeared in the outbox
	EvJobDone  = "job.finished"     // a job finished
	EvRepo     = "repo.updated"     // a bridge branch changed (or is new)

	EvClipStored  = "clip.stored"  // the hub stored the edge's clipboard item in its slot
	EvClipRefused = "clip.refused" // a clipboard item was not stored (from the edge) or not delivered (to the edge), and why
)

// Request is what the edge sends when subscribing: who it is and what it knows.
type Request struct {
	Edge      string   `json:"edge"`      // machine name
	User      string   `json:"user"`      // user
	Version   string   `json:"version"`   // the edge's gitsi version
	SHA256    string   `json:"sha256"`    // hash of the installed gitsi
	Announced string   `json:"announced"` // last published hash it was already notified about
	Outbox    []string `json:"outbox"`    // outbox ids already seen
	Jobs      []string `json:"jobs"`      // jobs whose end was already notified
	// Heads are the bridge branch tips the edge already knows: "repo/branch" -> commit.
	Heads     map[string]string `json:"heads"`
	First     bool              `json:"first"`     // no saved state: asks for a baseline
	Reactions []string          `json:"reactions"` // active reactions (names only, for 'gitsi edges')

	// The shared clipboard (docs/CLIP.md). Clip asks for it; the rest only matters then.
	Clip bool `json:"clip,omitempty"`
	// ClipAgeMS is how long ago, in milliseconds, this edge's watch started: only what is copied after that is delivered. A
	// duration instead of a time, so the two machines' clocks do not need to agree.
	ClipAgeMS int64  `json:"clip_age_ms,omitempty"`
	ClipSeen  string `json:"clip_seen,omitempty"` // the last item already received (a reconnection does not repeat it)
	ClipMax   int64  `json:"clip_max,omitempty"`  // the largest item this edge accepts
}

// Event is a notice from the hub. Data only: the fields its type does not use stay empty.
type Event struct {
	Type    string            `json:"type"`
	Time    time.Time         `json:"time"`
	Version string            `json:"version,omitempty"` // hello, update.available
	SHA256  string            `json:"sha256,omitempty"`  // update.available, baseline
	ID      string            `json:"id,omitempty"`      // outbox.item, job.finished
	Kind    string            `json:"kind,omitempty"`    // outbox.item: file | dir | text
	Name    string            `json:"name,omitempty"`    // outbox.item
	Size    int64             `json:"size,omitempty"`    // outbox.item
	Exit    string            `json:"exit,omitempty"`    // job.finished
	Outbox  []string          `json:"outbox,omitempty"`  // baseline
	Jobs    []string          `json:"jobs,omitempty"`    // baseline
	Heads   map[string]string `json:"heads,omitempty"`   // baseline: "repo/branch" -> commit
	Repo    string            `json:"repo,omitempty"`    // repo.updated
	Branch  string            `json:"branch,omitempty"`  // repo.updated
	Commit  string            `json:"commit,omitempty"`  // repo.updated
	Detail  string            `json:"detail,omitempty"`  // clip.refused: why
}

// HeadKey is the key of a branch tip in Heads: the repo name has no "/", so the split is at the first one.
func HeadKey(repo, branch string) string { return repo + "/" + branch }

var (
	itemIDRe = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
	shaRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	exitRe   = regexp.MustCompile(`^-?[0-9]{1,10}$`)
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]{0,199}$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// validHead checks a repo, a branch and a commit that arrive from the hub (they end up in the terminal and in a git fetch on the
// edge).
func validHead(repo, branch, commit string) bool {
	return repoRe.MatchString(repo) && branchRe.MatchString(branch) && !strings.Contains(branch, "..") && commitRe.MatchString(commit)
}

// Valid checks a received notice: one odd field discards the whole notice (what arrives ends up in the edge's terminal).
func (e Event) Valid() bool {
	switch e.Type {
	case EvHello:
		return len(e.Version) <= 80
	case EvBaseline:
		for _, id := range append(append([]string{}, e.Outbox...), e.Jobs...) {
			if !itemIDRe.MatchString(id) {
				return false
			}
		}
		for k, c := range e.Heads {
			repo, branch, ok := strings.Cut(k, "/")
			if !ok || !validHead(repo, branch, c) {
				return false
			}
		}
		return e.SHA256 == "" || shaRe.MatchString(e.SHA256)
	case EvRepo:
		return validHead(e.Repo, e.Branch, e.Commit)
	case EvUpdate:
		return shaRe.MatchString(e.SHA256) && len(e.Version) <= 80
	case EvOutbox:
		return itemIDRe.MatchString(e.ID) && (e.Kind == "file" || e.Kind == "dir" || e.Kind == "text") && e.Size >= 0 && len(e.Name) <= 255
	case EvJobDone:
		return itemIDRe.MatchString(e.ID) && exitRe.MatchString(e.Exit)
	case EvClipStored:
		return itemIDRe.MatchString(e.ID)
	case EvClipRefused:
		return itemIDRe.MatchString(e.ID) && len(e.Detail) <= 300
	}
	return false // unknown type: ignored
}

// Printable makes a text fit for the terminal: no control characters (a file name cannot move the cursor or change colors) and
// truncated.
func Printable(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			b.WriteRune('?')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len([]rune(out)) > max {
		out = string([]rune(out)[:max]) + "..."
	}
	return out
}

// ValidIdentity validates the machine or user name the edge sends (it ends up in a file name on the hub).
func ValidIdentity(s string) bool { return nameRe.MatchString(s) }
