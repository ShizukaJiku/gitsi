package clip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/archive"
	"github.com/ShizukaJiku/gitsi/internal/clipboard"
)

// Sharer turns clipboard copies into items and received items into clipboard content, on the hub and on the edge alike. Only how
// an item is sent differs (the hub stores it in its slot, the edge sends it in its watch session), and that is injected.
type Sharer struct {
	Read  clipboard.Reader
	Write clipboard.Writer
	// Seq returns the clipboard's change counter (GetClipboardSequenceNumber). It tells this process's own writes apart from the
	// user's copies. nil: echo suppression relies on the content only.
	Seq func() uint32
	// Private reports whether what is copied is marked as not to be monitored (password managers). nil: never.
	Private   func() bool
	MaxSize   int64
	FilesRoot string // where received files and images are left to paste (%LOCALAPPDATA%\gitsi\clipboard)
	Send      func(ctx context.Context, it Item, data []byte) error
	Log       func(format string, a ...any) // one line in watch's output
	Notify    func(title, message string)   // a tray notification; nil: none
	Audit     func(action, detail string)
	Debounce  time.Duration // auto mode: wait this long after the last change before reading (300 ms)
	Now       func() time.Time

	busy sync.Mutex // one copy is read and sent at a time (the hotkey and auto mode may fire together)

	mu      sync.Mutex
	auto    bool
	ownSeq  uint32 // change counter right after this process wrote the clipboard
	haveOwn bool
	lastIn  Item // last item written to the clipboard
	timer   *time.Timer
}

// SetAuto turns auto mode on or off (every copy is sent, not only on the hotkey).
func (s *Sharer) SetAuto(on bool) {
	s.mu.Lock()
	s.auto = on
	s.mu.Unlock()
}

// Auto reports whether auto mode is on.
func (s *Sharer) Auto() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auto
}

// Changed is called on every clipboard change. In auto mode it sends the copy once the clipboard has been quiet for Debounce:
// some programs write the clipboard in several steps, and only the last one counts.
func (s *Sharer) Changed(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auto {
		return
	}
	d := s.Debounce
	if d <= 0 {
		d = 300 * time.Millisecond
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(d, func() {
		if ctx.Err() == nil {
			_ = s.Share(ctx, true)
		}
	})
}

// Share reads the clipboard and sends it. auto is true when it comes from a change in auto mode: then this process's own writes,
// what it just received and what is marked as private are not sent. The hotkey (auto false) is an explicit act and sends what is
// there.
func (s *Sharer) Share(ctx context.Context, auto bool) error {
	s.busy.Lock()
	defer s.busy.Unlock()
	if auto && s.ownWrite() {
		return nil
	}
	if auto && s.Private != nil && s.Private() {
		s.logf("not sent: the copy is marked as private (a password manager)")
		return nil
	}
	c, err := s.Read(ctx)
	if errors.Is(err, clipboard.ErrEmpty) {
		if !auto {
			s.logf("nothing to send: the clipboard holds no text, image or files")
		}
		return nil
	}
	if err != nil {
		s.fail("could not read the clipboard", err)
		return err
	}
	if auto && s.isEcho(c) {
		return nil
	}
	it, data, err := s.encode(c)
	if err != nil {
		s.fail("not sent", err)
		return err
	}
	if auto && it.Kind == KindText && it.SHA256 == s.last().SHA256 {
		return nil // the text just received, copied again (e.g. from the clipboard history)
	}
	if err := s.Send(ctx, it, data); err != nil {
		s.fail("could not send "+it.Describe(), err)
		return err
	}
	s.logf("sent %s", it.Describe())
	s.audit("clip.send", it)
	return nil
}

// Receive writes a received item to the clipboard.
func (s *Sharer) Receive(ctx context.Context, it Item, data []byte) error {
	s.busy.Lock()
	defer s.busy.Unlock()
	if int64(len(data)) != it.Size || Sum(data) != it.SHA256 {
		err := errors.New("its content does not match its SHA256")
		s.fail("discarded a clipboard item", err)
		return err
	}
	c, err := s.decode(it, data)
	if err != nil {
		s.fail("could not receive "+it.Describe(), err)
		return err
	}
	if err := s.Write(ctx, c); err != nil {
		s.fail("could not write the clipboard", err)
		return err
	}
	s.mu.Lock()
	s.lastIn = it
	if s.Seq != nil {
		s.ownSeq, s.haveOwn = s.Seq(), true
	}
	s.mu.Unlock()
	from := it.Origin
	if from == "" {
		from = "the other machine"
	}
	s.logf("received %s from %s: paste it with Ctrl+V", it.Describe(), from)
	s.audit("clip.receive", it)
	return nil
}

func (s *Sharer) last() Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastIn
}

