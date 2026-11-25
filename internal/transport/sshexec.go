package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/procx"
)

// SSHExec implements Transport by running the system's ssh.exe.
type SSHExec struct {
	Host    string
	User    string
	Port    int
	KeyFile string
	Binary  string // default "ssh"
	// SCPBinary is the scp used for downloads (default "scp"); configurable to test without a network.
	SCPBinary string
	// Trace, when not nil, receives one line per ssh/scp run (command and duration). gitsi -v turns it on.
	Trace func(line string)
}

var _ Transport = (*SSHExec)(nil)

func (s *SSHExec) binary() string {
	if s.Binary != "" {
		return s.Binary
	}
	return "ssh"
}

// options returns the common ssh options (without the target).
func (s *SSHExec) options() []string {
	return s.commonOptions("-p", "-o", "ServerAliveInterval=30", "-o", "ForwardAgent=no", "-o", "ForwardX11=no")
}

// ConnectTimeout is ssh's and scp's deadline to reach the hub: a powered-off or unreachable hub fails in seconds instead of
// hanging the command. Whoever waits on a session (hubclient's greeting) must wait longer, so ssh reports the cause itself.
const ConnectTimeout = 10 * time.Second

// commonOptions is the base shared by ssh and scp: key only, no agent or X11. portFlag is -p (ssh) or -P (scp).
func (s *SSHExec) commonOptions(portFlag string, extra ...string) []string {
	a := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=" + strconv.Itoa(int(ConnectTimeout/time.Second)),
	}
	a = append(a, extra...)
	if s.KeyFile != "" {
		a = append(a, "-i", s.KeyFile)
	}
	if s.Port != 0 {
		a = append(a, portFlag, strconv.Itoa(s.Port))
	}
	return a
}

func (s *SSHExec) target() string { return s.User + "@" + s.Host }

// endOfOptions goes right before the target: whatever comes after it is never read as an ssh/scp option, even if user or host
// started with a dash (they are validated, this is the second barrier).
const endOfOptions = "--"

// trace reports a run when a Trace is configured. It shortens the remote command (it may carry base64 payloads).
func (s *SSHExec) trace(what, command string, start time.Time, outcome string) {
	if s.Trace == nil {
		return
	}
	c := strings.Join(strings.Fields(command), " ")
	if len(c) > 140 {
		c = c[:140] + "..."
	}
	s.Trace(fmt.Sprintf("%s %s: %s  [%s, %d ms]", what, s.target(), c, outcome, time.Since(start).Milliseconds()))
}

// classify turns ssh/scp stderr into a failure with a cause and an action (nil when it does not recognize it).
func (s *SSHExec) classify(stderr string) *fault.Error {
	return fault.Classify(stderr, fault.Where{Host: s.Host, KeyFile: s.KeyFile})
}

// lockedBuffer is a bytes.Buffer safe between the goroutine copying the process's stderr and whoever reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// runArgs builds the arguments for a remote command.
func (s *SSHExec) runArgs(command string) []string {
	return append(s.options(), endOfOptions, s.target(), command)
}

// forwardArgs builds the arguments for local tunnels. Only -L, always on 127.0.0.1.
func (s *SSHExec) forwardArgs(forwards []Forward) ([]string, error) {
	if len(forwards) == 0 {
		return nil, errors.New("no tunnels to open")
	}
	a := append(s.options(), "-N", "-o", "ExitOnForwardFailure=yes")
	for _, f := range forwards {
		if f.LocalPort <= 0 || f.RemotePort <= 0 {
			return nil, fmt.Errorf("invalid port in tunnel: %+v", f)
		}
		rh := f.RemoteHost
		if rh == "" {
			rh = "127.0.0.1"
		}
		a = append(a, "-L", fmt.Sprintf("127.0.0.1:%d:%s:%d", f.LocalPort, rh, f.RemotePort))
	}
	return append(a, endOfOptions, s.target()), nil
}

// Run implements Transport.
func (s *SSHExec) Run(ctx context.Context, command string) (Result, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, s.binary(), s.runArgs(command)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := procx.Background(cmd).Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	defer func() { s.trace("ssh", command, start, fmt.Sprintf("exit %d", res.ExitCode)) }()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		if res.ExitCode == 255 { // 255 is ssh itself failing (network, key, host key), not the remote command
			if fe := s.classify(res.Stderr); fe != nil {
				return res, fe
			}
		}
		return res, nil // a non-zero exit code is not a transport failure
	}
	return res, err
}

type sshStream struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
	stderr *lockedBuffer
	s      *SSHExec
	once   sync.Once // Close is called from several goroutines (cancellation, reader error, defer) and exec.Cmd.Wait is not concurrent-safe
}

// Stderr returns what the remote process (or ssh) wrote to stderr: the cause of a greeting that never arrives.
func (st *sshStream) Stderr() string { return st.stderr.String() }

// Fault classifies the accumulated stderr (nil when unrecognized).
func (st *sshStream) Fault() *fault.Error { return st.s.classify(st.stderr.String()) }

func (st *sshStream) Read(p []byte) (int, error)  { return st.out.Read(p) }
func (st *sshStream) Write(p []byte) (int, error) { return st.in.Write(p) }
func (st *sshStream) Close() error {
	st.once.Do(func() {
		_ = st.in.Close()
		_ = st.out.Close()
		if st.cmd.Process != nil {
			_ = st.cmd.Process.Kill()
		}
		_ = st.cmd.Wait()
	})
	return nil
}

