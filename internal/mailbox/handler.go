package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// Handler is the hub side of the mailbox protocol (it implements hub.Handler). There is one per session.
//
// Fixed direction: the edge only writes to the inbox and only reads from or deletes in the outbox.
type Handler struct {
	Store *Store

	active string // id of the upload in progress in this session
}

// Handles reports the mailbox frames (to share the session with other handlers, hub.Mux).
func (h *Handler) Handles(k proto.Kind) bool {
	switch k {
	case proto.KindPutBegin, proto.KindChunk, proto.KindPutEnd, proto.KindList, proto.KindGetBegin, proto.KindDelete:
		return true
	}
	return false
}

// Handle serves one frame; errors are returned and the server reports them to the edge.
func (h *Handler) Handle(ctx context.Context, f proto.Frame, w io.Writer) error {
	switch f.Kind {
	case proto.KindPutBegin:
		var req PutBeginReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return fmt.Errorf("invalid PutBegin: %w", err)
		}
		off, done, err := h.Store.Begin(Inbox, req.Item)
		if err != nil {
			return err
		}
		h.active = req.Item.ID
		if done {
			h.active = ""
		}
		return proto.WriteJSON(w, proto.KindOK, PutBeginResp{Offset: off, Complete: done})

	case proto.KindChunk:
		if h.active == "" {
			return errors.New("no upload in progress")
		}
		off, data, err := decodeChunk(f.Payload)
		if err != nil {
			return err
		}
		n, err := h.Store.Append(Inbox, h.active, off, data)
		if err != nil {
			return err
		}
		return proto.WriteJSON(w, proto.KindOK, ChunkAck{Offset: n})

	case proto.KindPutEnd:
		if h.active == "" {
			return errors.New("no upload in progress")
		}
		id := h.active
		h.active = ""
		it, err := h.Store.Commit(Inbox, id)
		if err != nil {
			return err
		}
		return proto.WriteJSON(w, proto.KindOK, it)

	case proto.KindList:
		var req ListReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return fmt.Errorf("invalid List: %w", err)
		}
		if req.Box != Outbox {
			return errors.New("the edge can only list the outbox")
		}
		items, err := h.Store.List(Outbox)
		if err != nil {
			return err
		}
		return proto.WriteJSON(w, proto.KindOK, ListResp{Items: items})

	case proto.KindGetBegin:
		var req GetReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return fmt.Errorf("invalid GetBegin: %w", err)
		}
		if req.Box != Outbox {
			return errors.New("the edge can only read from the outbox")
		}
		return h.serveGet(ctx, req, w)

	case proto.KindDelete:
		var req DeleteReq
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			return fmt.Errorf("invalid Delete: %w", err)
		}
		if req.Box != Outbox {
			return errors.New("the edge can only delete from the outbox")
		}
		if err := h.Store.Remove(Outbox, req.ID); err != nil {
			return err
		}
		return proto.WriteFrame(w, proto.Frame{Kind: proto.KindOK})
	}
	return fmt.Errorf("frame kind %d is not supported by the mailbox", f.Kind)
}

// serveGet answers OK(item) and then sends blocks to the end, closed by a PutEnd frame.
func (h *Handler) serveGet(ctx context.Context, req GetReq, w io.Writer) error {
	f, it, err := h.Store.Open(Outbox, req.ID)
	if err != nil {
		return err
	}
	defer f.Close()
	if req.Offset < 0 || req.Offset > it.Size {
		return fmt.Errorf("offset out of range: %d", req.Offset)
	}
	if err := proto.WriteJSON(w, proto.KindOK, it); err != nil {
		return err
	}
	buf := make([]byte, ChunkSize)
	for off := req.Offset; off < it.Size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := f.ReadAt(buf, off)
		if n > 0 {
			if err := proto.WriteFrame(w, proto.Frame{Kind: proto.KindChunk, Payload: encodeChunk(off, buf[:n])}); err != nil {
				return err
			}
			off += int64(n)
		}
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return rerr
		}
		if n == 0 {
			break
		}
	}
	return proto.WriteFrame(w, proto.Frame{Kind: proto.KindPutEnd})
}
