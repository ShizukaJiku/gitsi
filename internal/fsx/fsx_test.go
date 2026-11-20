package fsx

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriteFileAtomicCreatesFolderAndReplaces(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "dir", "f.json")
	if err := WriteFileAtomic(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatalf("%q", b)
	}
	if m, _ := filepath.Glob(p + ".tmp-*"); len(m) != 0 {
		t.Fatalf("no temporary files may remain: %v", m)
	}
}

// Writers and readers at once: a reader always sees a whole content (one of the two), never a mix.
func TestConcurrentWritersAndReadersNeverSeeAMix(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	long := bytes.Repeat([]byte("L"), 64<<10)
	short := []byte("short")
	if err := WriteFileAtomic(p, short, 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, data := range [][]byte{long, short} {
		wg.Add(1)
		go func(d []byte) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if err := WriteFileAtomic(p, d, 0o600); err != nil {
					t.Error(err)
					return
				}
			}
		}(data)
	}
	stop := make(chan struct{})
	readErr := make(chan string, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := ReadFileRetry(p)
			if err != nil || !(bytes.Equal(b, long) || bytes.Equal(b, short)) {
				select {
				case readErr <- strings.TrimSpace(err2s(err) + " len=" + strconv.Itoa(len(b))):
				default:
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	select {
	case e := <-readErr:
		t.Fatalf("a reader saw content that is neither of the two: %s", e)
	default:
	}
}

func TestWriteFileAtomicFailureLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o600)
	if err := WriteFileAtomic(filepath.Join(blocker, "inside"), []byte("x"), 0o600); err == nil {
		t.Fatal("cannot write inside a file")
	}
	p := filepath.Join(dir, "target-is-a-folder")
	os.MkdirAll(p, 0o700)
	if err := WriteFileAtomic(p, []byte("x"), 0o600); err == nil {
		t.Fatal("cannot replace a folder with a file")
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.tmp-*")); len(m) != 0 {
		t.Fatalf("no temporary files may remain: %v", m)
	}
}

func TestReadFileRetryMissingFileFailsAtOnce(t *testing.T) {
	if _, err := ReadFileRetry(filepath.Join(t.TempDir(), "no")); !os.IsNotExist(err) {
		t.Fatalf("%v", err)
	}
}

func err2s(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// An antivirus opens a freshly written executable to scan it: renaming it at that moment fails on Windows. RenameRetry must wait
// until the file is released.
func TestRenameRetryWaitsForAShortLock(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "gitsi.exe"), filepath.Join(dir, "gitsi.exe.old")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(src) // like the scanner: open without sharing delete or rename
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := os.Rename(src, dst); err == nil {
			t.Fatal("with the file open, rename should fail on Windows (the test does not reproduce the lock)")
		}
	}
	go func() { time.Sleep(100 * time.Millisecond); f.Close() }()
	if err := RenameRetry(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal(err)
	}
}
