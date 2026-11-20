package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/module"
)

// cmdRef recognizes, at the start of a segment, "gitsi <word> [<word>]" and what follows up to where the quoted command ends:
// quotes, a backtick, PowerShell separators (; |), a comment (#) or the end of the segment.
var cmdRef = regexp.MustCompile("^gitsi ([a-z]+)(?: ([a-z]+))?([^'\"`;|#]*)")

// optRef is a quoted option (-dest, --active).
var optRef = regexp.MustCompile(`^--?([a-z][a-z-]*)$`)

// passthrough are the commands whose rest, from the first argument on, is another command (its options are not gitsi's).
var passthrough = map[string]bool{"run": true, "job start": true}

// A message or document that recommends a missing subcommand or option leaves the user stuck: when something is renamed or
// removed, the texts that name it go stale without anything failing. The texts users see (code, docs and scripts; not the
// tests, which write incomplete commands on purpose) are checked against the registry.
func TestTextsReferenceExistingCommandsAndOptions(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "README.md")); err != nil {
		t.Skip("the repo root is not found (running outside the repo)")
	}
	reg := mustRegistry(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".md", ".ps1":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, seg := range segments(line) {
				if m := cmdRef.FindStringSubmatch(seg); m != nil {
					if msg := checkRef(reg, m[1], m[2], m[3]); msg != "" {
						t.Errorf("%s:%d: %s", path, i+1, msg)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A document the README does not link is found by nobody: each docs/*.md has its line in the documentation section.
func TestReadmeLinksEveryDoc(t *testing.T) {
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Skip("the README is not found (running outside the repo)")
	}
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil || len(docs) == 0 {
		t.Fatalf("no docs: %v", err)
	}
	for _, d := range docs {
		if link := "(docs/" + filepath.Base(d) + ")"; !strings.Contains(string(b), link) {
			t.Errorf("the README does not link docs/%s", filepath.Base(d))
		}
	}
}

// segments cuts the line into segments starting at each "gitsi ", so one quoted command does not spill over the next.
func segments(line string) []string {
	var out []string
	for {
		i := strings.Index(line, "gitsi ")
		if i < 0 {
			return out
		}
		line = line[i:]
		j := strings.Index(line[len("gitsi "):], "gitsi ")
		if j < 0 {
			return append(out, line)
		}
		out = append(out, line[:len("gitsi ")+j])
		line = line[len("gitsi ")+j:]
	}
}

// checkRef checks a quote "gitsi first [second] rest" and returns the problem, or "" when it is valid or not a gitsi command
// (prose like "gitsi on the hub": the first word is not a command).
func checkRef(reg *registry, first, second, rest string) string {
	var cmd module.Command
	name := first
	if g, ok := reg.groups[first]; ok && second != "" {
		e, ok := g.subs[second]
		if !ok {
			return "mentions 'gitsi " + first + " " + second + "', which does not exist"
		}
		cmd, name = e.cmd, first+" "+second
	} else if e, ok := reg.top[first]; ok {
		cmd = e.cmd
		rest = second + rest // the second word is already an argument of the command
	} else {
		return ""
	}
	known := map[string]bool{"h": true, "help": true}
	for _, f := range cmd.Flags {
		known[strings.TrimLeft(f, "-")] = true
	}
	for _, tok := range strings.Fields(rest) {
		m := optRef.FindStringSubmatch(tok)
		if m == nil {
			if passthrough[name] && !strings.HasPrefix(tok, "<") {
				break // the remote command starts
			}
			continue
		}
		if !known[m[1]] {
			return "mentions '" + tok + "' for 'gitsi " + name + "', which is not one of its options"
		}
	}
	return ""
}
