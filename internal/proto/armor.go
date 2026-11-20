package proto

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
)

// The real channel is the stdin/stdout of a process started by the remote shell (Windows PowerShell 5.1), which may reinterpret
// bytes (encoding, line endings). For binary frames to survive, the wire carries TEXT: each Write (one whole frame) goes out as
// one base64 line. It costs about 33% of bandwidth and buys robustness.

// armorWriter encodes each Write as one base64 line.
type armorWriter struct{ w io.Writer }

// NewArmorWriter wraps w so each Write travels as one line of text.
func NewArmorWriter(w io.Writer) io.Writer { return armorWriter{w} }

func (a armorWriter) Write(p []byte) (int, error) {
	line := make([]byte, base64.StdEncoding.EncodedLen(len(p))+1)
	base64.StdEncoding.Encode(line, p)
	line[len(line)-1] = '\n'
	if _, err := a.w.Write(line); err != nil {
		return 0, err
	}
	return len(p), nil
}

// armorReader decodes base64 lines and delivers them as a byte stream.
type armorReader struct {
	r   *bufio.Reader
	buf []byte
}

// NewArmorReader is the counterpart of NewArmorWriter. It tolerates CRLF and blank lines.
func NewArmorReader(r io.Reader) io.Reader { return &armorReader{r: bufio.NewReaderSize(r, 64<<10)} }

func (a *armorReader) Read(p []byte) (int, error) {
	for len(a.buf) == 0 {
		line, err := a.r.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			dec := make([]byte, base64.StdEncoding.DecodedLen(len(line)))
			n, derr := base64.StdEncoding.Decode(dec, line)
			if derr != nil {
				return 0, fmt.Errorf("the channel carries no valid frames (did the remote shell print foreign text?): %w", derr)
			}
			a.buf = dec[:n]
		}
		if err != nil && len(a.buf) == 0 {
			return 0, err
		}
	}
	n := copy(p, a.buf)
	a.buf = a.buf[n:]
	return n, nil
}

// ReadWriter joins a reader and a writer into an io.ReadWriter.
type ReadWriter struct {
	io.Reader
	io.Writer
}

// Armor wraps a bidirectional channel with the text encoding.
func Armor(rw io.ReadWriter) io.ReadWriter {
	return ReadWriter{Reader: NewArmorReader(rw), Writer: NewArmorWriter(rw)}
}
