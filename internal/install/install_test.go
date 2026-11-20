package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishSelfCopiesAtomicallyAndReturnsHash(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "gitsi.exe")
	os.WriteFile(src, []byte("binary"), 0o755)
	dst := filepath.Join(dir, "new", "folder", "gitsi.exe") // the folder does not exist: it is created

	sum, err := PublishSelf(src, dst)
	want, _ := FileSHA256(src)
	if err != nil || sum != want {
		t.Fatalf("%q %v", sum, err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "binary" {
		t.Fatalf("%q", b)
	}
	if _, err := os.Stat(dst + ".tmp"); err == nil {
		t.Fatal("the temporary file must not remain")
	}
	// publishing over itself copies nothing and breaks nothing
	if s2, err := PublishSelf(dst, dst); err != nil || s2 != want {
		t.Fatalf("%q %v", s2, err)
	}
	// replaces a previous binary
	os.WriteFile(src, []byte("other"), 0o755)
	s3, _ := PublishSelf(src, dst)
	if b, _ := os.ReadFile(dst); string(b) != "other" || s3 == want {
		t.Fatal("it must replace the previous one")
	}
}

func TestFileSHA256MissingFile(t *testing.T) {
	if _, err := FileSHA256(filepath.Join(t.TempDir(), "no")); err == nil {
		t.Fatal("a missing file must fail")
	}
}
