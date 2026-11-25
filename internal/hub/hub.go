// Package hub is the server side: the edge runs `gitsi serve --stdio` over SSH and it speaks the internal/proto protocol over
// stdin/stdout. It never opens a port (docs/ARCHITECTURE.md, section 2).
//
// The server does not know the CLI (it does not import module): it receives a Handler with the logic for each frame kind.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// Handler serves an already negotiated frame. It writes its answers to w. A returned error is reported to the edge as a
// KindError and the session goes on.
type Handler interface {
	Handle(ctx context.Context, f proto.Frame, w io.Writer) error
}

// SessionHandler is an optional Handler capability (checked by type assertion): for the kinds it takes over, it keeps the session
// for itself and also receives the rest of the edge's frames (r). The session ends when HandleSession returns. watch uses it: its
// session lasts while the edge listens, and the edge sends clipboard items inside it.
type SessionHandler interface {
	TakesOver(k proto.Kind) bool
	HandleSession(ctx context.Context, f proto.Frame, r io.Reader, w io.Writer) error
}

// Config describes the server.
type Config struct {
	Agent   string            // announced in the Hello
	Caps    []string          // capabilities this hub offers
	Info    map[string]string // data the hub announces in the greeting (layout, version...)
	Handler Handler           // nil: only the greeting is served
}

// Serve serves frames until the channel closes cleanly (nil) or there is a protocol error. Only frames go out on out. A Handler
// failure does not close the session; a protocol failure does.
func Serve(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	// 1. The first message must be a Hello with our version.
	f, err := proto.ReadFrame(in)
	if err != nil {
		if errors.Is(err, proto.ErrVersion) {
			_ = proto.WriteError(out, err.Error())
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if f.Kind != proto.KindHello {
		_ = proto.WriteError(out, "expected Hello")
		return errors.New("the client did not start with Hello")
	}
	var peer proto.Hello
	if err := json.Unmarshal(f.Payload, &peer); err != nil || peer.Version != proto.Version {
		_ = proto.WriteError(out, fmt.Sprintf("unsupported version (hub=%d)", proto.Version))
		return fmt.Errorf("%w: client %d", proto.ErrVersion, peer.Version)
	}
	if err := proto.WriteJSON(out, proto.KindHello, proto.Hello{Version: proto.Version, Agent: cfg.Agent, Caps: cfg.Caps, Info: cfg.Info}); err != nil {
		return err
	}

	// 2. Frame loop.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := proto.ReadFrame(in)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			_ = proto.WriteError(out, err.Error())
			return err
		}
		// cancellation may arrive while waiting for the frame: do not serve it (let alone answer it) once cancelled
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.Kind == proto.KindHello {
			_ = proto.WriteError(out, "repeated Hello")
			continue
		}
		if cfg.Handler == nil {
			_ = proto.WriteError(out, "operation not supported by this hub")
			continue
		}
		if sh, ok := cfg.Handler.(SessionHandler); ok && sh.TakesOver(f.Kind) {
			if err := sh.HandleSession(ctx, f, in, out); err != nil {
				_ = proto.WriteError(out, err.Error())
			}
			return nil // the handler owned the rest of the session
		}
		if err := cfg.Handler.Handle(ctx, f, out); err != nil {
			if werr := proto.WriteError(out, err.Error()); werr != nil {
				return werr
			}
		}
	}
}
