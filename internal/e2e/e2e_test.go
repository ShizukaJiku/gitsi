// Package e2e tests the real binary: it builds gitsi and uses it as a hub over a process's stdin/stdout.
// It checks what in-memory tests cannot: that the process's stdout carries only frames.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/proto"
	"github.com/ShizukaJiku/gitsi/internal/transport"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// procTransport launches the binary as the hub instead of ssh. It ignores the remote command (hubclient covers that).
type procTransport struct {
	bin  string
	home string
}

func (procTransport) Run(context.Context, string) (transport.Result, error) {
	return transport.Result{}, nil
}
func (procTransport) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}

type procStream struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (p *procStream) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *procStream) Write(b []byte) (int, error) { return p.in.Write(b) }
func (p *procStream) Close() error {
	p.in.Close()
	p.cmd.Process.Kill()
	p.cmd.Wait()
	return nil
}

func (t procTransport) Stream(ctx context.Context, _ string) (transport.Stream, error) {
	cmd := procx.Background(exec.CommandContext(ctx, t.bin, "serve", "--stdio"))
	cmd.Env = append(os.Environ(), "USERPROFILE="+t.home, "HOME="+t.home)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procStream{cmd, in, out}, nil
}

func TestRealBinaryAsHub(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "gitsi.exe")
	buildGitsi(t, bin)

	// hub configuration in an isolated HOME: only its role; the layout is the default one (<home>/gitsi/...)
	home := filepath.Join(dir, "home")
	os.MkdirAll(filepath.Join(home, ".gitsi"), 0o700)
	os.WriteFile(filepath.Join(home, ".gitsi", "config.json"), []byte(`{"version":1,"role":"hub"}`), 0o600)

	ctx := context.Background()
	conn, err := hubclient.Dial(ctx, procTransport{bin: bin, home: home}, mailbox.CapDrop)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !conn.Has(mailbox.CapDrop) {
		t.Fatalf("the real hub must announce the mailbox: %+v", conn.Hello)
	}
	// and it must announce ITS layout (the one the hub decides), valid and under its home folder
	lay, err := layout.FromInfo(conn.Hello.Info)
	if err != nil || lay.Home != filepath.ToSlash(filepath.Join(home, "gitsi")) || lay.Repos != lay.Home+"/repos" || lay.Mailbox != lay.Home+"/mailbox" {
		t.Fatalf("announced layout: %+v %v", lay, err)
	}

	// upload multi-block binary content through the real process
	data := make([]byte, 2*mailbox.ChunkSize+99)
	for i := range data {
		data[i] = byte(i * 7)
	}
	c := mailbox.NewClient(conn.RW)
	it, _ := mailbox.NewItem(mailbox.File, "bin.dat", bytes.NewReader(data), int64(len(data)))
	if _, err := c.Put(ctx, it, bytes.NewReader(data), nil); err != nil {
		t.Fatalf("upload through the real process: %v", err)
	}

	// the hub's store (real folder) has the verified item
	st := &mailbox.Store{Root: filepath.FromSlash(lay.Mailbox)}
	f, got, err := st.Open(mailbox.Inbox, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := mailbox.Verify(got, f); err != nil {
		t.Fatal(err)
	}

	// and the download from the real hub's outbox
	added, _ := st.Add(mailbox.Outbox, mailbox.File, "out.dat", bytes.NewReader(data))
	var back bytes.Buffer
	gi, err := c.Get(ctx, added.ID, 0, &back, nil)
	if err != nil || mailbox.Verify(gi, bytes.NewReader(back.Bytes())) != nil {
		t.Fatalf("download: %v", err)
	}
}

// psTransport reproduces what sshd does with the default shell: it runs the received command with the system's REAL
// powershell.exe (5.1), with the process's stdin/stdout as the channel. That is exactly the step the protocol needs and that
// could not be tested without SSH.
type psTransport struct {
	procTransport
	allow func(cmd string) bool
	used  *[]string
}

func (t psTransport) Stream(ctx context.Context, command string) (transport.Stream, error) {
	if t.allow != nil && !t.allow(command) {
		return nil, errors.New("launch form disabled in the test")
	}
	*t.used = append(*t.used, command)
	cmd := procx.Background(winps.Command(ctx, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command))
	cmd.Env = append(os.Environ(), "USERPROFILE="+t.home, "HOME="+t.home)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procStream{cmd, in, out}, nil
}

