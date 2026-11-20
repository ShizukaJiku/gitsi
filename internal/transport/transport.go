// Package transport defines how gitsi talks to the hub.
//
// Invariant (the edge exposes nothing): this interface does NOT offer reverse forwarding (-R) nor listening outside loopback.
// What is forbidden cannot be asked for: the API does not exist.
package transport

import (
	"context"
	"io"

	"github.com/ShizukaJiku/gitsi/internal/fault"
)

// Result is the output of a remote command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Stream is a bidirectional channel with a remote process (stdin/stdout), used by the hub protocol.
type Stream interface {
	io.ReadWriteCloser
}

// Forward is a local tunnel: 127.0.0.1:LocalPort (edge) -> RemoteHost:RemotePort (as seen from the hub). The local side is always
// 127.0.0.1; an empty RemoteHost means the hub's 127.0.0.1.
type Forward struct {
	LocalPort  int
	RemoteHost string
	RemotePort int
}

// Tunnel is a running tunnel process.
type Tunnel interface {
	// Wait blocks until the tunnel ends.
	Wait() error
	// Close stops it.
	Close() error
}

// Transport is the edge's only way to the hub.
type Transport interface {
	// Run runs a command on the hub (remote shell: PowerShell) and returns its output.
	Run(ctx context.Context, command string) (Result, error)
	// Stream runs a command on the hub and exposes its stdin/stdout (for `gitsi serve --stdio`).
	Stream(ctx context.Context, command string) (Stream, error)
	// Forward opens local tunnels (-L), always bound to 127.0.0.1.
	Forward(ctx context.Context, forwards []Forward) (Tunnel, error)
}

// Executor runs a command on the hub with live output (for `run`). It is optional, like Fetcher: the commands that need it ask
// for it with a type assertion and the rest never notice.
type Executor interface {
	// Exec returns the remote command's exit code; err only when ssh could not run.
	Exec(ctx context.Context, command string, stdout, stderr io.Writer) (int, error)
}

// StreamDiagnostics is optional: a Stream that can explain why it does not answer (stderr of the process or of ssh).
type StreamDiagnostics interface {
	Stderr() string
	Fault() *fault.Error
}

// TunnelDiagnostics is optional: a Tunnel that can explain why it ended.
type TunnelDiagnostics interface {
	Fault() *fault.Error
}
