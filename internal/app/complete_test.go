package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

func completeWith(t *testing.T, role config.Role, cfg *config.Config, line ...string) string {
	t.Helper()
	reg, err := newRegistry(Modules())
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = config.Default()
	}
	cfg.Role = role
	cur := line[len(line)-1]
	got := reg.complete(context.Background(), role, line[:len(line)-1], cur, func() *module.Env { return &module.Env{Config: cfg} })
	return strings.Join(got, " ")
}

func TestCompleteCommandsGroupsAndSubcommandsByRole(t *testing.T) {
	if got := completeWith(t, config.RoleEdge, nil, "s"); got != "send setup share status svc sync" {
		t.Errorf("edge, 's': %q", got)
	}
	if got := completeWith(t, config.RoleHub, nil, "s"); got != "send setup status sync" {
		t.Errorf("hub, 's' (no share or svc, which belong to the edge): %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "se"); strings.Contains(got, "serve") {
		t.Errorf("hidden ones are not proposed: %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "job", ""); got != "get logs ls start stop" {
		t.Errorf("subcommands: %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "-q", "take", "-l"); got != "-last -list" {
		t.Errorf("options, skipping the global ones: %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "repo", "get", "x", "-"); got != "-dest" {
		t.Errorf("a subcommand's options: %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "take", ""); got != "-clip -dir -keep -last -list -rm -show" {
		t.Errorf("with an empty word, the options (Windows PowerShell 5.1 does not complete words starting with -): %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "nothing", ""); got != "" {
		t.Errorf("unknown command: %q", got)
	}
}

func TestCompleteValuesFromTheCommand(t *testing.T) {
	cfg := config.Default()
	cfg.Services = []config.Service{{Name: "api", RemotePort: 3000, LocalPort: 13000}}
	if got := completeWith(t, config.RoleEdge, cfg, "svc", "rm", "a"); got != "api" {
		t.Errorf("svc rm: %q", got)
	}
	if got := completeWith(t, config.RoleEdge, cfg, "svc", "up", "p"); got != "postgres" {
		t.Errorf("svc up also proposes the known ones: %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "policy", "set", ""); got != "clip exec send services take" {
		t.Errorf("policy set: %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "policy", "set", "exec", ""); got != "off on" {
		t.Errorf("policy set exec: %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "config", "set", "watch.auto_update", ""); got != "off on" {
		t.Errorf("config set of an on/off key: %q", got)
	}
	if got := completeWith(t, config.RoleEdge, nil, "config", "set", "W"); !strings.Contains(got, "watch.report") {
		t.Errorf("keys, case-insensitive: %q", got)
	}
}
