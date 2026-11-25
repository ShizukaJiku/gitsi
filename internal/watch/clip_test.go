package watch

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/clip"
)

// received collects what the hub delivers to the edge.
type received struct {
	mu    sync.Mutex
	items []clip.Item
	data  [][]byte
}

func (r *received) add(_ context.Context, it clip.Item, data []byte) {
	r.mu.Lock()
	r.items, r.data = append(r.items, it), append(r.data, data)
	r.mu.Unlock()
}

func (r *received) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

func clipWorld(t *testing.T) (*world, *clip.Slot, *received) {
	w := newWorld(t)
	slot := &clip.Slot{Dir: t.TempDir()}
	w.hubH.Clip, w.hubH.ClipMax, w.hubH.ClipPoll = slot, 1<<20, 10*time.Millisecond
	rec := &received{}
	w.client.Clip = func() bool { return true }
	w.client.OnClip = rec.add
	w.client.ClipMax = 1 << 20
	return w, slot, rec
}

func newItem(data []byte) clip.Item {
	return clip.Item{ID: clip.NewID(), Kind: clip.KindText, Size: int64(len(data)), SHA256: clip.Sum(data), Time: time.Now()}
}

func sendWhenLive(t *testing.T, c *Client, it clip.Item, data []byte) error {
	t.Helper()
	var err error
	waitFor(t, "a session that carries the clipboard", func() bool {
		err = c.SendClip(context.Background(), it, data)
		return !errors.Is(err, ErrNoClip)
	})
	return err
}

// The edge's copy reaches the hub's slot, stamped by the hub with the edge's identity, and is not delivered back to it.
func TestEdgeClipReachesTheSlotAndIsNotEchoed(t *testing.T) {
	w, slot, rec := clipWorld(t)
	stop := w.run(t)
	defer stop()
	data := []byte("copied on the edge")
	it := newItem(data)
	it.Origin = "hub" // what the edge claims does not count
	if err := sendWhenLive(t, w.client, it, data); err != nil {
		t.Fatal(err)
	}
	cur, ok := slot.Current(time.Now())
	if !ok || cur.ID != it.ID || cur.Origin != "u@EDGE-1" {
		t.Fatalf("%+v %v", cur, ok)
	}
	if got, _ := slot.Data(cur); !bytes.Equal(got, data) {
		t.Fatalf("%q", got)
	}
	time.Sleep(60 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatal("the edge's own copy must not come back to it")
	}
}

// A copy on the hub reaches the edge once, even across a reconnection; one made before watch started never does.
func TestHubClipReachesTheEdgeOnce(t *testing.T) {
	w, slot, rec := clipWorld(t)
	old := []byte("copied before watch started")
	oldIt := newItem(old)
	oldIt.Origin, oldIt.Time = clip.OriginHub, time.Now().Add(-time.Minute)
	slot.Put(oldIt, old)

	stop := w.run(t)
	waitFor(t, "hello", func() bool { return strings.Contains(w.out.String(), "listening to the hub") })
	data := []byte("copied on the hub")
	it := newItem(data)
	it.Origin = clip.OriginHub
	time.Sleep(20 * time.Millisecond)
	it.Time = time.Now()
	slot.Put(it, data)
	waitFor(t, "the hub's copy", func() bool { return rec.count() == 1 })
	stop()

	stop = w.run(t) // a new session: the same item is not delivered again
	waitFor(t, "hello again", func() bool { return strings.Count(w.out.String(), "listening to the hub") == 2 })
	time.Sleep(80 * time.Millisecond)
	stop()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.items) != 1 || rec.items[0].ID != it.ID || !bytes.Equal(rec.data[0], data) {
		t.Fatalf("delivered: %+v", rec.items)
	}
}

