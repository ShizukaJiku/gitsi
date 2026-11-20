package remote

import (
	"context"
	"strings"
	"testing"
)

func TestJobsSummaryRespectsExecAndCountsStates(t *testing.T) {
	tr := &scriptTr{outs: []string{"JOB|aaaaaaaa|running||2026-11-16T22:00:00-05:00|docker\r\nJOB|bbbbbbbb|exited|5|2026-11-16T21:00:00-05:00|make\r\nJOB|cccccccc|lost||2026-11-16T20:00:00-05:00|x\r\n", ""}}
	e := envWith(t, tr)
	e.Config.Policy.AllowExec = false
	if got, err := Jobs(context.Background(), e); err != nil || !strings.Contains(got, "exec off") || len(tr.cmds) != 0 {
		t.Fatalf("with exec off it does not query the hub: %q %v cmds=%d", got, err, len(tr.cmds))
	}
	e.Config.Policy.AllowExec = true
	if got, err := Jobs(context.Background(), e); err != nil || got != "1 running, 2 finished (gitsi job ls)" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := Jobs(context.Background(), e); got != "none" {
		t.Fatalf("%q", got)
	}
}
