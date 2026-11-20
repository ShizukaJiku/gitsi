// Package fsx holds file helpers shared by several packages. It is a leaf: it depends only on the standard library.
package fsx

import (
	"os"
	"path/filepath"
	"time"
)

// WriteFileAtomic writes data to path so that nobody can ever see a half-written file: it writes to a temporary file in the same
// folder, flushes it to disk and renames it over the target. If two processes write at once, one of them wins whole; if power is
// lost, either the old or the new version remains. It creates the folder if missing. The new file inherits the folder's
// permissions (on Windows, perm only decides whether it is read-only).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmp, perm)
	return RenameRetry(tmp, path)
}

// RenameRetry renames, retrying for about 2 s: on Windows a rename fails while another process holds the source or the
// destination open (an antivirus scanning a freshly written executable, a concurrent write). A longer failure is returned.
func RenameRetry(src, dst string) error {
	err := os.Rename(src, dst)
	for i := 0; i < 100 && err != nil; i++ {
		if _, serr := os.Lstat(src); os.IsNotExist(serr) {
			return err // without a source there is nothing to wait for
		}
		time.Sleep(20 * time.Millisecond)
		err = os.Rename(src, dst)
	}
	return err
}

// ReadFileRetry reads a file, retrying briefly when it fails for a reason other than "does not exist" (on Windows, reading right
// while another process replaces it with WriteFileAtomic can fail with "file in use").
func ReadFileRetry(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	for i := 0; i < 50 && err != nil && !os.IsNotExist(err); i++ {
		time.Sleep(10 * time.Millisecond)
		b, err = os.ReadFile(path)
	}
	return b, err
}
