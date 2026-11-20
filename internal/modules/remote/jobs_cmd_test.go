package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// scriptTr answers each Run with the next output of the list (the last one repeats) and remembers the commands.
type scriptTr struct {
	outs []string
	cmds []string
}

func (s *scriptTr) Run(_ context.Context, c string) (transport.Result, error) {
	s.cmds = append(s.cmds, c)
	i := len(s.cmds) - 1
	if i >= len(s.outs) {
		i = len(s.outs) - 1
	}
	return transport.Result{Stdout: s.outs[i]}, nil
}
func (*scriptTr) Stream(context.Context, string) (transport.Stream, error) { return nil, nil }
func (*scriptTr) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}

func logResp(state, exit string, offset int, data string) string {
	return "STATE|" + state + "|" + exit + "|" + strconv.Itoa(offset) + "\r\nDATA|" + base64.StdEncoding.EncodeToString([]byte(data)) + "\r\n"
}

func newCmdEnv(t *testing.T, outs ...string) (*scriptTr, *bytes.Buffer, *bytes.Buffer, func(args ...string) error, func(args ...string) error, func(args ...string) error) {
	t.Helper()
	tr := &scriptTr{outs: outs}
	e := envWith(t, tr)
	var out, errb bytes.Buffer
	e.Stdout, e.Stderr = &out, &errb
	ctx := context.Background()
	return tr, &out, &errb,
		func(a ...string) error { return jobsCmd(ctx, e, a) },
		func(a ...string) error { return logsCmd(ctx, e, a) },
		func(a ...string) error { return stopCmd(ctx, e, a) }
}

