// Package mailbox is the hub's mailbox store: items (file, folder, text) that travel one way per box, always on the edge's
// initiative. It is content-agnostic: it only knows name, kind, size and hash.
//
// On-disk layout:
//
//	<root>/<box>/<id>/{meta.json,data}        complete, verified item
//	<root>/<box>/.part-<id>/{meta.json,data}  upload in progress (resumable)
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// Store manages the boxes under Root.
type Store struct {
	Root    string
	MaxSize int64 // 0 = DefaultMaxSize
	Now     func() time.Time

	mu        sync.Mutex
	lastPrune time.Time
}

func (s *Store) max() int64 {
	if s.MaxSize > 0 {
		return s.MaxSize
	}
	return DefaultMaxSize
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Store) dir(b Box, id string) string { return filepath.Join(s.Root, string(b), id) }

func (s *Store) part(b Box, id string) string { return filepath.Join(s.Root, string(b), ".part-"+id) }

func checkRef(b Box, id string) error {
	if !b.Valid() {
		return fmt.Errorf("invalid box: %q", b)
	}
	if !idRe.MatchString(id) {
		return fmt.Errorf("invalid id: %q", id)
	}
	return nil
}

func readMeta(dir string) (Item, error) {
	var it Item
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if errors.Is(err, os.ErrNotExist) {
		return it, ErrNotFound
	}
	if err != nil {
		return it, err
	}
	return it, json.Unmarshal(b, &it)
}

// Add stores a complete content locally, as is. It computes the hash while copying.
func (s *Store) Add(b Box, kind Kind, name string, r io.Reader) (Item, error) {
	return s.AddEnc(b, kind, "", name, r)
}

// AddEnc is Add with the content's encoding ("" or EncGzip): the hub stores the compressed folders it leaves with `send` this way.
func (s *Store) AddEnc(b Box, kind Kind, enc, name string, r io.Reader) (Item, error) {
	if !b.Valid() || !kind.Valid() || (enc != "" && enc != EncGzip) {
		return Item{}, errors.New("invalid box, kind or encoding")
	}
	if err := safe.FileName(name); err != nil {
		return Item{}, err
	}
	tmpDir := filepath.Join(s.Root, string(b))
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return Item{}, err
	}
	tmp, err := os.CreateTemp(tmpDir, ".add-*")
	if err != nil {
		return Item{}, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, s.max()+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Item{}, err
	}
	if n > s.max() {
		return Item{}, ErrTooLarge
	}
	sha := hex.EncodeToString(h.Sum(nil))
	it := Item{ID: NewID(kind, name, sha), Kind: kind, Name: name, Size: n, SHA256: sha, Enc: enc, Created: s.now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := readMeta(s.dir(b, it.ID)); err == nil {
		return it, nil // idempotent
	}
	dir := s.dir(b, it.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Item{}, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, "data")); err != nil {
		return Item{}, err
	}
	mb, _ := json.Marshal(it)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), mb, 0o600); err != nil {
		return Item{}, err
	}
	return it, nil
}

// List returns the complete items of a box, oldest first.
func (s *Store) List(b Box) ([]Item, error) {
	if !b.Valid() {
		return nil, fmt.Errorf("invalid box: %q", b)
	}
	ents, err := os.ReadDir(filepath.Join(s.Root, string(b)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, e := range ents {
		if !e.IsDir() || !idRe.MatchString(e.Name()) {
			continue // skips partials and temporaries
		}
		if it, err := readMeta(filepath.Join(s.Root, string(b), e.Name())); err == nil {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

// Find resolves a full id or a unique prefix.
func (s *Store) Find(b Box, idOrPrefix string) (Item, error) {
	items, err := s.List(b)
	if err != nil {
		return Item{}, err
	}
	var hit []Item
	for _, it := range items {
		if it.ID == idOrPrefix {
			return it, nil
		}
		if idOrPrefix != "" && strings.HasPrefix(it.ID, idOrPrefix) {
			hit = append(hit, it)
		}
	}
	switch len(hit) {
	case 0:
		return Item{}, ErrNotFound
	case 1:
		return hit[0], nil
	}
	return Item{}, fmt.Errorf("the prefix %q is ambiguous (%d items)", idOrPrefix, len(hit))
}

// Open opens a complete item's content.
func (s *Store) Open(b Box, id string) (*os.File, Item, error) {
	if err := checkRef(b, id); err != nil {
		return nil, Item{}, err
	}
	it, err := readMeta(s.dir(b, id))
	if err != nil {
		return nil, Item{}, err
	}
	f, err := os.Open(filepath.Join(s.dir(b, id), "data"))
	return f, it, err
}

// Remove deletes an item (complete or partial).
func (s *Store) Remove(b Box, id string) error {
	if err := checkRef(b, id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := readMeta(s.dir(b, id)); err != nil {
		return err
	}
	return os.RemoveAll(s.dir(b, id))
}
