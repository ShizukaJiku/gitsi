package archive

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, c string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPackUnpackRoundTrip(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "a.txt"), "one")
	write(t, filepath.Join(src, "sub", "b.bin"), "two\x00\xff")
	os.MkdirAll(filepath.Join(src, "empty"), 0o755)

	var buf bytes.Buffer
	if st, err := PackWith(src, &buf, Options{}); err != nil || st.Skipped != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := Unpack(&buf, dst, 0); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "b.bin")); string(b) != "two\x00\xff" {
		t.Fatalf("%q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(b) != "one" {
		t.Fatalf("%q", b)
	}
	if st, err := os.Stat(filepath.Join(dst, "empty")); err != nil || !st.IsDir() {
		t.Fatal("the empty folder must be kept")
	}
}

func evil(name string, typ byte, body string) *bytes.Buffer {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	tw.WriteHeader(&tar.Header{Name: name, Typeflag: typ, Mode: 0o644, Size: int64(len(body))})
	tw.Write([]byte(body))
	tw.Close()
	return &b
}

func TestUnpackRejectsPathEscapes(t *testing.T) {
	for _, name := range []string{"../out.txt", "a/../../out.txt", "/abs.txt", `..\out.txt`, "C:/x.txt", "a/./b", "con", "x\\y"} {
		dst := t.TempDir()
		if err := Unpack(evil(name, tar.TypeReg, "x"), dst, 0); err == nil {
			t.Errorf("%q should be rejected", name)
		}
	}
}

func TestUnpackRejectsLinksAndOversize(t *testing.T) {
	if err := Unpack(evil("lnk", tar.TypeSymlink, ""), t.TempDir(), 0); err == nil {
		t.Error("symbolic links must be rejected")
	}
	if err := Unpack(evil("big", tar.TypeReg, strings.Repeat("x", 100)), t.TempDir(), 50); err == nil {
		t.Error("the size limit must apply")
	}
}

func TestUnpackDoesNotOverwrite(t *testing.T) {
	dst := t.TempDir()
	write(t, filepath.Join(dst, "a.txt"), "original")
	if err := Unpack(evil("a.txt", tar.TypeReg, "new"), dst, 0); err == nil {
		t.Fatal("it must not overwrite existing files")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(b) != "original" {
		t.Fatal("the existing file was modified")
	}
}

func TestPackRejectsNonDir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x")
	write(t, f, "x")
	if _, err := PackWith(f, &bytes.Buffer{}, Options{}); err == nil {
		t.Fatal("a file is not a folder")
	}
}

func names(t *testing.T, tarData []byte) []string {
	t.Helper()
	var out []string
	tr := tar.NewReader(bytes.NewReader(tarData))
	for {
		h, err := tr.Next()
		if err != nil {
			return out
		}
		out = append(out, h.Name)
	}
}

func TestPackWithExclude(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "main.go"), "x")
	write(t, filepath.Join(src, "app.log"), "log")
	write(t, filepath.Join(src, "node_modules", "dep", "i.js"), "js")
	write(t, filepath.Join(src, "pkg", "node_modules", "z.js"), "js") // by name, at any depth
	write(t, filepath.Join(src, "pkg", "keep.go"), "k")
	write(t, filepath.Join(src, "build", "cache", "c.bin"), "c") // by relative path
	write(t, filepath.Join(src, "build", "out.exe"), "o")

	var buf bytes.Buffer
	st, err := PackWith(src, &buf, Options{Exclude: []string{"node_modules", "*.log", "build/cache"}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(names(t, buf.Bytes()), " ")
	for _, bad := range []string{"node_modules", "app.log", "cache"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q should not be packed: %s", bad, got)
		}
	}
	for _, want := range []string{"main.go", "pkg/keep.go", "build/out.exe"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
	if st.Excluded != 4 || st.Files != 3 || st.Bytes != int64(len("x")+len("k")+len("o")) {
		t.Fatalf("unexpected summary: %+v", st)
	}
}

func TestPackWithBadPatternFailsBeforeWriting(t *testing.T) {
	var buf bytes.Buffer
	if _, err := PackWith(t.TempDir(), &buf, Options{Exclude: []string{"[abc"}}); err == nil {
		t.Fatal("an invalid pattern must be rejected")
	}
	if buf.Len() != 0 {
		t.Fatal("it must write nothing when the pattern is invalid")
	}
}

func TestPackWithGzipRoundTripAndShrinks(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "a.txt"), strings.Repeat("repetitive code\n", 5000))
	write(t, filepath.Join(src, "sub", "b.bin"), "two\x00\xff")

	var raw, gz bytes.Buffer
	if _, err := PackWith(src, &raw, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := PackWith(src, &gz, Options{Gzip: true}); err != nil {
		t.Fatal(err)
	}
	if gz.Len()*5 > raw.Len() {
		t.Fatalf("repetitive text should compress a lot: %d -> %d", raw.Len(), gz.Len())
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := UnpackGzip(&gz, dst, 0); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "b.bin")); string(b) != "two\x00\xff" {
		t.Fatalf("%q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.txt")); len(b) != len(strings.Repeat("repetitive code\n", 5000)) {
		t.Fatal("a.txt incomplete")
	}
}