// ownWrite reports whether the clipboard still holds what this process wrote: the change that woke auto mode was its own.
func (s *Sharer) ownWrite() bool {
	if s.Seq == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.haveOwn && s.Seq() == s.ownSeq
}

// isEcho reports whether c is what this process just put on the clipboard, read back: files (and images, which are also written as
// a file) under the received item's folder.
func (s *Sharer) isEcho(c clipboard.Content) bool {
	last := s.last()
	if last.ID == "" || c.Kind != clipboard.Files || s.FilesRoot == "" {
		return false
	}
	dir := strings.ToLower(filepath.Join(s.FilesRoot, last.ID)) + string(filepath.Separator)
	for _, f := range c.Files {
		if !strings.HasPrefix(strings.ToLower(filepath.Clean(f)), dir) {
			return false
		}
	}
	return true
}

// encode turns clipboard content into an item, within MaxSize.
func (s *Sharer) encode(c clipboard.Content) (Item, []byte, error) {
	it := Item{ID: NewID(), Time: s.now()}
	var data []byte
	switch c.Kind {
	case clipboard.Text:
		it.Kind, data = KindText, []byte(c.Text)
	case clipboard.Image:
		it.Kind, it.Name, data = KindImage, "image.png", c.PNG
	case clipboard.Files:
		if _, err := archive.TreeSize(c.Files, s.MaxSize); errors.Is(err, archive.ErrTooLarge) {
			return Item{}, nil, fmt.Errorf("the copied files take more than %s (clip.max_size_mb)", Human(s.MaxSize))
		} else if err != nil {
			return Item{}, nil, err
		}
		var buf bytes.Buffer
		if _, err := archive.PackPaths(c.Files, &buf, archive.Options{Gzip: true}); err != nil {
			return Item{}, nil, err
		}
		it.Kind, it.Name, it.Count, data = KindFiles, filepath.Base(c.Files[0]), len(c.Files), buf.Bytes()
	default:
		return Item{}, nil, clipboard.ErrEmpty
	}
	if int64(len(data)) > s.MaxSize {
		return Item{}, nil, fmt.Errorf("the %s takes %s, more than %s (clip.max_size_mb)", it.Kind, Human(int64(len(data))), Human(s.MaxSize))
	}
	it.Size, it.SHA256 = int64(len(data)), Sum(data)
	return it, data, nil
}

// decode turns a received item into clipboard content. Files and images are left in FilesRoot/<id> (D19 deletes them after 7
// days): files paste in Explorer, an image pastes both as an image and as a file.
func (s *Sharer) decode(it Item, data []byte) (clipboard.Content, error) {
	switch it.Kind {
	case KindText:
		return clipboard.Content{Kind: clipboard.Text, Text: string(data)}, nil
	case KindImage:
		dir, err := s.itemDir(it)
		if err != nil {
			return clipboard.Content{}, err
		}
		p := filepath.Join(dir, "image.png")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return clipboard.Content{}, err
		}
		return clipboard.Content{Kind: clipboard.Image, PNG: data, Files: []string{p}}, nil
	case KindFiles:
		dir, err := s.itemDir(it)
		if err != nil {
			return clipboard.Content{}, err
		}
		// The limit applies to what is extracted, not to what traveled compressed: it guards against bombs.
		if err := archive.UnpackGzip(bytes.NewReader(data), dir, 16*max(s.MaxSize, 1<<20)); err != nil {
			_ = os.RemoveAll(dir)
			return clipboard.Content{}, err
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			return clipboard.Content{}, errors.New("the received files are empty")
		}
		var files []string
		for _, e := range entries {
			files = append(files, filepath.Join(dir, e.Name()))
		}
		sort.Strings(files)
		return clipboard.Content{Kind: clipboard.Files, Files: files}, nil
	}
	return clipboard.Content{}, fmt.Errorf("unknown kind %q", it.Kind)
}

func (s *Sharer) itemDir(it Item) (string, error) {
	if s.FilesRoot == "" {
		return "", errors.New("there is no folder to leave received files in")
	}
	if !idRe.MatchString(it.ID) {
		return "", fmt.Errorf("invalid item id %q", it.ID)
	}
	dir := filepath.Join(s.FilesRoot, it.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (s *Sharer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sharer) logf(format string, a ...any) {
	if s.Log != nil {
		s.Log(format, a...)
	}
}

// fail reports a problem in the output and, since the user may not be looking at it (tray), as a notification.
func (s *Sharer) fail(what string, err error) {
	s.logf("%s: %v", what, err)
	if s.Notify != nil {
		s.Notify("gitsi: clipboard", what+": "+err.Error())
	}
}

func (s *Sharer) audit(action string, it Item) {
	if s.Audit != nil {
		s.Audit(action, fmt.Sprintf("kind=%s size=%d sha256=%s id=%s origin=%s", it.Kind, it.Size, it.SHA256, it.ID, it.Origin))
	}
}
