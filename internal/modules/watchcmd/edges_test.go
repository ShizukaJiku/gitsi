package watchcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

func hubEnv(t *testing.T) (*module.Env, *bytes.Buffer) {
	t.Helper()
	cfg := config.Default()
	cfg.Role, cfg.Home = config.RoleHub, t.TempDir()
	var out bytes.Buffer
	return &module.Env{Stdout: &out, Stderr: &out, Config: cfg}, &out
}

func TestEdgesShowsWhoListensInTextAndJSON(t *testing.T) {
	env, out := hubEnv(t)
	if err := edgesCmd(context.Background(), env, nil); err != nil || !strings.Contains(out.String(), "No edge has listened") {
		t.Fatalf("%v %s", err, out)
	}
	dir := stateDir(env.Config)
	os.MkdirAll(dir, 0o700)
	now := time.Now()
	for _, st := range []watch.EdgeState{
		{Edge: "WORKSTATION", User: "ana", Version: "1.0.1", Connected: true, Since: now.Add(-time.Hour), LastPing: now.Add(-5 * time.Second), Reactions: []string{"update", "report"}},
		{Edge: "OLDBOX", User: "bea", Version: "1.0.0", Connected: true, Since: now.Add(-72 * time.Hour), LastPing: now.Add(-72 * time.Hour)},
	} {
		b, _ := json.Marshal(st)
		os.WriteFile(filepath.Join(dir, st.User+"@"+st.Edge+".json"), b, 0o600)
	}
	out.Reset()
	if err := edgesCmd(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ana@WORKSTATION", "listening", "update, report", "bea@OLDBOX", "disconnected", "3 days ago"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	env.JSON = true
	out.Reset()
	var rows []edgeRow
	if err := edgesCmd(context.Background(), env, nil); err != nil || json.Unmarshal(out.Bytes(), &rows) != nil || len(rows) != 2 || !rows[0].Connected || rows[1].Connected {
		t.Fatalf("%v %s", err, out)
	}
	if err := edgesCmd(context.Background(), env, []string{"x"}); err == nil {
		t.Fatal("edges takes no arguments")
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{12 * time.Second: "12 s ago", 5 * time.Minute: "5 min ago", 3 * time.Hour: "3 h ago", 50 * time.Hour: "2 days ago"} {
		if got := ago(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}

func TestIdentityReplacesOddNames(t *testing.T) {
	t.Setenv("COMPUTERNAME", "WORKSTATION-1")
	t.Setenv("USERNAME", "ana")
	if r := identity("abc"); r.Edge != "WORKSTATION-1" || r.User != "ana" || r.SHA256 != "abc" {
		t.Fatalf("%+v", r)
	}
	t.Setenv("COMPUTERNAME", "bad\x1b[2Jname")
	t.Setenv("USERNAME", "")
	if r := identity(""); r.Edge != "edge" || r.User != "user" {
		t.Fatalf("a name with control characters is replaced: %+v", r)
	}
}

func TestFatalOnlyForErrorsThatRetryingDoesNotFix(t *testing.T) {
	for _, err := range []error{errNoWatch, fault.New(fault.Auth, "key", ""), fault.New(fault.HostKey, "host key", "")} {
		if !fatal(err) {
			t.Errorf("%v should stop watch", err)
		}
	}
	for _, err := range []error{errors.New("the network dropped"), fault.New(fault.Unreachable, "no network", "")} {
		if fatal(err) {
			t.Errorf("%v should be retried", err)
		}
	}
}
