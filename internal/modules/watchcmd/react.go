package watchcmd

import (
	"context"
	"encoding/json"
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
	"github.com/ShizukaJiku/gitsi/internal/procx"
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
	out, err := procx.Background(cmd).CombinedOutput()
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
	// liveUpdate reads watch.auto_update as it is now (the tray's Settings change it while watch runs); nil: cfg's value.
	liveUpdate func() bool
	// liveMirror does the same for watch.mirror.
	liveMirror func() bool
	lastMirror string // the last mirror line printed, so a failure that lasts is said once
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
	if r.cfg.Mirror {
		n = append(n, "mirror")
	}
	return n
}

// mirrorEvery is how often watch brings the origin mirrors up to date (a var in tests).
var mirrorEvery = 10 * time.Minute

// mirrorLoop keeps the origin mirrors (gitsi mirror) up to date while watch runs: one cycle when it starts and one every
// mirrorEvery. Cycles run one after the other in this goroutine, so two never overlap: a slow one just skips the ticks it
// covered. watch.mirror is read as it is now on each tick (the tray's Settings change it while watch runs). The cycle is
// gitsi itself (mirror sync), like the other reactions: the same lock, audit and messages, and watch depends on no other module.
func (r *reactor) mirrorLoop(ctx context.Context) {
	tick := time.NewTicker(mirrorEvery)
	defer tick.Stop()
	for {
		if r.mirrorOn() {
			r.mirrorCycle(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (r *reactor) mirrorOn() bool {
	if r.liveMirror != nil {
		return r.liveMirror()
	}
	return r.cfg.Mirror
}

// mirrorCycle runs one cycle and says what happened when something did: a repo updated or failing, or a warning. A quiet cycle
// prints nothing, and the same line is not repeated cycle after cycle (an origin that stays down).
func (r *reactor) mirrorCycle(ctx context.Context) {
	out, err := r.self(ctx, "--json", "mirror", "sync")
	var res struct {
		Repos []struct {
			Name  string `json:"name"`
			Error string `json:"error"`
		} `json:"repos"`
		Changed int    `json:"changed"`
		Failed  int    `json:"failed"`
		Warning string `json:"warning"`
	}
	// the output is stdout and stderr together: the JSON comes first and the command's final error, if any, after it
	var line string
	if json.NewDecoder(strings.NewReader(out)).Decode(&res) != nil {
		line = "mirror: " + lastLine(out, err)
	} else if res.Changed > 0 || res.Failed > 0 || res.Warning != "" {
		line = fmt.Sprintf("mirror: %d repo(s), %d updated, %d failed", len(res.Repos), res.Changed, res.Failed)
		for _, repo := range res.Repos {
			if repo.Error != "" {
				line += fmt.Sprintf(" (%s: %s)", watch.Printable(repo.Name, 40), watch.Printable(repo.Error, 160))
				break
			}
		}
		if res.Warning != "" {
			line += "; " + watch.Printable(res.Warning, 200)
		}
	}
	if line == "" {
		r.lastMirror = ""
		return
	}
	if line != r.lastMirror || res.Changed > 0 {
		r.printf("%s", line)
	}
	r.lastMirror = line
}

func (r *reactor) printf(format string, a ...any) {
	fmt.Fprintf(r.out, "[%s] %s\n", r.now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

// react implements watch.Client.React. It returns true when watch must restart (a new version was installed).
func (r *reactor) react(ctx context.Context, e watch.Event) bool {
	switch e.Type {
	case watch.EvUpdate:
		auto := r.cfg.AutoUpdate
		if r.liveUpdate != nil {
			auto = r.liveUpdate()
		}
		if !auto {
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

// restartSelf starts watch again with the new binary (a variable in tests). In a terminal it runs in the same one and waits for it
// to end; in the tray it starts a new tray process and this one ends.
var restartSelf = func(env *module.Env, tray bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if tray {
		cmd := procx.Detached(exec.Command(exe, "watch", "-tray"))
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("could not restart watch with the new version: %w", err)
		}
		return nil
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
