package remote

import (
	"context"
	"fmt"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/module"
)

// Jobs summarizes the hub's jobs, for `gitsi status`. Listing jobs is part of the exec feature: when it is off, it says so instead
// of querying.
func Jobs(ctx context.Context, env *module.Env) (string, error) {
	if !env.Config.Policy.AllowExec {
		return "(exec off: gitsi policy set exec on)", nil
	}
	dir, err := jobsDirOf(ctx, env)
	if err != nil {
		return "", err
	}
	res, err := env.RunOnHub(ctx, listScript(dir))
	if err != nil {
		return "", err
	}
	jobs := parseJobs(res.Stdout)
	running, done := 0, 0
	for _, j := range jobs {
		if j.State == "running" {
			running++
		} else {
			done++
		}
	}
	if len(jobs) == 0 {
		return "none", nil
	}
	var parts []string
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	if done > 0 {
		parts = append(parts, fmt.Sprintf("%d finished", done))
	}
	return strings.Join(parts, ", ") + " (gitsi job ls)", nil
}
