package clip

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/clipboard"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

func item(kind string, data []byte) Item {
	return Item{ID: NewID(), Kind: kind, Size: int64(len(data)), SHA256: Sum(data), Time: time.Now()}
}

func TestItemValidation(t *testing.T) {
	ok := item(KindText, []byte("hi"))
	if !ok.Valid(10) {
		t.Fatal("a plain text item must be valid")
	}
	for name, mut := range map[string]func(*Item){
		"unknown kind":     func(i *Item) { i.Kind = "exe" },
		"odd id":           func(i *Item) { i.ID = "../../x" },
		"odd sha":          func(i *Item) { i.SHA256 = "zz" },
		"over the limit":   func(i *Item) { i.Size = 11 },
		"negative size":    func(i *Item) { i.Size = -1 },
		"control in name":  func(i *Item) { i.Name = "a\x1b[2Jb" },
		"forged origin":    func(i *Item) { i.Origin = "evil origin" },
		"negative count":   func(i *Item) { i.Count = -2 },
		"overlong name":    func(i *Item) { i.Name = strings.Repeat("a", 256) },
		"origin with path": func(i *Item) { i.Origin = "a@b/c" },
	} {
		it := ok
		mut(&it)
		if it.Valid(10) {
			t.Errorf("%s: must be invalid", name)
		}
	}
	for _, o := range []string{"", OriginHub, "user@MACHINE-1"} {
		it := ok
		it.Origin = o
		if !it.Valid(10) {
			t.Errorf("origin %q must be valid", o)
		}
	}
}

func TestDescribeAndHuman(t *testing.T) {
	if got := (Item{Kind: KindFiles, Count: 2, Name: "a.pdf", Size: 3 << 20}).Describe(); got != "2 files: a.pdf... (3.0 MB)" {
		t.Fatal(got)
	}
	if got := (Item{Kind: KindText, Size: 12}).Describe(); got != "text (12 B)" {
		t.Fatal(got)
	}
}

// An item crosses the wire in several frames and arrives verified.
func TestWireRoundTripInChunks(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), chunk/4) // 2.5 chunks
	it := item(KindImage, data)
	var buf bytes.Buffer
	if err := WriteItem(&buf, it, data); err != nil {
		t.Fatal(err)
	}
	h, _ := proto.ReadFrame(&buf)
	got, b, err := ReadItem(h, &buf, int64(len(data)))
	if err != nil || got.ID != it.ID || !bytes.Equal(b, data) || buf.Len() != 0 {
		t.Fatalf("%v %d %d", err, len(b), buf.Len())
	}
}

// A refused item is drained: the next frame in the session is read correctly.
func TestWireRefusalKeepsTheSessionInSync(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 3000)
	var buf bytes.Buffer
	WriteItem(&buf, item(KindText, data), data)
	proto.WriteFrame(&buf, proto.Frame{Kind: proto.KindPing})

	h, _ := proto.ReadFrame(&buf)
	if _, _, err := ReadItem(h, &buf, 100); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("too large: %v", err)
	}
	if f, err := proto.ReadFrame(&buf); err != nil || f.Kind != proto.KindPing {
		t.Fatalf("the session must stay in sync: %+v %v", f, err)
	}

	bad := item(KindText, data)
	bad.SHA256 = Sum([]byte("other"))
	buf.Reset()
	WriteItem(&buf, bad, data)
	h, _ = proto.ReadFrame(&buf)
	if _, _, err := ReadItem(h, &buf, 1<<20); !errors.Is(err, ErrRefused) {
		t.Fatalf("a content that does not match its SHA256 must be refused: %v", err)
	}
	if err := WriteItem(io.Discard, item(KindText, data), data[:10]); err == nil {
		t.Fatal("a size that does not match the content must not be written")
	}
}

func TestSlotReplacesAndExpires(t *testing.T) {
	s := &Slot{Dir: filepath.Join(t.TempDir(), "clip")}
	now := time.Now()
	if _, ok := s.Current(now); ok {
		t.Fatal("an empty slot has no item")
	}
	a := item(KindText, []byte("one"))
	if err := s.Put(a, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Current(now); !ok || got.ID != a.ID {
		t.Fatalf("%+v %v", got, ok)
	}
	b := item(KindText, []byte("two"))
	s.Put(b, []byte("two"))
	if got, _ := s.Current(now); got.ID != b.ID {
		t.Fatal("the new item must replace the old one")
	}
	if _, err := s.Data(a); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("the replaced item's content is gone: %v", err)
	}
	if d, err := s.Data(b); err != nil || string(d) != "two" {
		t.Fatalf("%q %v", d, err)
	}
	if _, ok := s.Current(now.Add(TTL + time.Second)); ok {
		t.Fatal("an expired item must not be returned")
	}
	if left, _ := os.ReadDir(s.Dir); len(left) != 0 {
		t.Fatalf("an expired slot leaves nothing behind: %v", left)
	}
}

