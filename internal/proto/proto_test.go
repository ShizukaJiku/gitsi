package proto

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var b bytes.Buffer
	in := Frame{Kind: KindChunk, Payload: []byte("hello\x00binary\xff")}
	if err := WriteFrame(&b, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFrame(&b)
	if err != nil || out.Kind != in.Kind || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := ReadFrame(&b); !errors.Is(err, io.EOF) {
		t.Fatalf("an empty channel must give a clean EOF, got %v", err)
	}
}

func TestEmptyPayload(t *testing.T) {
	var b bytes.Buffer
	WriteFrame(&b, Frame{Kind: KindOK})
	f, err := ReadFrame(&b)
	if err != nil || f.Kind != KindOK || len(f.Payload) != 0 {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestRejectsBadFrames(t *testing.T) {
	// different version
	if _, err := ReadFrame(bytes.NewReader([]byte{9, 1, 0, 0, 0, 0})); !errors.Is(err, ErrVersion) {
		t.Errorf("version: %v", err)
	}
	// unknown kind
	if _, err := ReadFrame(bytes.NewReader([]byte{Version, 200, 0, 0, 0, 0})); err == nil {
		t.Error("an unknown kind must fail")
	}
	// payload above the cap: must not allocate memory
	if _, err := ReadFrame(bytes.NewReader([]byte{Version, byte(KindChunk), 0xff, 0xff, 0xff, 0xff})); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("cap: %v", err)
	}
	// cut header and cut payload
	if _, err := ReadFrame(bytes.NewReader([]byte{Version, 1})); err == nil || errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("cut header: %v", err)
	}
	if _, err := ReadFrame(bytes.NewReader([]byte{Version, byte(KindChunk), 0, 0, 0, 5, 'a'})); err == nil {
		t.Error("a cut payload must fail")
	}
	// writing validates too
	if err := WriteFrame(io.Discard, Frame{Kind: 0}); err == nil {
		t.Error("kind 0 cannot be written")
	}
	if err := WriteFrame(io.Discard, Frame{Kind: KindChunk, Payload: make([]byte, MaxPayload+1)}); err == nil {
		t.Error("payload above the cap")
	}
}

func TestExpectSurfacesRemoteError(t *testing.T) {
	var b bytes.Buffer
	WriteError(&b, "no space left")
	_, err := Expect(&b, KindOK)
	var re *RemoteError
	if !errors.As(err, &re) || re.Message != "no space left" {
		t.Fatalf("%v", err)
	}
	b.Reset()
	WriteFrame(&b, Frame{Kind: KindList})
	if _, err := Expect(&b, KindOK); err == nil {
		t.Fatal("an unexpected kind must fail")
	}
}
