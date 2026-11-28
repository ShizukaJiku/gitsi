package watchcmd

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
)

// mirrorSelf answers "mirror sync" with the given outputs in turn (the last one repeats) and counts the calls; slow makes each
// cycle take that long, and running tells whether two ever overlapped.
type mirrorSelf struct {
	mu       sync.Mutex
	outs     []string
	calls    int
	slow     time.Duration
	running  atomic.Int32
	overlaps atomic.Int32
}

func (m *mirrorSelf) run(_ context.Context, args ...string) (string, error) {
	if strings.Join(args, " ") != "--json mirror sync" {
		return "", nil
	}
	if m.running.Add(1) > 1 {
		m.overlaps.Add(1)
	}
	defer m.running.Add(-1)
	time.Sleep(m.slow)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.outs[min(m.calls, len(m.outs)-1)]
	m.calls++
	return out, nil
}

func (m *mirrorSelf) count() int { m.mu.Lock(); defer m.mu.Unlock(); return m.calls }

func withInterval(t *testing.T, d time.Duration) {
	prev := mirrorEvery
	mirrorEvery = d
	t.Cleanup(func() { mirrorEvery = prev })
}

const (
	quiet   = `{"repos":[{"name":"app"}],"changed":0,"failed":0}`
	changed = `{"repos":[{"name":"app"}],"changed":1,"failed":0}`
	broken  = `{"repos":[{"name":"app","error":"fatal: Authentication failed"}],"changed":0,"failed":1}` + "\nerror: 1 of 1 mirrors failed\n"
)

// Off by default: no cycle runs.
func TestMirrorOffByDefault(t *testing.T) {
	withInterval(t, 5*time.Millisecond)
	m := &mirrorSelf{outs: []string{quiet}}
	r, _ := newReactor(config.Watch{}, &fakeSelf{})
	r.self = m.run
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	r.mirrorLoop(ctx)
	if m.count() != 0 {
		t.Fatalf("%d cycles", m.count())
	}
}

// On: a cycle when watch starts and one per interval, never two at once; it follows watch.mirror as it is now.
func TestMirrorCyclesWithoutOverlap(t *testing.T) {
	withInterval(t, 10*time.Millisecond)
	m := &mirrorSelf{outs: []string{quiet}, slow: 25 * time.Millisecond} // each cycle is longer than the interval
	r, _ := newReactor(config.Watch{}, &fakeSelf{})
	r.self = m.run
	var on atomic.Bool
	on.Store(true)
	r.liveMirror = on.Load
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.mirrorLoop(ctx); close(done) }()
	time.Sleep(150 * time.Millisecond)
	on.Store(false) // turned off from the tray: no more cycles
	time.Sleep(40 * time.Millisecond)
	n := m.count()
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done
	if n < 2 || m.overlaps.Load() != 0 || m.count() > n+1 {
		t.Fatalf("cycles=%d then %d, overlaps=%d", n, m.count(), m.overlaps.Load())
	}
}

// What a cycle says: nothing when quiet; the news when a repo is updated; a lasting failure only once.
func TestMirrorCycleLines(t *testing.T) {
	m := &mirrorSelf{outs: []string{quiet, changed, broken, broken, quiet, broken}}
	r, out := newReactor(config.Watch{Mirror: true}, &fakeSelf{})
	r.self = m.run
	for range 6 {
		r.mirrorCycle(context.Background())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "1 updated, 0 failed") || !strings.Contains(lines[1], "app: fatal: Authentication failed") ||
		!strings.Contains(lines[2], "1 failed") {
		t.Fatal(out.String())
	}
	if !contains(r.names(), "mirror") {
		t.Fatal("the hub sees the reaction")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