func TestSlotDetectsTamperedContent(t *testing.T) {
	s := &Slot{Dir: t.TempDir()}
	a := item(KindText, []byte("one"))
	s.Put(a, []byte("one"))
	os.WriteFile(filepath.Join(s.Dir, a.ID+".bin"), []byte("evil"), 0o600)
	if _, err := s.Data(a); err == nil {
		t.Fatal("a content that does not match its SHA256 must be refused")
	}
}

// fakeClip is an in-memory clipboard with a change counter.
type fakeClip struct {
	mu  sync.Mutex
	c   clipboard.Content
	seq uint32
}

func (f *fakeClip) read(context.Context) (clipboard.Content, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.c.Kind == clipboard.None && f.c.Text == "" && len(f.c.Files) == 0 && len(f.c.PNG) == 0 {
		return clipboard.Content{}, clipboard.ErrEmpty
	}
	c := f.c
	if len(c.Files) > 0 { // what the real reader returns: files first
		c.Kind = clipboard.Files
	}
	return c, nil
}

func (f *fakeClip) write(_ context.Context, c clipboard.Content) error {
	f.copy(c)
	return nil
}

func (f *fakeClip) copy(c clipboard.Content) {
	f.mu.Lock()
	f.c = c
	f.seq++
	f.mu.Unlock()
}

func (f *fakeClip) counter() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seq
}

type sent struct {
	it   Item
	data []byte
}

func newSharer(t *testing.T, fc *fakeClip) (*Sharer, *[]sent, *[]string) {
	var out []sent
	var lines []string
	var mu sync.Mutex
	s := &Sharer{
		Read: fc.read, Write: fc.write, Seq: fc.counter, MaxSize: 1 << 20, FilesRoot: t.TempDir(),
		Send: func(_ context.Context, it Item, data []byte) error {
			mu.Lock()
			out = append(out, sent{it, data})
			mu.Unlock()
			return nil
		},
		Log: func(f string, a ...any) {
			mu.Lock()
			lines = append(lines, f)
			mu.Unlock()
		},
		Debounce: 20 * time.Millisecond,
	}
	return s, &out, &lines
}

// Text, image and files go out as items and come back as the same clipboard content on the other side.
func TestShareAndReceiveEachKind(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("A"), 0o644)
	os.MkdirAll(filepath.Join(src, "dir"), 0o755)
	os.WriteFile(filepath.Join(src, "dir", "b.txt"), []byte("B"), 0o644)

	for name, c := range map[string]clipboard.Content{
		"text":  {Kind: clipboard.Text, Text: "hola, ñandú"},
		"image": {Kind: clipboard.Image, PNG: []byte("\x89PNG fake")},
		"files": {Kind: clipboard.Files, Files: []string{filepath.Join(src, "a.txt"), filepath.Join(src, "dir")}},
	} {
		a := &fakeClip{}
		a.copy(c)
		sa, out, _ := newSharer(t, a)
		if err := sa.Share(context.Background(), false); err != nil || len(*out) != 1 {
			t.Fatalf("%s: %v %d", name, err, len(*out))
		}
		b := &fakeClip{}
		sb, _, _ := newSharer(t, b)
		got := (*out)[0]
		if err := sb.Receive(context.Background(), got.it, got.data); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		switch name {
		case "text":
			if b.c.Text != c.Text {
				t.Fatalf("%q", b.c.Text)
			}
		case "image":
			if !bytes.Equal(b.c.PNG, c.PNG) || len(b.c.Files) != 1 {
				t.Fatalf("an image pastes as an image and as a file: %+v", b.c)
			}
		case "files":
			if len(b.c.Files) != 2 || filepath.Base(b.c.Files[0]) != "a.txt" || got.it.Count != 2 {
				t.Fatalf("%v %+v", b.c.Files, got.it)
			}
			if d, _ := os.ReadFile(filepath.Join(b.c.Files[1], "b.txt")); string(d) != "B" {
				t.Fatalf("%q", d)
			}
		}
	}
}

// What this process writes does not go back in auto mode (by the change counter, and by content when the counter moved).
func TestAutoModeDoesNotEchoWhatItReceived(t *testing.T) {
	fc := &fakeClip{}
	s, out, _ := newSharer(t, fc)
	s.SetAuto(true)
	data := []byte("from the other side")
	s.Receive(context.Background(), item(KindText, data), data)
	s.Share(context.Background(), true)
	if len(*out) != 0 {
		t.Fatal("its own write must not be sent back")
	}
	fc.copy(clipboard.Content{Kind: clipboard.Text, Text: "from the other side"}) // e.g. pasted from the clipboard history
	s.Share(context.Background(), true)
	if len(*out) != 0 {
		t.Fatal("the text just received must not be sent back")
	}
	// received files read back from the clipboard are not sent back either
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "f.txt"), []byte("F"), 0o644)
	other, more, _ := newSharer(t, &fakeClip{c: clipboard.Content{Kind: clipboard.Files, Files: []string{filepath.Join(src, "f.txt")}}})
	other.Share(context.Background(), false)
	s.Receive(context.Background(), (*more)[0].it, (*more)[0].data)
	fc.mu.Lock()
	fc.seq++ // the counter moved (another program touched the clipboard) but the content is the same
	fc.mu.Unlock()
	s.Share(context.Background(), true)
	if len(*out) != 0 {
		t.Fatal("received files must not be sent back")
	}
	fc.copy(clipboard.Content{Kind: clipboard.Text, Text: "a new copy"})
	s.Share(context.Background(), true)
	if len(*out) != 1 {
		t.Fatal("a real copy must be sent")
	}
}

