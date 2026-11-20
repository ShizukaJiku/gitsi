package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/install"
)

// hookFetch downloads normally (writes the content) and then runs after on the local file: it simulates something (an
// antivirus) touching the file right after it is downloaded.
type hookFetch struct {
	*fakeT
	content string // what is written (default: the hub's)
	after   func(local string)
}

func (h hookFetch) Fetch(_ context.Context, _, local string) error {
	c := h.content
	if c == "" {
		c = h.fakeT.content
	}
	if err := os.WriteFile(local, []byte(c), 0o755); err != nil {
		return err
	}
	if h.after != nil {
		h.after(local)
	}
	return nil
}

func TestDownloadDeletedRightAfterScpIsReportedAsSuch(t *testing.T) {
	env, _, self, ft := setup(t, "stale", "fresh")
	err := apply(context.Background(), env, hookFetch{fakeT: ft, after: func(local string) { os.Remove(local) }}, self, false)
	if err == nil || !strings.Contains(err.Error(), "could not read the download") || strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a file that disappears after the download is NOT a different hash; it must say the real cause: %v", err)
	}
	if !strings.Contains(err.Error(), "antivirus") {
		t.Fatalf("it must point at the likely cause: %v", err)
	}
	if b, _ := os.ReadFile(self); string(b) != "stale" {
		t.Fatal("nothing may be replaced")
	}
}

func TestIncompleteDownloadIsReportedWithSizes(t *testing.T) {
	big := strings.Repeat("x", 4096)
	env, _, self, ft := setup(t, "stale", big)
	ft.size = int64(len(big))
	err := apply(context.Background(), env, hookFetch{fakeT: ft, content: big[:1000]}, self, false)
	if err == nil || !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "1000 of 4096") {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(self + ".new"); serr == nil {
		t.Fatal("the temporary file must not remain")
	}
}

func TestWrongContentStillReportsAHashMismatch(t *testing.T) {
	env, _, self, ft := setup(t, "stale", "fresh")
	err := apply(context.Background(), env, hookFetch{fakeT: ft, content: "fresX"} /* same size, other content */, self, false)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("%v", err)
	}
}

func TestVerifyInstalled(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "gitsi.exe")
	os.WriteFile(self, []byte("fresh"), 0o755)
	want := mustHash(t, self)
	if err := verifyInstalled(self, want); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(self, []byte("other"), 0o755)
	if err := verifyInstalled(self, want); err == nil || !strings.Contains(err.Error(), "does not match the published one") {
		t.Fatalf("a different executable must be detected: %v", err)
	}
	os.Remove(self)
	if err := verifyInstalled(self, want); err == nil || !strings.Contains(err.Error(), "installed executable") {
		t.Fatalf("a removed one must be detected: %v", err)
	}
}

func mustHash(t *testing.T, path string) string {
	t.Helper()
	h, err := install.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestVerboseTracesEveryStepWithTimesAndFileState(t *testing.T) {
	old := postCheckDelay
	postCheckDelay = 10 * time.Millisecond
	defer func() { postCheckDelay = old }()
	env, out, self, ft := setup(t, "stale", "fresh")
	env.Verbose = true
	if err := apply(context.Background(), env, ft, self, false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	s := out.String()
	for _, want := range []string{
		"[v] update +", "the hub publishes", "current executable", "downloading with scp", "scp ended", "downloaded file: exists, 5 bytes",
		"downloaded sha256", "hash correct", "replacement done; exists, 5 bytes", "late check: exists", "Updated.",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in the trace:\n%s", want, s)
		}
	}
}

func TestWithoutVerboseThereIsNoTraceAndNoDelay(t *testing.T) {
	old := postCheckDelay
	postCheckDelay = 5 * time.Second // if it waited without -v, the test would be slow
	defer func() { postCheckDelay = old }()
	env, out, self, ft := setup(t, "stale", "fresh")
	start := time.Now()
	if err := apply(context.Background(), env, ft, self, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "[v]") || time.Since(start) > 2*time.Second {
		t.Fatalf("without -v there is no trace and no wait:\n%s", out)
	}
}

// A delayed verdict (something removes the executable a moment AFTER it is put in place) is detected by -v's second check.
func TestVerboseCatchesADeletionThatHappensAfterTheReplacement(t *testing.T) {
	old := postCheckDelay
	postCheckDelay = 400 * time.Millisecond
	defer func() { postCheckDelay = old }()
	env, out, self, ft := setup(t, "stale", "fresh")
	env.Verbose = true
	after := func(string) {
		go func() { // "the antivirus": once the installed executable is the new one, it removes it shortly after
			for i := 0; i < 200; i++ {
				if b, err := os.ReadFile(self); err == nil && string(b) == "fresh" {
					time.Sleep(60 * time.Millisecond)
					os.Remove(self)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	err := apply(context.Background(), env, hookFetch{fakeT: ft, after: after}, self, false)
	if err == nil || !strings.Contains(err.Error(), "installed executable") {
		t.Fatalf("the late check must detect the removal: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "late check: does NOT exist") {
		t.Fatalf("the trace must show at which step it disappeared:\n%s", out)
	}
}
