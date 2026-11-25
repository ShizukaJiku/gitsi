package hub

import (
	"context"
	"fmt"
	"io"

	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// Router is a Handler that says which frame kinds it serves. The handlers that share a session (mailbox, watch) implement it.
type Router interface {
	Handler
	Handles(k proto.Kind) bool
}

// Mux hands each frame to the first handler that serves it. A frame nobody serves is reported to the edge as an error.
type Mux []Router

// Handle implements Handler.
func (m Mux) Handle(ctx context.Context, f proto.Frame, w io.Writer) error {
	for _, h := range m {
		if h.Handles(f.Kind) {
			return h.Handle(ctx, f, w)
		}
	}
	return fmt.Errorf("operation not supported by this hub (frame %d)", f.Kind)
}

// TakesOver implements SessionHandler: a kind is taken over when the handler that serves it is a SessionHandler for it.
func (m Mux) TakesOver(k proto.Kind) bool {
	for _, h := range m {
		if h.Handles(k) {
			sh, ok := h.(SessionHandler)
			return ok && sh.TakesOver(k)
		}
	}
	return false
}

// HandleSession implements SessionHandler.
func (m Mux) HandleSession(ctx context.Context, f proto.Frame, r io.Reader, w io.Writer) error {
	for _, h := range m {
		if h.Handles(f.Kind) {
			if sh, ok := h.(SessionHandler); ok {
				return sh.HandleSession(ctx, f, r, w)
			}
			return h.Handle(ctx, f, w)
		}
	}
	return fmt.Errorf("operation not supported by this hub (frame %d)", f.Kind)
}
