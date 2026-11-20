package watch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeSource is the hub's state, changeable during the test.
type fakeSource struct {
	mu     sync.Mutex
	sha    string
	ver    string
	outbox []OutboxItem
	jobs   map[string]string
	heads  map[string]string
}

func (f *fakeSource) Binary() (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sha, f.ver, nil
}
func (f *fakeSource) Outbox() ([]OutboxItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OutboxItem(nil), f.outbox...), nil
}
func (f *fakeSource) FinishedJobs() (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]string{}
	for k, v := range f.jobs {
		m[k] = v
	}
	return m, nil
}
func (f *fakeSource) Heads() (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]string{}
	for k, v := range f.heads {
		m[k] = v
	}
	return m, nil
}
func (f *fakeSource) set(fn func(*fakeSource)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

// syncBuf is a buffer safe to read while the client writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type world struct {
	src      *fakeSource
	hubH     *Handler
	stateDir string
	out      *syncBuf
	client   *Client
	dials    int
	mu       sync.Mutex
	sessions sync.WaitGroup // hub sessions in progress: the test waits for them before removing their folders
}

// newWorld connects a Client to a real hub (hub.Serve + Mux) over in-memory pipes; each Dial opens a new session.
func newWorld(t *testing.T) *world {
	w := &world{
		src:      &fakeSource{sha: shaA, ver: "0.5.2", jobs: map[string]string{}},
		stateDir: filepath.Join(t.TempDir(), "watch"),
		out:      &syncBuf{},
	}
	w.hubH = &Handler{Source: w.src, StateDir: w.stateDir, HubVersion: "0.5.2", Poll: 10 * time.Millisecond, Ping: 40 * time.Millisecond}
	w.client = &Client{
		Identity:  Request{Edge: "EDGE-1", User: "u", Version: "0.5.2", SHA256: shaA, Reactions: []string{"report"}},
		StatePath: filepath.Join(t.TempDir(), "watch-state.json"),
		Out:       w.out,
		Dead:      400 * time.Millisecond,
		Backoff:   []time.Duration{20 * time.Millisecond},
		Stable:    time.Hour,
		Dial: func(ctx context.Context) (io.ReadWriter, func(), error) {
			w.mu.Lock()
			w.dials++
			w.mu.Unlock()
			cl, sv := net.Pipe()
			sctx, cancel := context.WithCancel(ctx)
			w.sessions.Add(1)
			go func() {
				defer w.sessions.Done()
				hub.Serve(sctx, sv, sv, hub.Config{Agent: "t", Caps: []string{Cap}, Handler: hub.Mux{w.hubH}})
				sv.Close()
			}()
			if _, err := proto.ClientHandshake(cl, "edge", Cap); err != nil {
				cancel()
				return nil, nil, err
			}
			return cl, func() { cancel(); cl.Close() }, nil
		},
	}
	// registered after the TempDirs: it runs before they are removed (cleanups run in reverse order)
	t.Cleanup(w.sessions.Wait)
	return w
}

func (w *world) run(t *testing.T) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.client.Run(ctx) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not end on cancel")
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not happen: %s", what)
}

func TestFirstConnectionTakesABaselineAndThenAnnouncesChanges(t *testing.T) {
	w := newWorld(t)
	w.src.set(func(s *fakeSource) {
		s.outbox = []OutboxItem{{ID: "1111111111111111", Kind: "file", Name: "old.zip", Size: 10}}
		s.jobs["aaaaaaaa"] = "0"
	})
	stop := w.run(t)
	waitFor(t, "hello", func() bool { return strings.Contains(w.out.String(), "listening to the hub (gitsi 0.5.2)") })
	time.Sleep(60 * time.Millisecond)
	if s := w.out.String(); strings.Contains(s, "old.zip") || strings.Contains(s, "aaaaaaaa") {
		t.Fatalf("what already existed is not news:\n%s", s)
	}

	w.src.set(func(s *fakeSource) {
		s.outbox = append(s.outbox, OutboxItem{ID: "2222222222222222", Kind: "dir", Name: "results", Size: 2048})
		s.jobs["bbbbbbbb"] = "3"
		s.sha, s.ver = shaB, "0.5.3"
	})
	waitFor(t, "the three notices", func() bool {
		s := w.out.String()
		return strings.Contains(s, "arrived in the outbox: results") && strings.Contains(s, "job bbbbbbbb finished (exit code 3)") &&
			strings.Contains(s, "new gitsi version is on the hub: 0.5.3")
	})
	stop()
	if !strings.Contains(w.out.String(), "gitsi take 2222222222222222") || !strings.Contains(w.out.String(), "gitsi job logs bbbbbbbb") {
		t.Fatalf("each notice says the command to follow up:\n%s", w.out)
	}
}

