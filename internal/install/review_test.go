package install

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/procx"
)

// TestHelperProcess is not a test: it is the "executable in use" of the tests below (a copy of the test binary is started and
// keeps waiting, like the gitsi serving an edge with 'serve --stdio').
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GITSI_INSTALL_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func copyTo(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	out.Close()
}

// runFrom starts the executable dst and leaves it running until the end of the test.
func runFrom(t *testing.T, dst string) {
	t.Helper()
	c := procx.Background(exec.Command(dst, "-test.run=^TestHelperProcess$"))
	c.Env = append(os.Environ(), "GITSI_INSTALL_HELPER=1")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	time.Sleep(300 * time.Millisecond)
}

// On the hub, the published gitsi is the one serving the edges ('serve --stdio'). Publishing while an edge is connected must not
// fail: Windows does not allow replacing an executable in use, but it does allow moving it aside.
func TestPublishSelfReplacesAnExecutableInUse(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("locking executables in use is a Windows behavior")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "pub", "gitsi.exe")
	os.MkdirAll(filepath.Dir(dst), 0o755)
	copyTo(t, os.Args[0], dst)
	runFrom(t, dst) // a connected edge

	src := filepath.Join(dir, "new.exe")
	os.WriteFile(src, []byte("new version"), 0o755)
	sum, err := PublishSelf(src, dst)
	if err != nil {
		t.Fatalf("publishing with the binary in use must work: %v", err)
	}
	want, _ := FileSHA256(src)
	if sum != want {
		t.Fatalf("published hash %s, want %s", sum, want)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new version" {
		t.Fatal("dst must hold the new version")
	}
	if _, err := os.Stat(dst + ".tmp"); err == nil {
		t.Fatal("the temporary file must not remain")
	}

	// The next publish (with nobody using the moved copy anymore) cleans it up.
	os.WriteFile(src, []byte("another version"), 0o755)
	if _, err := PublishSelf(src, dst); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(dst + ".old*"); len(m) > 1 {
		t.Fatalf("earlier moved copies must be cleaned up: %v", m)
	}
}

// The same file spelled differently (upper case, '/' instead of '\') is not copied over itself.
func TestPublishSelfRecognisesTheSameFileWrittenDifferently(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive paths: Windows")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "gitsi.exe")
	copyTo(t, os.Args[0], dst)
	runFrom(t, dst) // runs from the published one: copying it over itself would fail
	alt := strings.ToUpper(filepath.ToSlash(dst))
	before, _ := FileSHA256(dst)
	sum, err := PublishSelf(alt, dst)
	if err != nil {
		t.Fatalf("publishing itself (another spelling of the path) must not fail: %v", err)
	}
	if sum != before {
		t.Fatal("the content must not change")
	}
	if m, _ := filepath.Glob(dst + ".*"); len(m) != 0 {
		t.Fatalf("it must create no temporary or moved copies: %v", m)
	}
}

// When the copy fails (missing source), dst stays intact and no temporary files remain.
func TestPublishSelfFailureLeavesDestinationIntact(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "gitsi.exe")
	os.WriteFile(dst, []byte("good version"), 0o755)
	if _, err := PublishSelf(filepath.Join(dir, "missing.exe"), dst); err == nil {
		t.Fatal("a missing source is an error")
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, []byte("good version")) {
		t.Fatal("dst must not be touched")
	}
	if m, _ := filepath.Glob(dst + ".*"); len(m) != 0 {
		t.Fatalf("no temporary files may remain: %v", m)
	}
	// impossible target folder (under a file)
	if _, err := PublishSelf(dst, filepath.Join(dst, "sub", "x.exe")); err == nil {
		t.Fatal("cannot publish inside a file")
	}
}