func TestUnpackGzipRejectsNonGzipAndBombs(t *testing.T) {
	if err := UnpackGzip(strings.NewReader("this is not gzip"), filepath.Join(t.TempDir(), "o"), 0); err == nil {
		t.Fatal("non-gzip content must be rejected")
	}
	src := t.TempDir()
	write(t, filepath.Join(src, "big.txt"), strings.Repeat("A", 1<<20)) // 1 MiB that compresses to almost nothing
	var gz bytes.Buffer
	if _, err := PackWith(src, &gz, Options{Gzip: true}); err != nil {
		t.Fatal(err)
	}
	if gz.Len() > 10_000 {
		t.Fatalf("the test bomb should be tiny when compressed: %d", gz.Len())
	}
	if err := UnpackGzip(&gz, filepath.Join(t.TempDir(), "o"), 1<<10); err == nil {
		t.Fatal("the limit applies to what is extracted, not to what is compressed")
	}
}

func TestExcludeFollowsWindowsNameRules(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "Node_Modules", "x.js"), "x")
	write(t, filepath.Join(src, "dist", "a", "b.js"), "b")
	write(t, filepath.Join(src, "dist", "c.js"), "c")
	write(t, filepath.Join(src, "keep.txt"), "k")
	var buf bytes.Buffer
	st, err := PackWith(src, &buf, Options{Exclude: []string{"node_modules", `dist\a`}}) // lower case and "\" as on Windows
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(names(t, buf.Bytes()), " ")
	if strings.Contains(strings.ToLower(got), "node_modules") || strings.Contains(got, "dist/a") {
		t.Errorf("Windows-style patterns must exclude: %s", got)
	}
	if !strings.Contains(got, "dist/c.js") || !strings.Contains(got, "keep.txt") || st.Excluded != 2 {
		t.Errorf("it must not exclude too much: %s %+v", got, st)
	}
}

func TestPackWithRejectsNamesTheReceiverWouldRefuse(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "ok.txt"), "x")
	write(t, filepath.Join(src, "lib", "aux.js"), "reserved on Windows")
	var buf bytes.Buffer
	_, err := PackWith(src, &buf, Options{})
	if err == nil || !strings.Contains(err.Error(), "aux.js") || !strings.Contains(err.Error(), "-exclude") {
		t.Fatalf("it must fail while packing (not after uploading) and suggest -exclude: %v", err)
	}
	// excluding it, it can be sent
	buf.Reset()
	if _, err := PackWith(src, &buf, Options{Exclude: []string{"aux.js"}}); err != nil {
		t.Fatalf("what is excluded is not validated: %v", err)
	}
}

func TestExcludePatternWithTrailingSeparator(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "node_modules", "x.js"), "x")
	write(t, filepath.Join(src, "build", "o.bin"), "o")
	write(t, filepath.Join(src, "keep.txt"), "k")
	var buf bytes.Buffer
	// Windows tab completion leaves "node_modules\" and it is natural to type "build/"
	st, err := PackWith(src, &buf, Options{Exclude: []string{`node_modules\`, "build/"}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(names(t, buf.Bytes()), " ")
	if strings.Contains(got, "node_modules") || strings.Contains(got, "build") || st.Excluded != 2 || !strings.Contains(got, "keep.txt") {
		t.Fatalf("a trailing separator must not void the pattern: %s %+v", got, st)
	}
}

// dirBomb builds a tar with n empty folders: with gzip it fits in very little space.
func dirBomb(t *testing.T, n int) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < n; i++ {
		if err := tw.WriteHeader(&tar.Header{Name: "d" + strings.Repeat("x", 6) + itoa(i) + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	return &buf
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestUnpackLimitsEntryCount(t *testing.T) {
	old := MaxEntries
	MaxEntries = 50
	defer func() { MaxEntries = old }()

	if err := Unpack(dirBomb(t, 200), filepath.Join(t.TempDir(), "o"), 0); err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("200 folders with a limit of 50 must be rejected: %v", err)
	}
	MaxEntries = 1_000_000
	// headers do not count as content: a tree with many folders and little content is not rejected by size
	if err := Unpack(dirBomb(t, 200), filepath.Join(t.TempDir(), "o"), 1<<10); err != nil {
		t.Fatalf("a normal archive still passes: %v", err)
	}
}

func TestExcludeHintWorksForNamesWithGlobCharacters(t *testing.T) {
	src := t.TempDir()
	long := "[" + strings.Repeat("x", 122) + "]" // longer than 120 characters: the receiver would reject it; it has glob brackets
	write(t, filepath.Join(src, long), "x")
	write(t, filepath.Join(src, "ok.txt"), "k")
	var buf bytes.Buffer
	_, err := PackWith(src, &buf, Options{})
	if err == nil || !strings.Contains(err.Error(), "-exclude ?"+strings.Repeat("x", 122)+"?") {
		t.Fatalf("the hint must use a pattern that matches the literal name: %v", err)
	}
	hint := "?" + strings.Repeat("x", 122) + "?"
	buf.Reset()
	if st, err := PackWith(src, &buf, Options{Exclude: []string{hint}}); err != nil || st.Excluded != 1 {
		t.Fatalf("the suggested pattern must work: %+v %v", st, err)
	}
}
