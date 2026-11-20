package statuscmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/version"
)

func edgeEnv(hubVersion string, hubErr error) (*module.Env, *bytes.Buffer) {
	cfg := config.Default()
	cfg.Role = config.RoleEdge
	cfg.Hub.Host = "10.0.0.9"
	var out bytes.Buffer
	env := &module.Env{Stdout: &out, Stderr: &out, Config: cfg}
	env.RefreshLayoutFn = func(context.Context) (layout.Layout, string, error) { return layout.Layout{}, hubVersion, hubErr }
	return env, &out
}

func fixed(text string, calls *int) func(context.Context, *module.Env) (string, error) {
	return func(context.Context, *module.Env) (string, error) { *calls++; return text, nil }
}

func TestStatusShowsEachProbeForTheRole(t *testing.T) {
	var mailbox, watch, repo int
	m := New(
		Probe{Label: "mailbox", Roles: module.Both, NeedsHub: true, Run: fixed("nothing to receive", &mailbox)},
		Probe{Label: "watch", Roles: module.Hub, Run: fixed("no", &watch)},
		Probe{Label: "repo", Roles: module.Both, Run: func(context.Context, *module.Env) (string, error) { repo++; return "", errors.New("git broken") }},
	).(mod)
	env, out := edgeEnv(version.Version, nil)
	if err := m.run(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	want := "hub       10.0.0.9 answers, gitsi " + version.Version + " (same as this machine)\n" +
		"mailbox   nothing to receive\n" +
		"repo      error: git broken\n"
	if out.String() != want || watch != 0 {
		t.Fatalf("hub probes do not run on the edge; a failing one is shown and does not stop the rest:\n%s", out)
	}
}

func TestStatusSaysWhenTheHubHasAnotherVersion(t *testing.T) {
	env, out := edgeEnv("9.9.9", nil)
	if err := New().(mod).run(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "gitsi 9.9.9 and this machine "+version.Version+" (gitsi update)") {
		t.Fatal(out.String())
	}
}

func TestStatusWithoutHubSkipsWhatNeedsItAndFails(t *testing.T) {
	var mailbox, repo int
	m := New(
		Probe{Label: "mailbox", Roles: module.Both, NeedsHub: true, Run: fixed("x", &mailbox)},
		Probe{Label: "repo", Roles: module.Both, Run: fixed("up to date", &repo)},
	).(mod)
	hubDown := errors.New("could not connect to the hub")
	env, out := edgeEnv("", hubDown)
	if err := m.run(context.Background(), env, nil); !errors.Is(err, hubDown) {
		t.Fatalf("without a hub, status fails with the cause: %v", err)
	}
	if mailbox != 0 || repo != 1 || !strings.Contains(out.String(), "mailbox   error: no hub") || !strings.Contains(out.String(), "repo      up to date") {
		t.Fatalf("mailbox=%d repo=%d\n%s", mailbox, repo, out)
	}
}

func TestStatusOnTheHubAndInJSON(t *testing.T) {
	var watch int
	m := New(Probe{Label: "watch", Roles: module.Hub, Run: fixed("1 edge listening", &watch)}).(mod)
	cfg := config.Default()
	cfg.Role = config.RoleHub
	var out bytes.Buffer
	env := &module.Env{Stdout: &out, Stderr: &out, Config: cfg, JSON: true}
	if err := m.run(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	var got []line
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 2 || got[0].Text != "hub, gitsi "+version.Version || got[1].Text != "1 edge listening" {
		t.Fatalf("%v %s", err, out.String())
	}
}

// The hub announces its full version ("1.0.0 (commit abc, built ...)"): what is compared is the number.
func TestStatusComparesOnlyTheVersionNumber(t *testing.T) {
	env, out := edgeEnv(version.Version+" (commit abc1234, built 2026-11-19)", nil)
	if err := New().(mod).run(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(same as this machine)") || strings.Contains(out.String(), "gitsi update") {
		t.Fatal(out.String())
	}
}
