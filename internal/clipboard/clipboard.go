// Package clipboard reads and writes the Windows clipboard (files, image or text) through the system's powershell.exe, with no
// native dependencies or extra binaries. It only writes when the user asks for it with -clip (take -clip).
package clipboard

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// Kind is what the clipboard holds.
type Kind int

const (
	None  Kind = iota // empty or an unsupported format
	Image             // image, delivered as PNG
	Text              // UTF-8 text
	Files             // files or folders copied in Explorer (their paths)
)

// Content is the clipboard's content. Reading fills it according to Kind; writing sets every present format at once (e.g. an
// image as an image AND as a file, so it pastes both into a chat and into Explorer).
type Content struct {
	Kind  Kind
	PNG   []byte   // image (for writing, any format .NET opens works: png, jpg, bmp, gif)
	Text  string   // text
	Files []string // paths of files or folders, as when copied in Explorer
}

// Reader reads the clipboard. It is injected so tests run without Windows or a real clipboard.
type Reader func(ctx context.Context) (Content, error)

// Writer writes to the clipboard (replacing what was there). Also injectable.
type Writer func(ctx context.Context, c Content) error

// ErrEmpty means nothing usable is copied.
var ErrEmpty = errors.New("the clipboard holds no files, image or text")

// script prints a single line: FIL:<base64 paths separated by newlines> | IMG:<base64 png> | TXT:<base64 utf8> | NONE. Base64
// avoids console encoding problems. Files go first: a .png copied in Explorer is sent as the original file, not as a capture.
const script = `Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing;
if ([Windows.Forms.Clipboard]::ContainsFileDropList()) {
  'FIL:' + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(([Windows.Forms.Clipboard]::GetFileDropList() -join [char]10)))
} elseif ([Windows.Forms.Clipboard]::ContainsImage()) {
  $i = [Windows.Forms.Clipboard]::GetImage(); $m = New-Object IO.MemoryStream;
  $i.Save($m, [Drawing.Imaging.ImageFormat]::Png); 'IMG:' + [Convert]::ToBase64String($m.ToArray())
} elseif ([Windows.Forms.Clipboard]::ContainsText()) {
  'TXT:' + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([Windows.Forms.Clipboard]::GetText()))
} else { 'NONE' }`

// Parse interprets the script's output.
func Parse(out string) (Content, error) {
	out = strings.TrimSpace(out)
	switch {
	case out == "NONE" || out == "":
		return Content{}, ErrEmpty
	case strings.HasPrefix(out, "FIL:"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(out, "FIL:"))
		if err != nil {
			return Content{}, fmt.Errorf("invalid clipboard file list: %v", err)
		}
		var files []string
		for _, f := range strings.Split(string(b), string(rune(10))) {
			if f = strings.TrimSpace(f); f != "" {
				files = append(files, f)
			}
		}
		if len(files) == 0 {
			return Content{}, ErrEmpty
		}
		return Content{Kind: Files, Files: files}, nil
	case strings.HasPrefix(out, "IMG:"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(out, "IMG:"))
		if err != nil || len(b) == 0 {
			return Content{}, fmt.Errorf("invalid clipboard image: %v", err)
		}
		return Content{Kind: Image, PNG: b}, nil
	case strings.HasPrefix(out, "TXT:"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(out, "TXT:"))
		if err != nil {
			return Content{}, fmt.Errorf("invalid clipboard text: %v", err)
		}
		if len(b) == 0 {
			return Content{}, ErrEmpty
		}
		return Content{Kind: Text, Text: string(b)}, nil
	}
	return Content{}, fmt.Errorf("unexpected answer while reading the clipboard: %.60q", out)
}

// Read is the real Reader: PowerShell in STA mode (the Windows clipboard requires it).
func Read(ctx context.Context) (Content, error) {
	out, err := procx.Background(winps.Command(ctx, "-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command", script)).Output()
	if err != nil {
		return Content{}, fmt.Errorf("could not read the clipboard: %w", err)
	}
	return Parse(string(out))
}

// writeScript reads TXT:<base64 utf8>, IMG:<base64 image> or FIL:<base64 utf8 path> lines from stdin and puts ALL those formats in
// one clipboard object. SetDataObject with copy=true keeps it there after powershell exits. The data goes through stdin (not the
// command line): no size limit and no quoting problems.
const writeScript = `Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing;
$o = New-Object Windows.Forms.DataObject; $files = New-Object Collections.Specialized.StringCollection; $n = 0
foreach ($line in [Console]::In.ReadToEnd().Split([char]10)) {
  $line = $line.Trim(); if (-not $line) { continue }
  $k, $d = $line.Split(':', 2); $b = [Convert]::FromBase64String($d)
  if ($k -eq 'TXT') { $o.SetText([Text.Encoding]::UTF8.GetString($b)); $n++ }
  elseif ($k -eq 'IMG') { $m = New-Object IO.MemoryStream(,$b); $o.SetImage([Drawing.Image]::FromStream($m)); $n++ }
  elseif ($k -eq 'FIL') { [void]$files.Add([Text.Encoding]::UTF8.GetString($b)) }
  else { throw 'unknown format' }
}
if ($files.Count -gt 0) { $o.SetFileDropList($files); $n++ }
if ($n -eq 0) { throw 'nothing to copy' }
[Windows.Forms.Clipboard]::SetDataObject($o, $true)
'OK'`

// Encode builds writeScript's input with every format present in c.
func Encode(c Content) (string, error) {
	var b strings.Builder
	if c.Text != "" || (c.Kind == Text && len(c.PNG) == 0 && len(c.Files) == 0) {
		b.WriteString("TXT:" + base64.StdEncoding.EncodeToString([]byte(c.Text)) + "\n")
	}
	if len(c.PNG) > 0 {
		b.WriteString("IMG:" + base64.StdEncoding.EncodeToString(c.PNG) + "\n")
	}
	for _, f := range c.Files {
		if f == "" {
			return "", errors.New("empty path")
		}
		b.WriteString("FIL:" + base64.StdEncoding.EncodeToString([]byte(f)) + "\n")
	}
	if b.Len() == 0 {
		return "", errors.New("nothing to copy: no text, image or files")
	}
	return b.String(), nil
}

// Write is the real Writer: PowerShell in STA mode, with the content on stdin.
func Write(ctx context.Context, c Content) error {
	in, err := Encode(c)
	if err != nil {
		return err
	}
	cmd := winps.Command(ctx, "-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command", writeScript)
	cmd.Stdin = strings.NewReader(in)
	out, err := procx.Background(cmd).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		return fmt.Errorf("could not write to the clipboard: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