func TestReconnectNeverRepeatsAndCatchesUpOnWhatHappenedMeanwhile(t *testing.T) {
	w := newWorld(t)
	stop := w.run(t)
	waitFor(t, "hello", func() bool { return strings.Contains(w.out.String(), "listening to the hub") })
	time.Sleep(30 * time.Millisecond) // so the initial state (baseline) is already taken
	w.src.set(func(s *fakeSource) {
		s.outbox = []OutboxItem{{ID: "3333333333333333", Kind: "text", Name: "note.txt", Size: 5}}
		s.sha, s.ver = shaB, "0.5.3"
	})
	waitFor(t, "first notices", func() bool {
		s := w.out.String()
		return strings.Contains(s, "note.txt") && strings.Contains(s, "0.5.3")
	})
	stop()

	// while the edge was not listening: a job finished
	w.src.set(func(s *fakeSource) { s.jobs["cccccccc"] = "0" })
	w.out = &syncBuf{}
	w.client.Out = w.out
	stop = w.run(t)
	waitFor(t, "what happened meanwhile", func() bool { return strings.Contains(w.out.String(), "job cccccccc finished") })
	time.Sleep(80 * time.Millisecond)
	stop()
	if s := w.out.String(); strings.Contains(s, "note.txt") || strings.Contains(s, "0.5.3") {
		t.Fatalf("a reconnection does not repeat what was already notified:\n%s", s)
	}
}

func TestNoUpdateAnnouncedWhenTheEdgeAlreadyHasThePublishedBinary(t *testing.T) {
	w := newWorld(t) // the edge has shaA and the hub publishes shaA
	stop := w.run(t)
	waitFor(t, "hello", func() bool { return strings.Contains(w.out.String(), "listening to the hub") })
	time.Sleep(60 * time.Millisecond)
	stop()
	if strings.Contains(w.out.String(), "new gitsi version") {
		t.Fatalf("%s", w.out)
	}
}

// A hub that stops giving news (network down, hub hung) is detected by the missing heartbeats and reconnected on its own.
func TestSilentHubIsDetectedAndTheClientReconnects(t *testing.T) {
	w := newWorld(t)
	w.hubH.Ping = time.Hour // the hub sends no heartbeats
	w.hubH.Poll = time.Hour
	w.client.Dead = 100 * time.Millisecond
	stop := w.run(t)
	waitFor(t, "reconnection", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.dials >= 2
	})
	stop()
	if !strings.Contains(w.out.String(), "no news from the hub") || !strings.Contains(w.out.String(), "Retrying in") {
		t.Fatalf("%s", w.out)
	}
}

func TestFatalDialErrorStopsInsteadOfLooping(t *testing.T) {
	w := newWorld(t)
	boom := errors.New("the hub does not offer watch")
	w.client.Dial = func(context.Context) (io.ReadWriter, func(), error) { return nil, nil, boom }
	w.client.Fatal = func(err error) bool { return errors.Is(err, boom) }
	if err := w.client.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
}

// The hub knows who listens: state per edge, connected while the session lasts and disconnected when it ends.
func TestHubKeepsAStateFilePerEdge(t *testing.T) {
	w := newWorld(t)
	stop := w.run(t)
	waitFor(t, "connected state", func() bool {
		es, _ := Edges(w.stateDir, time.Now(), time.Minute)
		return len(es) == 1 && es[0].Connected && es[0].Edge == "EDGE-1" && es[0].Version == "0.5.2" && es[0].Reactions[0] == "report"
	})
	stop()
	waitFor(t, "disconnected state", func() bool {
		es, _ := Edges(w.stateDir, time.Now(), time.Minute)
		return len(es) == 1 && !es[0].Connected
	})
	// a session that died without closing the file shows as disconnected when its heartbeat is old
	es, _ := Edges(w.stateDir, time.Now().Add(time.Hour), time.Minute)
	if es[0].Connected {
		t.Fatal("an old heartbeat is a dead session")
	}
}

func TestHubRejectsAnInvalidIdentity(t *testing.T) {
	h := &Handler{Source: &fakeSource{}}
	for _, bad := range []string{`{"edge":"a/b","user":"u"}`, `{"edge":"x","user":"..\\y"}`, `no-json`} {
		if err := h.Handle(context.Background(), proto.Frame{Kind: proto.KindWatch, Payload: []byte(bad)}, io.Discard); err == nil {
			t.Errorf("%s: must be rejected", bad)
		}
	}
}

