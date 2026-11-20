// Package proto defines the frames of the hub<->edge protocol over the stdio of SSH.
//
// Frame: [version u8][kind u8][length u32 big-endian][payload]. The version lets the protocol evolve without breaking older
// clients. The channel is the stdin/stdout of `gitsi serve --stdio`: only frames travel on stdout (human messages go to stderr).
package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Version of the protocol.
const Version uint8 = 1

// MaxPayload caps a frame's payload: a faulty or malicious peer cannot make us allocate memory without bound.
const MaxPayload = 8 << 20

// Kind identifies the frame type.
type Kind uint8

const (
	KindHello    Kind = iota + 1 // negotiation: version and capabilities
	KindError                    // human-readable error
	KindPutBegin                 // starts an item (metadata: kind, name, size, sha256)
	KindChunk                    // content block with its sha256 (resumable)
	KindPutEnd                   // end and verification
	KindList                     // list of mailbox items
	KindGetBegin                 // asks for an item (with an offset to resume)
	KindManifest                 // folder manifest (to send only the differences)
	KindOK                       // generic acknowledgement (optional payload)
	KindDelete                   // deletes a mailbox item (after receiving it)
	KindWatch                    // the edge subscribes to the hub's notices and says what it knows (watch)
	KindEvent                    // a notice from the hub (watch)
	KindPing                     // hub heartbeat during watch
)

// Known reports whether the kind exists in this protocol version.
func (k Kind) Known() bool { return k >= KindHello && k <= KindPing }

// Frame is a complete frame.
type Frame struct {
	Kind    Kind
	Payload []byte
}

// ErrVersion means the other side speaks a different version.
var ErrVersion = errors.New("incompatible protocol version")

// WriteFrame writes a frame in one go (header and payload in a single Write).
func WriteFrame(w io.Writer, f Frame) error {
	if !f.Kind.Known() {
		return fmt.Errorf("unknown frame kind: %d", f.Kind)
	}
	if len(f.Payload) > MaxPayload {
		return fmt.Errorf("payload too large: %d > %d", len(f.Payload), MaxPayload)
	}
	buf := make([]byte, 6+len(f.Payload))
	buf[0], buf[1] = Version, byte(f.Kind)
	binary.BigEndian.PutUint32(buf[2:6], uint32(len(f.Payload)))
	copy(buf[6:], f.Payload)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads a frame. It returns io.EOF only when the channel was closed cleanly between frames.
func ReadFrame(r io.Reader) (Frame, error) {
	var h [6]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, fmt.Errorf("channel cut in the middle of a header: %w", err)
		}
		return Frame{}, err // clean io.EOF
	}
	if h[0] != Version {
		return Frame{}, fmt.Errorf("%w: the other side uses %d, this side uses %d", ErrVersion, h[0], Version)
	}
	k := Kind(h[1])
	if !k.Known() {
		return Frame{}, fmt.Errorf("unknown frame kind: %d", h[1])
	}
	n := binary.BigEndian.Uint32(h[2:6])
	if n > MaxPayload {
		return Frame{}, fmt.Errorf("payload too large: %d > %d", n, MaxPayload)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return Frame{}, fmt.Errorf("channel cut in the middle of a payload: %w", err)
	}
	return Frame{Kind: k, Payload: p}, nil
}

// Hello is the payload of KindHello: each side announces its version, agent and capabilities.
type Hello struct {
	Version uint8    `json:"version"`
	Agent   string   `json:"agent"`
	Caps    []string `json:"caps,omitempty"`
	// Info is data the hub announces about itself (folder layout, version...). It is an open map: adding keys does not break a
	// client that does not know them (it ignores them), so the protocol Version does not need to change.
	Info map[string]string `json:"info,omitempty"`
}

// ErrorPayload is the payload of KindError.
type ErrorPayload struct {
	Message string `json:"message"`
}

// WriteJSON writes a frame with a JSON payload.
func WriteJSON(w io.Writer, k Kind, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteFrame(w, Frame{Kind: k, Payload: b})
}

// WriteError writes an error frame.
func WriteError(w io.Writer, msg string) error {
	return WriteJSON(w, KindError, ErrorPayload{Message: msg})
}

// RemoteError is an error sent by the other side.
type RemoteError struct{ Message string }

func (e *RemoteError) Error() string { return "the hub answered: " + e.Message }

// Expect reads a frame and requires the given kind; a KindError is returned as a *RemoteError.
func Expect(r io.Reader, k Kind) (Frame, error) {
	f, err := ReadFrame(r)
	if err != nil {
		return Frame{}, err
	}
	if f.Kind == KindError {
		var e ErrorPayload
		_ = json.Unmarshal(f.Payload, &e)
		return Frame{}, &RemoteError{Message: e.Message}
	}
	if f.Kind != k {
		return Frame{}, fmt.Errorf("expected frame %d and got %d", k, f.Kind)
	}
	return f, nil
}

// ClientHandshake runs on the edge: it sends its Hello and validates the hub's.
func ClientHandshake(rw io.ReadWriter, agent string, caps ...string) (Hello, error) {
	if err := WriteJSON(rw, KindHello, Hello{Version: Version, Agent: agent, Caps: caps}); err != nil {
		return Hello{}, err
	}
	f, err := Expect(rw, KindHello)
	if err != nil {
		return Hello{}, err
	}
	var h Hello
	if err := json.Unmarshal(f.Payload, &h); err != nil {
		return Hello{}, fmt.Errorf("invalid hello: %w", err)
	}
	if h.Version != Version {
		return Hello{}, fmt.Errorf("%w: hub %d, edge %d", ErrVersion, h.Version, Version)
	}
	return h, nil
}
