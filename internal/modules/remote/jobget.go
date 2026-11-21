package remote

import (
	"context"
	"errors"
	"fmt"

	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// jobGet is `job get <n|id> <path>`: it brings to Downloads\gitsi a path from the folder where a job ran (the -cwd one or the
// -repo working clone). It is `run`'s -get for what was started with `job start`, which does not wait for it to end.
func (m mod) jobGet(ctx context.Context, env *module.Env, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: gitsi job get <n|id> <path>   (the path, relative to the job's folder: e.g. TestResults)")
	}
	if m.take == nil {
		return errors.New("job get is not available here")
	}
	if err := checkJobArg(args[0]); err != nil {
		return err
	}
	if err := checkGetPath(args[1]); err != nil {
		return err
	}
	if err := policy.Check(env.Config.Policy, policy.Take); err != nil {
		return err // brings content to this machine: it requires take, checked before asking the hub anything
	}
	dir, err := jobsDirOf(ctx, env)
	if err != nil {
		return err
	}
	j, err := findJob(ctx, env, dir, args[0])
	if err != nil {
		return err
	}
	switch {
	case j.State == "running":
		return fmt.Errorf("job %s is still running: its folder may be half written (wait with 'gitsi job logs %s -f')", j.ID, j.ID)
	case j.Cwd == "":
		return fmt.Errorf("job %s ran without its own folder: job get works for jobs started with -cwd or -repo", j.ID)
	}
	if err := safe.Path("job folder", j.Cwd); err != nil {
		return err // it comes from the hub: validated like any path that ends up in a script
	}
	return getResult(ctx, env, j.Cwd, args[1], m.take)
}

// findJob looks a job up by `job ls` number or by id, with its state and folder.
func findJob(ctx context.Context, env *module.Env, dir, arg string) (Job, error) {
	n, isIndex := module.Index(arg)
	if !isIndex {
		if err := checkID(arg); err != nil {
			return Job{}, err
		}
	}
	res, err := env.RunOnHub(ctx, listScript(dir))
	if err != nil {
		return Job{}, err
	}
	jobs := parseJobs(res.Stdout)
	if isIndex {
		if n > len(jobs) {
			return Job{}, fmt.Errorf("there is no job number %d: there are %d (see gitsi job ls)", n, len(jobs))
		}
		return jobs[n-1], nil
	}
	for _, j := range jobs {
		if j.ID == arg {
			return j, nil
		}
	}
	return Job{}, fmt.Errorf("there is no job %s (see 'gitsi job ls')", arg)
}
