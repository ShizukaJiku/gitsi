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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/app"
	"github.com/ShizukaJiku/gitsi/internal/clip"
	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/transport"
	"github.com/ShizukaJiku/gitsi/internal/watch"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// TestThroughRealOpenSSH runs the edge's real gitsi commands (app.Run) against a REAL Windows sshd (OpenSSH, PowerShell 5.1
// shell). It is opt-in: it needs a disposable sshd instance on loopback whose environment sets the hub's USERPROFILE (SetEnv), so
// no real profile is touched. scripts/e2e-ssh.ps1 sets all of that up and runs it.
//
//	GITSI_E2E_SSH_PORT=2299 GITSI_E2E_SSH_KEY=<private key> GITSI_E2E_SSH_HUBHOME=<hub USERPROFILE> go test ./internal/e2e -run OpenSSH -v
func TestThroughRealOpenSSH(t *testing.T) {
	portS, key, hubHome := os.Getenv("GITSI_E2E_SSH_PORT"), os.Getenv("GITSI_E2E_SSH_KEY"), os.Getenv("GITSI_E2E_SSH_HUBHOME")
	if portS == "" || key == "" || hubHome == "" {
		t.Skip("opt-in: set GITSI_E2E_SSH_PORT, GITSI_E2E_SSH_KEY and GITSI_E2E_SSH_HUBHOME")
	}
	port, _ := strconv.Atoi(portS)
	user := os.Getenv("USERNAME")
	dir := t.TempDir()

	// The HUB decides its layout: here it has no configuration at all, so it applies the defaults under its home folder
	// (<hubHome>/gitsi). The real binary is placed where the hub itself says its gitsi lives.
	hubRoot := filepath.Join(hubHome, "gitsi")
	hubBin := filepath.Join(hubRoot, "gitsi.exe")
	os.MkdirAll(hubRoot, 0o755)
	buildGitsi(t, hubBin)
	hubRepos := filepath.Join(hubRoot, "repos") // does not exist yet: the first 'share' creates it

	// Hub configuration: only its role (everything else by default), in the USERPROFILE the test sshd sets.
	os.MkdirAll(filepath.Join(hubHome, ".gitsi"), 0o700)
	if err := os.WriteFile(filepath.Join(hubHome, ".gitsi", "config.json"), []byte(`{"version":1,"role":"hub"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Edge configuration in an isolated HOME: WITHOUT any hub path (no repos_dir): it discovers it.
	edgeHome := filepath.Join(dir, "edge")
	os.MkdirAll(filepath.Join(edgeHome, ".gitsi"), 0o700)
	os.MkdirAll(filepath.Join(edgeHome, ".ssh"), 0o700)
	edgeCfg, _ := json.Marshal(map[string]any{
		"version": 1, "role": "edge", "remote_name": "hub",
		"hub":    map[string]any{"host": "127.0.0.1", "user": user, "port": port, "key_file": key},
		"policy": map[string]any{"allow_send": true, "allow_take": true, "allow_exec": true, "audit": true},
	})
	os.WriteFile(filepath.Join(edgeHome, ".gitsi", "config.json"), edgeCfg, 0o600)
	t.Setenv("USERPROFILE", edgeHome)
	t.Setenv("HOME", edgeHome)

	st := &mailbox.Store{Root: filepath.Join(hubRoot, "mailbox")}
	run := func(stdin string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := app.Run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
		return code, out.String(), errb.String()
	}

	t.Run("check", func(t *testing.T) {
		code, out, errb := run("", "check")
		if !strings.Contains(out, "hub protocol") || strings.Contains(out, "[FAIL] hub protocol") {
			t.Fatalf("code=%d\n%s%s", code, out, errb)
		}
		t.Log("\n" + out)
	})

	t.Run("status asks the hub for its version and mailbox", func(t *testing.T) {
		code, out, errb := run("", "status")
		if code != 0 || !strings.Contains(out, "(same as this machine)") || !strings.Contains(out, "mailbox") || strings.Contains(out, "error") {
			t.Fatalf("code=%d\n%s%s", code, out, errb)
		}
		t.Log("\n" + out)
	})

	t.Run("send text, binary file and folder", func(t *testing.T) {
		if code, out, errb := run("", "send", "-text", "hello over a real sshd"); code != 0 {
			t.Fatalf("%d %s%s", code, out, errb)
		}
		big := make([]byte, 2*mailbox.ChunkSize+5)
		for i := range big {
			big[i] = byte(i * 31)
		}
		f := filepath.Join(dir, "data.bin")
		os.WriteFile(f, big, 0o644)
		os.MkdirAll(filepath.Join(dir, "proy", "sub"), 0o755)
		os.WriteFile(filepath.Join(dir, "proy", "sub", "a.txt"), []byte("A"), 0o644)
		if code, out, errb := run("", "send", f, filepath.Join(dir, "proy")); code != 0 {
			t.Fatalf("%d %s%s", code, out, errb)
		}
		items, _ := st.List(mailbox.Inbox)
		if len(items) != 3 {
			t.Fatalf("the hub must have 3 items: %+v", items)
		}
		for _, it := range items {
			fo, got, err := st.Open(mailbox.Inbox, it.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := mailbox.Verify(got, fo); err != nil {
				t.Errorf("%s: %v", it.Name, err)
			}
			fo.Close()
		}
	})

	t.Run("take from the outbox", func(t *testing.T) {
		st.Add(mailbox.Outbox, mailbox.File, "result.txt", strings.NewReader("hub output"))
		dest := filepath.Join(dir, "received")
		if code, out, errb := run("", "take", "-dir", dest); code != 0 {
			t.Fatalf("%d %s%s", code, out, errb)
		}
		if b, _ := os.ReadFile(filepath.Join(dest, "result.txt")); string(b) != "hub output" {
			t.Fatalf("%q", b)
		}
		if left, _ := st.List(mailbox.Outbox); len(left) != 0 {
			t.Fatal("the outbox must end up empty")
		}
	})

	// watch over a real ssh: the notice of a new outbox item reaches the edge, and the hub records who listens.
	t.Run("watch notifies a new outbox item", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var out, errb lockedBuf
		done := make(chan int, 1)
		go func() { done <- app.Run(ctx, []string{"watch"}, strings.NewReader(""), &out, &errb) }()
		deadline := time.Now().Add(30 * time.Second)
		for !strings.Contains(out.String(), "listening to the hub") && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if !strings.Contains(out.String(), "listening to the hub") {
			cancel()
			t.Fatalf("watch did not connect:\n%s%s", out.String(), errb.String())
		}
		time.Sleep(time.Second) // let the initial state (baseline) be taken
		st.Add(mailbox.Outbox, mailbox.Text, "notice-e2e.txt", strings.NewReader("hello"))
		for !strings.Contains(out.String(), "arrived in the outbox: notice-e2e.txt") && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		es, _ := watch.Edges(filepath.Join(hubRoot, "watch"), time.Now(), time.Minute)
		cancel()
		if code := <-done; code != 0 {
			t.Errorf("Ctrl+C is not an error: %d %s", code, errb.String())
		}
		if !strings.Contains(out.String(), "arrived in the outbox: notice-e2e.txt") {
			t.Fatalf("the notice did not arrive:\n%s", out.String())
		}
		if len(es) != 1 || !es[0].Connected {
			t.Fatalf("the hub must record the listening edge: %+v", es)
		}
		for _, it := range func() []mailbox.Item { l, _ := st.List(mailbox.Outbox); return l }() {
			st.Remove(mailbox.Outbox, it.ID)
		}
	})

	// The shared clipboard over a real ssh (docs/CLIP.md): inside one watch session the edge's copy reaches the hub's slot, and a
	// copy the hub stores there reaches the edge. The clipboard itself is not touched: the items go in and out of the session.
	t.Run("watch carries the clipboard both ways", func(t *testing.T) {
		cfgPath := filepath.Join(hubHome, ".gitsi", "config.json")
		os.WriteFile(cfgPath, []byte(`{"version":1,"role":"hub","policy":{"allow_clip":true}}`), 0o600)
		defer os.WriteFile(cfgPath, []byte(`{"version":1,"role":"hub"}`), 0o600)

		tr := &transport.SSHExec{Host: "127.0.0.1", User: user, Port: port, KeyFile: key}
		var got lockedItems
		var out lockedBuf
		c := &watch.Client{
			Dial: func(ctx context.Context) (io.ReadWriter, func(), error) {
				conn, err := hubclient.Dial(ctx, tr, watch.Cap, clip.Cap)
				if err != nil {
					return nil, nil, err
				}
				if !conn.Has(clip.Cap) {
					conn.Close()
					return nil, nil, errors.New("the hub must offer the clipboard with its clip policy on")
				}
				return conn.RW, func() { conn.Close() }, nil
			},
			Fatal:    func(error) bool { return true },
			Identity: watch.Request{Edge: "E2E", User: "edge", Version: "e2e"},
			Out:      &out,
			Clip:     func() bool { return true },
			ClipMax:  1 << 20,
			OnClip:   got.add,
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		defer func() { cancel(); <-done }()

		data := []byte("copied on the edge, over a real sshd")
		it := clip.Item{ID: clip.NewID(), Kind: clip.KindText, Size: int64(len(data)), SHA256: clip.Sum(data), Time: time.Now()}
		deadline := time.Now().Add(30 * time.Second)
		var err error
		for {
			if err = c.SendClip(ctx, it, data); !errors.Is(err, watch.ErrNoClip) || time.Now().After(deadline) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("send: %v\n%s", err, out.String())
		}
		slot := &clip.Slot{Dir: filepath.Join(hubRoot, "clip")}
		cur, ok := slot.Current(time.Now())
		if b, _ := slot.Data(cur); !ok || cur.Origin != "edge@E2E" || string(b) != string(data) {
			t.Fatalf("the hub's slot: %+v %q", cur, b)
		}

		back := []byte("copied on the hub")
		hubIt := clip.Item{ID: clip.NewID(), Kind: clip.KindText, Size: int64(len(back)), SHA256: clip.Sum(back), Origin: clip.OriginHub, Time: time.Now()}
		if err := slot.Put(hubIt, back); err != nil {
			t.Fatal(err)
		}
		for got.count() == 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if items, datas := got.all(); len(items) != 1 || items[0].ID != hubIt.ID || string(datas[0]) != string(back) {
			t.Fatalf("the edge must receive the hub's copy once: %+v\n%s", items, out.String())
		}
	})

	t.Run("run: output and exit code", func(t *testing.T) {
		code, out, errb := run("", "run", "Write-Output", "hello-from-the-hub")
		if code != 0 || !strings.Contains(out, "hello-from-the-hub") {
			t.Fatalf("%d %q %q", code, out, errb)
		}
		if code, _, _ := run("", "run", "exit", "7"); code != 7 {
			t.Errorf("the remote exit code must propagate: %d", code)
		}
		if code, _, _ := run("", "run", "cmd", "/c", "exit", "3"); code != 3 {
			t.Errorf("a native executable's code: %d", code)
		}
		if code, _, _ := run("", "run", "Get-Item", "C:/does/not/exist/xyz"); code == 0 {
			t.Error("a failing cmdlet must give a non-zero code")
		}
	})

	// Each run() call opens a new SSH session and closes it at the end: the job staying alive between calls proves it does NOT
	// hang from the session (it is launched through WMI, outside sshd's process tree).
	t.Run("background jobs outlive the session", func(t *testing.T) {
		code, out, errb := run("", "job", "start", "Write-Output", "start;", "Start-Sleep", "4;", "Write-Output", "end;", "exit", "5")
		if code != 0 || !strings.Contains(out, "Job ") {
			t.Fatalf("%d %q %q", code, out, errb)
		}
		id := strings.Fields(strings.SplitN(out, "Job ", 2)[1])[0]

		// another session: still running
		_, jobsOut, _ := run("", "job", "ls")
		if !strings.Contains(jobsOut, id) || !strings.Contains(jobsOut, "running") {
			t.Fatalf("the job should keep running after the session that launched it closed: %s", jobsOut)
		}

		// logs -f follows it until it ends and reports the code
		code, logsOut, logsErr := run("", "job", "logs", id, "-f")
		if code != 0 || !strings.Contains(logsOut, "start") || !strings.Contains(logsOut, "end") || !strings.Contains(logsErr, "exit code 5") {
			t.Fatalf("%d out=%q err=%q", code, logsOut, logsErr)
		}
		_, jobsOut, _ = run("", "job", "ls")
		if !strings.Contains(jobsOut, "exited") || !strings.Contains(jobsOut, " 5 ") {
			t.Fatalf("jobs must show code 5: %s", jobsOut)
		}
		// logs without -f and with -tail
		if _, o, _ := run("", "job", "logs", id, "-tail", "1"); !strings.Contains(o, "end") || strings.Contains(o, "start") {
			t.Errorf("-tail 1: %q", o)
		}
	})

	t.Run("stop stops a job", func(t *testing.T) {
		_, out, _ := run("", "job", "start", "Start-Sleep", "120")
		id := strings.Fields(strings.SplitN(out, "Job ", 2)[1])[0]
		if code, o, e := run("", "job", "stop", id); code != 0 || !strings.Contains(o, "stopped") {
			t.Fatalf("%d %q %q", code, o, e)
		}
		_, jobsOut, _ := run("", "job", "ls")
		var line string
		for _, l := range strings.Split(jobsOut, "\x0a") {
			if strings.Contains(l, " "+id+" ") {
				line = l
			}
		}
		if !strings.Contains(line, "exited") || !strings.Contains(line, "137") {
			t.Fatalf("after stop it must show as finished (137): %q", line)
		}
		if _, o, _ := run("", "job", "stop", id); !strings.Contains(o, "was not running") {
			t.Errorf("second stop: %q", o)
		}
		if code, _, errb := run("", "job", "logs", "deadbeef"); code == 0 || strings.Contains(errb, "unknown command") {
			t.Error("a missing id must fail")
		}
	})

	// Full send -clip flow: REAL Windows clipboard -> gitsi -> real SSH -> hub mailbox.
	// Opt-in because it overwrites the session's clipboard.
	t.Run("send -clip with the real clipboard", func(t *testing.T) {
		if os.Getenv("GITSI_REAL_CLIPBOARD") != "1" {
			t.Skip("opt-in: changes the real clipboard (GITSI_REAL_CLIPBOARD=1)")
		}
		set := procx.Background(winps.Command(context.Background(), "-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command",
			"Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; "+
				"$b = New-Object Drawing.Bitmap 40,30; $b.SetPixel(3,3,[Drawing.Color]::Blue); [Windows.Forms.Clipboard]::SetImage($b)"))
		if err := set.Run(); err != nil {
			t.Skipf("no clipboard available: %v", err)
		}
		before, _ := st.List(mailbox.Inbox)
		if code, out, errb := run("", "send", "-clip"); code != 0 || !strings.Contains(out, "capture-") {
			t.Fatalf("%d %q %q", code, out, errb)
		}
		after, _ := st.List(mailbox.Inbox)
		if len(after) != len(before)+1 {
			t.Fatalf("1 new item should arrive: %d -> %d", len(before), len(after))
		}
		it := after[len(after)-1]
		fo, got, err := st.Open(mailbox.Inbox, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer fo.Close()
		head := make([]byte, 8)
		fo.Read(head)
		if !strings.HasSuffix(it.Name, ".png") || string(head[1:4]) != "PNG" {
			t.Fatalf("it must be a valid PNG: %s % x", it.Name, head)
		}
		fo.Seek(0, 0)
		if err := mailbox.Verify(got, fo); err != nil {
			t.Fatal(err)
		}
	})

	// gitsi check --active: the report that runs on a real edge, here against the real sshd.
	t.Run("check --active (full report)", func(t *testing.T) {
		code, out, errb := run("", "check", "--active", "-out", filepath.Join(dir, "report.txt"))
		if code != 0 {
			t.Fatalf("check failed (%d): %s %s", code, out, errb)
		}
		for _, want := range []string{"[OK  ] SSH login", "[OK  ] hub shell", "[OK  ] git on the hub", "[OK  ] hub protocol",
			"[OK  ] -L tunnel", "[OK  ] send test", "[OK  ] run test", "Summary:"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in the report: %s", want, out)
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, "report.txt")); err != nil || !strings.Contains(string(b), "=== gitsi check ===") {
			t.Errorf("the report must be saved to a file: %v", err)
		}
		t.Log(strings.Join([]string{"", out}, string(rune(10))))
	})

	// The edge configures no repos path: share, list and get work with the folder THE HUB DECIDES, created by the first share.
	t.Run("repos in the folder the hub decides (share, list, get)", func(t *testing.T) {
		// 'config set' regenerates the ~/.ssh/config block with the alias git uses (it tests that sync too)
		if code, out, errb := run("", "config", "set", "hub.host", "127.0.0.1"); code != 0 {
			t.Fatalf("%d %s%s", code, out, errb)
		}
		if _, err := os.Stat(hubRepos); err == nil {
			t.Fatal("the repos folder must not exist yet")
		}
		if code, out, errb := run("", "repo", "list"); code != 0 || !strings.Contains(out, "No repos") {
			t.Fatalf("empty list: %d %q %q", code, out, errb)
		}

		repo := filepath.Join(dir, "proy-e2e")
		os.MkdirAll(repo, 0o755)
		git := func(args ...string) {
			c := procx.Background(exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...))
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
		git("init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("repo content"), 0o644)
		git("add", ".")
		git("commit", "-q", "-m", "initial")
		t.Chdir(repo)

		if code, out, errb := run("", "repo", "share", "-yes"); code != 0 {
			t.Fatalf("share: %d\n%s\n%s", code, out, errb)
		}
		bare := filepath.Join(hubRepos, "proy-e2e.git")
		if st, err := os.Stat(bare); err != nil || !st.IsDir() {
			t.Fatalf("the repo must end up in the folder the hub decides (%s): %v", bare, err)
		}
		if _, out, _ := run("", "repo", "list"); !strings.Contains(out, "proy-e2e") {
			t.Fatalf("list: %q", out)
		}
		clon := filepath.Join(dir, "clon")
		if code, out, errb := run("", "repo", "get", "proy-e2e", "-dest", clon); code != 0 {
			t.Fatalf("get: %d\n%s\n%s", code, out, errb)
		}
		if b, _ := os.ReadFile(filepath.Join(clon, "hello.txt")); string(b) != "repo content" {
			t.Fatalf("the clone does not have the content: %q", b)
		}

		// run -repo: the folder as is (with an uncommitted change) reaches the hub's working clone and the command runs there
		os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("changed, not committed"), 0o644)
		if code, out, errb := run("", "run", "-repo", "Get-Content", "hello.txt"); code != 0 || !strings.Contains(out, "changed, not committed") {
			t.Fatalf("run -repo: %d\n%s\n%s", code, out, errb)
		}
		// -get: what the command leaves in the working clone reaches the edge's Downloads\gitsi
		if code, out, errb := run("", "run", "-repo", "-get", "out.txt", "Set-Content", "out.txt", "hub-result"); code != 0 || !strings.Contains(out, "Received:") {
			t.Fatalf("run -repo -get: %d\n%s\n%s", code, out, errb)
		}
		got, _ := os.ReadFile(filepath.Join(os.Getenv("USERPROFILE"), "Downloads", "gitsi", "out.txt"))
		if !strings.Contains(string(got), "hub-result") {
			t.Fatalf("-get must leave the file in Downloads\\gitsi: %q", got)
		}

		// job start -repo + job get: what a long job leaves in the clone reaches Downloads\gitsi when it ends
		code, out, errb := run("", "job", "start", "-repo", "Set-Content", "report.txt", "job-report")
		if code != 0 || !strings.Contains(out, "Job ") {
			t.Fatalf("job start -repo: %d\n%s\n%s", code, out, errb)
		}
		id := strings.Fields(strings.SplitN(out, "Job ", 2)[1])[0]
		if code, _, errb := run("", "job", "logs", id, "-f"); code != 0 || !strings.Contains(errb, "exit code 0") {
			t.Fatalf("job logs -f: %d %s", code, errb)
		}
		if code, out, errb := run("", "job", "get", id, "report.txt"); code != 0 || !strings.Contains(out, "Received:") {
			t.Fatalf("job get: %d\n%s\n%s", code, out, errb)
		}
		got, _ = os.ReadFile(filepath.Join(os.Getenv("USERPROFILE"), "Downloads", "gitsi", "report.txt"))
		if !strings.Contains(string(got), "job-report") {
			t.Fatalf("job get must leave the file in Downloads\\gitsi: %q", got)
		}
	})

	// The diagnosis shows the layout the hub announces and the gitsi published in its folder.
	t.Run("check shows the layout announced by the hub", func(t *testing.T) {
		_, out, _ := run("", "check")
		for _, want := range []string{"hub layout", filepath.ToSlash(hubRepos), filepath.ToSlash(filepath.Join(hubRoot, "mailbox")), "hub protocol"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	})

	// update -check asks over a real ssh for the published binary's hash AND size (without downloading or replacing anything: the
	// test executable must not be touched). It validates the PowerShell command through the real sshd and its shell.
	t.Run("update -check asks for the published binary's hash and size", func(t *testing.T) {
		code, out, errb := run("", "update", "-check")
		if code != 0 || !(strings.Contains(out, "different version") || strings.Contains(out, "up to date")) {
			t.Fatalf("update -check: %d\n%s%s", code, out, errb)
		}
	})

	// Performance measurement (opt-in: GITSI_E2E_BIG_MB=64): how long send and take of a big file take over a real SSH.
	t.Run("big file performance", func(t *testing.T) {
		mbS := os.Getenv("GITSI_E2E_BIG_MB")
		if mbS == "" {
			t.Skip("opt-in: GITSI_E2E_BIG_MB=<MiB>")
		}
		mb, _ := strconv.Atoi(mbS)
		big := make([]byte, mb<<20)
		for i := range big {
			big[i] = byte(i*131 + i>>7)
		}
		f := filepath.Join(dir, "big.bin")
		os.WriteFile(f, big, 0o644)
		start := time.Now()
		if code, out, errb := run("", "-q", "send", f); code != 0 {
			t.Fatalf("send: %d %s%s", code, out, errb)
		}
		up := time.Since(start)
		t.Logf("SEND    %d MiB in %.1f s = %.1f MiB/s", mb, up.Seconds(), float64(mb)/up.Seconds())

		items, _ := st.List(mailbox.Inbox)
		var big2 mailbox.Item
		for _, it := range items {
			if it.Name == "big.bin" {
				big2 = it
			}
		}
		// leave it in the hub's outbox to measure the download
		fo, _, _ := st.Open(mailbox.Inbox, big2.ID)
		added, err := st.Add(mailbox.Outbox, mailbox.File, "download.bin", fo)
		fo.Close()
		if err != nil {
			t.Fatal(err)
		}
		_ = added
		start = time.Now()
		if code, out, errb := run("", "-q", "take", "-dir", filepath.Join(dir, "downloads")); code != 0 {
			t.Fatalf("take: %d %s%s", code, out, errb)
		}
		down := time.Since(start)
		t.Logf("RECEIVE %d MiB in %.1f s = %.1f MiB/s", mb, down.Seconds(), float64(mb)/down.Seconds())
	})
}

// lockedBuf is a buffer safe to read while another goroutine writes.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// lockedItems collects the clipboard items watch delivers (from its session goroutine) for the test to read.
type lockedItems struct {
	mu    sync.Mutex
	items []clip.Item
	data  [][]byte
}

func (l *lockedItems) add(_ context.Context, it clip.Item, data []byte) {
	l.mu.Lock()
	l.items, l.data = append(l.items, it), append(l.data, data)
	l.mu.Unlock()
}

func (l *lockedItems) count() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.items) }

func (l *lockedItems) all() ([]clip.Item, [][]byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.items, l.data
}
