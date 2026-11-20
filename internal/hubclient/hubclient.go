// Package hubclient opens the edge's session with `gitsi serve --stdio` on the hub, through the Transport interface.
//
// It is the only place that knows how to start the server in the remote shell (PowerShell) and how to protect itself from it:
// the channel travels as base64 text (proto.Armor) and the process is started with `& <hub's gitsi> serve --stdio`.
//
// Verified with a real powershell.exe 5.1 (internal/e2e): the direct form passes stdin/stdout through; the alternative
// `$input | & exe ...` does NOT work (PowerShell buffers $input until EOF and the server never receives the greeting).
package hubclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/proto"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// HandshakeTimeout is how long each way of starting the server waits for the greeting.
var HandshakeTimeout = 10 * time.Second

// Conn is a negotiated session with the hub.
type Conn struct {
	RW     io.ReadWriter // channel already wrapped in base64
	Hello  proto.Hello   // the hub's greeting
	Method string        // the launch form that worked ("hub-config" or "path")
	stream transport.Stream
}

// Close ends the session and the remote process.
func (c *Conn) Close() error { return c.stream.Close() }

type method struct{ name, command string }

// launchFromHubConfig starts the server on the hub WITHOUT the edge knowing any path: the command itself reads the hub's
// configuration (~/.gitsi/config.json, always in the user's home folder) to learn where gitsi lives, and falls back to the default
// folder. The hub (config.home) decides where the binary is, not the edge.
const launchFromHubConfig = `$c = $null; try { $c = Get-Content -Raw -LiteralPath (Join-Path $env:USERPROFILE '.gitsi\config.json') -ErrorAction Stop | ConvertFrom-Json } catch { }; ` +
	`$h = if ($c -and $c.home) { $c.home } else { Join-Path $env:USERPROFILE 'gitsi' }; & (Join-Path $h 'gitsi.exe') serve --stdio`

// launchMethods are the ways to start the server in the remote shell, in order. Each must be verified before it goes here.
func launchMethods() []method {
	return []method{
		{"hub-config", launchFromHubConfig},
		{"path", "& gitsi serve --stdio"}, // fallback: gitsi is on the PATH of the hub's user
	}
}

// Dial opens a session with `gitsi serve --stdio` on the hub. It needs no path: the hub knows where its gitsi is.
func Dial(ctx context.Context, tr transport.Transport, caps ...string) (*Conn, error) {
	if tr == nil {
		return nil, fault.New(fault.NotConfigured, "this machine has no hub configured", "gitsi setup edge -host <hub-ip> -user <user>")
	}
	var errs []string
	for _, m := range launchMethods() {
		c, err := try(ctx, tr, m, caps)
		if err == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var re *proto.RemoteError
		fe, isFault := fault.As(err)
		if (isFault && fe.Kind != fault.HubBinary) || errors.As(err, &re) || errors.Is(err, proto.ErrVersion) {
			return nil, err // the cause is already known (network, key, server): trying another form does not help
		}
		errs = append(errs, fmt.Sprintf("%s: %v", m.name, err))
	}
	return nil, fmt.Errorf("the hub did not answer the greeting (%v). Is gitsi published on the hub? Run 'gitsi update -publish' on the hub", errs)
}

func try(ctx context.Context, tr transport.Transport, m method, caps []string) (*Conn, error) {
	st, err := tr.Stream(ctx, m.command)
	if err != nil {
		return nil, err
	}
	rw := proto.Armor(st)
	type result struct {
		h   proto.Hello
		err error
	}
	ch := make(chan result, 1)
	go func() {
		h, err := proto.ClientHandshake(rw, "gitsi-edge", caps...)
		ch <- result{h, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			fe, tail := diagnose(st)
			st.Close()
			if fe != nil {
				return nil, fe
			}
			if tail != "" {
				return nil, fmt.Errorf("%w (hub error output: %s)", r.err, tail)
			}
			return nil, r.err
		}
		return &Conn{RW: rw, Hello: r.h, Method: m.name, stream: st}, nil
	case <-time.After(HandshakeTimeout):
		fe, tail := diagnose(st)
		st.Close()
		if fe != nil {
			return nil, fe
		}
		if tail != "" {
			return nil, fmt.Errorf("no answer (hub error output: %s)", tail)
		}
		return nil, errors.New("no answer")
	case <-ctx.Done():
		st.Close()
		return nil, ctx.Err()
	}
}

// Has reports whether the hub announced a capability.
func (c *Conn) Has(cap string) bool {
	for _, x := range c.Hello.Caps {
		if x == cap {
			return true
		}
	}
	return false
}

// diagnose asks the Stream (when it can tell) for the classified cause and the end of its stderr.
func diagnose(st transport.Stream) (*fault.Error, string) {
	d, ok := st.(transport.StreamDiagnostics)
	if !ok {
		return nil, ""
	}
	tail := strings.TrimSpace(d.Stderr())
	if len(tail) > 300 {
		tail = "..." + tail[len(tail)-300:]
	}
	return d.Fault(), strings.Join(strings.Fields(strings.ReplaceAll(tail, "\r", " ")), " ")
}