// TestThroughRealPowerShell: handshake and binary transfer through a real powershell.exe, with hubclient's REAL launch command,
// which finds the hub's gitsi by reading the HUB's configuration (the edge passes no path). Two scenarios: the default folder
// (<profile>/gitsi) and a folder chosen by the hub (config.home).
func TestThroughRealPowerShell(t *testing.T) {
	if testing.Short() || runtime.GOOS != "windows" {
		t.Skip("needs Windows powershell.exe and building the binary")
	}
	dir := t.TempDir()
	built := filepath.Join(dir, "gitsi-built.exe")
	buildGitsi(t, built)
	exe, _ := os.ReadFile(built)

	data := make([]byte, 3*mailbox.ChunkSize+17)
	for i := range data {
		data[i] = byte(i*13 + i>>8) // covers every byte value, including 0x00, 0x0a and 0x0d
	}

	for _, tc := range []struct {
		name string
		// customHome: the hub picks its folder (config.home); otherwise it uses the default one
		customHome bool
	}{{"hub default folder", false}, {"folder chosen by the hub", true}} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "profile") // the hub's USERPROFILE in this test
			os.MkdirAll(filepath.Join(home, ".gitsi"), 0o700)
			gitsiHome := filepath.Join(home, "gitsi")
			cfg := `{"version":1,"role":"hub"}`
			if tc.customHome {
				gitsiHome = filepath.Join(filepath.Dir(home), "another", "location")
				b, _ := json.Marshal(map[string]any{"version": 1, "role": "hub", "home": filepath.ToSlash(gitsiHome)})
				cfg = string(b)
			}
			os.MkdirAll(gitsiHome, 0o755)
			os.WriteFile(filepath.Join(gitsiHome, "gitsi.exe"), exe, 0o755)
			os.WriteFile(filepath.Join(home, ".gitsi", "config.json"), []byte(cfg), 0o600)

			var used []string
			tr := psTransport{procTransport: procTransport{home: home}, used: &used}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			conn, err := hubclient.Dial(ctx, tr, mailbox.CapDrop)
			if err != nil {
				t.Fatalf("PowerShell could not start the hub's gitsi from its configuration: %v", err)
			}
			defer conn.Close()
			if conn.Method != "hub-config" || !conn.Has(mailbox.CapDrop) || len(used) != 1 {
				t.Fatalf("%+v %v", conn, used)
			}
			lay, err := layout.FromInfo(conn.Hello.Info)
			if err != nil || lay.Home != filepath.ToSlash(gitsiHome) {
				t.Fatalf("the hub announces the folder IT decided: %+v %v", lay, err)
			}

			c := mailbox.NewClient(conn.RW)
			it, _ := mailbox.NewItem(mailbox.File, "ps.bin", bytes.NewReader(data), int64(len(data)))
			if _, err := c.Put(ctx, it, bytes.NewReader(data), nil); err != nil {
				t.Fatalf("transfer through PowerShell: %v", err)
			}
			st := &mailbox.Store{Root: filepath.FromSlash(lay.Mailbox)}
			f, got, err := st.Open(mailbox.Inbox, it.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := mailbox.Verify(got, f); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestPipedInputFormDoesNotWork documents WHY there is no `$input | & exe` form: with powershell.exe 5.1 the server does not
// receive the greeting until stdin closes. Should that ever change, this test will tell.
func TestPipedInputFormDoesNotWork(t *testing.T) {
	if testing.Short() || runtime.GOOS != "windows" {
		t.Skip("needs Windows powershell.exe")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "gitsi.exe")
	buildGitsi(t, bin)
	home := filepath.Join(dir, "home")
	os.MkdirAll(filepath.Join(home, ".gitsi"), 0o700)
	cfg, _ := json.Marshal(map[string]any{"version": 1, "role": "hub", "repos_dir": filepath.ToSlash(filepath.Join(dir, "r"))})
	os.WriteFile(filepath.Join(home, ".gitsi", "config.json"), cfg, 0o600)

	var used []string
	tr := psTransport{procTransport: procTransport{bin: bin, home: home}, used: &used}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	st, err := tr.Stream(ctx, "$input | & '"+filepath.ToSlash(bin)+"' serve --stdio")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	done := make(chan error, 1)
	go func() { _, err := proto.ClientHandshake(proto.Armor(st), "t"); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("the $input form works now (%v): reconsider hubclient", err)
	case <-time.After(3 * time.Second):
		// expected: no answer
	}
}
