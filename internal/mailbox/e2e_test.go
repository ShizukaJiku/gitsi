package mailbox_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// session starts a real hub (Serve + Handler + on-disk Store) and returns a client connected over a text (base64) channel like
// the production one.
func session(t *testing.T, store *mailbox.Store) (*mailbox.Client, net.Conn) {
	t.Helper()
	cl, sv := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.Serve(context.Background(), proto.NewArmorReader(sv), proto.NewArmorWriter(sv),
			hub.Config{Agent: "test", Caps: []string{mailbox.CapDrop}, Handler: &mailbox.Handler{Store: store}})
		sv.Close()
	}()
	t.Cleanup(func() { cl.Close(); <-done })
	rw := proto.Armor(cl)
	if _, err := proto.ClientHandshake(rw, "edge", mailbox.CapDrop); err != nil {
		t.Fatal(err)
	}
	return mailbox.NewClient(rw), cl
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b)
	return b
}

func TestPutThenHubSeesItem(t *testing.T) {
	store := &mailbox.Store{Root: t.TempDir()}
	c, _ := session(t, store)
	data := randBytes(3*mailbox.ChunkSize + 123) // several blocks, binary content
	it, err := mailbox.NewItem(mailbox.File, "data.bin", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	got, err := c.Put(context.Background(), it, bytes.NewReader(data), func(d, _ int64) { last = d })
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

func TestPutResumesAfterDroppedConnection(t *testing.T) {
	store := &mailbox.Store{Root: t.TempDir()}
	data := randBytes(2*mailbox.ChunkSize + 10)
	it, _ := mailbox.NewItem(mailbox.File, "x.bin", bytes.NewReader(data), int64(len(data)))

	// first session: cut after the first block
	c1, conn1 := session(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := c1.Put(ctx, it, bytes.NewReader(data), func(d, _ int64) {
		if d >= mailbox.ChunkSize {
			cancel()
			conn1.Close()
		}
	})
	if err == nil {
		t.Fatal("the first upload should be interrupted")
	}
	// second session: continues where it stopped and finishes
	c2, _ := session(t, store)
	var first int64 = -1
	if _, err := c2.Put(context.Background(), it, bytes.NewReader(data), func(d, _ int64) {
		if first < 0 {
			first = d
		}
	}); err != nil {
		t.Fatal(err)
	}
	if first <= mailbox.ChunkSize {
		t.Fatalf("it should resume (first progress %d), not resend from zero", first)
	}
	if items, _ := store.List(mailbox.Inbox); len(items) != 1 {
		t.Fatalf("%+v", items)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	store := &mailbox.Store{Root: t.TempDir()}
	c, _ := session(t, store)
	data := []byte("text")
	it, _ := mailbox.NewItem(mailbox.Text, "note", bytes.NewReader(data), int64(len(data)))
	for i := 0; i < 2; i++ {
		if _, err := c.Put(context.Background(), it, bytes.NewReader(data), nil); err != nil {
			t.Fatal(err)
		}
	}
	if items, _ := store.List(mailbox.Inbox); len(items) != 1 {
		t.Fatalf("%+v", items)
	}
}

func TestListGetDeleteFromOutbox(t *testing.T) {
	store := &mailbox.Store{Root: t.TempDir()}
	data := randBytes(mailbox.ChunkSize + 7)
	added, err := store.Add(mailbox.Outbox, mailbox.File, "result.bin", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := session(t, store)
	items, err := c.List()
	if err != nil || len(items) != 1 || items[0].ID != added.ID {
		t.Fatalf("%+v %v", items, err)
	}
	var out bytes.Buffer
	it, err := c.Get(context.Background(), added.ID, 0, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := mailbox.Verify(it, bytes.NewReader(out.Bytes())); err != nil {
		t.Fatal(err)
	}
	// resume from the middle
	var tail bytes.Buffer
	half := int64(len(data) / 2)
	if _, err := c.Get(context.Background(), added.ID, half, &tail, nil); err != nil || !bytes.Equal(tail.Bytes(), data[half:]) {
		t.Fatalf("download resume: %v", err)
	}
	if err := c.Delete(added.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := c.List(); len(items) != 0 {
		t.Fatal("the item should be deleted")
	}
	var re *proto.RemoteError
	if _, err := c.Get(context.Background(), added.ID, 0, io.Discard, nil); !errors.As(err, &re) {
		t.Fatalf("asking for a deleted item must give a hub error: %v", err)
	}
}

// Principle: the edge can only write to the inbox and only read from/delete in the outbox.
func TestDirectionRulesAreEnforcedByHub(t *testing.T) {
	store := &mailbox.Store{Root: t.TempDir()}
	inItem, _ := store.Add(mailbox.Inbox, mailbox.Text, "hub-secret", strings.NewReader("x"))
	_, conn := session(t, store)
	rw := proto.Armor(conn)

	ask := func(kind proto.Kind, req any) error {
		if err := proto.WriteJSON(rw, kind, req); err != nil {
			t.Fatal(err)
		}
		_, err := proto.Expect(rw, proto.KindOK)
		return err
	}
	var re *proto.RemoteError
	if err := ask(proto.KindList, mailbox.ListReq{Box: mailbox.Inbox}); !errors.As(err, &re) {
		t.Errorf("listing the inbox must be refused: %v", err)
	}
	if err := ask(proto.KindGetBegin, mailbox.GetReq{Box: mailbox.Inbox, ID: inItem.ID}); !errors.As(err, &re) {
		t.Errorf("reading from the inbox must be refused: %v", err)
	}
	if err := ask(proto.KindDelete, mailbox.DeleteReq{Box: mailbox.Inbox, ID: inItem.ID}); !errors.As(err, &re) {
		t.Errorf("deleting from the inbox must be refused: %v", err)
	}
	if _, err := store.Find(mailbox.Inbox, inItem.ID); err != nil {
		t.Fatal("the inbox item must not have been touched")
	}
}

func TestChunkWithoutBeginAndBadHash(t *testing.T) {
	store := &mailbox.Store{Root: t.TempDir()}
	_, conn := session(t, store)
	rw := proto.Armor(conn)
	proto.WriteFrame(rw, proto.Frame{Kind: proto.KindChunk, Payload: make([]byte, 12)})
	var re *proto.RemoteError
	if _, err := proto.Expect(rw, proto.KindOK); !errors.As(err, &re) {
		t.Fatalf("chunk without Begin: %v", err)
	}

	// content that does not match the declared hash
	c := mailbox.NewClient(rw)
	good := []byte("real content")
	it, _ := mailbox.NewItem(mailbox.File, "a", bytes.NewReader(good), int64(len(good)))
	if _, err := c.Put(context.Background(), it, bytes.NewReader([]byte("FAKE content")), nil); !errors.As(err, &re) {
		t.Fatalf("the hub must reject content with another hash: %v", err)
	}
	if items, _ := store.List(mailbox.Inbox); len(items) != 0 {
		t.Fatal("it must not be published")
	}
}