// A refused item gets its reason back and the session keeps working.
func TestTooLargeClipIsRefusedAndTheSessionGoesOn(t *testing.T) {
	w, slot, _ := clipWorld(t)
	w.hubH.ClipMax = 10
	stop := w.run(t)
	defer stop()
	big := bytes.Repeat([]byte("x"), 100)
	err := sendWhenLive(t, w.client, newItem(big), big)
	if err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("%v", err)
	}
	small := []byte("ok")
	if err := w.client.SendClip(context.Background(), newItem(small), small); err != nil {
		t.Fatalf("the session must go on after a refusal: %v", err)
	}
	if cur, ok := slot.Current(time.Now()); !ok || cur.Size != 2 {
		t.Fatalf("%+v", cur)
	}
}

// A hub copy larger than the edge accepts is not sent; the edge is told why.
func TestHubClipLargerThanTheEdgeAcceptsIsExplained(t *testing.T) {
	w, slot, rec := clipWorld(t)
	w.client.ClipMax = 10
	stop := w.run(t)
	waitFor(t, "hello", func() bool { return strings.Contains(w.out.String(), "listening to the hub") })
	big := bytes.Repeat([]byte("x"), 100)
	it := newItem(big)
	it.Origin = clip.OriginHub
	time.Sleep(20 * time.Millisecond)
	it.Time = time.Now()
	slot.Put(it, big)
	waitFor(t, "the explanation", func() bool { return strings.Contains(w.out.String(), "was not delivered") })
	stop()
	if rec.count() != 0 {
		t.Fatal("it must not be delivered")
	}
}

// A hub that does not share its clipboard drains what an older or misconfigured edge sends and says why.
func TestHubWithoutClipRefusesAndExplains(t *testing.T) {
	w, _, _ := clipWorld(t)
	w.hubH.Clip = nil
	stop := w.run(t)
	defer stop()
	data := []byte("x")
	err := sendWhenLive(t, w.client, newItem(data), data)
	if err == nil || !strings.Contains(err.Error(), "policy set clip on") {
		t.Fatalf("%v", err)
	}
}

func TestSendClipWithoutASession(t *testing.T) {
	c := &Client{}
	if err := c.SendClip(context.Background(), newItem([]byte("x")), []byte("x")); !errors.Is(err, ErrNoClip) {
		t.Fatalf("%v", err)
	}
}

func TestClipNoticesAreValidated(t *testing.T) {
	if !(Event{Type: EvClipStored, ID: clip.NewID()}).Valid() || (Event{Type: EvClipStored, ID: "../x"}).Valid() {
		t.Fatal("clip.stored")
	}
	if (Event{Type: EvClipRefused, ID: clip.NewID(), Detail: strings.Repeat("a", 301)}).Valid() {
		t.Fatal("an overlong reason must be refused")
	}
}

// The hub's clip policy is read live: an edge connected while it was off shares the clipboard as soon as it is turned on,
// without reconnecting; turned off again, the hub refuses and delivers nothing.
func TestHubClipPolicyAppliesWithoutReconnecting(t *testing.T) {
	w, slot, rec := clipWorld(t)
	var allowed atomic.Bool
	w.hubH.ClipAllowed = allowed.Load
	stop := w.run(t)
	defer stop()
	data := []byte("before the policy")
	err := sendWhenLive(t, w.client, newItem(data), data)
	if err == nil || !strings.Contains(err.Error(), "policy set clip on") {
		t.Fatalf("with the policy off the hub refuses and says why: %v", err)
	}

	allowed.Store(true)
	data = []byte("after the policy")
	if err := w.client.SendClip(context.Background(), newItem(data), data); err != nil {
		t.Fatalf("turned on, the same session shares the clipboard: %v", err)
	}
	it := newItem([]byte("from the hub"))
	it.Origin = clip.OriginHub
	slot.Put(it, []byte("from the hub"))
	waitFor(t, "the hub's copy", func() bool { return rec.count() == 1 })

	allowed.Store(false)
	it2 := newItem([]byte("not shared"))
	it2.Origin = clip.OriginHub
	slot.Put(it2, []byte("not shared"))
	time.Sleep(80 * time.Millisecond)
	if rec.count() != 1 {
		t.Fatal("turned off, the hub delivers nothing")
	}
}
