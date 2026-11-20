package hub

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// A channel that closes without saying anything (e.g. the edge hung up before greeting) is a clean close, not an error.
func TestEOFBeforeHelloIsACleanClose(t *testing.T) {
	c, done := pair(t, Config{})
	c.Close()
	if err := wait(t, done); err != nil {
		t.Fatalf("%v", err)
	}
}

// Garbage instead of the greeting (another program, or a broken channel) ends the session with an error.
func TestGarbageInsteadOfHelloEndsTheSession(t *testing.T) {
	c, done := pair(t, Config{})
	go func() { c.Write([]byte{proto.Version, 0xEE, 0xFF, 0xFF, 0xFF, 0xFF}); c.Close() }()
	if err := wait(t, done); err == nil {
		t.Fatal("an unreadable frame must give an error")
	}
}

// A second Hello is rejected without closing the session; serving goes on afterwards.
func TestRepeatedHelloIsRejectedButTheSessionGoesOn(t *testing.T) {
	got := make(chan proto.Kind, 1)
	c, done := pair(t, Config{Handler: handlerFunc(func(_ context.Context, f proto.Frame, w io.Writer) error {
		got <- f.Kind
		return proto.WriteFrame(w, proto.Frame{Kind: proto.KindList})
	})})
	if _, err := proto.ClientHandshake(c, "edge"); err != nil {
		t.Fatal(err)
	}
	go proto.WriteJSON(c, proto.KindHello, proto.Hello{Version: proto.Version})
	var re *proto.RemoteError
	if _, err := proto.Expect(c, proto.KindList); !errors.As(err, &re) || !strings.Contains(re.Error(), "repeated Hello") {
		t.Fatalf("%v", err)
	}
	go proto.WriteFrame(c, proto.Frame{Kind: proto.KindList})
	if _, err := proto.Expect(c, proto.KindList); err != nil {
		t.Fatalf("the session must go on: %v", err)
	}
	if k := <-got; k != proto.KindList {
		t.Fatalf("%v", k)
	}
	c.Close()
	wait(t, done)
}

// An unreadable frame mid-session is reported to the edge and ends the session (it is a protocol error, not a Handler one).
func TestGarbageMidSessionIsReportedAndEndsTheSession(t *testing.T) {
	c, done := pair(t, Config{})
	if _, err := proto.ClientHandshake(c, "edge"); err != nil {
		t.Fatal(err)
	}
	go c.Write([]byte{proto.Version, 0xEE, 0xFF, 0xFF, 0xFF, 0xFF})
	_, _ = proto.ReadFrame(c) // an error frame or the close: either is fine as long as Serve ends with an error
	if err := wait(t, done); err == nil {
		t.Fatal("Serve must end with an error")
	}
}
