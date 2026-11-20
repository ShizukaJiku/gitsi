package hubclient

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/proto"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// fakeTr simulates the remote shell: depending on the command, a real hub is on the other side or a mute process.
type fakeTr struct {
	works   func(cmd string) bool // true: the server starts with this form
	cmds    []string
	version uint8
}

type pipeStream struct{ net.Conn }

func (fakeTr) Run(context.Context, string) (transport.Result, error) { return transport.Result{}, nil }
func (fakeTr) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}
func (f *fakeTr) Stream(_ context.Context, cmd string) (transport.Stream, error) {
	f.cmds = append(f.cmds, cmd)
	cl, sv := net.Pipe()
	if f.works(cmd) {
		go func() {
			hub.Serve(context.Background(), proto.NewArmorReader(sv), proto.NewArmorWriter(sv),
				hub.Config{Agent: "fake-hub", Caps: []string{"drop"}})
			sv.Close()
		}()
	} // otherwise nobody serves; the client waits until the timeout
	return pipeStream{cl}, nil
}

func fast(t *testing.T) {
	old := HandshakeTimeout
	HandshakeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { HandshakeTimeout = old })
}

func TestDialDirect(t *testing.T) {
	tr := &fakeTr{works: func(string) bool { return true }}
	c, err := Dial(context.Background(), tr, "drop")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Method != "hub-config" || !c.Has("drop") || c.Hello.Agent != "fake-hub" {
		t.Fatalf("%+v", c)
	}
	// the edge passes no path: the command reads the hub's configuration to find its gitsi
	if len(tr.cmds) != 1 || !strings.Contains(tr.cmds[0], `.gitsi\config.json`) || !strings.Contains(tr.cmds[0], "serve --stdio") || !strings.Contains(tr.cmds[0], "$c.home") {
		t.Fatalf("%v", tr.cmds)
	}
}

// When gitsi is not where the hub's config says, the one on the PATH is tried.
func TestDialFallsBackToPathWhenHubConfigLaunchFindsNothing(t *testing.T) {
	fast(t)
	tr := &fakeTr{works: func(cmd string) bool { return cmd == "& gitsi serve --stdio" }}
	c, err := Dial(context.Background(), tr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Method != "path" || len(tr.cmds) != 2 {
		t.Fatalf("%s %v", c.Method, tr.cmds)
	}
}

func TestDialFailsWithActionableMessage(t *testing.T) {
	fast(t)
	tr := &fakeTr{works: func(string) bool { return false }}
	_, err := Dial(context.Background(), tr)
	if err == nil || !strings.Contains(err.Error(), "update -publish") {
		t.Fatalf("%v", err)
	}
}

func TestDialNeedsATransportAndInterpolatesNothing(t *testing.T) {
	_, err := Dial(context.Background(), nil)
	if fe, ok := fault.As(err); !ok || fe.Kind != fault.NotConfigured {
		t.Fatalf("without a transport: %v", err)
	}
	// the launch command is a constant: no user or hub input is interpolated into the remote shell
	tr := &fakeTr{works: func(string) bool { return true }}
	c, err := Dial(context.Background(), tr, "drop")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if tr.cmds[0] != launchFromHubConfig {
		t.Fatalf("the command must be constant: %q", tr.cmds[0])
	}
}

func TestDialDoesNotRetryOnServerError(t *testing.T) {
	fast(t)
	// a server that answers with a version error: trying another form makes no sense
	tr := &errTr{}
	_, err := Dial(context.Background(), tr)
	var re *proto.RemoteError
	if !errors.As(err, &re) || tr.calls != 1 {
		t.Fatalf("%v calls=%d", err, tr.calls)
	}
}

type errTr struct {
	fakeTr
	calls int
}

func (e *errTr) Stream(_ context.Context, _ string) (transport.Stream, error) {
	e.calls++
	cl, sv := net.Pipe()
	go func() {
		bufio.NewReader(sv).ReadString(10) // waits for the client's Hello line
		proto.WriteError(proto.NewArmorWriter(sv), "unsupported version")
	}()
	return pipeStream{cl}, nil
}
