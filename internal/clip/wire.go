package clip

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// chunk is the size of each KindClipData frame: small enough that a big item does not need a big frame buffer, large enough that
// the frames' overhead does not matter.
const chunk = 1 << 20

// ErrRefused is returned by ReadItem when the header is invalid or the item is larger than the receiver accepts. The content was
// read and discarded, so the session stays in sync and goes on.
var ErrRefused = errors.New("clipboard item refused")

// WriteItem writes an item: its header (KindClip) and its content in KindClipData frames. The caller must be the only writer of w
// while it runs (frames must not interleave with other frames).
func WriteItem(w io.Writer, it Item, data []byte) error {
	if int64(len(data)) != it.Size {
		return fmt.Errorf("item size %d does not match its content (%d bytes)", it.Size, len(data))
	}
	if err := proto.WriteJSON(w, proto.KindClip, it); err != nil {
		return err
	}
	for len(data) > 0 {
		n := min(len(data), chunk)
		if err := proto.WriteFrame(w, proto.Frame{Kind: proto.KindClipData, Payload: data[:n]}); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// ReadItem reads the content that follows a KindClip header already read from r, and verifies it. max is the receiver's limit.
// An invalid or too large item returns an error wrapping ErrRefused, after draining its content; an item whose content does not
// match its SHA256 also wraps ErrRefused. Any other error is the channel's (the session is over).
func ReadItem(header proto.Frame, r io.Reader, max int64) (Item, []byte, error) {
	var it Item
	if err := json.Unmarshal(header.Payload, &it); err != nil {
		return Item{}, nil, fmt.Errorf("%w: invalid header", ErrRefused) // no size known: nothing can be drained
	}
	keep := it.Valid(max)
	var data []byte
	if keep {
		data = make([]byte, 0, it.Size)
	}
	for got := int64(0); got < it.Size; {
		f, err := proto.ReadFrame(r)
		if err != nil {
			return Item{}, nil, err
		}
		if f.Kind != proto.KindClipData {
			return Item{}, nil, fmt.Errorf("expected clipboard content and got frame %d", f.Kind)
		}
		got += int64(len(f.Payload))
		if got > it.Size {
			return Item{}, nil, errors.New("the clipboard content is longer than its header says")
		}
		if keep {
			data = append(data, f.Payload...)
		}
	}
	if !keep {
		if it.Size > max {
			return it, nil, fmt.Errorf("%w: %s is larger than the limit here (%s)", ErrRefused, Human(it.Size), Human(max))
		}
		return it, nil, fmt.Errorf("%w: invalid header", ErrRefused)
	}
	if Sum(data) != it.SHA256 {
		return it, nil, fmt.Errorf("%w: its content does not match its SHA256", ErrRefused)
	}
	return it, data, nil
}