func TestEventValidationAndPrintable(t *testing.T) {
	good := []Event{
		{Type: EvOutbox, ID: "1111111111111111", Kind: "file", Name: "a.zip"},
		{Type: EvJobDone, ID: "aaaaaaaa", Exit: "137"},
		{Type: EvUpdate, SHA256: shaA, Version: "0.5.3"},
		{Type: EvBaseline, Outbox: []string{"1111111111111111"}, Jobs: []string{"aaaaaaaa"}},
	}
	for _, e := range good {
		if !e.Valid() {
			t.Errorf("valid: %+v", e)
		}
	}
	bad := []Event{
		{Type: "exec", ID: "aaaaaaaa"}, // unknown type: ignored
		{Type: EvOutbox, ID: "../x", Kind: "file"},
		{Type: EvOutbox, ID: "1111111111111111", Kind: "exe"},
		{Type: EvJobDone, ID: "aaaaaaaa", Exit: "0; rm"},
		{Type: EvUpdate, SHA256: "short"},
		{Type: EvBaseline, Jobs: []string{"not valid"}},
	}
	for _, e := range bad {
		if e.Valid() {
			t.Errorf("must be discarded: %+v", e)
		}
	}
	if got := Printable("a\x1b[31mred\x07", 100); strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("no control characters: %q", got)
	}
	if got := Printable(strings.Repeat("x", 50), 10); got != strings.Repeat("x", 10)+"..." {
		t.Fatalf("%q", got)
	}
}

func TestMuxRoutesAndReportsUnknownFrames(t *testing.T) {
	m := hub.Mux{&Handler{Source: &fakeSource{}}}
	if err := m.Handle(context.Background(), proto.Frame{Kind: proto.KindList}, io.Discard); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("%v", err)
	}
}

func TestFSSourceReadsTheRealFiles(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gitsi.exe")
	os.WriteFile(bin, []byte("binario uno"), 0o755)
	jobs := filepath.Join(dir, "jobs")
	os.MkdirAll(filepath.Join(jobs, "aaaaaaaa"), 0o755)
	os.MkdirAll(filepath.Join(jobs, "bbbbbbbb"), 0o755) // running: no exit
	os.WriteFile(filepath.Join(jobs, "aaaaaaaa", "exit"), []byte("\xef\xbb\xbf137\r\n"), 0o644)
	s := &FSSource{BinaryPath: bin, JobsDir: jobs}
	sha1, _, err := s.Binary()
	if err != nil || len(sha1) != 64 {
		t.Fatalf("%q %v", sha1, err)
	}
	os.WriteFile(bin, []byte("binario dos, distinto"), 0o755)
	if sha2, _, _ := s.Binary(); sha2 == sha1 {
		t.Fatal("a new binary must change the hash")
	}
	got, err := s.FinishedJobs()
	if err != nil || len(got) != 1 || got["aaaaaaaa"] != "137" {
		t.Fatalf("only the finished ones, with their clean code: %v %v", got, err)
	}
	if m, _ := (&FSSource{JobsDir: filepath.Join(dir, "missing")}).FinishedJobs(); len(m) != 0 {
		t.Fatal("without a jobs folder there is nothing")
	}
}

// Reactions: a new version is tried ONCE (even across reconnections or failures); each new outbox item triggers its own; when
// the reaction asks to restart, Run ends with ErrRestart.
func TestReactionsRunOncePerVersionAndRestartStopsRun(t *testing.T) {
	w := newWorld(t)
	var mu sync.Mutex
	var got []string
	restartOn := ""
	w.client.React = func(_ context.Context, e Event) bool {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, e.Type+":"+e.ID+e.Version)
		return e.Version != "" && e.Version == restartOn
	}
	stop := w.run(t)
	waitFor(t, "hello", func() bool { return strings.Contains(w.out.String(), "listening to the hub") })
	time.Sleep(30 * time.Millisecond)
	w.src.set(func(s *fakeSource) {
		s.sha, s.ver = shaB, "0.5.3"
		s.outbox = []OutboxItem{{ID: "4444444444444444", Kind: "file", Name: "a.zip", Size: 1}}
	})
	waitFor(t, "two reactions", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 2 })
	stop()

	// reconnect: the same version is not tried again (even if the edge still lacks it)
	w.client.Identity.SHA256 = shaA
	stateBefore := len(got)
	stop = w.run(t)
	waitFor(t, "hello", func() bool { return strings.Count(w.out.String(), "listening to the hub") >= 2 })
	time.Sleep(80 * time.Millisecond)
	stop()
	mu.Lock()
	if len(got) != stateBefore {
		t.Fatalf("the same version is not retried on its own: %v", got)
	}
	mu.Unlock()

	// a different version is, and when the reaction asks to restart, Run ends with ErrRestart
	restartOn = "0.5.4"
	w.src.set(func(s *fakeSource) { s.sha, s.ver = strings.Repeat("c", 64), "0.5.4" })
	done := make(chan error, 1)
	go func() { done <- w.client.Run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRestart) {
			t.Fatalf("%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run should end to restart")
	}
}
