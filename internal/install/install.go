// Package install publishes gitsi's own executable and computes file hashes. `update -publish` and `setup hub` share it (two
// modules that must not depend on each other).
package install

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/fsx"
)

// FileSHA256 computes a file's lowercase hexadecimal hash.
func FileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PublishSelf copies the executable src to dst (creating the folder; the copy is atomic: temporary file and rename) and returns
// the SHA256 of the result. When src and dst are the same file (even if the path is spelled differently) it copies nothing.
//
// On the hub, dst is the gitsi that serves the edges ('serve --stdio'): it may be running while publishing. Windows does not allow
// replacing an executable in use, but it does allow renaming it: it is moved aside to dst.old-<n> and the new one takes its place.
// Processes already running keep using the moved one; later publishes remove the moved copies nobody uses anymore.
func PublishSelf(src, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	cleanupAside(dst)
	if !sameFile(src, dst) {
		if err := copyFile(src, dst); err != nil {
			return "", err
		}
	}
	return FileSHA256(dst)
}

// sameFile compares by file identity, not by text: on Windows C:\x and c:/X are the same.
func sameFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// cleanupAside removes the dst.old-* of earlier publishes; those still in use fail silently and are retried later.
func cleanupAside(dst string) {
	old, _ := filepath.Glob(dst + ".old-*")
	for _, o := range old {
		_ = os.Remove(o)
	}
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil { // on disk before renaming: a power loss never leaves a half-written executable
		out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, dst); err == nil {
		return nil
	}
	// dst in use: move it aside (renaming an executable in use is allowed) and put the new one in its place
	aside := fmt.Sprintf("%s.old-%d", dst, time.Now().UnixNano())
	if aerr := fsx.RenameRetry(dst, aside); aerr != nil {
		return fmt.Errorf("%w (and the previous one could not be moved aside: %v)", err, aerr)
	}
	if err = fsx.RenameRetry(tmp, dst); err != nil {
		_ = os.Rename(aside, dst) // undo: the previous one goes back to its place
		return err
	}
	return nil
}
