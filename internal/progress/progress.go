// Package progress shows the user the progress of slow work: named phases (packing, hashing, verifying...) and transfers with
// percentage, speed and remaining time. It writes only to the diagnostics output (stderr), never to stdout.
//
// On a real console it redraws a single line; on a pipe or a log it writes separate, spaced-out lines so as not to flood it. With
// Quiet it writes nothing. It has no dependencies: console detection uses only the standard library.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Options tunes the Reporter. Zero values take the default.
type Options struct {
	Out   io.Writer // where to write (stderr); nil = write nothing
	Quiet bool      // write nothing
	TTY   bool      // Out is a console: redraw one line instead of appending lines

	Now        func() time.Time // clock (for tests)
	MinSize    int64            // a smaller transfer shows nothing (default 1 MiB)
	PhaseDelay time.Duration    // a phase is announced only if it lasts longer than this (default 1 s)
	TTYEvery   time.Duration    // on a console, how often to redraw (default 400 ms)
	LineEvery  time.Duration    // without a console, longest gap between lines even if the percentage does not change (default 10 s)
}

// Reporter is safe for concurrent use.
type Reporter struct {
	opt  Options
	mu   sync.Mutex
	live bool // a console line is half-written (no newline yet)
	last int  // width of that line, to erase the leftover when redrawing
}

// New creates a Reporter.
func New(o Options) *Reporter {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MinSize <= 0 {
		o.MinSize = 1 << 20
	}
	if o.PhaseDelay <= 0 {
		o.PhaseDelay = time.Second
	}
	if o.TTYEvery <= 0 {
		o.TTYEvery = 400 * time.Millisecond
	}
	if o.LineEvery <= 0 {
		o.LineEvery = 10 * time.Second
	}
	if o.Out == nil {
		o.Quiet = true
	}
	return &Reporter{opt: o}
}

// IsTerminal reports whether w is a console (and not a file or a pipe).
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// endLive ends the current console line (if any) so what follows starts on a new line. Called with r.mu held.
func (r *Reporter) endLive() {
	if r.live {
		fmt.Fprintln(r.opt.Out)
		r.live, r.last = false, 0
	}
}

// Phase announces a phase ("Packing folder") only if it lasts longer than PhaseDelay: fast work does not clutter the output. Call
// the returned function when the phase ends (defer works too).
func (r *Reporter) Phase(msg string) (end func()) {
	if r == nil || r.opt.Quiet {
		return func() {}
	}
	var (
		mu   sync.Mutex
		done bool
	)
	t := time.AfterFunc(r.opt.PhaseDelay, func() {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.endLive()
		fmt.Fprintf(r.opt.Out, "  %s...\n", msg)
	})
	return func() {
		mu.Lock()
		done = true
		mu.Unlock()
		t.Stop()
	}
}

// Transfer returns the function to pass as the progress callback (done, total) of a transfer of total bytes, or nil when there
// is nothing to show (Quiet or a small transfer). after, when not empty, is appended to the final line: what follows the
// transfer ("verifying on the hub").
func (r *Reporter) Transfer(label string, total int64, after string) func(done, total int64) {
	if r == nil || r.opt.Quiet || total < r.opt.MinSize {
		return nil
	}
	var (
		started, finished bool
		start, lastPrint  time.Time
		base              int64 // what was already done when it started (a resumed upload)
		lastDecile        = -1
	)
	return func(done, tot int64) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if finished {
			return
		}
		if tot <= 0 {
			tot = total
		}
		if done > tot {
			done = tot
		}
		now := r.opt.Now()
		if !started {
			started, start, base = true, now, done
		}
		elapsed := now.Sub(start)
		rate := rateOf(done-base, elapsed)

		if done >= tot {
			finished = true
			line := fmt.Sprintf("  %s: 100%% (%s in %s", label, Bytes(tot), Duration(elapsed))
			if avg := rateOf(tot-base, elapsed); avg > 0 {
				line += ", " + Bytes(int64(avg)) + "/s"
			}
			line += ")"
			if after != "" {
				line += " - " + after + "..."
			}
			r.write(line, true)
			return
		}

		pct := int(done * 100 / tot)
		line := fmt.Sprintf("  %s: %d%% (%s / %s)", label, pct, Bytes(done), Bytes(tot))
		if rate > 0 {
			line += fmt.Sprintf(" %s/s", Bytes(int64(rate)))
			if eta := time.Duration(float64(tot-done) / rate * float64(time.Second)); eta > 0 {
				line += ", ~" + Duration(eta) + " left"
			}
		}
		if r.opt.TTY {
			if !lastPrint.IsZero() && now.Sub(lastPrint) < r.opt.TTYEvery {
				return
			}
			lastPrint = now
			r.write(line, false)
			return
		}
		// without a console: one line per ten percent, or a heartbeat when the link is so slow that it takes long
		if pct/10 == lastDecile && !lastPrint.IsZero() && now.Sub(lastPrint) < r.opt.LineEvery {
			return
		}
		lastDecile, lastPrint = pct/10, now
		r.write(line, true)
	}
}

// write writes a line. On a console it leaves it open and replaces it on the next call; final closes it. Called with r.mu held.
func (r *Reporter) write(line string, final bool) {
	if !r.opt.TTY {
		fmt.Fprintln(r.opt.Out, line)
		return
	}
	pad := ""
	if n := r.last - len([]rune(line)); n > 0 {
		pad = strings.Repeat(" ", n) // the previous line was longer: erase the leftover
	}
	fmt.Fprint(r.opt.Out, "\r"+line+pad)
	r.live, r.last = true, len([]rune(line))
	if final {
		r.endLive()
	}
}

// rateOf returns bytes per second, or 0 while there is no basis for an estimate (under a second or no progress).
func rateOf(bytes int64, elapsed time.Duration) float64 {
	if bytes <= 0 || elapsed < time.Second {
		return 0
	}
	return float64(bytes) / elapsed.Seconds()
}

// Bytes gives a readable size (binary units: KiB, MiB, GiB).
func Bytes(n int64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k:
		return fmt.Sprintf("%.1f GiB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.1f MiB", f/(k*k))
	case f >= k:
		return fmt.Sprintf("%.1f KiB", f/k)
	}
	return fmt.Sprintf("%d B", n)
}

// Duration gives a short readable duration: "4 s", "2 min 5 s", "1 h 3 min".
func Duration(d time.Duration) string {
	s := int64((d + 500*time.Millisecond) / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d min %d s", s/60, s%60)
	}
	return fmt.Sprintf("%d h %d min", s/3600, (s%3600)/60)
}
