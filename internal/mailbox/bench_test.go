package mailbox_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// delayedPipe is an in-memory channel with a fixed one-way latency and an unbounded buffer: it models SSH's stdio over a
// high-latency link. Write never blocks; bytes reach the reader after delay, in order.
type delayedPipe struct {
	delay time.Duration
	q     chan delayedChunk
	pr    *io.PipeReader
	pw    *io.PipeWriter
}

type delayedChunk struct {
	at   time.Time
	data []byte
}

func newDelayedPipe(delay time.Duration) *delayedPipe {
	p := &delayedPipe{delay: delay, q: make(chan delayedChunk, 1<<16)}
	p.pr, p.pw = io.Pipe()
	go func() {
		for c := range p.q {
			if d := time.Until(c.at); d > 0 {
				time.Sleep(d)
			}
			if _, err := p.pw.Write(c.data); err != nil {
				return
			}
		}
		p.pw.Close()
	}()
	return p
}

func (p *delayedPipe) Write(b []byte) (int, error) {
	p.q <- delayedChunk{at: time.Now().Add(p.delay), data: append([]byte(nil), b...)}
	return len(b), nil
}
func (p *delayedPipe) Read(b []byte) (int, error) { return p.pr.Read(b) }
func (p *delayedPipe) Close() error               { close(p.q); return nil }

// duplex joins the read end of one direction and the write end of the other.
type duplex struct {
	io.Reader
	io.Writer
	closers []io.Closer
}

func (d duplex) Close() error {
	for _, c := range d.closers {
		c.Close()
	}
	return nil
}

// delayedSession starts a real hub with a one-way latency of delay between the edge and the hub.
func delayedSession(tb testing.TB, store *mailbox.Store, delay time.Duration) *mailbox.Client {
	tb.Helper()
	up, down := newDelayedPipe(delay), newDelayedPipe(delay) // edge->hub, hub->edge
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.Serve(context.Background(), proto.NewArmorReader(up), proto.NewArmorWriter(down),
			hub.Config{Agent: "bench", Caps: []string{mailbox.CapDrop}, Handler: &mailbox.Handler{Store: store}})
	}()
	edge := duplex{Reader: down, Writer: up, closers: []io.Closer{up}}
	tb.Cleanup(func() { edge.Close(); <-done })
	rw := proto.Armor(edge)
	if _, err := proto.ClientHandshake(rw, "edge", mailbox.CapDrop); err != nil {
		tb.Fatal(err)
	}
	return mailbox.NewClient(rw)
}

// BenchmarkPutLatency measures sending 8 MiB with 20 ms of latency each way (40 ms RTT, a typical remote link). With
// stop-and-wait the ceiling is ChunkSize/RTT (~6 MB/s); with a window it should approach the channel's bandwidth.
func BenchmarkPutLatency(b *testing.B) {
	data := randBytes(8 << 20)
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		store := &mailbox.Store{Root: b.TempDir()}
		c := delayedSession(b, store, 20*time.Millisecond)
		it, err := mailbox.NewItem(mailbox.File, "b.bin", bytes.NewReader(data), int64(len(data)))
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := c.Put(context.Background(), it, bytes.NewReader(data), nil); err != nil {
			b.Fatal(err)
		}
	}
}

