package update

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseHashAndSize(t *testing.T) {
	h := strings.Repeat("AB", 32)
	for _, tc := range []struct {
		in       string
		wantHash string
		wantSize int64
	}{
		{h + "|4812345\r\n", strings.ToLower(h), 4812345},
		{h + "\r\n", "", 0},                           // no size: not the expected answer
		{h + "|\r\n", "", 0},                          // empty size
		{h + "|abc", "", 0},                           // unreadable size
		{h + "|-5", "", 0},                            // absurd size
		{"  " + h + " | 77 ", strings.ToLower(h), 77}, // spaces
		{"", "", 0},
	} {
		if hash, size := parseHashAndSize(tc.in); hash != tc.wantHash || size != tc.wantSize {
			t.Errorf("%q -> (%q, %d), want (%q, %d)", tc.in, hash, size, tc.wantHash, tc.wantSize)
		}
	}
}

func TestRemoteHashCmdAsksForHashAndSize(t *testing.T) {
	c := remoteHashCmd("C:/Users/x/gitsi/gitsi.exe")
	for _, want := range []string{"C:/Users/x/gitsi/gitsi.exe", "Get-FileHash", "SHA256", ".Length"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in %s", want, c)
		}
	}
}

func TestWatchSizeReportsGrowthAndStopsCleanly(t *testing.T) {
	path := t.TempDir() + "/download.new"
	var mu sync.Mutex
	var seen []int64
	stop := watchSize(path, 1000, func(done, total int64) {
		mu.Lock()
		seen = append(seen, done)
		mu.Unlock()
		if total != 1000 {
			t.Errorf("total = %d", total)
		}
	})
	time.Sleep(300 * time.Millisecond) // the file does not exist yet: no calls and no errors
	if err := os.WriteFile(path, make([]byte, 400), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	stop()
	mu.Lock()
	n := len(seen)
	mu.Unlock()
	if n == 0 || seen[n-1] != 400 {
		t.Fatalf("it should see the file grow up to 400: %v", seen)
	}
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	after := len(seen)
	mu.Unlock()
	if after != n {
		t.Fatalf("after stop there must be no more calls (%d -> %d)", n, after)
	}
	watchSize(path, 1, nil)() // without a callback: it does nothing
}

// slowFetch writes the file in parts, like a real download, so the watcher sees partial sizes.
type slowFetch struct {
	*fakeT
}

func (s slowFetch) Fetch(_ context.Context, _, local string) error {
	f, err := os.OpenFile(local, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()
	third := len(s.content) / 3
	for i := 0; i < 3; i++ {
		end := (i + 1) * third
		if i == 2 {
			end = len(s.content)
		}
		if _, err := f.WriteString(s.content[i*third : end]); err != nil {
			return err
		}
		f.Sync()
		time.Sleep(350 * time.Millisecond)
	}
	return nil
}

func TestApplyShowsDownloadProgressWhenTheHubReportsTheSize(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	env, out, self, ft := setup(t, "stale", big)
	ft.size = int64(len(big))
	if err := apply(context.Background(), env, slowFetch{ft}, self, false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	s := out.String()
	for _, want := range []string{"Downloading gitsi: 100%", "3.0 MiB", "verifying...", "Updated."} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if b, _ := os.ReadFile(self); string(b) != big {
		t.Fatal("the binary must have been replaced")
	}
}

func TestApplyWithQuietOrUnknownSizeShowsNoPercentage(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	env, out, self, ft := setup(t, "stale", big)
	ft.size = int64(len(big))
	env.Quiet = true
	if err := apply(context.Background(), env, ft, self, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "%") {
		t.Fatalf("-q shows no progress:\n%s", out)
	}

}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