// A program that writes the same copy twice, a second apart (a screenshot tool adds formats to the image it just put there), sends
// it once in auto mode: the other machine received the same image twice. The same copy sent again later, or with the hotkey, goes.
func TestAutoModeSendsARewrittenCopyOnce(t *testing.T) {
	fc := &fakeClip{}
	s, out, _ := newSharer(t, fc)
	now := time.Now()
	s.Now = func() time.Time { return now }
	s.SetAuto(true)
	shot := clipboard.Content{Kind: clipboard.Image, PNG: []byte("\x89PNG screenshot")}
	fc.copy(shot)
	s.Share(context.Background(), true)
	now = now.Add(time.Second)
	fc.copy(shot) // the tool writes it again: the counter moves, the content is the same
	s.Share(context.Background(), true)
	if len(*out) != 1 {
		t.Fatalf("the same image a second later is one copy: %d sent", len(*out))
	}
	s.Share(context.Background(), false)
	if len(*out) != 2 {
		t.Fatal("the hotkey sends it again: it is an explicit act")
	}
	now = now.Add(repeatWindow + time.Second)
	fc.copy(shot)
	s.Share(context.Background(), true)
	if len(*out) != 3 {
		t.Fatal("copied again later, it is sent again (the other machine may have copied something else meanwhile)")
	}
	fc.copy(clipboard.Content{Kind: clipboard.Text, Text: "something else"})
	s.Share(context.Background(), true)
	if len(*out) != 4 {
		t.Fatal("a different copy is always sent")
	}
}

// The hotkey always sends; auto mode skips what is marked private and what is too large is never sent.
func TestPrivateAndSizeLimits(t *testing.T) {
	fc := &fakeClip{}
	fc.copy(clipboard.Content{Kind: clipboard.Text, Text: "p4ssw0rd"})
	s, out, lines := newSharer(t, fc)
	s.Private = func() bool { return true }
	s.Share(context.Background(), true)
	if len(*out) != 0 || !strings.Contains(strings.Join(*lines, "\n"), "private") {
		t.Fatalf("auto mode must skip private copies: %v", *lines)
	}
	s.Share(context.Background(), false)
	if len(*out) != 1 {
		t.Fatal("the hotkey is an explicit act: it sends")
	}
	var notes []string
	s.Notify = func(_, m string) { notes = append(notes, m) }
	s.MaxSize = 4
	if err := s.Share(context.Background(), false); err == nil || len(*out) != 1 || len(notes) != 1 {
		t.Fatalf("too large: %v %d %v", err, len(*out), notes)
	}
}

// Auto mode waits until the clipboard is quiet and sends only the last state; with auto off, changes send nothing.
func TestChangedDebouncesAndHonorsAuto(t *testing.T) {
	fc := &fakeClip{}
	s, out, _ := newSharer(t, fc)
	ctx := context.Background()
	fc.copy(clipboard.Content{Kind: clipboard.Text, Text: "x"})
	s.Changed(ctx)
	time.Sleep(60 * time.Millisecond)
	if len(*out) != 0 {
		t.Fatal("with auto off a change sends nothing")
	}
	s.SetAuto(true)
	for _, txt := range []string{"a", "ab", "abc"} {
		fc.copy(clipboard.Content{Kind: clipboard.Text, Text: txt})
		s.Changed(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.busy.Lock()
		n := len(*out)
		s.busy.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.busy.Lock()
	defer s.busy.Unlock()
	if len(*out) != 1 || string((*out)[0].data) != "abc" {
		t.Fatalf("only the last state must be sent once: %d", len(*out))
	}
}

func TestReceiveRejectsMismatchedContent(t *testing.T) {
	s, _, _ := newSharer(t, &fakeClip{})
	it := item(KindText, []byte("one"))
	if err := s.Receive(context.Background(), it, []byte("two")); err == nil {
		t.Fatal("must be refused")
	}
}

// The change counter alone recognizes this process's own write: auto mode does not even read the clipboard (an image read back
// as an image would not match the received one by content).
func TestOwnWriteIsRecognizedByTheCounterBeforeReading(t *testing.T) {
	fc := &fakeClip{}
	s, _, _ := newSharer(t, fc)
	reads := 0
	s.Read = func(ctx context.Context) (clipboard.Content, error) { reads++; return fc.read(ctx) }
	s.SetAuto(true)
	png := []byte("\x89PNG received")
	s.Receive(context.Background(), item(KindImage, png), png)
	s.Share(context.Background(), true)
	if reads != 0 {
		t.Fatalf("its own write must be skipped before reading (%d reads)", reads)
	}
	fc.copy(clipboard.Content{Kind: clipboard.Text, Text: "a real copy"})
	s.Share(context.Background(), true)
	if reads != 1 {
		t.Fatalf("a real copy is read: %d", reads)
	}
}
