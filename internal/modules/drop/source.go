package drop

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/archive"
	"github.com/ShizukaJiku/gitsi/internal/clipboard"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// maxUncompressed is the cap on the content the receiver extracts from a folder (a variable so it can be tested).
var maxUncompressed = mailbox.DefaultMaxSize

// source is a content ready to send or leave: kind, name, random-access reader and size.
type source struct {
	Kind    mailbox.Kind
	Name    string
	R       io.ReaderAt
	Size    int64
	cleanup func()
	// Enc is the content's encoding (mailbox.EncGzip for a compressed tar).
	Enc string
	// Uncompressed is the packed folder's uncompressed content. Size (what travels) may be much smaller, but the receiver limits
	// what it EXTRACTS: without this figure, a 6 GiB folder that compresses to 1.5 GiB would upload and then fail on receipt.
	Uncompressed int64
	// Excluded is how many entries -exclude left out when packing a folder (0 without a filter).
	Excluded int
}

// excludeFlag is a repeatable -exclude: each use adds a glob pattern.
type excludeFlag []string

func (e *excludeFlag) String() string     { return strings.Join(*e, ",") }
func (e *excludeFlag) Set(v string) error { *e = append(*e, v); return nil }

func (s *source) Close() {
	if s.cleanup != nil {
		s.cleanup()
	}
}

// fromPath opens a file, or packs a folder into a temporary tar.
func fromPath(p string, opt archive.Options) (*source, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(abs)
	if err := safe.FileName(name); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	if st.IsDir() {
		tmp, err := os.CreateTemp("", "gitsi-dir-*.tar")
		if err != nil {
			return nil, err
		}
		cleanup := func() { tmp.Close(); os.Remove(tmp.Name()) }
		stats, err := archive.PackWith(abs, tmp, opt)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("could not pack %s: %w", p, err)
		}
		ts, err := tmp.Stat()
		if err != nil {
			cleanup()
			return nil, err
		}
		s := &source{Kind: mailbox.Dir, Name: name, R: tmp, Size: ts.Size(), cleanup: cleanup, Excluded: stats.Excluded, Uncompressed: stats.Bytes}
		if opt.Gzip {
			s.Enc = mailbox.EncGzip
		}
		return s, nil
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is neither a file nor a folder", p)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	return &source{Kind: mailbox.File, Name: name, R: f, Size: st.Size(), cleanup: func() { f.Close() }}, nil
}

// fromText creates an in-memory text content.
func fromText(name, text string) (*source, error) {
	if name == "" {
		name = "text.txt"
	}
	if err := safe.FileName(name); err != nil {
		return nil, err
	}
	return &source{Kind: mailbox.Text, Name: name, R: strings.NewReader(text), Size: int64(len(text))}, nil
}

// checkSendArgs validates what can be known without reading anything or connecting: send calls it before opening the SSH session
// so a usage error does not cost a connection, and sources applies it again (a single source of truth for these rules).
func checkSendArgs(paths []string, text *string, name string, exclude []string) error {
	if err := archive.ValidatePatterns(exclude); err != nil {
		return err
	}
	if text != nil {
		if len(paths) > 0 {
			return errors.New("use paths or -text, not both")
		}
		if *text == "" {
			return errors.New("the text is empty")
		}
		if name != "" {
			return safe.FileName(name)
		}
		return nil
	}
	if len(paths) == 0 {
		return errors.New("give at least one path, or -text \"...\" (use -text - to read stdin)")
	}
	for _, p := range paths { // they exist, before connecting or packing anything
		if _, err := os.Stat(p); err != nil {
			return err
		}
	}
	return nil
}

// checkUncompressed rejects, before sending ANYTHING, the folders whose uncompressed content exceeds what the receiver extracts:
// a batch of several paths must not be left half done because the last one does not fit.
func checkUncompressed(srcs []*source) error {
	for _, s := range srcs {
		if s.Kind == mailbox.Dir && s.Uncompressed > maxUncompressed {
			return fmt.Errorf("%s: the folder takes %s uncompressed and the receiver only extracts up to %s; send smaller parts or use -exclude",
				s.Name, human(s.Uncompressed), human(maxUncompressed))
		}
	}
	return nil
}

// sources interprets send's arguments: paths or -text.
func sources(paths []string, text *string, name string, stdin io.Reader, opt archive.Options) ([]*source, error) {
	if err := checkSendArgs(paths, text, name, opt.Exclude); err != nil {
		return nil, err
	}
	if text != nil {
		t := *text
		if t == "-" {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return nil, err
			}
			t = string(b)
		}
		if t == "" {
			return nil, errors.New("the text is empty")
		}
		s, err := fromText(name, t)
		if err != nil {
			return nil, err
		}
		return []*source{s}, nil
	}
	var out []*source
	for _, p := range paths {
		s, err := fromPath(p, opt)
		if err != nil {
			for _, o := range out {
				o.Close()
			}
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// materialize puts a received item in its final place. src is the already verified content.
//
//	file -> dir/<name>  (never overwrites)
//	dir  -> dir/<name>/ (tar extracted with path protection)
//	text -> stdout
func materialize(it mailbox.Item, src io.Reader, dir string, stdout io.Writer, maxBytes int64) (string, error) {
	switch it.Kind {
	case mailbox.Text:
		b, err := io.ReadAll(src)
		if err != nil {
			return "", err
		}
		stdout.Write(b)
		if len(b) > 0 && b[len(b)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
		return "(text shown)", nil
	case mailbox.Dir:
		dest := filepath.Join(dir, it.Name)
		if _, err := os.Stat(dest); err == nil {
			return "", fmt.Errorf("%s already exists; move it or use another -dir", dest)
		}
		unpack := archive.Unpack
		if it.Enc == mailbox.EncGzip {
			unpack = archive.UnpackGzip
		} else if it.Enc != "" {
			return "", fmt.Errorf("unknown encoding %q: update gitsi on this machine", it.Enc)
		}
		if err := unpack(src, dest, maxBytes); err != nil {
			os.RemoveAll(dest)
			return "", err
		}
		return dest, nil
	default:
		dest := filepath.Join(dir, it.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("%s already exists; move it or use another -dir", dest)
			}
			return "", err
		}
		_, cerr := io.Copy(f, src)
		if e := f.Close(); cerr == nil {
			cerr = e
		}
		if cerr != nil {
			os.Remove(dest)
			return "", cerr
		}
		return dest, nil
	}
}

// newSectionReader gives a sequential io.Reader over the content (for Store.Add).
func newSectionReader(s *source) io.Reader { return io.NewSectionReader(s.R, 0, s.Size) }

// fromClipboard turns a clipboard image or text into a content: image -> timestamped PNG file; text -> text. (Files copied in
// Explorer do not go through here: they are sent as paths.)
func fromClipboard(c clipboard.Content, now time.Time) (*source, error) {
	switch c.Kind {
	case clipboard.Image:
		name := "capture-" + now.Format("20060102-150405") + ".png"
		return &source{Kind: mailbox.File, Name: name, R: bytes.NewReader(c.PNG), Size: int64(len(c.PNG))}, nil
	case clipboard.Text:
		return fromText("clipboard.txt", c.Text)
	}
	return nil, clipboard.ErrEmpty
}
