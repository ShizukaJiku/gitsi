// Package archive packs folders into tar and extracts them safely. It is content-agnostic: it neither filters nor inspects
// files; it only protects itself from paths that escape the destination.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// Options tunes Pack. The filter is the user's decision (opt-in) and only looks at names, never at content.
type Options struct {
	// Exclude are glob patterns (path.Match). One that matches the name of a file or folder, or its relative path (with "/"),
	// leaves it out; for a folder, it is not entered at all (e.g. "node_modules", ".git", "*.log", "build/cache"). gitsi is a
	// Windows tool (names are validated with its rules): matching is case-insensitive and "\" equals "/" (there is no "\"
	// escape).
	Exclude []string
	// Gzip compresses the tar (fast level: the bottleneck is the network, not the CPU).
	Gzip bool
}

// Stats summarizes what was packed.
type Stats struct {
	Files    int   // files included
	Bytes    int64 // content bytes included
	Skipped  int   // symbolic links and other unsupported types (left out)
	Excluded int   // entries (files or whole folders) left out by Options.Exclude
}

// normPattern puts a pattern (or a name) in the form used for comparison: with "/" and case-insensitive, like the Windows file
// system (where node_modules and Node_Modules are the same folder and "\" separates folders).
func normPattern(s string) string {
	return strings.TrimRight(strings.ToLower(strings.ReplaceAll(s, `\`, "/")), "/") // "node_modules\\" (from tab completion) = "node_modules"
}

// excluded reports whether the entry (name and relative path with "/") matches any pattern.
func (o Options) excluded(name, rel string) bool {
	name, rel = normPattern(name), normPattern(rel)
	for _, pat := range o.Exclude {
		pat = normPattern(pat)
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
	}
	return false
}

// PackWith writes to w a tar with the content of src (paths relative to src, with "/"). Only files and folders: symbolic links
// are left out and counted in Stats.Skipped.
func PackWith(src string, w io.Writer, opt Options) (st Stats, err error) {
	if err := ValidatePatterns(opt.Exclude); err != nil {
		return st, err
	}
	root, err := filepath.Abs(src)
	if err != nil {
		return st, err
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return st, fmt.Errorf("%s is not a folder", src)
	}
	var zw *gzip.Writer
	if opt.Gzip {
		zw, _ = gzip.NewWriterLevel(w, gzip.BestSpeed) // the level is valid: no error
		w = zw
	}
	tw := tar.NewWriter(w)
	if err := addTree(tw, root, "", opt, &st); err != nil {
		return st, err
	}
	if err := tw.Close(); err != nil {
		return st, err
	}
	if zw != nil {
		return st, zw.Close()
	}
	return st, nil
}

// PackPaths writes to w a tar with one top-level entry per path: a file by its name, a folder by its name with its content inside
// (what Explorer copies to the clipboard: several files and folders from one folder). Two paths with the same name are refused:
// the second would overwrite the first when extracted.
func PackPaths(paths []string, w io.Writer, opt Options) (st Stats, err error) {
	if err := ValidatePatterns(opt.Exclude); err != nil {
		return st, err
	}
	if len(paths) == 0 {
		return st, errors.New("nothing to pack")
	}
	var zw *gzip.Writer
	if opt.Gzip {
		zw, _ = gzip.NewWriterLevel(w, gzip.BestSpeed)
		w = zw
	}
	tw := tar.NewWriter(w)
	seen := map[string]bool{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return st, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return st, err
		}
		name := filepath.Base(abs)
		if err := safe.FileName(name); err != nil {
			return st, fmt.Errorf("cannot pack %q: %v", name, err)
		}
		if seen[strings.ToLower(name)] {
			return st, fmt.Errorf("two items are called %q: pack them from different folders", name)
		}
		seen[strings.ToLower(name)] = true
		switch {
		case info.IsDir():
			if err := writeHeader(tw, info, name+"/"); err != nil {
				return st, err
			}
			if err := addTree(tw, abs, name+"/", opt, &st); err != nil {
				return st, err
			}
		case info.Mode().IsRegular():
			if err := addFile(tw, abs, info, name, &st); err != nil {
				return st, err
			}
		default:
			st.Skipped++
		}
	}
	if err := tw.Close(); err != nil {
		return st, err
	}
	if zw != nil {
		return st, zw.Close()
	}
	return st, nil
}

// ErrTooLarge is returned by TreeSize when the content exceeds the limit.
var ErrTooLarge = errors.New("the content exceeds the size limit")

// TreeSize adds up the size of the files under paths (files or folders), stopping as soon as it exceeds limit (> 0): a folder of
// several GB copied by mistake is refused without walking it whole, let alone packing it.
func TreeSize(paths []string, limit int64) (int64, error) {
	var total int64
	for _, p := range paths {
		err := filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				info, err := d.Info()
				if err != nil {
					return err
				}
				total += info.Size()
				if limit > 0 && total > limit {
					return ErrTooLarge
				}
			}
			return nil
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// addTree adds the content of root (not root itself) with names prefixed by prefix ("" or "name/").
func addTree(tw *tar.Writer, root, prefix string, opt Options, st *Stats) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == root {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if opt.excluded(d.Name(), rel) {
			st.Excluded++
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			st.Skipped++
			return nil
		}
		// The same rule Unpack enforces when extracting: better to fail here than after uploading several GB.
		if nerr := safe.FileName(d.Name()); nerr != nil {
			return fmt.Errorf("cannot send %q: %v (exclude it with -exclude %s)", rel, nerr, globFor(d.Name()))
		}
		if info.IsDir() {
			return writeHeader(tw, info, prefix+rel+"/")
		}
		return addFile(tw, p, info, prefix+rel, st)
	})
}

func writeHeader(tw *tar.Writer, info fs.FileInfo, name string) error {
	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	h.Name = name
	h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
	return tw.WriteHeader(h)
}

func addFile(tw *tar.Writer, p string, info fs.FileInfo, name string, st *Stats) error {
	if err := writeHeader(tw, info, name); err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(tw, f)
	st.Files++
	st.Bytes += n
	return err
}

// ValidatePatterns checks the syntax of the Exclude patterns. Call it before packing (or before connecting) so a mistyped
// pattern fails right away.
func ValidatePatterns(patterns []string) error {
	for _, pat := range patterns {
		if _, err := path.Match(normPattern(pat), ""); err != nil {
			return fmt.Errorf("invalid exclusion pattern %q: %w", pat, err)
		}
	}
	return nil
}

// globFor returns an -exclude pattern that matches the literal name: characters with a meaning in a glob ([ ] * ?) become "?"
// (any character), because -exclude has no "\" escape.
func globFor(name string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune("[]*?", r) {
			return '?'
		}
		return r
	}, name)
}

// MaxEntries is the maximum number of entries (files and folders) Unpack extracts from one archive. It is a variable so the
// limit can be tested without creating a million entries.
var MaxEntries = 1_000_000

// UnpackGzip is Unpack over a gzip-compressed tar. maxBytes limits what is extracted, not what is compressed: it guards against
// bombs.
func UnpackGzip(r io.Reader, dest string, maxBytes int64) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("the content is not valid gzip: %w", err)
	}
	defer zr.Close()
	return Unpack(zr, dest, maxBytes)
}

// safeRel validates and normalizes a tar entry path.
func safeRel(name string) (string, error) {
	n := strings.TrimSuffix(name, "/")
	if n == "" || strings.HasPrefix(n, "/") || strings.Contains(n, "\\") || strings.Contains(n, ":") {
		return "", fmt.Errorf("path not allowed in the archive: %q", name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == "." || part == ".." {
			return "", fmt.Errorf("path not allowed in the archive: %q", name)
		}
		if err := safe.FileName(part); err != nil {
			return "", fmt.Errorf("path not allowed in the archive: %q (%v)", name, err)
		}
	}
	return filepath.FromSlash(n), nil
}

// Unpack extracts the tar into dest (which is created). It rejects paths outside dest, links and excess size. maxBytes <= 0
// means no limit.
func Unpack(r io.Reader, dest string, maxBytes int64) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	var total int64
	for entries := 0; ; entries++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		// With gzip, millions of (highly compressible) headers fit in a few MB: the transferred size no longer bounds how many
		// entries there are, so their number is capped (the size limit only counts file content).
		if entries >= MaxEntries {
			return fmt.Errorf("the archive has too many entries (more than %d)", MaxEntries)
		}
		rel, err := safeRel(h.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if maxBytes > 0 && total > maxBytes {
				return errors.New("the extracted content exceeds the size limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(f, io.LimitReader(tr, h.Size))
			if e := f.Close(); cerr == nil {
				cerr = e
			}
			if cerr != nil {
				return cerr
			}
		default:
			return fmt.Errorf("unsupported entry type in %q (files and folders only)", h.Name)
		}
	}
}
