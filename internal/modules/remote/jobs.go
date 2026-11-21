package remote

// Edge commands for background jobs (job start/ls/logs/stop). The scripts they run on the hub are in jobs_hub.go.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// jobsDirOf returns the hub's jobs folder, which the hub announces (the edge does not configure it).
func jobsDirOf(ctx context.Context, env *module.Env) (string, error) {
	lay, err := env.HubLayout(ctx)
	if err != nil {
		return "", err
	}
	return lay.Jobs, safe.Path("jobs folder", lay.Jobs)
}

// resolveJob interprets arg as a `job ls` number ("2") or as a job id, and returns the id.
func resolveJob(ctx context.Context, env *module.Env, dir, arg string) (string, error) {
	n, ok := module.Index(arg)
	if !ok {
		return arg, checkID(arg)
	}
	res, err := env.RunOnHub(ctx, listScript(dir))
	if err != nil {
		return "", err
	}
	jobs := parseJobs(res.Stdout)
	if n > len(jobs) {
		return "", fmt.Errorf("there is no job number %d: there are %d (see gitsi job ls)", n, len(jobs))
	}
	return jobs[n-1].ID, nil
}

// checkJobArg validates a <n|id> argument before anything connects: a typo fails at once, without waiting for the hub.
func checkJobArg(arg string) error {
	if _, ok := module.Index(arg); ok || jobIDRe.MatchString(arg) {
		return nil
	}
	return fmt.Errorf("invalid job: %q (a number from gitsi job ls, or an id of 8 hexadecimal characters)", arg)
}

func checkID(id string) error {
	if !jobIDRe.MatchString(id) {
		return fmt.Errorf("invalid job id: %q (8 hexadecimal characters)", id)
	}
	return nil
}

// runBG starts a background job and reports its id.
func runBG(ctx context.Context, env *module.Env, cwd, command string) error {
	if len(command) > maxCommandLen {
		return fmt.Errorf("the command is too long (%d > %d); save it in a script on the hub", len(command), maxCommandLen)
	}
	dir, err := jobsDirOf(ctx, env)
	if err != nil {
		return err
	}
	id, err := newJobID()
	if err != nil {
		return err
	}
	res, err := env.RunOnHub(ctx, startScript(dir, id, cwd, command))
	if err != nil {
		return err
	}
	if !strings.Contains(res.Stdout, "STARTED|"+id) {
		return fmt.Errorf("the hub could not start the job (exit code %d): %s%s", res.ExitCode, strings.TrimSpace(res.Stdout), strings.TrimSpace(res.Stderr))
	}
	_ = policy.Audit(env.Config.Policy, "run.bg", fmt.Sprintf("id=%s exe=%s cwd=%s", id, exeName(command), cwd))
	fmt.Fprintf(env.Stdout, "Job %s in the background. See: gitsi job ls | gitsi job logs %s -f | gitsi job stop %s\n", id, id, id)
	return nil
}

func jobsCmd(ctx context.Context, env *module.Env, _ []string) error {
	dir, err := jobsDirOf(ctx, env)
	if err != nil {
		return err
	}
	res, err := env.RunOnHub(ctx, listScript(dir))
	if err != nil {
		return err
	}
	jobs := parseJobs(res.Stdout)
	if env.JSON {
		if jobs == nil {
			jobs = []Job{}
		}
		return env.PrintJSON(jobs)
	}
	if len(jobs) == 0 {
		fmt.Fprintln(env.Stdout, "No background jobs.")
		return nil
	}
	fmt.Fprintf(env.Stdout, "%-4s %-9s %-9s %-6s %-26s %s\n", "N", "ID", "STATE", "EXIT", "STARTED", "COMMAND")
	for i, j := range jobs {
		fmt.Fprintf(env.Stdout, "%-4s %-9s %-9s %-6s %-26s %s\n", fmt.Sprintf("[%d]", i+1), j.ID, j.State, j.Exit, j.Started, j.Exe)
	}
	return nil
}

// pollInterval is the wait between `logs -f` queries.
var pollInterval = time.Second

func logsCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("job logs", env.Stderr)
	follow := fs.Bool("f", false, "follow the output until the job ends")
	tail := fs.Int("tail", 0, "show only the last N lines (without -f)")
	pos, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: gitsi job logs <n|id> [-f] [-tail N]")
	}
	if err := checkJobArg(pos[0]); err != nil {
		return err
	}
	dir, err := jobsDirOf(ctx, env)
	if err != nil {
		return err
	}
	id, err := resolveJob(ctx, env, dir, pos[0])
	if err != nil {
		return err
	}
	if !*follow && *tail > 0 {
		return tailLogs(ctx, env, dir, id, *tail)
	}
	var offset int64
	var all []byte
	for {
		res, err := env.RunOnHub(ctx, logsScript(dir, id, offset))
		if err != nil {
			return err
		}
		c, err := parseLogs(res.Stdout)
		if err != nil {
			return err
		}
		if c.NoJob {
			return fmt.Errorf("there is no job %s (see 'gitsi job ls')", id)
		}
		offset = c.Offset
		if *follow {
			env.Stdout.Write(c.Data)
		} else {
			all = append(all, c.Data...)
		}
		more := len(c.Data) == 1<<20 // there was more to read
		if !more && (!*follow || c.State != "running") {
			if !*follow {
				text := string(all)
				if *tail > 0 {
					text = lastLines(text, *tail)
				}
				fmt.Fprint(env.Stdout, text)
			}
			reportEnd(env, id, c)
			return nil
		}
		if !more {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(pollInterval):
			}
		}
	}
}

func reportEnd(env *module.Env, id string, c logChunk) {
	switch c.State {
	case "exited":
		fmt.Fprintf(env.Stderr, "[job %s finished, exit code %s]\n", id, c.Exit)
	case "lost":
		fmt.Fprintf(env.Stderr, "[job %s: the process no longer exists and left no exit code]\n", id)
	}
}

// tailLogs shows the last n lines asking the hub only for the end of the log (a negative offset counts from the end) instead of
// downloading it all: a 64 KiB window that grows (256 KiB, 1 MiB) when it does not hold n complete lines. With huge lines it may
// show fewer than n: the limit is 1 MiB per query.
func tailLogs(ctx context.Context, env *module.Env, dir, id string, n int) error {
	var c logChunk
	for back := int64(64 << 10); ; back *= 4 {
		res, err := env.RunOnHub(ctx, logsScript(dir, id, -back))
		if err != nil {
			return err
		}
		if c, err = parseLogs(res.Stdout); err != nil {
			return err
		}
		if c.NoJob {
			return fmt.Errorf("there is no job %s (see 'gitsi job ls')", id)
		}
		start := c.Offset - int64(len(c.Data))
		// n complete lines: there are n+1 newlines (the window's first line may come cut), or it is already the start
		if start == 0 || strings.Count(string(c.Data), "\n") > n || back >= 1<<20 {
			text := string(c.Data)
			if start > 0 {
				if i := strings.IndexByte(text, '\n'); i >= 0 {
					text = text[i+1:] // drops the first line, possibly cut
				}
			}
			fmt.Fprint(env.Stdout, lastLines(text, n))
			reportEnd(env, id, c)
			return nil
		}
	}
}

func lastLines(s string, n int) string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "")
}

func stopCmd(ctx context.Context, env *module.Env, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: gitsi job stop <n|id>")
	}
	if err := checkJobArg(args[0]); err != nil {
		return err
	}
	dir, err := jobsDirOf(ctx, env)
	if err != nil {
		return err
	}
	id, err := resolveJob(ctx, env, dir, args[0])
	if err != nil {
		return err
	}
	res, err := env.RunOnHub(ctx, stopScript(dir, id))
	if err != nil {
		return err
	}
	switch {
	case strings.Contains(res.Stdout, "NOJOB"):
		return fmt.Errorf("there is no job %s", id)
	case strings.Contains(res.Stdout, "STOPPED"):
		_ = policy.Audit(env.Config.Policy, "run.stop", "id="+id)
		fmt.Fprintf(env.Stdout, "Job %s stopped.\n", id)
	default:
		fmt.Fprintf(env.Stdout, "Job %s was not running.\n", id)
	}
	return nil
}
