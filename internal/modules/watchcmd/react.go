package watchcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/gitx"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// selfRunner runs gitsi itself with some arguments and returns its output (a variable in tests). Without interactive input: when
// a policy is missing, the command fails and says so, instead of waiting for an answer.
type selfRunner func(ctx context.Context, args ...string) (string, error)

func execSelf(ctx context.Context, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute) // an update over a slow link may take a while
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fetcher fetches the given repo from the bridge when it is the repo of the folder where watch runs; it says whether it was (a
// variable in tests).
type fetcher func(ctx context.Context, repo string) (bool, error)

// fetchHere is the real fetcher: it only acts when the current folder is a repo whose remote rn points to the bridge repo (through
// gitsi's alias). git fetch only updates the remote branches: it touches neither the user's branch nor their files.
func fetchHere(rn, alias string) fetcher {
	return func(ctx context.Context, repo string) (bool, error) {
		g := gitx.New("")
		root, err := g.Root(ctx)
		if err != nil {
			return false, nil
		}
		g.Dir = root
		url, err := g.Out(ctx, "config", "--get", "remote."+rn+".url")
		if err != nil || !strings.HasPrefix(url, alias+":") || path.Base(strings.TrimPrefix(url, alias+":")) != repo+".git" {
			return false, nil
		}
		_, err = g.Out(ctx, "fetch", "--quiet", rn)
		return true, err
	}
}

// reactor runs the automatic reactions the user enabled in the configuration (all off by default).
type reactor struct {
	cfg   config.Watch
	self  selfRunner
	fetch fetcher
	out   io.Writer
	edge  string
	now   func() time.Time
}

// names are the active reactions (announced to the hub for 'gitsi edges').
func (r *reactor) names() []string {
	var n []string
	if r.cfg.AutoUpdate {
		n = append(n, "update")
	}
	if r.cfg.AutoTake != "" {
		n = append(n, "take")
	}
	if r.cfg.Report {
		n = append(n, "report")
	}
	if r.cfg.AutoFetch {
		n = append(n, "fetch")
	}
	return n
}

func (r *reactor) printf(format string, a ...any) {
	fmt.Fprintf(r.out, "[%s] %s\n", r.now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

// react implements watch.Client.React. It returns true when watch must restart (a new version was installed).
func (r *reactor) react(ctx context.Context, e watch.Event) bool {
	switch e.Type {
	case watch.EvUpdate:
		if !r.cfg.AutoUpdate {
			return false
		}
		r.printf("installing the new version...")
		out, err := r.self(ctx, "update")
		r.report(ctx, "watch-update.txt", "install version "+e.Version, out, err)
		if err != nil {
			r.printf("could not install it (this version is not retried): %s", lastLine(out, err))
			return false
		}
		r.printf("installed; restarting watch with the new version")
		return true
	case watch.EvOutbox:
		if r.cfg.AutoTake == "" {
			return false
		}
		out, err := r.self(ctx, "take", e.ID, "-dir", r.cfg.AutoTake)
		r.report(ctx, "watch-take.txt", "receive "+e.Name, out, err)
		if err != nil {
			r.printf("could not receive %s: %s", watch.Printable(e.Name, 80), lastLine(out, err))
		} else {
			r.printf("received %s in %s", watch.Printable(e.Name, 80), r.cfg.AutoTake)
		}
	case watch.EvRepo:
		if !r.cfg.AutoFetch || r.fetch == nil {
			return false
		}
		here, err := r.fetch(ctx, e.Repo)
		switch {
		case err != nil:
			r.printf("could not fetch %s: %v", e.Repo, err)
		case here:
			r.printf("fetched %s: branch %s is now at hub/%s (gitsi sync to integrate it)", e.Repo, e.Branch, e.Branch)
		}
	}
	return false
}

// report leaves a reaction's result in the hub's inbox (one item per reaction), when the user enabled it.
func (r *reactor) report(ctx context.Context, name, what, out string, err error) {
	if !r.cfg.Report {
		return
	}
	res := "OK"
	if err != nil {
		res = "FAILED"
	}
	text := fmt.Sprintf("gitsi watch on %s (%s)\n%s: %s\n\n%s", r.edge, r.now().Format(time.RFC3339), what, res, strings.TrimSpace(out))
	if len(text) > 8000 {
		text = text[:8000] + "\n..."
	}
	if sout, serr := r.self(ctx, "send", "-text", text, "-name", name); serr != nil {
		r.printf("could not leave the report on the hub: %s", lastLine(sout, serr))
	}
}

func lastLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return err.Error()
}

// restartSelf starts 'gitsi watch' with the new binary in the same terminal and waits for it to end (a variable in tests).
var restartSelf = func(env *module.Env) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "watch")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			return &module.ExitError{Code: xe.ExitCode()}
		}
		return fmt.Errorf("could not restart watch with the new version: %w", err)
	}
	return nil
}