func TestPutPipelinedOverDelayedChannel(t *testing.T) {
	store := &mailbox.Store{Root: t.TempDir()}
	c := delayedSession(t, store, 3*time.Millisecond)
	data := randBytes(mailbox.Window*mailbox.ChunkSize*2 + 77) // more blocks than the window, with an uneven tail
	it, err := mailbox.NewItem(mailbox.File, "v.bin", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	got, err := c.Put(context.Background(), it, bytes.NewReader(data), func(d, _ int64) {
		if d < last {
			t.Errorf("progress went backwards: %d after %d", d, last)
		}
		last = d
	})
	if err != nil || got.ID != it.ID || last != int64(len(data)) {
		t.Fatalf("%+v %v last=%d", got, err, last)
	}
	f, _, err := store.Open(mailbox.Inbox, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if b, _ := io.ReadAll(f); !bytes.Equal(b, data) {
		t.Fatal("the content on the hub does not match")
	}
}

// failingAt is a source that fails to read past a certain offset.
type failingAt struct {
	data []byte
	at   int64
}

func (f failingAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= f.at {
		return 0, io.ErrUnexpectedEOF
	}
	return bytes.NewReader(f.data).ReadAt(p, off)
}

func TestPutFailingSourceReturnsInsteadOfHanging(t *testing.T) {
	store := &mailbox.Store{Root: t.TempDir()}
	c := delayedSession(t, store, time.Millisecond)
	data := randBytes(6 * mailbox.ChunkSize)
	it, _ := mailbox.NewItem(mailbox.File, "f.bin", bytes.NewReader(data), int64(len(data)))
	res := make(chan error, 1)
	go func() {
		_, err := c.Put(context.Background(), it, failingAt{data: data, at: 3 * mailbox.ChunkSize}, nil)
		res <- err
	}()
	select {
	case err := <-res:
		if err == nil {
			t.Fatal("expected a source read error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Put hung on a failing source")
	}
}

// BenchmarkPutCPU measures the CPU cost of the data path (no latency): frame encoding, base64 and writing to disk.
func BenchmarkPutCPU(b *testing.B) {
	data := randBytes(16 << 20)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		store := &mailbox.Store{Root: b.TempDir()}
		c := delayedSession(b, store, 0)
		it, err := mailbox.NewItem(mailbox.File, "b.bin", bytes.NewReader(data), int64(len(data)))
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := c.Put(context.Background(), it, bytes.NewReader(data), nil); err != nil {
			b.Fatal(err)
		}
	}
}

// After a Put error, the ACK reader that is still alive must not call the caller's progress again.
func TestPutDoesNotCallProgressAfterReturning(t *testing.T) {
	store := &mailbox.Store{Root: t.TempDir()}
	c := delayedSession(t, store, time.Millisecond)
	data := randBytes(6 * mailbox.ChunkSize)
	it, _ := mailbox.NewItem(mailbox.File, "p.bin", bytes.NewReader(data), int64(len(data)))
	var returned atomic.Bool
	var late atomic.Int32
	_, err := c.Put(context.Background(), it, failingAt{data: data, at: 3 * mailbox.ChunkSize}, func(int64, int64) {
		if returned.Load() {
			late.Add(1)
		}
	})
	returned.Store(true)
	if err == nil {
		t.Fatal("expected the source read error")
	}
	time.Sleep(200 * time.Millisecond) // let the ACKs of what was already in flight arrive
	if n := late.Load(); n != 0 {
		t.Fatalf("progress was called %d times after Put returned", n)
	}
}

// rejectingHub rejects every block and counts how many arrive.
type rejectingHub struct {
	chunks   atomic.Int32
	rejected chan struct{}
	once     sync.Once
}

func (h *rejectingHub) Handle(_ context.Context, f proto.Frame, w io.Writer) error {
	if f.Kind == proto.KindChunk {
		h.chunks.Add(1)
		h.once.Do(func() { close(h.rejected) })
		return errors.New("disk full")
	}
	return (&mailbox.Handler{Store: &mailbox.Store{Root: os.TempDir()}}).Handle(context.Background(), f, w)
}

// After a rejection from the hub nothing more may be sent: Window free slots are no reason to send more blocks.
func TestPutStopsSendingOnceTheHubRejects(t *testing.T) {
	for i := 0; i < 20; i++ {
		hubSide := &rejectingHub{rejected: make(chan struct{})}
		cl, sv := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			hub.Serve(context.Background(), proto.NewArmorReader(sv), proto.NewArmorWriter(sv),
				hub.Config{Agent: "t", Caps: []string{mailbox.CapDrop}, Handler: hubSide})
			sv.Close()
		}()
		rw := proto.Armor(cl)
		if _, err := proto.ClientHandshake(rw, "edge", mailbox.CapDrop); err != nil {
			t.Fatal(err)
		}
		data := randBytes(40 * mailbox.ChunkSize)
		it := mailbox.Item{Kind: mailbox.File, Name: "r.bin", Size: int64(len(data))}
		sum := sha256.Sum256(data)
		it.SHA256 = hex.EncodeToString(sum[:])
		it.ID = mailbox.NewID(it.Kind, it.Name, it.SHA256)
		store := &mailbox.Store{Root: t.TempDir()}
		_ = store

		// the hub already rejected the first block when the second is read: from then on none may go out
		src := gatedAt{data: data, at: mailbox.ChunkSize, gate: hubSide.rejected}
		c := mailbox.NewClient(rw)
		// If the defect comes back, the hub blocks writing errors nobody reads and Put would never return: the guard turns it into a failure.
		guard := time.AfterFunc(10*time.Second, func() { cl.Close() })
		_, err := c.Put(context.Background(), it, src, nil)
		timedOut := !guard.Stop()
		cl.Close()
		<-done
		if timedOut {
			t.Fatalf("iteration %d: Put did not return after the hub's rejection (it kept sending to a hub that no longer reads)", i)
		}
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("the hub's rejection must reach the user: %v", err)
		}
		if n := hubSide.chunks.Load(); n > 2 {
			t.Fatalf("iteration %d: the hub received %d blocks; after rejecting the first only the one already in progress could go out", i, n)
		}
	}
}

