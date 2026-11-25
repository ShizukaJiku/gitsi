package archive

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What Explorer copies (a file and a folder side by side) comes back as the same top-level entries.
func TestPackPathsKeepsEachItemByName(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "report.txt"), "one")
	write(t, filepath.Join(src, "photos", "a.png"), "png")
	write(t, filepath.Join(src, "photos", "deep", "b.txt"), "two")
	write(t, filepath.Join(src, "not-copied.txt"), "x")

	var buf bytes.Buffer
	st, err := PackPaths([]string{filepath.Join(src, "report.txt"), filepath.Join(src, "photos")}, &buf, Options{Gzip: true})
	if err != nil || st.Files != 3 {
		t.Fatalf("%+v %v", st, err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := UnpackGzip(&buf, dst, 0); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dst)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "photos,report.txt" {
		t.Fatalf("top-level entries: %v", names)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "photos", "deep", "b.txt")); string(b) != "two" {
		t.Fatalf("%q", b)
	}
}

// Two items with the same name (from different folders) would overwrite each other when extracted.
func TestPackPathsRefusesRepeatedNames(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write(t, filepath.Join(a, "same.txt"), "1")
	write(t, filepath.Join(b, "SAME.txt"), "2")
	if _, err := PackPaths([]string{filepath.Join(a, "same.txt"), filepath.Join(b, "SAME.txt")}, &bytes.Buffer{}, Options{}); err == nil {
		t.Fatal("repeated names (case-insensitive, like Windows) must be refused")
	}
	if _, err := PackPaths(nil, &bytes.Buffer{}, Options{}); err == nil {
		t.Fatal("nothing to pack must fail")
	}
}

// TreeSize stops at the limit instead of walking a huge folder whole.
func TestTreeSizeStopsAtTheLimit(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "a"), strings.Repeat("x", 600))
	write(t, filepath.Join(src, "d", "b"), strings.Repeat("x", 600))
	if n, err := TreeSize([]string{filepath.Join(src, "a"), filepath.Join(src, "d")}, 0); err != nil || n != 1200 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := TreeSize([]string{src}, 1000); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
}
