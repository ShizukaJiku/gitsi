package mailbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Begin starts or resumes an upload. It returns the offset to continue from and whether the item was already complete.
func (s *Store) Begin(b Box, it Item) (offset int64, complete bool, err error) {
	if err := checkRef(b, it.ID); err != nil {
		return 0, false, err
	}
	if err := it.Validate(s.max()); err != nil {
		return 0, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := readMeta(s.dir(b, it.ID)); err == nil {
		return it.Size, true, nil // already received: uploading again is idempotent
	}
	s.pruneParts(b, it.ID)
	pd := s.part(b, it.ID)
	if st, err := os.Stat(filepath.Join(pd, "data")); err == nil {
		if prev, perr := readMeta(pd); perr == nil && prev.ID == it.ID && prev.SHA256 == it.SHA256 && st.Size() <= it.Size {
			// resuming counts as activity: another upload starting now must not clean this one up as stale
			now := s.now()
			_ = os.Chtimes(filepath.Join(pd, "data"), now, now)
			return st.Size(), false, nil
		}
		_ = os.RemoveAll(pd) // inconsistent partial: start over
	}
	if err := os.MkdirAll(pd, 0o700); err != nil {
		return 0, false, err
	}
	it.Created = s.now()
	mb, _ := json.Marshal(it)
	if err := os.WriteFile(filepath.Join(pd, "meta.json"), mb, 0o600); err != nil {
		return 0, false, err
	}
	if err := os.WriteFile(filepath.Join(pd, "data"), nil, 0o600); err != nil {
		return 0, false, err
	}
	return 0, false, nil
}

// PartialMaxAge is how long a half-done upload is kept without receiving data: past that, nobody will resume it and it only takes
// disk space (a cancelled multi-GB send would otherwise stay forever).
const PartialMaxAge = 7 * 24 * time.Hour

// PruneInterval is how often, at most, a box is walked looking for expired partials (a variable for tests).
var PruneInterval = 10 * time.Minute

// pruneParts removes a box's half-done uploads that have received no data for more than PartialMaxAge, except `keep` (the one
// being started or resumed). It runs while starting an upload: no separate process is needed. It must be called with s.mu held.
// It is best effort: a cleanup error never blocks the current upload.
func (s *Store) pruneParts(b Box, keep string) {
	if now := s.now(); !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < PruneInterval {
		return // walking the folder on every upload start, with the lock held, would slow the others down
	}
	s.lastPrune = s.now()
	root := filepath.Join(s.Root, string(b))
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := s.now().Add(-PartialMaxAge)
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, ".part-") || name == ".part-"+keep {
			continue
		}
		last := filepath.Join(root, name, "data")
		st, err := os.Stat(last)
		if err != nil { // no data: measured by the folder
			if st, err = os.Stat(filepath.Join(root, name)); err != nil {
				continue
			}
		}
		if st.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(root, name))
		}
	}
}

// Append adds a block at the exact offset, so out-of-order retries never corrupt a partial.
func (s *Store) Append(b Box, id string, offset int64, data []byte) (int64, error) {
	if err := checkRef(b, id); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pd := s.part(b, id)
	it, err := readMeta(pd)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(filepath.Join(pd, "data"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size() != offset {
		return st.Size(), fmt.Errorf("%w (have %d, got %d)", ErrOffset, st.Size(), offset)
	}
	if offset+int64(len(data)) > it.Size {
		return st.Size(), errors.New("the block exceeds the declared size")
	}
	if _, err := f.Write(data); err != nil {
		return st.Size(), err
	}
	return offset + int64(len(data)), nil
}

// Commit verifies size and SHA256 and publishes the item. On a mismatch it discards the partial.
func (s *Store) Commit(b Box, id string) (Item, error) {
	if err := checkRef(b, id); err != nil {
		return Item{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pd := s.part(b, id)
	it, err := readMeta(pd)
	if err != nil {
		return Item{}, err
	}
	sum, size, err := hashFile(filepath.Join(pd, "data"))
	if err != nil {
		return Item{}, err
	}
	if size != it.Size {
		return Item{}, fmt.Errorf("data is missing: have %d of %d bytes", size, it.Size)
	}
	if sum != it.SHA256 {
		_ = os.RemoveAll(pd)
		return Item{}, ErrHash
	}
	if err := os.Rename(pd, s.dir(b, id)); err != nil {
		return Item{}, err
	}
	return it, nil
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
