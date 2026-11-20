package proto

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestArmorRoundTripBinaryFrames(t *testing.T) {
	payload := make([]byte, 100000)
	for i := range payload {
		payload[i] = byte(i) // includes 0x00, 0x0a, 0x0d, 0xff
	}
	var wire bytes.Buffer
	for i := 0; i < 3; i++ {
		if err := WriteFrame(NewArmorWriter(&wire), Frame{Kind: KindChunk, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	// the wire is line text: no control bytes except the trailing newline
	for _, b := range wire.Bytes() {
		if b != '\n' && (b < 0x20 || b > 0x7e) {
			t.Fatalf("non-printable byte on the wire: %#x", b)
		}
	}
	r := NewArmorReader(&wire)
	for i := 0; i < 3; i++ {
		f, err := ReadFrame(r)
		if err != nil || !bytes.Equal(f.Payload, payload) {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if _, err := ReadFrame(r); !errors.Is(err, io.EOF) {
		t.Fatalf("should end with a clean EOF: %v", err)
	}
}

// PowerShell may turn \n into \r\n: the reader must tolerate it.
func TestArmorToleratesCRLFAndBlankLines(t *testing.T) {
	var wire bytes.Buffer
	WriteFrame(NewArmorWriter(&wire), Frame{Kind: KindOK, Payload: []byte("hello")})
	mangled := "\r\n" + strings.ReplaceAll(wire.String(), "\n", "\r\n") + "\r\n"
	f, err := ReadFrame(NewArmorReader(strings.NewReader(mangled)))
	if err != nil || string(f.Payload) != "hello" {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestArmorRejectsGarbage(t *testing.T) {
	_, err := ReadFrame(NewArmorReader(strings.NewReader("Welcome to Windows PowerShell!\n")))
	if err == nil || !strings.Contains(err.Error(), "no valid frames") {
		t.Fatalf("%v", err)
	}
}

func TestArmorBidirectional(t *testing.T) {
	var buf bytes.Buffer
	rw := Armor(ReadWriter{Reader: &buf, Writer: &buf})
	WriteFrame(rw, Frame{Kind: KindList})
	if f, err := ReadFrame(rw); err != nil || f.Kind != KindList {
		t.Fatalf("%+v %v", f, err)
	}
}