// gatedAt reads normally, but reading the block at `at` waits until the hub has rejected and leaves time for the client to see it.
type gatedAt struct {
	data []byte
	at   int64
	gate chan struct{}
}

func (g gatedAt) ReadAt(p []byte, off int64) (int, error) {
	if off == g.at {
		<-g.gate
		time.Sleep(50 * time.Millisecond)
	}
	return bytes.NewReader(g.data).ReadAt(p, off)
}

// stallingHub serves the greeting and the start of the upload, then stops reading (it blocks on the first block).
type stallingHub struct {
	inner   *mailbox.Handler
	release chan struct{}
}

func (h *stallingHub) Handle(ctx context.Context, f proto.Frame, w io.Writer) error {
	if f.Kind == proto.KindChunk {
		<-h.release
		return errors.New("released")
	}
	return h.inner.Handle(ctx, f, w)
}

// With a hub that stopped reading, the pending write never returns on its own: cancelling must close the channel and return Put.
func TestPutCancelUnblocksAWriteStuckOnAStalledHub(t *testing.T) {
	hubSide := &stallingHub{inner: &mailbox.Handler{Store: &mailbox.Store{Root: t.TempDir()}}, release: make(chan struct{})}
	cl, sv := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.Serve(context.Background(), proto.NewArmorReader(sv), proto.NewArmorWriter(sv),
			hub.Config{Agent: "t", Caps: []string{mailbox.CapDrop}, Handler: hubSide})
		sv.Close()
	}()
	t.Cleanup(func() { close(hubSide.release); cl.Close(); <-done })

	rw := proto.Armor(cl)
	if _, err := proto.ClientHandshake(rw, "edge", mailbox.CapDrop); err != nil {
		t.Fatal(err)
	}
	data := randBytes(20 * mailbox.ChunkSize)
	it, _ := mailbox.NewItem(mailbox.File, "s.bin", bytes.NewReader(data), int64(len(data)))
	c := mailbox.NewClient(rw)
	c.Abort = func() { cl.Close() }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res := make(chan error, 1)
	go func() { _, err := c.Put(ctx, it, bytes.NewReader(data), nil); res <- err }()
	select {
	case err := <-res:
		if !errors.Is(err, context.DeadlineExceeded) { // the cause (the deadline), not the "closed pipe" that Abort causes
			t.Fatalf("on cancel the context's cause must be reported, not an I/O error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Put is still blocked on a write to a hub that stopped reading: cancelling does not unblock it")
	}
}

// After an upload error, answers are left unread: the client refuses to go on with that channel (instead of stealing another operation's answer).
func TestClientRefusesToReuseABrokenChannel(t *testing.T) {
	c := delayedSession(t, &mailbox.Store{Root: t.TempDir()}, time.Millisecond)
	data := randBytes(6 * mailbox.ChunkSize)
	it, _ := mailbox.NewItem(mailbox.File, "b.bin", bytes.NewReader(data), int64(len(data)))
	if _, err := c.Put(context.Background(), it, failingAt{data: data, at: 3 * mailbox.ChunkSize}, nil); err == nil {
		t.Fatal("expected the read error")
	}
	if _, err := c.List(); !errors.Is(err, mailbox.ErrBroken) {
		t.Fatalf("List after an upload error must return ErrBroken: %v", err)
	}
	if _, err := c.Put(context.Background(), it, bytes.NewReader(data), nil); !errors.Is(err, mailbox.ErrBroken) {
		t.Fatalf("Put after an error must return ErrBroken: %v", err)
	}
	if _, err := c.Get(context.Background(), "x", 0, io.Discard, nil); !errors.Is(err, mailbox.ErrBroken) {
		t.Fatalf("Get after an error must return ErrBroken: %v", err)
	}
}

// A rejection BEFORE the stream starts (the item does not exist) desynchronizes nothing: the channel stays usable.
func TestGetOfAMissingItemDoesNotBreakTheChannel(t *testing.T) {
	c := delayedSession(t, &mailbox.Store{Root: t.TempDir()}, time.Millisecond)
	if _, err := c.Get(context.Background(), "0123456789abcdef", 0, io.Discard, nil); err == nil {
		t.Fatal("a missing item must give an error")
	}
	if _, err := c.List(); err != nil {
		t.Fatalf("the channel must keep working after an early rejection: %v", err)
	}
}
