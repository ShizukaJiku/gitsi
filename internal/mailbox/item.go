package mailbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// Box is a mailbox: Inbox receives what the edge sends; Outbox keeps what the hub leaves for the edge.
type Box string

const (
	Inbox  Box = "inbox"
	Outbox Box = "outbox"
)

// Valid reports whether the box exists.
func (b Box) Valid() bool { return b == Inbox || b == Outbox }

// Kind is the content type of an item.
type Kind string

const (
	File Kind = "file"
	Dir  Kind = "dir" // tar archive of a folder
	Text Kind = "text"
)

// Valid reports whether the kind exists.
func (k Kind) Valid() bool { return k == File || k == Dir || k == Text }

// DefaultMaxSize is the per-item cap when no other is configured.
const DefaultMaxSize int64 = 4 << 30

// Item is an item's metadata.
type Item struct {
	ID      string    `json:"id"`
	Kind    Kind      `json:"kind"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	SHA256  string    `json:"sha256"`
	Created time.Time `json:"created"`
	// Enc is the encoding of the stored content ("" = as is, EncGzip = compressed tar). Size and SHA256 describe the bytes that
	// travel, already encoded, so the hub verifies them without knowing about compression.
	Enc string `json:"enc,omitempty"`
}

// EncGzip marks a Dir item whose tar travels gzip-compressed.
const EncGzip = "gzip"

// Typed errors so upper layers decide the message.
var (
	ErrNotFound = errors.New("the item does not exist")
	ErrOffset   = errors.New("the offset does not match what was already received")
	ErrTooLarge = errors.New("the item exceeds the maximum allowed size")
	ErrHash     = errors.New("the content hash does not match")
)

var (
	idRe  = regexp.MustCompile(`^[0-9a-f]{16}$`)
	shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// NewID derives an item's id: deterministic, so an interrupted upload resumes with the same id.
func NewID(kind Kind, name, sha string) string {
	h := sha256.Sum256([]byte(string(kind) + "\x00" + name + "\x00" + sha))
	return hex.EncodeToString(h[:])[:16]
}

// Validate checks a complete item before accepting it.
func (it Item) Validate(max int64) error {
	if !it.Kind.Valid() {
		return fmt.Errorf("invalid kind: %q", it.Kind)
	}
	if err := safe.FileName(it.Name); err != nil {
		return err
	}
	if !shaRe.MatchString(it.SHA256) {
		return errors.New("invalid sha256")
	}
	if it.Size < 0 {
		return errors.New("negative size")
	}
	if it.Size > max {
		return fmt.Errorf("%w (%d > %d)", ErrTooLarge, it.Size, max)
	}
	if it.ID != NewID(it.Kind, it.Name, it.SHA256) {
		return errors.New("the id does not match the content")
	}
	switch it.Enc {
	case "":
	case EncGzip:
		if it.Kind != Dir {
			return fmt.Errorf("the %q encoding only applies to folders, not to %q", it.Enc, it.Kind)
		}
	default:
		return fmt.Errorf("unsupported encoding: %q", it.Enc) // rejected at the start, not after downloading everything
	}
	return nil
}
