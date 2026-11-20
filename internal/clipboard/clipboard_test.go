package clipboard

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/winps"
)

func TestParse(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	c, err := Parse("IMG:" + base64.StdEncoding.EncodeToString(png) + "\r\n")
	if err != nil || c.Kind != Image || !bytes.Equal(c.PNG, png) {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = Parse("TXT:" + base64.StdEncoding.EncodeToString([]byte("hello ñandú")))
	if err != nil || c.Kind != Text || c.Text != "hello ñandú" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []string{"NONE", "", "TXT:", "IMG:!!!", "IMG:", "garbage"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	if _, err := Parse("NONE"); !errors.Is(err, ErrEmpty) {
		t.Error("NONE must be ErrEmpty")
	}
}

// Real test against the Windows clipboard: it OVERWRITES the session's clipboard (copies an image and a text), so it only runs
// on request: GITSI_REAL_CLIPBOARD=1 go test ./internal/clipboard
func TestReadRealClipboard(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("GITSI_REAL_CLIPBOARD") != "1" {
		t.Skip("opt-in: modifies the real clipboard (GITSI_REAL_CLIPBOARD=1)")
	}
	set := func(script string) error {
		return winps.Command(context.Background(), "-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command",
			"Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; "+script).Run()
	}
	if err := set(`$b = New-Object Drawing.Bitmap 8,6; $b.SetPixel(1,1,[Drawing.Color]::Red); [Windows.Forms.Clipboard]::SetImage($b)`); err != nil {
		t.Skipf("no clipboard available in this session: %v", err)
	}
	c, err := Read(context.Background())
	if err != nil || c.Kind != Image || !bytes.HasPrefix(c.PNG, []byte("\x89PNG")) {
		t.Fatalf("real image: %+v %v", c, err)
	}
	if err := set(`[Windows.Forms.Clipboard]::SetText('test ñ gitsi')`); err != nil {
		t.Skip(err)
	}
	c, err = Read(context.Background())
	if err != nil || c.Kind != Text || c.Text != "test ñ gitsi" {
		t.Fatalf("real text: %+v %v", c, err)
	}
}

// Reading the real clipboard does NOT modify it: this test always runs (on Windows) and checks that the PowerShell script still
// returns something Parse understands, whatever the clipboard holds right now.
func TestReadRealClipboardIsParseable(t *testing.T) {
	if runtime.GOOS != "windows" || testing.Short() {
		t.Skip("Windows clipboard")
	}
	c, err := Read(context.Background())
	switch {
	case err == nil:
		if c.Kind != Text && c.Kind != Image && c.Kind != Files {
			t.Fatalf("unknown kind: %+v", c.Kind)
		}
	case errors.Is(err, ErrEmpty):
	case strings.Contains(err.Error(), "unexpected answer"):
		t.Fatalf("the script returned something Parse does not understand: %v", err)
	default:
		t.Skipf("no clipboard available in this session: %v", err)
	}
}

func TestEncode(t *testing.T) {
	if s, err := Encode(Content{Kind: Text, Text: "hola"}); err != nil || s != "TXT:aG9sYQ==\n" {
		t.Fatalf("%q %v", s, err)
	}
	if s, err := Encode(Content{PNG: []byte{1, 2}, Files: []string{"a"}}); err != nil || s != "IMG:AQI=\nFIL:YQ==\n" {
		t.Fatalf("%q %v", s, err)
	}
	for _, c := range []Content{{Kind: Image}, {Kind: None}, {Files: []string{""}}} {
		if _, err := Encode(c); err == nil {
			t.Errorf("%+v must be rejected", c)
		}
	}
}

// Writes to the REAL clipboard and reads it back (accented text and an image). Opt-in, like reading: GITSI_REAL_CLIPBOARD=1. It
// saves whatever was there (text or image) and restores it at the end.
func TestWriteRealClipboardRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("GITSI_REAL_CLIPBOARD") != "1" {
		t.Skip("opt-in: modifies the real clipboard (GITSI_REAL_CLIPBOARD=1)")
	}
	ctx := context.Background()
	before, berr := Read(ctx)
	defer func() {
		if berr == nil {
			_ = Write(ctx, before)
		}
	}()
	text := "gitsi take -clip: áéíóú ñ €"
	if err := Write(ctx, Content{Kind: Text, Text: text}); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(ctx); err != nil || got.Kind != Text || got.Text != text {
		t.Fatalf("text: %+v %v", got, err)
	}
	// a 2x2 image created by .NET itself, to have a valid PNG
	out, err := winps.Command(context.Background(), "-NoProfile", "-Command", `Add-Type -AssemblyName System.Drawing; $b = New-Object Drawing.Bitmap 2,2; $m = New-Object IO.MemoryStream; $b.Save($m, [Drawing.Imaging.ImageFormat]::Png); [Convert]::ToBase64String($m.ToArray())`).Output()
	if err != nil {
		t.Fatal(err)
	}
	png, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err := Write(ctx, Content{Kind: Image, PNG: png}); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(ctx); err != nil || got.Kind != Image || !bytes.HasPrefix(got.PNG, []byte("\x89PNG")) {
		t.Fatalf("image: kind=%v err=%v", got.Kind, err)
	}
}

// Files and folders to the REAL clipboard (as when copied in Explorer), and an image together with its file. Opt-in.
func TestWriteRealClipboardFilesAndImageWithFile(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("GITSI_REAL_CLIPBOARD") != "1" {
		t.Skip("opt-in: modifies the real clipboard (GITSI_REAL_CLIPBOARD=1)")
	}
	ctx := context.Background()
	before, berr := Read(ctx)
	defer func() {
		if berr == nil {
			_ = Write(ctx, before)
		}
	}()
	dir := t.TempDir()
	f := dir + `\report with spaces ñ.pdf`
	os.WriteFile(f, []byte("%PDF"), 0o644)
	sub := dir + `\folder`
	os.MkdirAll(sub, 0o755)
	if err := Write(ctx, Content{Files: []string{f, sub}}); err != nil {
		t.Fatal(err)
	}
	drop := func() string {
		out, err := winps.Command(context.Background(), "-NoProfile", "-STA", "-Command",
			`Add-Type -AssemblyName System.Windows.Forms; [Console]::OutputEncoding = [Text.Encoding]::UTF8; ([Windows.Forms.Clipboard]::GetFileDropList() -join '|') + '#' + [Windows.Forms.Clipboard]::ContainsImage()`).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := drop(); got != f+"|"+sub+"#False" {
		t.Fatalf("file list: %q", got)
	}
	out, _ := winps.Command(context.Background(), "-NoProfile", "-Command", `Add-Type -AssemblyName System.Drawing; $b = New-Object Drawing.Bitmap 2,2; $m = New-Object IO.MemoryStream; $b.Save($m, [Drawing.Imaging.ImageFormat]::Png); [Convert]::ToBase64String($m.ToArray())`).Output()
	png, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	img := dir + `\capture.png`
	os.WriteFile(img, png, 0o644)
	if err := Write(ctx, Content{PNG: png, Files: []string{img}}); err != nil {
		t.Fatal(err)
	}
	if got := drop(); got != img+"#True" {
		t.Fatalf("image and file at once: %q", got)
	}
}

// What is copied in Explorer (a file list) reads as Files, with their paths, before an image. Opt-in.
func TestReadRealClipboardFiles(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("GITSI_REAL_CLIPBOARD") != "1" {
		t.Skip("opt-in: modifies the real clipboard (GITSI_REAL_CLIPBOARD=1)")
	}
	ctx := context.Background()
	before, berr := Read(ctx)
	defer func() {
		if berr == nil {
			_ = Write(ctx, before)
		}
	}()
	dir := t.TempDir()
	a := dir + `\folder with spaces`
	b := dir + `\photo ñ.png`
	os.MkdirAll(a, 0o755)
	os.WriteFile(b, []byte("x"), 0o644)
	if err := Write(ctx, Content{Files: []string{a, b}, PNG: nil}); err != nil {
		t.Fatal(err)
	}
	got, err := Read(ctx)
	if err != nil || got.Kind != Files || len(got.Files) != 2 || got.Files[0] != a || got.Files[1] != b {
		t.Fatalf("%+v %v", got, err)
	}
}
