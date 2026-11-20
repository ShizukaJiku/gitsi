package mailbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func item(kind Kind, name, content string) Item {
	h := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(h[:])
	return Item{ID: NewID(kind, name, sha), Kind: kind, Name: name, Size: int64(len(content)), SHA256: sha}
}

func TestBeginAppendCommit(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	it := item(File, "a.txt", "hello word")
	off, done, err := s.Begin(Inbox, it)
	if err != nil || off != 0 || done {
		t.Fatalf("%d %v %v", off, done, err)
	}
	if off, err = s.Append(Inbox, it.ID, 0, []byte("hello")); err != nil || off != 5 {
		t.Fatal(off, err)
	}
	if off, err = s.Append(Inbox, it.ID, 5, []byte(" word")); err != nil || off != 10 {
		t.Fatal(off, err)
	}
	got, err := s.Commit(Inbox, it.ID)
	if err != nil || got.Name != "a.txt" {
		t.Fatalf("%+v %v", got, err)
	}
	f, _, err := s.Open(Inbox, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if b, _ := io.ReadAll(f); string(b) != "hello word" {
		t.Fatalf("%q", b)
	}
	items, _ := s.List(Inbox)
	if len(items) != 1 || items[0].ID != it.ID {
		t.Fatalf("%+v", items)
	}
	// uploading the same thing again is idempotent
	if off, done, err := s.Begin(Inbox, it); err != nil || !done || off != it.Size {
		t.Fatalf("%d %v %v", off, done, err)
	}
}

func TestResumeAfterInterruption(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	it := item(File, "big.bin", "0123456789")
	s.Begin(Inbox, it)
	s.Append(Inbox, it.ID, 0, []byte("01234"))
	// "restart": a new Begin returns what was already received
	off, done, err := s.Begin(Inbox, it)
	if err != nil || done || off != 5 {
		t.Fatalf("%d %v %v", off, done, err)
	}
	// an out-of-order block is rejected and says where to continue
	if cur, err := s.Append(Inbox, it.ID, 7, []byte("789")); !errors.Is(err, ErrOffset) || cur != 5 {
		t.Fatalf("%d %v", cur, err)
	}
	s.Append(Inbox, it.ID, 5, []byte("56789"))
	if _, err := s.Commit(Inbox, it.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCommitRejectsWrongHashAndIncomplete(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	it := item(File, "x", "abcd")
	s.Begin(Inbox, it)
	s.Append(Inbox, it.ID, 0, []byte("ab"))
	if _, err := s.Commit(Inbox, it.ID); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("incomplete: %v", err)
	}
	s.Append(Inbox, it.ID, 2, []byte("XX")) // same size, other content
	if _, err := s.Commit(Inbox, it.ID); !errors.Is(err, ErrHash) {
		t.Fatalf("hash: %v", err)
	}
	if items, _ := s.List(Inbox); len(items) != 0 {
		t.Fatal("a corrupt item must not be published")
	}
	if _, err := os.Stat(s.part(Inbox, it.ID)); err == nil {
		t.Fatal("the corrupt partial must be discarded")
	}
}

func TestBeginValidation(t *testing.T) {
	s := &Store{Root: t.TempDir(), MaxSize: 10}
	big := item(File, "big", "01234567890")
	if _, _, err := s.Begin(Inbox, big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("size: %v", err)
	}
	bad := item(File, "ok", "x")
	bad.ID = "0000000000000000"
	if _, _, err := s.Begin(Inbox, bad); err == nil {
		t.Error("id that does not match the content")
	}
	trav := item(File, "../evil", "x")
	if _, _, err := s.Begin(Inbox, trav); err == nil {
		t.Error("name with a path")
	}
	if _, _, err := s.Begin("other", item(File, "a", "x")); err == nil {
		t.Error("invalid box")
	}
	if _, err := s.Append(Inbox, "../../etc", 0, nil); err == nil {
		t.Error("id with a path")
	}
	it := item(File, "a", "abc")
	s.Begin(Inbox, it)
	if _, err := s.Append(Inbox, it.ID, 0, []byte("abcdef")); err == nil {
		t.Error("a block cannot exceed the declared size")
	}
}

func TestAddLocalFindRemove(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	a, err := s.Add(Outbox, Text, "note", strings.NewReader("contents!"))
	if err != nil || a.Size != 9 || a.ID != NewID(Text, "note", a.SHA256) {
		t.Fatalf("%+v %v", a, err)
	}
	if again, _ := s.Add(Outbox, Text, "note", strings.NewReader("contents!")); again.ID != a.ID {
		t.Fatal("Add must be idempotent")
	}
	if items, _ := s.List(Outbox); len(items) != 1 {
		t.Fatalf("%+v", items)
	}
	if got, err := s.Find(Outbox, a.ID[:6]); err != nil || got.ID != a.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.Find(Outbox, "zzzz"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Remove(Outbox, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(Outbox, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("removing twice must give NotFound")
	}
	// the limit also applies when adding locally
	small := &Store{Root: t.TempDir(), MaxSize: 3}
	if _, err := small.Add(Inbox, File, "x", strings.NewReader("12345")); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	// and it leaves no temporaries
	ents, _ := os.ReadDir(filepath.Join(small.Root, "inbox"))
	if len(ents) != 0 {
		t.Fatalf("temporaries were left: %v", ents)
	}
}

func TestFindAmbiguousPrefix(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	a, _ := s.Add(Inbox, Text, "a", strings.NewReader("1"))
	if got, err := s.Find(Inbox, ""); err == nil {
		t.Fatalf("an empty prefix must match nothing: %+v", got)
	}
	_ = a
}

// ageParts ages an item's half-done upload (as if it had received no data for `ago`).
func ageParts(t *testing.T, s *Store, it Item, ago time.Duration) {
	t.Helper()
	old := time.Now().Add(-ago)
	if err := os.Chtimes(filepath.Join(s.part(Inbox, it.ID), "data"), old, old); err != nil {
		t.Fatal(err)
	}
}

func hasPart(s *Store, it Item) bool {
	_, err := os.Stat(s.part(Inbox, it.ID))
	return err == nil
}

func TestBeginPrunesAbandonedPartialsButKeepsFreshAndCurrent(t *testing.T) {
	savedInterval := PruneInterval
	PruneInterval = 0 // the test ages files between one Begin and the next: no frequency limit
	defer func() { PruneInterval = savedInterval }()
	s := &Store{Root: t.TempDir()}
	stale, fresh, resumed, next := item(File, "old.bin", "aaaa"), item(File, "new.bin", "bbbb"), item(File, "resume.bin", "cccc"), item(File, "other.bin", "dddd")
	for _, it := range []Item{stale, fresh, resumed} {
		if _, _, err := s.Begin(Inbox, it); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Append(Inbox, resumed.ID, 0, []byte("cc")); err != nil {
		t.Fatal(err)
	}
	ageParts(t, s, stale, PartialMaxAge+time.Hour)
	ageParts(t, s, resumed, PartialMaxAge+time.Hour) // old, but it is the one being resumed now
	ageParts(t, s, fresh, time.Hour)

	// resuming the old one does not delete it and keeps what was received
	if off, done, err := s.Begin(Inbox, resumed); err != nil || done || off != 2 {
		t.Fatalf("resume: off=%d done=%v err=%v", off, done, err)
	}
	if hasPart(s, stale) {
		t.Fatal("the upload abandoned for more than PartialMaxAge should be cleaned up")
	}
	if !hasPart(s, fresh) {
		t.Fatal("a recent upload must not be touched")
	}

	// a new upload does not delete what is current either
	if _, _, err := s.Begin(Inbox, next); err != nil {
		t.Fatal(err)
	}
	if !hasPart(s, fresh) || !hasPart(s, resumed) {
		t.Fatal("only what expired is cleaned up")
	}
}

func TestPruneIgnoresCompletedItemsAndForeignFiles(t *testing.T) {
	savedInterval := PruneInterval
	PruneInterval = 0
	defer func() { PruneInterval = savedInterval }()
	s := &Store{Root: t.TempDir()}
	done := item(File, "done.txt", "hola")
	if _, _, err := s.Begin(Inbox, done); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(Inbox, done.ID, 0, []byte("hola")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(Inbox, done.ID); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(s.Root, string(Inbox), "not-a-partial.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * PartialMaxAge)
	_ = os.Chtimes(stray, old, old)
	_ = os.Chtimes(s.dir(Inbox, done.ID), old, old)

	if _, _, err := s.Begin(Inbox, item(File, "x.bin", "zzzz")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatal("what is not a gitsi partial must not be deleted")
	}
	if got, _ := s.List(Inbox); len(got) != 1 || got[0].ID != done.ID {
		t.Fatalf("a complete item, however old, is not cleaned up: %+v", got)
	}
}

func TestValidateRejectsUnknownOrMisplacedEncoding(t *testing.T) {
	good := item(Dir, "folder", "tar")
	good.Enc = EncGzip
	if err := good.Validate(1 << 20); err != nil {
		t.Fatalf("a gzip folder is valid: %v", err)
	}
	plain := item(File, "a.txt", "x")
	if err := plain.Validate(1 << 20); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Item){
		"gzip on a file": func(i *Item) { i.Kind, i.Enc = File, EncGzip },
		"gzip on a text": func(i *Item) { i.Kind, i.Enc = Text, EncGzip },
		"odd encoding":   func(i *Item) { i.Enc = "zstd" },
		"huge enc":       func(i *Item) { i.Enc = strings.Repeat("x", 10_000) },
	} {
		it := item(Dir, "folder", "tar")
		mut(&it)
		it.ID = NewID(it.Kind, it.Name, it.SHA256) // the id is valid: only Enc is wrong
		if err := it.Validate(1 << 20); err == nil {
			t.Errorf("%s must be rejected when the upload starts", name)
		}
	}
}
