package mailbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/ShizukaJiku/gitsi/internal/proto"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// Progress reports progress (done, total) in bytes. It may be nil. While uploading it is called from another goroutine (the one
// reading the acknowledgements), never concurrently with itself and never after Put has returned.
type Progress func(done, total int64)

// Client is the edge side: it talks to the hub's Handler over an already negotiated channel (proto.ClientHandshake). It is not
// safe for concurrent use: one operation at a time per channel.
type Client struct {
	rw io.ReadWriter
	// Abort, when not nil, closes the channel. Put calls it when an upload is cancelled or rejected by the hub: if the hub stopped
	// reading, a pending write never returns on its own and only closing the channel unblocks it. It must be safe to call twice.
	Abort func()

	broken atomic.Bool // an upload failed with answers still pending: the channel is not reliable
}

// ErrBroken is returned by a Client whose channel was left in an undetermined state after an error: open another one.
var ErrBroken = errors.New("the channel was left in an undetermined state after an error: open a new session")

// NewClient wraps a channel whose greeting is already done.
func NewClient(rw io.ReadWriter) *Client { return &Client{rw: rw} }

func (c *Client) call(kind proto.Kind, req any, resp any) error {
	if c.broken.Load() {
		return ErrBroken
	}
	if req == nil {
		if err := proto.WriteFrame(c.rw, proto.Frame{Kind: kind}); err != nil {
			return err
		}
	} else if err := proto.WriteJSON(c.rw, kind, req); err != nil {
		return err
	}
	f, err := proto.Expect(c.rw, proto.KindOK)
	if err != nil {
		return err
	}
	if resp != nil {
		return json.Unmarshal(f.Payload, resp)
	}
	return nil
}

