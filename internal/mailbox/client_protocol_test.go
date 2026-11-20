package mailbox_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// serveWith starts a real hub with the given Handler and returns the edge side's already negotiated channel.
func serveWith(t *testing.T, h hub.Handler) (io.ReadWriter, net.Conn) {
	t.Helper()
	cl, sv := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.Serve(context.Background(), proto.NewArmorReader(sv), proto.NewArmorWriter(sv),
			hub.Config{Agent: "t", Caps: []string{mailbox.CapDrop}, Handler: h})
		sv.Close()
	}()
	t.Cleanup(func() { cl.Close(); <-done })
	rw := proto.Armor(cl)
	if _, err := proto.ClientHandshake(rw, "edge", mailbox.CapDrop); err != nil {
		t.Fatal(err)
	}
	return rw, cl
}

// midStreamErrorHub answers GetBegin with the header and then an error (the hub turns it into a KindError frame): the stream
// ends cleanly and the hub keeps serving.
type midStreamErrorHub struct{ inner *mailbox.Handler }

func (h *midStreamErrorHub) Handle(ctx context.Context, f proto.Frame, w io.Writer) error {
	if f.Kind == proto.KindGetBegin {
		if err := proto.WriteJSON(w, proto.KindOK, mailbox.Item{Size: 10}); err != nil {
			return err
		}
		return errors.New("failed reading the item")
	}
	return h.inner.Handle(ctx, f, w)
}

func TestGetEndedByARemoteErrorFrameDoesNotBreakTheChannel(t *testing.T) {
	rw, _ := serveWith(t, &midStreamErrorHub{inner: &mailbox.Handler{Store: &mailbox.Store{Root: t.TempDir()}}})
	c := mailbox.NewClient(rw)
	if _, err := c.Get(context.Background(), "0123456789abcdef", 0, io.Discard, nil); err == nil || !strings.Contains(err.Error(), "failed reading") {
		t.Fatalf("the hub's error must arrive: %v", err)
	}
	// take downloads several items: one failing item must not block the others
	if _, err := c.List(); err != nil {
		t.Fatalf("the error frame closed the stream: the channel stays in sync: %v", err)
	}
}

// doubleAckHub acknowledges the first block twice: an acknowledgement that matches nothing sent.
type doubleAckHub struct {
	inner *mailbox.Handler
	once  sync.Once
}

func (h *doubleAckHub) Handle(ctx context.Context, f proto.Frame, w io.Writer) error {
	if f.Kind == proto.KindChunk {
		first := false
		h.once.Do(func() { first = true })
		if first {
			if err := h.inner.Handle(ctx, f, w); err != nil { // legitimate acknowledgement
				return err
			}
			return proto.WriteJSON(w, proto.KindOK, mailbox.ChunkAck{Offset: mailbox.ChunkSize}) // and one too many
		}
	}
	return h.inner.Handle(ctx, f, w)
}

// slowAt reads normally, but reading the block at `at` takes d.
type slowAt struct {
	data []byte
	at   int64
	d    time.Duration
}

func (s slowAt) ReadAt(p []byte, off int64) (int, error) {
	if off == s.at {
		time.Sleep(s.d)
	}
	return bytes.NewReader(s.data).ReadAt(p, off)
}

func TestPutFailsOnAnUnsolicitedAckInsteadOfHanging(t *testing.T) {
	rw, cl := serveWith(t, &doubleAckHub{inner: &mailbox.Handler{Store: &mailbox.Store{Root: t.TempDir()}}})
	data := randBytes(6 * mailbox.ChunkSize)
	it, _ := mailbox.NewItem(mailbox.File, "d.bin", bytes.NewReader(data), int64(len(data)))
	c := mailbox.NewClient(rw)
	c.Abort = func() { cl.Close() }
	// the second block is slow to read: when the extra acknowledgement arrives, only one is in flight
	src := slowAt{data: data, at: mailbox.ChunkSize, d: 300 * time.Millisecond}
	res := make(chan error, 1)
	go func() { _, err := c.Put(context.Background(), it, src, nil); res <- err }()
	select {
	case err := <-res:
		if err == nil || !strings.Contains(err.Error(), "unexpected") {
			t.Fatalf("an extra acknowledgement must be a protocol error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Put kept waiting after an acknowledgement that matches no block")
	}
}

// Walking the box on every Begin, with the lock held, would slow the other uploads down: pruning runs at most every PruneInterval.
func TestPruneRunsAtMostOncePerInterval(t *testing.T) {
	s := &mailbox.Store{Root: t.TempDir()}
	mk := func(name, content string) mailbox.Item {
		it, _ := mailbox.NewItem(mailbox.File, name, strings.NewReader(content), int64(len(content)))
		return it
	}
	stale, a, b := mk("old.bin", "aaaa"), mk("a.bin", "bbbb"), mk("b.bin", "cccc")
	if _, _, err := s.Begin(mailbox.Inbox, stale); err != nil { // first upload: prunes (nothing to prune)
		t.Fatal(err)
	}
	old := time.Now().Add(-(mailbox.PartialMaxAge + time.Hour))
	partOf := func(it mailbox.Item) string { return s.Root + "/inbox/.part-" + it.ID }
	if err := os.Chtimes(partOf(stale)+"/data", old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Begin(mailbox.Inbox, a); err != nil {
		t.Fatal(err)
	}
	if !exists(partOf(stale)) {
		t.Fatal("within the interval the box is not walked again")
	}
	clock := time.Now().Add(mailbox.PruneInterval + time.Minute)
	s.Now = func() time.Time { return clock }
	if _, _, err := s.Begin(mailbox.Inbox, b); err != nil {
		t.Fatal(err)
	}
	if exists(partOf(stale)) {
		t.Fatal("once the interval passed, pruning must run again")
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
