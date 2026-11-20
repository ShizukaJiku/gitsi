package progress

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// clock is a clock that only moves when the test says so.
type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newRep(tty bool) (*Reporter, *bytes.Buffer, *clock) {
	var out bytes.Buffer
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	return New(Options{Out: &out, TTY: tty, Now: clk.Now}), &out, clk
}

const mib = 1 << 20

func TestTransferWithoutConsolePrintsOneLinePerDecileWithRateAndETA(t *testing.T) {
	r, out, clk := newRep(false)
	cb := r.Transfer("big.bin", 100*mib, "verifying on the hub")
	if cb == nil {
		t.Fatal("a big transfer must show progress")
	}
	cb(0, 100*mib)
	for done := int64(1); done <= 100; done++ { // 1 MiB per second
		clk.Advance(time.Second)
		cb(done*mib, 100*mib)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	// 0%, 10%, 20% ... 90% and the final one: one line per decile
	if len(lines) != 11 {
		t.Fatalf("expected 11 lines (one per decile and the final one), got %d:\n%s", len(lines), out)
	}
	mid := lines[5] // 50%
	for _, want := range []string{"big.bin: 50%", "50.0 MiB / 100.0 MiB", "1.0 MiB/s", "~50 s left"} {
		if !strings.Contains(mid, want) {
			t.Errorf("missing %q in %q", want, mid)
		}
	}
	last := lines[len(lines)-1]
	for _, want := range []string{"100%", "100.0 MiB in 1 min 40 s", "1.0 MiB/s", "verifying on the hub..."} {
		if !strings.Contains(last, want) {
			t.Errorf("missing %q in the final line %q", want, last)
		}
	}
	if strings.Contains(out.String(), "\r") {
		t.Error("without a console there is no redrawing")
	}
}

func TestTransferInConsoleRedrawsOneLineAndThrottles(t *testing.T) {
	r, out, clk := newRep(true)
	cb := r.Transfer("x.bin", 10*mib, "")
	cb(0, 10*mib)
	n := strings.Count(out.String(), "\r")
	clk.Advance(100 * time.Millisecond) // less than TTYEvery: no redraw
	cb(1*mib, 10*mib)
	if strings.Count(out.String(), "\r") != n {
		t.Fatal("must not redraw within the interval")
	}
	clk.Advance(time.Second)
	cb(5*mib, 10*mib)
	if strings.Count(out.String(), "\r") != n+1 || !strings.Contains(out.String(), "50%") {
		t.Fatalf("must redraw once the interval passed:\n%q", out.String())
	}
	if strings.Contains(out.String(), "\n") {
		t.Fatal("the current line has no newline until it ends")
	}
	clk.Advance(time.Second)
	cb(10*mib, 10*mib)
	if !strings.HasSuffix(out.String(), "\n") || !strings.Contains(out.String(), "100%") {
		t.Fatalf("the line is closed at the end:\n%q", out.String())
	}
}

func TestRedrawErasesTheLeftoverOfALongerPreviousLine(t *testing.T) {
	r, out, clk := newRep(true)
	cb := r.Transfer("x.bin", 10*mib, "")
	cb(0, 10*mib)
	clk.Advance(2 * time.Second)
	cb(5*mib, 10*mib) // with speed and ETA: a long line
	long := out.Len()
	clk.Advance(2 * time.Second)
	cb(6*mib, 10*mib)
	if out.Len() <= long {
		t.Fatal("expected another write")
	}
	tail := out.String()[long:]
	if !strings.HasPrefix(tail, "\r") {
		t.Fatalf("must go back to the start of the line: %q", tail)
	}
}

func TestSmallQuietAndNilReportersShowNothing(t *testing.T) {
	r, out, _ := newRep(false)
	if r.Transfer("small", mib-1, "") != nil {
		t.Error("below MinSize nothing is shown")
	}
	q := New(Options{Out: out, Quiet: true})
	if q.Transfer("x", 100*mib, "") != nil {
		t.Error("with Quiet nothing is shown")
	}
	q.Phase("anything")() // neither writes nor fails
	var nilRep *Reporter
	if nilRep.Transfer("x", 100*mib, "") != nil {
		t.Error("a nil Reporter must not fail")
	}
	nilRep.Phase("x")()
	if New(Options{}).Transfer("x", 100*mib, "") != nil { // no output
		t.Error("without Out nothing is written")
	}
	if out.Len() != 0 {
		t.Fatalf("it should have written nothing: %q", out)
	}
}

func TestResumedTransferComputesRateFromWhatWasActuallyTransferred(t *testing.T) {
	r, out, clk := newRep(false)
	cb := r.Transfer("r.bin", 100*mib, "")
	cb(90*mib, 100*mib) // resumes with 90% already done
	clk.Advance(5 * time.Second)
	cb(95*mib, 100*mib)
	clk.Advance(5 * time.Second)
	cb(100*mib, 100*mib)
	if !strings.Contains(out.String(), "1.0 MiB/s") { // 10 MiB in 10 s, not 100 MiB in 10 s
		t.Fatalf("speed must be measured over what is transferred now:\n%s", out)
	}
	if strings.Contains(out.String(), "10.0 MiB/s") {
		t.Fatalf("speed must not include what was already done:\n%s", out)
	}
}

func TestSlowLinkStillShowsAHeartbeat(t *testing.T) {
	r, out, clk := newRep(false)
	cb := r.Transfer("slow.bin", 100*mib, "")
	cb(0, 100*mib)
	n := strings.Count(out.String(), "\n")
	for i := 0; i < 5; i++ { // within the same decile and within LineEvery: nothing
		clk.Advance(time.Second)
		cb(int64(i+1)*100<<10, 100*mib)
	}
	if strings.Count(out.String(), "\n") != n {
		t.Fatal("without a new decile or LineEvery passing nothing is written")
	}
	clk.Advance(10 * time.Second) // heartbeat
	cb(1*mib, 100*mib)
	if strings.Count(out.String(), "\n") != n+1 {
		t.Fatalf("a slow link must show a heartbeat:\n%s", out)
	}
}

func TestCallbackIgnoresCallsAfterFinishAndClampsOvershoot(t *testing.T) {
	r, out, clk := newRep(false)
	cb := r.Transfer("x", 10*mib, "")
	cb(0, 10*mib)
	clk.Advance(time.Second)
	cb(15*mib, 10*mib) // more than declared
	before := out.String()
	cb(10*mib, 10*mib)
	if out.String() != before || !strings.Contains(before, "100%") {
		t.Fatalf("nothing is written after finishing:\n%s", out)
	}
}

func TestPhaseAnnouncesOnlyWhenItIsSlow(t *testing.T) {
	var out bytes.Buffer
	r := New(Options{Out: &out, PhaseDelay: 30 * time.Millisecond})
	r.Phase("fast")() // ends before the delay
	time.Sleep(60 * time.Millisecond)
	if out.Len() != 0 {
		t.Fatalf("a fast phase is not announced: %q", out.String())
	}
	end := r.Phase("packing")
	time.Sleep(90 * time.Millisecond)
	end()
	if got := out.String(); got != "  packing...\n" {
		t.Fatalf("a slow phase is announced once: %q", got)
	}
}

func TestPhaseClosesALiveConsoleLineFirst(t *testing.T) {
	var out bytes.Buffer
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	r := New(Options{Out: &out, TTY: true, Now: clk.Now, PhaseDelay: 10 * time.Millisecond})
	cb := r.Transfer("x", 10*mib, "")
	cb(0, 10*mib)
	end := r.Phase("verifying")
	time.Sleep(50 * time.Millisecond)
	end()
	s := out.String()
	if !strings.Contains(s, "\n  verifying...\n") {
		t.Fatalf("the phase must start on a new line:\n%q", s)
	}
}

func TestBytesAndDuration(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 5 * mib: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		400 * time.Millisecond: "0 s", 4 * time.Second: "4 s", 59 * time.Second: "59 s",
		125 * time.Second: "2 min 5 s", 3780 * time.Second: "1 h 3 min",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestIsTerminalIsFalseForBuffersAndFiles(t *testing.T) {
	if IsTerminal(&bytes.Buffer{}) {
		t.Fatal("a buffer is not a console")
	}
	f, err := openTemp(t)
	if err != nil {
		t.Fatal(err)
	}
	if IsTerminal(f) {
		t.Fatal("a file is not a console")
	}
}

func openTemp(t *testing.T) (*os.File, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "p")
	if err == nil {
		t.Cleanup(func() { f.Close() })
	}
	return f, err
}