// NewItem computes a content's metadata: it reads src once to get its SHA256.
func NewItem(kind Kind, name string, src io.ReaderAt, size int64) (Item, error) {
	if err := safe.FileName(name); err != nil {
		return Item{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(src, 0, size)); err != nil {
		return Item{}, err
	}
	sha := hex.EncodeToString(h.Sum(nil))
	return Item{ID: NewID(kind, name, sha), Kind: kind, Name: name, Size: size, SHA256: sha}, nil
}

// Window is the number of unacknowledged blocks Put keeps in flight. With one block per ACK (stop-and-wait) throughput is capped
// at ChunkSize/RTT (about 6 MB/s at 40 ms RTT); with a window the cap becomes Window*ChunkSize/RTT. The hub is unchanged: it
// processes frames in order and answers one ACK per block.
const Window = 8

// Put uploads an item to the hub's inbox, resuming if it was left half done. It returns the item confirmed by the hub.
//
// When Put returns an error, the channel has answers pending and must not be reused: close it and, to continue, open another one
// and repeat (the hub resumes from what it stored).
func (c *Client) Put(ctx context.Context, it Item, src io.ReaderAt, prog Progress) (Item, error) {
	var begin PutBeginResp
	if err := c.call(proto.KindPutBegin, PutBeginReq{Item: it}, &begin); err != nil {
		return Item{}, err
	}
	if begin.Complete {
		if prog != nil {
			prog(it.Size, it.Size)
		}
		return it, nil
	}
	if begin.Offset < it.Size {
		if err := c.sendChunks(ctx, it, begin.Offset, src, prog); err != nil {
			c.broken.Store(true) // answers are left unread: the channel cannot be reused
			if cerr := ctx.Err(); cerr != nil {
				err = cerr // on cancel, Abort closes the channel and the I/O errors that follow are a consequence: report the cause
			}
			return Item{}, err
		}
	}
	var done Item
	if err := c.call(proto.KindPutEnd, nil, &done); err != nil {
		return Item{}, err
	}
	return done, nil
}

// sendChunks sends from start to the end with up to Window unacknowledged blocks. A separate reader consumes the ACKs (needed:
// if both sides wrote without reading, a channel with a small buffer would deadlock).
func (c *Client) sendChunks(ctx context.Context, it Item, start int64, src io.ReaderAt, prog Progress) error {
	if c.Abort != nil { // Ctrl-C or deadline with a stuck write: closing the channel is the only way to unblock it
		stop := context.AfterFunc(ctx, c.Abort)
		defer stop()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The ACK reader may still be alive (blocked on the channel) when sendChunks has already returned with an error. This lock
	// guarantees that, once returned, it never calls the caller's progress again (nor concurrently with the return).
	var mu sync.Mutex
	closed := false
	defer func() { mu.Lock(); closed = true; mu.Unlock() }()

	slots := make(chan struct{}, Window)
	acked := make(chan error, 1)
	go func() {
		for {
			f, err := proto.Expect(c.rw, proto.KindOK)
			var ack ChunkAck
			if err == nil {
				err = json.Unmarshal(f.Payload, &ack)
			}
			if err != nil {
				acked <- err
				if c.Abort != nil { // the sender may be blocked writing to a hub that no longer reads; the error goes first via acked
					c.Abort()
				}
				return
			}
			select {
			case <-slots:
			default: // an acknowledgement that matches no sent block: the hub breaks the protocol
				acked <- errors.New("the hub sent an unexpected acknowledgement")
				return
			}
			mu.Lock()
			if prog != nil && !closed {
				prog(ack.Offset, it.Size)
			}
			mu.Unlock()
			if ack.Offset >= it.Size {
				acked <- nil
				return
			}
		}
	}()

	early := func(err error) error { // the reader ended before everything was sent
		if err == nil {
			return errors.New("the hub acknowledged before receiving everything")
		}
		return err
	}
	buf := make([]byte, ChunkSize)
	for off := start; off < it.Size; {
		// If the hub already rejected something, not one more block is sent (select would choose at random between a free slot and
		// the error).
		select {
		case err := <-acked:
			return early(err)
		default:
		}
		select {
		case slots <- struct{}{}:
		case err := <-acked:
			return early(err)
		case <-ctx.Done():
			return ctx.Err()
		}
		n, err := src.ReadAt(buf, off)
		if n == 0 && err != nil {
			return fmt.Errorf("could not read the source at %d: %w", off, err)
		}
		if int64(n) > it.Size-off {
			n = int(it.Size - off)
		}
		if err := proto.WriteFrame(c.rw, proto.Frame{Kind: proto.KindChunk, Payload: encodeChunk(off, buf[:n])}); err != nil {
			select { // if the reader already had the hub's error, that one explains what happened (the write error is a consequence)
			case aerr := <-acked:
				return early(aerr)
			default:
			}
			return err
		}
		off += int64(n)
	}
	select {
	case err := <-acked:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// List returns the outbox items.
func (c *Client) List() ([]Item, error) {
	var r ListResp
	if err := c.call(proto.KindList, ListReq{Box: Outbox}, &r); err != nil {
		return nil, err
	}
	return r.Items, nil
}

// Get downloads an outbox item starting at offset, writing to w. It returns the item's metadata. The caller verifies the SHA256
// of the complete result (Verify).
func (c *Client) Get(ctx context.Context, id string, offset int64, w io.Writer, prog Progress) (it Item, err error) {
	if c.broken.Load() {
		return Item{}, ErrBroken
	}
	if err := proto.WriteJSON(c.rw, proto.KindGetBegin, GetReq{Box: Outbox, ID: id, Offset: offset}); err != nil {
		return Item{}, err
	}
	f, err := proto.Expect(c.rw, proto.KindOK)
	if err != nil {
		return Item{}, err
	}
	if err := json.Unmarshal(f.Payload, &it); err != nil {
		return Item{}, err
	}
	// From here on the hub streams blocks: if the download is cut, frames are left unread and the channel is no longer reliable.
	// (A rejection before starting, e.g. "does not exist", desynchronizes nothing and does not mark the channel.)
	defer func() {
		var remote *proto.RemoteError
		// A KindError from the hub ENDS the stream cleanly (the hub keeps serving): the channel stays in sync. Any other failure
		// (read, write, order, cancellation) leaves frames unread.
		if err != nil && !errors.As(err, &remote) {
			c.broken.Store(true)
		}
	}()
	next := offset
	for {
		if err := ctx.Err(); err != nil {
			return it, err
		}
		fr, err := proto.ReadFrame(c.rw)
		if err != nil {
			return it, err
		}
		switch fr.Kind {
		case proto.KindError:
			var e proto.ErrorPayload
			_ = json.Unmarshal(fr.Payload, &e)
			return it, &proto.RemoteError{Message: e.Message}
		case proto.KindPutEnd:
			if next != it.Size {
				return it, fmt.Errorf("incomplete transfer: %d of %d bytes", next, it.Size)
			}
			return it, nil
		case proto.KindChunk:
			off, data, err := decodeChunk(fr.Payload)
			if err != nil {
				return it, err
			}
			if off != next {
				return it, fmt.Errorf("block out of order: expected %d, got %d", next, off)
			}
			if _, err := w.Write(data); err != nil {
				return it, err
			}
			next += int64(len(data))
			if prog != nil {
				prog(next, it.Size)
			}
		default:
			return it, errors.New("unexpected frame during the download")
		}
	}
}

// Delete removes an outbox item (after receiving and verifying it).
func (c *Client) Delete(id string) error {
	return c.call(proto.KindDelete, DeleteReq{Box: Outbox, ID: id}, nil)
}

// Verify checks that r's content has the item's size and SHA256.
func Verify(it Item, r io.Reader) error {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return err
	}
	if n != it.Size {
		return fmt.Errorf("size differs: %d, expected %d", n, it.Size)
	}
	if hex.EncodeToString(h.Sum(nil)) != it.SHA256 {
		return ErrHash
	}
	return nil
}
