package hub

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// pair connects a client and a server with in-memory pipes (no SSH or processes).
func pair(t *testing.T, cfg Config) (client net.Conn, done chan error) {
	c, s := net.Pipe()
	done = make(chan error, 1)
	go func() {
		err := Serve(context.Background(), s, s, cfg)
		s.Close()
		done <- err
	}()
	t.Cleanup(func() { c.Close() })
	return c, done
}

func wait(t *testing.T, done chan error) error {
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not finish")
		return nil
	}
}

func TestHandshakeAndCleanClose(t *testing.T) {
	c, done := pair(t, Config{Agent: "gitsi/test", Caps: []string{"drop"}})
	h, err := proto.ClientHandshake(c, "edge", "drop")
	if err != nil || h.Agent != "gitsi/test" || len(h.Caps) != 1 || h.Caps[0] != "drop" {
		t.Fatalf("%+v %v", h, err)
	}
	c.Close() // clean close between frames
	if err := wait(t, done); err != nil {
		t.Fatalf("a clean close must give nil: %v", err)
	}
}

func TestRejectsOldClientVersion(t *testing.T) {
	c, done := pair(t, Config{})
	go c.Write([]byte{99, byte(proto.KindHello), 0, 0, 0, 2, '{', '}'})
	_, err := proto.Expect(c, proto.KindHello)
	var re *proto.RemoteError
	if !errors.As(err, &re) {
		t.Fatalf("the client must receive a readable error: %v", err)
	}
	if err := wait(t, done); !errors.Is(err, proto.ErrVersion) {
		t.Fatalf("%v", err)
	}
}

func TestMustStartWithHello(t *testing.T) {
	c, done := pair(t, Config{})
	go proto.WriteFrame(c, proto.Frame{Kind: proto.KindList})
	if _, err := proto.Expect(c, proto.KindHello); err == nil {
		t.Fatal("it should answer with an error")
	}
	if wait(t, done) == nil {
		t.Fatal("Serve must end with an error when there is no Hello")
	}
}

// handlerFunc adapts a function to Handler (only the tests need it).
type handlerFunc func(ctx context.Context, f proto.Frame, w io.Writer) error

func (h handlerFunc) Handle(ctx context.Context, f proto.Frame, w io.Writer) error {
	return h(ctx, f, w)
}

func TestHandlerErrorKeepsSessionAlive(t *testing.T) {
	calls := 0
	h := handlerFunc(func(_ context.Context, f proto.Frame, w io.Writer) error {
		calls++
		if string(f.Payload) == "bad" {
			return errors.New("rejected")
		}
		return proto.WriteFrame(w, proto.Frame{Kind: proto.KindOK, Payload: f.Payload})
	})
	c, done := pair(t, Config{Handler: h})
	if _, err := proto.ClientHandshake(c, "edge"); err != nil {
		t.Fatal(err)
	}
	// 1: handler error, the session goes on
	go proto.WriteFrame(c, proto.Frame{Kind: proto.KindPutBegin, Payload: []byte("bad")})
	var re *proto.RemoteError
	if _, err := proto.Expect(c, proto.KindOK); !errors.As(err, &re) || re.Message != "rejected" {
		t.Fatalf("%v", err)
	}
	// 2: the next request works
	go proto.WriteFrame(c, proto.Frame{Kind: proto.KindPutBegin, Payload: []byte("good")})
	f, err := proto.Expect(c, proto.KindOK)
	if err != nil || string(f.Payload) != "good" {
		t.Fatalf("%+v %v", f, err)
	}
	c.Close()
	if err := wait(t, done); err != nil || calls != 2 {
		t.Fatalf("%v calls=%d", err, calls)
	}
}

func TestNoHandlerAnswersUnsupported(t *testing.T) {
	c, _ := pair(t, Config{})
	proto.ClientHandshake(c, "edge")
	go proto.WriteFrame(c, proto.Frame{Kind: proto.KindList})
	var re *proto.RemoteError
	if _, err := proto.Expect(c, proto.KindOK); !errors.As(err, &re) {
		t.Fatalf("%v", err)
	}
}

func TestContextCancelStopsBetweenFrames(t *testing.T) {
	cl, s := net.Pipe()
	defer cl.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, s, s, Config{}) }()
	proto.ClientHandshake(cl, "edge")
	cancel()
	go proto.WriteFrame(cl, proto.Frame{Kind: proto.KindList}) // unblocks the pending read
	if err := wait(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

// The hub may announce data about itself in the greeting; a client that does not know it ignores it (open map).
func TestHelloCarriesInfo(t *testing.T) {
	c, done := pair(t, Config{Agent: "gitsi/test", Info: map[string]string{"layout.repos": "C:/r", "version": "9"}})
	h, err := proto.ClientHandshake(c, "edge")
	if err != nil || h.Info["layout.repos"] != "C:/r" || h.Info["version"] != "9" {
		t.Fatalf("%+v %v", h, err)
	}
	c.Close()
	wait(t, done)
}