// Stream implements Transport.
func (s *SSHExec) Stream(ctx context.Context, command string) (Stream, error) {
	s.trace("ssh (channel)", command, time.Now(), "open")
	cmd := exec.CommandContext(ctx, s.binary(), s.runArgs(command)...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errb := &lockedBuffer{}
	cmd.Stderr = errb
	if err := procx.Background(cmd).Start(); err != nil {
		return nil, err
	}
	return &sshStream{cmd: cmd, in: in, out: out, stderr: errb, s: s}, nil
}

type sshTunnel struct {
	cmd    *exec.Cmd
	stderr *lockedBuffer
	s      *SSHExec
}

// Fault classifies why the tunnel ended (nil when unrecognized).
func (t *sshTunnel) Fault() *fault.Error { return t.s.classify(t.stderr.String()) }

func (t *sshTunnel) Wait() error { return t.cmd.Wait() }
func (t *sshTunnel) Close() error {
	if t.cmd.Process != nil {
		return t.cmd.Process.Kill()
	}
	return nil
}

// Forward implements Transport.
func (s *SSHExec) Forward(ctx context.Context, forwards []Forward) (Tunnel, error) {
	var desc []string
	for _, f := range forwards {
		desc = append(desc, fmt.Sprintf("127.0.0.1:%d->%d", f.LocalPort, f.RemotePort))
	}
	s.trace("ssh -L", strings.Join(desc, " "), time.Now(), "open")
	args, err := s.forwardArgs(forwards)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, s.binary(), args...)
	errb := &lockedBuffer{}
	cmd.Stderr = errb
	if err := procx.Background(cmd).Start(); err != nil {
		return nil, err
	}
	return &sshTunnel{cmd: cmd, stderr: errb, s: s}, nil
}

// Fetcher brings a file from the hub to the edge (scp). Download only: the reverse operation does not exist.
type Fetcher interface {
	Fetch(ctx context.Context, remotePath, localPath string) error
}

var _ Fetcher = (*SSHExec)(nil)

// fetchArgs builds the scp arguments to download a file. legacy is scp's old protocol (OpenSSH before 9.0): there the remote path
// goes through the hub's shell, which would split a path with spaces, so it is quoted. Double quotes work for PowerShell, cmd and
// a direct start alike, and the paths that reach here (safe.Path) carry no quotes or metacharacters. In SFTP mode (9.0 and later)
// the path is not interpreted by a shell and quotes would become part of the name, so it goes as is.
func (s *SSHExec) fetchArgs(remotePath, localPath string, legacy bool) []string {
	if legacy && strings.ContainsAny(remotePath, " \t") {
		remotePath = `"` + remotePath + `"`
	}
	a := s.commonOptions("-P", "-q")
	return append(a, endOfOptions, s.target()+":"+remotePath, localPath)
}

// legacySCP reports whether the OpenSSH that prints this version (`ssh -V`) still uses scp's old protocol by default: before 9.0.
// An unrecognized version counts as modern (SFTP), the default of every supported Windows today.
func legacySCP(version string) bool {
	m := opensshVersionRe.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	return major < 9
}

var opensshVersionRe = regexp.MustCompile(`OpenSSH_(?:for_Windows_)?(\d+)\.(\d+)`)

// sshVersion runs `<ssh> -V` (it prints to stderr). A variable so tests do not depend on the installed OpenSSH.
var sshVersion = func(ctx context.Context, bin string) string {
	out, _ := procx.Background(exec.CommandContext(ctx, bin, "-V")).CombinedOutput()
	return string(out)
}

// Fetch implements Fetcher with the system's scp.exe.
func (s *SSHExec) Fetch(ctx context.Context, remotePath, localPath string) error {
	bin := s.SCPBinary
	if bin == "" {
		bin = "scp"
	}
	// scp has no -V: the version is asked to ssh, which ships in the same OpenSSH. Only when it matters (a path with spaces).
	legacy := strings.ContainsAny(remotePath, " \t") && legacySCP(sshVersion(ctx, s.binary()))
	cmd := exec.CommandContext(ctx, bin, s.fetchArgs(remotePath, localPath, legacy)...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	start := time.Now()
	runErr := procx.Background(cmd).Run()
	s.trace("scp", remotePath+" -> "+localPath, start, map[bool]string{true: "ok", false: "failed"}[runErr == nil])
	if err := runErr; err != nil {
		if fe := s.classify(errb.String()); fe != nil {
			return fe
		}
		return fmt.Errorf("scp failed: %w: %s", err, errb.String())
	}
	return nil
}

var _ Executor = (*SSHExec)(nil)

// Exec implements Executor.
func (s *SSHExec) Exec(ctx context.Context, command string, stdout, stderr io.Writer) (code int, err error) {
	start := time.Now()
	defer func() { s.trace("ssh (live)", command, start, fmt.Sprintf("exit %d", code)) }()
	cmd := exec.CommandContext(ctx, s.binary(), s.runArgs(command)...)
	errb := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, io.MultiWriter(stderr, errb) // live for the user and a copy to classify
	err = procx.Background(cmd).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == 255 { // ssh itself failed (network, key, host key), not the remote command
			if fe := s.classify(errb.String()); fe != nil {
				return 255, fe
			}
		}
		return ee.ExitCode(), nil
	}
	return 0, err
}
