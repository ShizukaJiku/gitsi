package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/install"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/procx"
)

// FSSource is the hub's real state: the published binary, the mailbox's outbox, the jobs folder and the bridge repos.
type FSSource struct {
	BinaryPath string
	Mailbox    *mailbox.Store
	JobsDir    string
	ReposDir   string

	mu      sync.Mutex
	binSize int64
	binMod  time.Time
	binSHA  string
	binVer  string
}

// Binary returns the published binary's hash and version. It only recomputes them when the size or the date change (it is queried
// every few seconds).
func (s *FSSource) Binary() (string, string, error) {
	st, err := os.Stat(s.BinaryPath)
	if err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.binSHA != "" && st.Size() == s.binSize && st.ModTime().Equal(s.binMod) {
		return s.binSHA, s.binVer, nil
	}
	sha, err := install.FileSHA256(s.BinaryPath)
	if err != nil {
		return "", "", err // e.g. halfway through being published: retried on the next query
	}
	s.binSize, s.binMod, s.binSHA, s.binVer = st.Size(), st.ModTime(), sha, binaryVersion(s.BinaryPath)
	return s.binSHA, s.binVer, nil
}

// binaryVersion asks the published binary for its version (it is the hub's own gitsi). When it does not answer, it stays empty.
func binaryVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := procx.Background(exec.CommandContext(ctx, path, "version")).Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return strings.TrimPrefix(line, "gitsi ")
}

// Outbox lists the mailbox's outbox.
func (s *FSSource) Outbox() ([]OutboxItem, error) {
	items, err := s.Mailbox.List(mailbox.Outbox)
	if err != nil {
		return nil, err
	}
	out := make([]OutboxItem, 0, len(items))
	for _, it := range items {
		out = append(out, OutboxItem{ID: it.ID, Kind: string(it.Kind), Name: it.Name, Size: it.Size})
	}
	return out, nil
}

// FinishedJobs returns the jobs with an 'exit' file (finished) and their code.
func (s *FSSource) FinishedJobs() (map[string]string, error) {
	entries, err := os.ReadDir(s.JobsDir)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.JobsDir, e.Name(), "exit"))
		if err != nil {
			continue // running (or lost): nothing to notify yet
		}
		out[e.Name()] = strings.TrimSpace(strings.TrimPrefix(string(b), string(rune(0xFEFF)))) // PowerShell may prepend a BOM
	}
	return out, nil
}

// Heads reads the branch tips of each bare bridge repo (<repos>/<name>.git) straight from its ref files: it is queried every few
// seconds and there is no need to start git for that. Loose refs (refs/heads/...) win over packed-refs.
func (s *FSSource) Heads() (map[string]string, error) {
	out := map[string]string{}
	if s.ReposDir == "" {
		return out, nil
	}
	repos, err := os.ReadDir(s.ReposDir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, r := range repos {
		if !r.IsDir() || !strings.HasSuffix(r.Name(), ".git") {
			continue
		}
		name := strings.TrimSuffix(r.Name(), ".git")
		dir := filepath.Join(s.ReposDir, r.Name())
		heads := map[string]string{}
		if b, err := os.ReadFile(filepath.Join(dir, "packed-refs")); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				f := strings.Fields(l)
				if len(f) == 2 && strings.HasPrefix(f[1], "refs/heads/") {
					heads[strings.TrimPrefix(f[1], "refs/heads/")] = f[0]
				}
			}
		}
		root := filepath.Join(dir, "refs", "heads")
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil // halfway through being written: seen on the next query
			}
			rel, _ := filepath.Rel(root, p)
			heads[filepath.ToSlash(rel)] = strings.TrimSpace(string(b))
			return nil
		})
		for branch, commit := range heads {
			if validHead(name, branch, commit) {
				out[HeadKey(name, branch)] = commit
			}
		}
	}
	return out, nil
}
