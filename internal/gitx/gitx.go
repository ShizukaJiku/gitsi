// Package gitx wraps the system's git.exe. The Runner can be replaced in tests.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/fault"
)

// Runner runs git with arguments in a directory and returns stdout, stderr and the error.
type Runner func(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error)

// ExecRunner is the real Runner: it runs git.exe.
func ExecRunner(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// EnvRunner is ExecRunner with extra environment variables (e.g. GIT_INDEX_FILE to build a commit without touching the user's
// index).
func EnvRunner(vars ...string) Runner {
	return func(ctx context.Context, dir string, args ...string) (string, string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), vars...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}
}

// Git operates on a repository (Dir; empty = current directory).
type Git struct {
	Dir string
	Run Runner
}

// New creates a Git that uses the real git.exe.
func New(dir string) *Git { return &Git{Dir: dir, Run: ExecRunner} }

// Out runs git and returns its trimmed stdout. On failure, the error includes stderr.
func (g *Git) Out(ctx context.Context, args ...string) (string, error) {
	out, errOut, err := g.Run(ctx, g.Dir, args...)
	if err != nil {
		if fe := fault.Classify(errOut, fault.Where{}); fe != nil {
			return strings.TrimSpace(out), fe // e.g. the hub's key or host key, when git goes over ssh
		}
		return strings.TrimSpace(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut))
	}
	return strings.TrimSpace(out), nil
}

// Passthrough runs git showing its output live (for push/clone with progress).
func (g *Git) Passthrough(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// CurrentBranch returns the current branch, or "main" when there is none (repo without commits or detached HEAD).
func (g *Git) CurrentBranch(ctx context.Context) string {
	b, err := g.Out(ctx, "branch", "--show-current")
	if err != nil || b == "" {
		return "main"
	}
	return b
}

// HasRemote reports whether a remote with that name exists.
func (g *Git) HasRemote(ctx context.Context, name string) bool {
	_, err := g.Out(ctx, "remote", "get-url", name)
	return err == nil
}

// SetRemote creates the remote or updates its URL.
func (g *Git) SetRemote(ctx context.Context, name, url string) error {
	if g.HasRemote(ctx, name) {
		_, err := g.Out(ctx, "remote", "set-url", name, url)
		return err
	}
	_, err := g.Out(ctx, "remote", "add", name, url)
	return err
}

// HasCommits reports whether HEAD points to an existing commit.
func (g *Git) HasCommits(ctx context.Context) bool {
	_, err := g.Out(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// HasRef reports whether the reference exists (e.g. refs/remotes/hub/main).
func (g *Git) HasRef(ctx context.Context, ref string) bool {
	_, err := g.Out(ctx, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// Root returns the root of the repository that contains Dir.
func (g *Git) Root(ctx context.Context) (string, error) {
	return g.Out(ctx, "rev-parse", "--show-toplevel")
}

// RemoteBranches returns the remote branches (without the HEAD -> line).
func (g *Git) RemoteBranches(ctx context.Context) ([]string, error) {
	out, err := g.Out(ctx, "branch", "-r")
	if err != nil {
		return nil, err
	}
	var res []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.Contains(l, "->") {
			res = append(res, l)
		}
	}
	return res, nil
}