func TestJobsCmdTableJSONAndEmpty(t *testing.T) {
	listing := "JOB|aaaaaaaa|running||2026-11-16T22:00:00-05:00|docker\r\nJOB|bbbbbbbb|exited|5|2026-11-16T21:00:00-05:00|make\r\n"
	_, out, _, jobs, _, _ := newCmdEnv(t, listing)
	if err := jobs(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "STATE", "aaaaaaaa", "running", "bbbbbbbb", "exited", "make"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// JSON
	tr := &scriptTr{outs: []string{listing}}
	e := envWith(t, tr)
	var jout bytes.Buffer
	e.Stdout, e.JSON = &jout, true
	if err := jobsCmd(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	var got []Job
	if err := json.Unmarshal(jout.Bytes(), &got); err != nil || len(got) != 2 || got[0].Exit != "5" {
		t.Fatalf("%v %s", err, jout.String())
	}

	// empty: in JSON it is [] (not null) and in text a notice
	tr.outs = []string{""}
	jout.Reset()
	if err := jobsCmd(context.Background(), e, nil); err != nil || strings.TrimSpace(jout.String()) != "[]" {
		t.Fatalf("%v %q", err, jout.String())
	}
	_, out2, _, jobs2, _, _ := newCmdEnv(t, "")
	if err := jobs2(); err != nil || !strings.Contains(out2.String(), "No background jobs") {
		t.Fatalf("%v %s", err, out2)
	}
}

func TestJobsAreNumberedByStartAndResolvedByNumber(t *testing.T) {
	listing := "JOB|aaaaaaaa|running||2026-11-16T22:00:00-05:00|docker\r\nJOB|bbbbbbbb|exited|5|2026-11-16T21:00:00-05:00|make\r\n"
	_, out, _, jobs, _, _ := newCmdEnv(t, listing)
	if err := jobs(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if !strings.HasPrefix(lines[1], "[1]  bbbbbbbb") || !strings.HasPrefix(lines[2], "[2]  aaaaaaaa") {
		t.Fatalf("by start time:\n%s", out)
	}
	tr, out, _, _, _, stop := newCmdEnv(t, listing, "STOPPED\r\n")
	if err := stop("2"); err != nil || !strings.Contains(out.String(), "aaaaaaaa stopped") || !strings.Contains(tr.cmds[1], "aaaaaaaa") {
		t.Fatalf("%v %s", err, out)
	}
	_, _, _, _, logs, _ := newCmdEnv(t, listing)
	if err := logs("3"); err == nil || !strings.Contains(err.Error(), "there are 2") {
		t.Fatalf("number outside the list: %v", err)
	}
}

func TestLogsCmdOneShotAndTail(t *testing.T) {
	_, out, errb, _, logs, _ := newCmdEnv(t, logResp("exited", "5", 11, "uno\ndos\ntres"))
	if err := logs("aaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "uno\ndos\ntres" || !strings.Contains(errb.String(), "finished, exit code 5") {
		t.Fatalf("out=%q err=%q", out, errb)
	}

	_, out, _, _, logs, _ = newCmdEnv(t, logResp("exited", "0", 13, "uno\ndos\ntres\n"))
	if err := logs("aaaaaaaa", "-tail", "2"); err != nil || out.String() != "dos\ntres\n" {
		t.Fatalf("%v %q", err, out)
	}
}

// -tail asks only for the end (negative offset), not the whole log; and when the window starts mid-line, it drops that line.
func TestLogsTailAsksOnlyForTheEndAndDropsTheCutLine(t *testing.T) {
	// a 64 KiB window that starts in the middle of a line ("t cut"): final offset 1 MiB, 30 bytes of data
	data := "ta cortada\nuno\ndos\ntres\ncuatro\n"
	tr, out, errb, _, logs, _ := newCmdEnv(t, logResp("exited", "0", 1<<20, data))
	if err := logs("aaaaaaaa", "-tail", "2"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "tres\ncuatro\n" || len(tr.cmds) != 1 || !strings.Contains(tr.cmds[0], "$off = -65536;") {
		t.Fatalf("out=%q cmds=%d %v", out, len(tr.cmds), tr.cmds)
	}
	if !strings.Contains(errb.String(), "finished, exit code 0") {
		t.Fatalf("it must report the job's end: %q", errb)
	}
	// it asks for more lines than the window holds, without being the start: dropping the cut line must lose nothing useful
	_, out, _, _, logs, _ = newCmdEnv(t, logResp("exited", "0", 1<<20, data))
	if err := logs("aaaaaaaa", "-tail", "3"); err != nil || out.String() != "dos\ntres\ncuatro\n" {
		t.Fatalf("%v %q", err, out)
	}
}

// When the window does not hold N complete lines and does not reach the file's start, it grows (64 KiB, 256 KiB, 1 MiB).
func TestLogsTailWidensTheWindowUntilItHasEnoughLines(t *testing.T) {
	few := "xx\nlinea1\n" // 1 inner newline: not enough for 3 complete lines
	enough := "yy\nlinea1\nlinea2\nlinea3\nlinea4\n"
	tr, out, _, _, logs, _ := newCmdEnv(t, logResp("exited", "0", 1<<20, few), logResp("exited", "0", 1<<20, enough))
	if err := logs("aaaaaaaa", "-tail", "3"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "linea2\nlinea3\nlinea4\n" || len(tr.cmds) != 2 ||
		!strings.Contains(tr.cmds[0], "$off = -65536;") || !strings.Contains(tr.cmds[1], "$off = -262144;") {
		t.Fatalf("out=%q cmds=%v", out, tr.cmds)
	}
}

// A short log (the window reaches the start) is shown whole without dropping its first line or asking again.
func TestLogsTailOfAShortLogKeepsItsFirstLine(t *testing.T) {
	tr, out, _, _, logs, _ := newCmdEnv(t, logResp("exited", "0", 9, "uno\ndos\n"+"x"))
	if err := logs("aaaaaaaa", "-tail", "10"); err != nil || out.String() != "uno\ndos\nx" || len(tr.cmds) != 1 {
		t.Fatalf("%v %q cmds=%d", err, out, len(tr.cmds))
	}
}

// At the 1 MiB ceiling it shows whatever there is even if not enough; it does not keep asking forever.
func TestLogsTailStopsWideningAtOneMiB(t *testing.T) {
	tr, out, _, _, logs, _ := newCmdEnv(t, logResp("exited", "0", 1<<30, "zz\nsolo1\n"))
	if err := logs("aaaaaaaa", "-tail", "50"); err != nil || out.String() != "solo1\n" || len(tr.cmds) != 3 {
		t.Fatalf("%v %q cmds=%d", err, out, len(tr.cmds))
	}
	if !strings.Contains(tr.cmds[2], "$off = -1048576;") {
		t.Fatalf("the last query is 1 MiB: %s", tr.cmds[2])
	}
}

func TestLogsTailOfMissingJob(t *testing.T) {
	_, _, _, _, logs, _ := newCmdEnv(t, "NOJOB\r\n")
	if err := logs("aaaaaaaa", "-tail", "5"); err == nil || !strings.Contains(err.Error(), "there is no job") {
		t.Fatalf("%v", err)
	}
}

func TestLogsCmdLostAndMissingJob(t *testing.T) {
	_, _, errb, _, logs, _ := newCmdEnv(t, logResp("lost", "", 0, ""))
	if err := logs("aaaaaaaa"); err != nil || !strings.Contains(errb.String(), "no longer exists") {
		t.Fatalf("%v %q", err, errb)
	}
	_, _, _, _, logs, _ = newCmdEnv(t, "NOJOB\r\n")
	if err := logs("aaaaaaaa"); err == nil || !strings.Contains(err.Error(), "there is no job") {
		t.Fatalf("%v", err)
	}
	_, _, _, _, logs, _ = newCmdEnv(t, "")
	if err := logs("aaaaaaaa"); err == nil {
		t.Fatal("an empty answer from the hub must be an error, not empty output")
	}
}

func TestLogsCmdValidation(t *testing.T) {
	tr, _, _, _, logs, _ := newCmdEnv(t, "")
	for _, args := range [][]string{nil, {"a", "b"}, {"no-es-id"}, {"-zzz"}} {
		if err := logs(args...); err == nil {
			t.Errorf("%v must be rejected", args)
		}
	}
	if len(tr.cmds) != 0 {
		t.Fatal("invalid input must not reach the hub")
	}
}

// When the hub returns the maximum (1 MiB) there was more to read: it asks again right away, from the new offset.
func TestLogsCmdPagesLargeOutputWithoutWaiting(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	tr, out, _, _, logs, _ := newCmdEnv(t, logResp("exited", "0", 1<<20, big), logResp("exited", "0", 1<<20+3, "fin"))
	old := pollInterval
	pollInterval = time.Hour // if it waited between pages, the test would never end
	defer func() { pollInterval = old }()
	if err := logs("aaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if out.String() != big+"fin" || len(tr.cmds) != 2 || !strings.Contains(tr.cmds[1], "$off = 1048576") {
		t.Fatalf("len(out)=%d cmds=%d", out.Len(), len(tr.cmds))
	}
}

func TestLogsFollowStreamsUntilTheJobEnds(t *testing.T) {
	tr, out, errb, _, logs, _ := newCmdEnv(t,
		logResp("running", "", 1, "a"), logResp("running", "", 2, "b"), logResp("exited", "0", 3, "c"))
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	if err := logs("aaaaaaaa", "-f"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "abc" || len(tr.cmds) != 3 || !strings.Contains(errb.String(), "finished, exit code 0") {
		t.Fatalf("out=%q cmds=%d err=%q", out, len(tr.cmds), errb)
	}
	for i, off := range []string{"$off = 0;", "$off = 1;", "$off = 2;"} {
		if !strings.Contains(tr.cmds[i], off) {
			t.Errorf("query %d must ask from %s", i, off)
		}
	}
}

func TestLogsFollowStopsOnCancel(t *testing.T) {
	tr := &scriptTr{outs: []string{logResp("running", "", 0, "")}}
	e := envWith(t, tr)
	var out bytes.Buffer
	e.Stdout, e.Stderr = &out, &out
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := logsCmd(ctx, e, []string{"aaaaaaaa", "-f"}); err != nil {
		t.Fatalf("Ctrl+C during -f is not an error: %v", err)
	}
}

func TestStopCmdOutcomes(t *testing.T) {
	cases := []struct {
		resp, want string
		wantErr    bool
	}{
		{"STOPPED\r\n", "stopped", false},
		{"NOTRUNNING\r\n", "was not running", false},
		{"NOJOB\r\n", "", true},
	}
	for _, c := range cases {
		_, out, _, _, _, stop := newCmdEnv(t, c.resp)
		err := stop("aaaaaaaa")
		if (err != nil) != c.wantErr || !strings.Contains(out.String(), c.want) {
			t.Errorf("%q: err=%v out=%q", c.resp, err, out)
		}
	}
	_, _, _, _, _, stop := newCmdEnv(t, "")
	for _, args := range [][]string{nil, {"a", "b"}, {"malo"}} {
		if err := stop(args...); err == nil {
			t.Errorf("%v must be rejected", args)
		}
	}
}
