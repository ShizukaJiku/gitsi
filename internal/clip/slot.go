package clip

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/fsx"
)

// Slot is the hub's single clipboard slot (<hub home>/clip): the latest copy, from the hub or from an edge, replaced by each new
// one. It is the meeting point of the processes that touch it (each edge's watch session and the hub's own watch), which only
// share files: slot.json describes the item and <id>.bin holds its content.
//
// The content is written first and slot.json is then replaced atomically: whoever reads sees the old item or the new one, never
// half of one.
type Slot struct {
	Dir string

	mu      sync.Mutex
	modTime time.Time // slot.json's date and size when cached was read: it is checked every 200 ms
	size    int64
	cached  Item
	have    bool
}

const slotFile = "slot.json"

// Put stores an item and its content, replacing the previous one.
func (s *Slot) Put(it Item, data []byte) error {
	if !idRe.MatchString(it.ID) {
		return fmt.Errorf("invalid item id %q", it.ID)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	prev, hadPrev := s.read()
	if err := fsx.WriteFileAtomic(s.bin(it.ID), data, 0o600); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(it, "", "  ")
	if err := fsx.WriteFileAtomic(filepath.Join(s.Dir, slotFile), b, 0o600); err != nil {
		_ = os.Remove(s.bin(it.ID))
		return err
	}
	if hadPrev && prev.ID != it.ID {
		_ = os.Remove(s.bin(prev.ID))
	}
	return nil
}

// Current returns the item in the slot. An expired one (older than TTL) is deleted, with any content another Put left behind,
// and is not returned.
func (s *Slot) Current(now time.Time) (Item, bool) {
	it, ok := s.read()
	if !ok {
		return Item{}, false
	}
	if now.Sub(it.Time) > TTL {
		s.clear()
		return Item{}, false
	}
	return it, true
}

// Data reads an item's content and verifies it. When the item was replaced in the meantime its content is gone: the error says
// so and the caller waits for the next one.
func (s *Slot) Data(it Item) ([]byte, error) {
	b, err := os.ReadFile(s.bin(it.ID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("the clipboard item was replaced by a newer one")
	}
	if err != nil {
		return nil, err
	}
	if Sum(b) != it.SHA256 {
		return nil, errors.New("the clipboard item's content does not match its SHA256")
	}
	return b, nil
}

func (s *Slot) bin(id string) string { return filepath.Join(s.Dir, id+".bin") }

// read returns slot.json's item, read again only when the file's date or size changed.
func (s *Slot) read() (Item, bool) {
	p := filepath.Join(s.Dir, slotFile)
	st, err := os.Stat(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.have = false
		return Item{}, false
	}
	if s.have && st.ModTime().Equal(s.modTime) && st.Size() == s.size {
		return s.cached, true
	}
	b, err := fsx.ReadFileRetry(p)
	if err != nil {
		return Item{}, false
	}
	var it Item
	if json.Unmarshal(b, &it) != nil || !idRe.MatchString(it.ID) {
		return Item{}, false
	}
	s.cached, s.modTime, s.size, s.have = it, st.ModTime(), st.Size(), true
	return it, true
}

// clear deletes the slot and every content file (an expired slot leaves nothing behind).
func (s *Slot) clear() {
	_ = os.Remove(filepath.Join(s.Dir, slotFile))
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bin") {
			_ = os.Remove(filepath.Join(s.Dir, e.Name()))
		}
	}
	s.mu.Lock()
	s.have = false
	s.mu.Unlock()
}
