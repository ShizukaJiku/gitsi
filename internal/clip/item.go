// Package clip is the clipboard shared between hub and edge (docs/CLIP.md): the item that travels, how it travels inside a watch
// session, the single slot the hub keeps and the logic that reads, filters, sends, receives and writes the clipboard. It knows
// neither the CLI nor ssh nor the Windows API: the clipboard, the change counter and the sending are injected.
package clip

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Cap is the capability a hub announces in its greeting when it shares its clipboard (its clip policy is on).
const Cap = "clip"

// TTL is how long an item stays deliverable: a copy older than this never lands on a clipboard by surprise (the other machine
// was off or disconnected), and the slot holding it is deleted.
const TTL = 10 * time.Minute

// Kinds of item.
const (
	KindText  = "text"  // UTF-8 text
	KindImage = "image" // PNG
	KindFiles = "files" // files and folders copied in Explorer, as a gzip-compressed tar with one top-level entry per item
)

// OriginHub is the origin of what is copied on the hub; an edge's items carry "<user>@<machine>".
const OriginHub = "hub"

// Item describes a clipboard copy. The content travels apart (wire) and is verified against SHA256.
type Item struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Name   string    `json:"name,omitempty"`  // what to show: the first file's name, image.png...
	Count  int       `json:"count,omitempty"` // files: how many top-level entries
	Size   int64     `json:"size"`
	SHA256 string    `json:"sha256"`
	Origin string    `json:"origin,omitempty"` // set by the hub, never taken from what an edge claims
	Time   time.Time `json:"time"`             // when the hub stored it (the hub's clock)
}

var (
	idRe     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	shaRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	originRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}@[A-Za-z0-9._-]{1,64}$`)
)

// NewID returns a random item id.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand does not fail on supported systems
	return hex.EncodeToString(b)
}

// Sum returns the hex SHA256 of data.
func Sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// Valid checks an item that arrives from the other machine (it ends up in file names, the terminal and the tray). max is the
// receiver's size limit.
func (it Item) Valid(max int64) bool {
	switch it.Kind {
	case KindText, KindImage, KindFiles:
	default:
		return false
	}
	if !idRe.MatchString(it.ID) || !shaRe.MatchString(it.SHA256) || it.Size < 0 || it.Size > max || it.Count < 0 {
		return false
	}
	if len(it.Name) > 255 || strings.ContainsFunc(it.Name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	return it.Origin == "" || it.Origin == OriginHub || originRe.MatchString(it.Origin)
}

// Describe is how an item is shown in a line: "text (12 B)", "2 files: report.pdf... (3.1 MB)".
func (it Item) Describe() string {
	switch it.Kind {
	case KindText:
		return "text (" + Human(it.Size) + ")"
	case KindImage:
		return "image (" + Human(it.Size) + ")"
	}
	what, name := "files", it.Name
	if it.Count == 1 {
		what = "file"
	}
	if it.Count > 1 {
		name += "..."
	}
	return fmt.Sprintf("%d %s: %s (%s)", it.Count, what, name, Human(it.Size))
}

// Human formats a size in bytes.
func Human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
