package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
)

func mustRegistry(t *testing.T) *registry {
	t.Helper()
	r, err := newRegistry(Modules())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// setRole leaves an isolated HOME with a role's configuration (or without configuration when role == "").
func setRole(t *testing.T, role config.Role) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	if role != "" {
		os.MkdirAll(filepath.Join(home, ".gitsi"), 0o700)
		b, _ := json.Marshal(map[string]any{"version": 1, "role": role, "repos_dir": "C:/x/repos"})
		os.WriteFile(filepath.Join(home, ".gitsi", "config.json"), b, 0o600)
	}
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

// Names cannot repeat across modules, groups, shortcuts and aliases.
func TestRegistryNoDuplicates(t *testing.T) {
	mustRegistry(t)
}

func TestRegistryDetectsCollisions(t *testing.T) {
	cmd := func(name string, f func(c *module.Command)) module.Module {
		c := module.Command{Name: name, Roles: module.Both}
		f(&c)
		return fakeMod{c}
	}
	for name, mods := range map[string][]module.Module{
		"repeated shortcut":        {cmd("a", func(c *module.Command) { c.Group, c.Shortcut = "repo", true }), cmd("a", func(c *module.Command) {})},
		"repeated subcommand":      {cmd("a", func(c *module.Command) { c.Group = "repo" }), cmd("a", func(c *module.Command) { c.Group = "repo" })},
		"undeclared group":         {cmd("a", func(c *module.Command) { c.Group = "made-up" })},
		"group clashes w/ command": {cmd("repo", func(c *module.Command) {}), cmd("a", func(c *module.Command) { c.Group = "repo" })},
	} {
		if _, err := newRegistry(mods); err == nil {
			t.Errorf("%s: should be detected", name)
		}
	}
}

type fakeMod struct{ c module.Command }

func (fakeMod) Name() string                 { return "fake" }
func (f fakeMod) Commands() []module.Command { return []module.Command{f.c} }

func TestCommandsResolve(t *testing.T) {
	r := mustRegistry(t)
	for _, name := range []string{"check", "config", "policy", "publish", "take", "run", "send", "share", "sync", "update"} {
		if res := r.resolve([]string{name}); res.kind != found {
			t.Errorf("%q must resolve", name)
		}
	}
}

func TestResolveGroupsAndShortcuts(t *testing.T) {
	r := mustRegistry(t)
	cases := map[string]string{ // line -> command it must run (group/subcommand)
		"send":        "send",
		"repo share":  "share",
		"share":       "share",
		"repo status": "status",
		"svc up":      "up",
		"job ls":      "ls",
		"job start":   "start",
		"setup edge":  "edge",
		"setup hub":   "hub",
		"setup key":   "key",
		"check":       "check",
	}
	for line, want := range cases {
		res := r.resolve(strings.Fields(line))
		if res.kind != found || res.cmd.Name != want {
			t.Errorf("%q => %+v (want %q)", line, res, want)
		}
	}
	// arguments: what follows group+subcommand is for the command
	if res := r.resolve([]string{"send", "a.txt", "-text", "x"}); len(res.rest) != 3 || res.rest[0] != "a.txt" {
		t.Errorf("rest: %v", res.rest)
	}
	if res := r.resolve([]string{"send", "a.txt"}); len(res.rest) != 1 {
		t.Errorf("shortcut rest: %v", res.rest)
	}
	if res := r.resolve([]string{"repo"}); res.kind != showGroup {
		t.Error("a bare group shows its help")
	}
	if res := r.resolve([]string{"repo", "nada"}); res.kind != unknownSub {
		t.Error("missing subcommand")
	}
	if res := r.resolve([]string{"nada"}); res.kind != unknownCmd {
		t.Error("missing command")
	}
}

func TestHelpIsGroupedAndFilteredByRole(t *testing.T) {
	setRole(t, config.RoleEdge)
	code, out, _ := run(t, "help")
	if code != 0 {
		t.Fatal(out)
	}
	for _, want := range []string{"EDGE", "Groups:", "setup", "repo", "svc", "job", "Daily use:", "send", "take", "check", "update"} {
		if !strings.Contains(out, want) {
			t.Errorf("the edge's help must contain %q:\n%s", want, out)
		}
	}
	for _, hidden := range []string{"serve"} {
		if strings.Contains(out, hidden) {
			t.Errorf("the edge's help must not show %q:\n%s", hidden, out)
		}
	}
	if lines := strings.Count(out, "\n"); lines > 22 {
		t.Errorf("the main help must be short, it has %d lines:\n%s", lines, out)
	}

	setRole(t, config.RoleHub)
	_, out, _ = run(t, "help")
	if !strings.Contains(out, "HUB") || !strings.Contains(out, "take") || !strings.Contains(out, "get") {
		t.Errorf("the hub's help:\n%s", out)
	}
	// send and take exist on both machines: on the hub they work with its own mailbox
	if !strings.Contains(out, "send") || !strings.Contains(out, "take") {
		t.Errorf("the hub also has send and take:\n%s", out)
	}
	for _, g := range []string{"svc", "job"} {
		if strings.Contains(out, "  "+g+" ") {
			t.Errorf("the hub does not have the %s group:\n%s", g, out)
		}
	}
}

func TestHelpTellsWhenMachineIsNotConfigured(t *testing.T) {
	setRole(t, "")
	_, out, _ := run(t, "help")
	if !strings.Contains(out, "not set up yet") || !strings.Contains(out, "gitsi setup hub") {
		t.Fatalf("it must guide toward setting up the role:\n%s", out)
	}
	setRole(t, config.RoleHub)
	_, out, _ = run(t, "help")
	if strings.Contains(out, "not set up yet") {
		t.Fatal("a configured machine must not show that notice")
	}
}

func TestGroupHelpFiltersByRoleAndShowsShortcut(t *testing.T) {
	setRole(t, config.RoleEdge)
	code, out, _ := run(t, "repo")
	if code != 0 || !strings.Contains(out, "gitsi repo sync") || !strings.Contains(out, "[shortcut: gitsi sync]") {
		t.Fatalf("repo en edge:\n%s", out)
	}
}

func TestVersionAndUnknown(t *testing.T) {
	setRole(t, config.RoleEdge)
	if code, out, _ := run(t, "version"); code != 0 || !strings.Contains(out, "gitsi") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, errb := run(t, "missing"); code != 2 || !strings.Contains(errb, "unknown command") {
		t.Fatalf("unknown: %d %q", code, errb)
	}
	if code, _, errb := run(t, "repo", "missing"); code != 2 || !strings.Contains(errb, "gitsi repo") {
		t.Fatalf("unknown subcommand: %d %q", code, errb)
	}
	// a command of another role is explained, not hidden behind a generic error
	if code, _, errb := run(t, "edges"); code != 2 || !strings.Contains(errb, "not available for this machine's edge role") {
		t.Fatalf("wrong role: %d %q", code, errb)
	}
}

// The dispatcher enforces the policy and the hub precondition before running the command.
func TestAuthorize(t *testing.T) {
	gated := module.Command{Name: "x", Feature: policy.Services}
	hubbed := module.Command{Name: "y", NeedsHub: true}

	cfg := config.Default()
	env := &module.Env{Config: cfg}
	err := authorize(gated, env)
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.FeatureOff || !strings.Contains(fe.Hint, "gitsi policy set services on") {
		t.Fatalf("without the policy it must refuse with a category and a hint: %v", err)
	}
	cfg.Policy.AllowServices = true
	if err := authorize(gated, env); err != nil {
		t.Fatalf("with the policy it must pass: %v", err)
	}
	if err := authorize(hubbed, env); err == nil {
		t.Fatal("an edge without a transport must refuse")
	}
	cfg.Role = config.RoleHub // on the hub no transport is required (e.g. update -publish)
	if err := authorize(hubbed, env); err != nil {
		t.Fatalf("the hub needs no transport: %v", err)
	}
}

// The policy applies through any path: group or shortcut.
func TestPolicyAppliesThroughEveryName(t *testing.T) {
	setRole(t, config.RoleEdge)
	for _, line := range [][]string{{"send", "-text", "x"}, {"job", "start", "echo", "x"}, {"svc", "up"}} {
		code, _, errb := run(t, line...)
		if code != fault.FeatureOff.ExitCode() || !strings.Contains(errb, "is disabled") || !strings.Contains(errb, "gitsi policy set") {
			t.Errorf("%v must be refused by policy: %d %q", line, code, errb)
		}
	}
}

// Every declared sensitive feature must be one the policy knows.
func TestDeclaredFeaturesAreKnown(t *testing.T) {
	known := map[policy.Feature]bool{policy.Send: true, policy.Take: true, policy.Services: true, policy.Exec: true}
	reg := mustRegistry(t)
	check := func(n string, c module.Command) {
		if c.Feature != "" && !known[c.Feature] {
			t.Errorf("%s declares an unknown feature %q", n, c.Feature)
		}
	}
	for n, e := range reg.top {
		check(n, e.cmd)
	}
	for gn, g := range reg.groups {
		for n, e := range g.subs {
			check(gn+" "+n, e.cmd)
		}
	}
}

// Exit codes are part of the CLI contract: 1 = error, and ExitError carries its own without printing.
func TestExitCodes(t *testing.T) {
	var errb bytes.Buffer
	if c := exitCode("x", &module.ExitError{Code: 7}, &errb); c != 7 || errb.Len() != 0 {
		t.Errorf("ExitError: %d %q", c, errb.String())
	}
	if c := exitCode("x", errors.New("boom"), &errb); c != 1 {
		t.Errorf("plain error: %d", c)
	}
	// a wrapped ExitError is recognized too
	if c := exitCode("x", fmt.Errorf("wrapped: %w", &module.ExitError{Code: 9}), &errb); c != 9 {
		t.Errorf("wrapped: %d", c)
	}
}

// A categorized failure prints with its cause and action, and exits with its category's stable code.
func TestExitCodeForFaults(t *testing.T) {
	for kind, code := range map[fault.Kind]int{fault.NotConfigured: 10, fault.Unreachable: 11, fault.Auth: 12, fault.HostKey: 13, fault.HubBinary: 14, fault.FeatureOff: 15, fault.Integrity: 16, fault.Timeout: 17} {
		var errb bytes.Buffer
		err := fmt.Errorf("wrapped: %w", fault.New(kind, "something failed", "do this"))
		if c := exitCode("cmd", err, &errb); c != code {
			t.Errorf("%v: code %d, want %d", kind, c, code)
		}
		if !strings.Contains(errb.String(), "something failed") || !strings.Contains(errb.String(), "-> do this") {
			t.Errorf("%v: it must print cause and action: %q", kind, errb.String())
		}
	}
}

// An edge without a configured hub gets its own category (NotConfigured), not a generic error.
func TestMissingHubIsNotConfigured(t *testing.T) {
	setRole(t, config.RoleEdge)
	code, _, errb := run(t, "repo", "list")
	if code != fault.NotConfigured.ExitCode() || !strings.Contains(errb, "gitsi setup edge") {
		t.Fatalf("%d %q", code, errb)
	}
}

// A likely typo gets a one-line "did you mean" (exit 2) instead of the whole help; with nothing close, the help as before.
func TestTyposGetASuggestion(t *testing.T) {
	setRole(t, config.RoleEdge)
	for args, want := range map[string]string{
		"stauts":   "did you mean gitsi status?",
		"tkae":     "did you mean gitsi take?",
		"reop":     "did you mean gitsi repo?",
		"repo lst": "did you mean gitsi repo list?",
		"job lgos": "did you mean gitsi job logs?",
	} {
		code, out, errb := run(t, strings.Fields(args)...)
		if code != 2 || !strings.Contains(errb, want) || strings.Contains(errb, "Groups:") || out != "" {
			t.Errorf("%s: %d\nstderr: %s", args, code, errb)
		}
	}
	// a hub-only command is not proposed on an edge
	if _, _, errb := run(t, "edgs"); strings.Contains(errb, "gitsi edges") {
		t.Errorf("edges is the hub's: %s", errb)
	}
	code, _, errb := run(t, "xyzzy")
	if code != 2 || strings.Contains(errb, "did you mean") || !strings.Contains(errb, "Groups:") {
		t.Errorf("nothing close: the full help: %d %s", code, errb)
	}
}

// A mistyped option: exit 2 (wrong usage), the cause once, the closest option, and no usage dump.
func TestMistypedOptionIsAUsageErrorWithASuggestion(t *testing.T) {
	setRole(t, config.RoleEdge)
	code, out, errb := run(t, "clean", "-dryrun")
	if code != 2 || out != "" || strings.Count(errb, "not defined: -dryrun") != 1 || !strings.Contains(errb, "did you mean -dry-run?") ||
		strings.Contains(errb, "Usage of") {
		t.Fatalf("%d\nstderr: %s", code, errb)
	}
}

// 'gitsi help <command>' shows the command's help and never runs it (before, it ran the command with "help" as an argument: 'help
// run' ran PowerShell's help on the hub). The help needs no enabled feature and no configured hub, and asking for it exits 0.
func TestHelpForACommandShowsItsOptionsAndNeverRunsIt(t *testing.T) {
	setRole(t, config.RoleEdge) // no hub configured, every optional feature off
	for _, args := range [][]string{{"help", "take"}, {"take", "-h"}, {"help", "run"}, {"run", "--help"}, {"help", "repo", "get"}} {
		code, out, errb := run(t, args...)
		if code != 0 || !strings.Contains(out, "Options:") || strings.Contains(errb, "Enable it now") || strings.Contains(errb, "no hub configured") {
			t.Errorf("%v: %d\nstdout: %s\nstderr: %s", args, code, out, errb)
		}
	}
	if code, out, _ := run(t, "help", "run"); code != 0 || !strings.Contains(out, "-cwd") || !strings.Contains(out, `"exec"`) {
		t.Errorf("run's help names its options and its optional feature: %s", out)
	}
	// nothing ran: 'run' audits every execution
	if b, err := os.ReadFile(filepath.Join(os.Getenv("USERPROFILE"), ".gitsi", "audit.log")); err == nil && strings.Contains(string(b), "run") {
		t.Fatalf("a help request must not run the command: %s", b)
	}
	// a group still lists its commands
	if code, out, _ := run(t, "help", "repo"); code != 0 || !strings.Contains(out, "gitsi repo share") {
		t.Errorf("%d %s", code, out)
	}
	// a command without options shows its summary
	if code, out, _ := run(t, "help", "status"); code != 0 || !strings.Contains(out, "gitsi status:") {
		t.Errorf("%d %s", code, out)
	}
}

// A hand-edited config.json with an invalid hub value never reaches ssh: the command says what is wrong and how to fix it, and
// 'gitsi config set' (which needs no hub) still works to fix it.
func TestInvalidHubConfigNeverReachesSSH(t *testing.T) {
	setRole(t, "")
	home := os.Getenv("USERPROFILE")
	os.MkdirAll(filepath.Join(home, ".gitsi"), 0o700)
	b, _ := json.Marshal(map[string]any{"version": 1, "role": "edge", "hub": map[string]any{"host": "192.0.2.4", "user": "-oProxyCommand"}})
	os.WriteFile(filepath.Join(home, ".gitsi", "config.json"), b, 0o600)

	code, _, errb := run(t, "repo", "list")
	if code != fault.NotConfigured.ExitCode() || !strings.Contains(errb, "hub.user") || !strings.Contains(errb, "gitsi config set") {
		t.Fatalf("%d %q", code, errb)
	}
	if code, _, errb := run(t, "config", "set", "hub.user", "user"); code != 0 {
		t.Fatalf("the fix must be possible: %d %q", code, errb)
	}
	if c, err := config.Load(); err != nil || c.Hub.User != "user" || c.Hub.Validate() != nil {
		t.Fatalf("%+v %v", c, err)
	}
}

// The documentation does not drift from the command registry: every visible group, shortcut and subcommand is in the README.
func TestReadmeDocumentsEveryGroupShortcutAndSubcommand(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Skip("the README is not found (running outside the repo)")
	}
	readme := string(b)
	reg := mustRegistry(t)
	for name, g := range reg.groups {
		if !strings.Contains(readme, "`"+name+"`") {
			t.Errorf("the README does not mention the group `%s`", name)
		}
		for sub, e := range g.subs {
			if e.cmd.Hidden {
				continue
			}
			if !strings.Contains(readme, "`"+sub+"`") && !strings.Contains(readme, name+" "+sub) && !strings.Contains(readme, "gitsi "+sub) {
				t.Errorf("the README does not mention `%s %s`", name, sub)
			}
		}
	}
	for name, e := range reg.top {
		if e.cmd.Hidden || name != e.cmd.Name {
			continue
		}
		if !strings.Contains(readme, "`"+name+"`") && !strings.Contains(readme, "gitsi "+name) {
			t.Errorf("the README does not mention the command `%s`", name)
		}
	}
}

// The policy protects the edge: on the hub, send and take do not require it (they only move files within its own mailbox).
func TestHubSendReceiveDoNotRequirePolicy(t *testing.T) {
	setRole(t, config.RoleHub)
	code, out, errb := run(t, "take", "-list")
	if code != 0 || strings.Contains(errb, "disabled") {
		t.Fatalf("on the hub take requires no policy: %d %s %s", code, out, errb)
	}
	setRole(t, config.RoleEdge)
	if code, _, errb := run(t, "take", "-list"); code != fault.FeatureOff.ExitCode() || !strings.Contains(errb, "disabled") {
		t.Fatalf("on the edge it does: %d %s", code, errb)
	}
}

// A disabled feature can be enabled on the spot at a terminal: it is explained, asked (y/N) and the command goes on.
func TestOfferToEnableAFeatureOnUse(t *testing.T) {
	old := interactive
	defer func() { interactive = old }()
	interactive = func(io.Reader) bool { return true }
	runIn := func(stdin string, args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := Run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
		return code, errb.String()
	}

	setRole(t, config.RoleEdge)
	code, errb := runIn("n\n", "send", "-text", "x")
	if code != fault.FeatureOff.ExitCode() || !strings.Contains(errb, "Enable it now?") {
		t.Fatalf("answering no leaves the feature off: %d %s", code, errb)
	}
	if c, _ := config.Load(); c.Policy.AllowSend {
		t.Fatal("it must not be enabled without a yes")
	}

	code, errb = runIn("y\n", "send", "-text", "x")
	if !strings.Contains(errb, "send: enabled") || code == fault.FeatureOff.ExitCode() {
		t.Fatalf("with a yes it is enabled and goes on: %d %s", code, errb)
	}
	if c, _ := config.Load(); !c.Policy.AllowSend {
		t.Fatal("it must stay saved")
	}

	// without a terminal (script, pipe) it never asks: an error with the hint, as always
	interactive = func(io.Reader) bool { return false }
	setRole(t, config.RoleEdge)
	code, errb = runIn("y\n", "take")
	if code != fault.FeatureOff.ExitCode() || strings.Contains(errb, "Enable it now") || !strings.Contains(errb, "gitsi policy set take on") {
		t.Fatalf("%d %s", code, errb)
	}
}

// The help's summaries are short (half a line); the options' detail comes from 'gitsi <command> -h'.
func TestSummariesAreShort(t *testing.T) {
	for _, m := range Modules() {
		for _, c := range m.Commands() {
			if !c.Hidden && len([]rune(c.Summary)) > 75 {
				t.Errorf("%s: summary of %d characters (at most 75): %q", c.Name, len([]rune(c.Summary)), c.Summary)
			}
		}
	}
}

// No module test may touch the user's real profile (their mailbox, audit.log, config): every internal/modules package with tests
// isolates the profile in its TestMain with testhome.Run.
func TestEveryModuleIsolatesTheUserProfileInTests(t *testing.T) {
	dirs, err := os.ReadDir(filepath.Join("..", "modules"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		tests, _ := filepath.Glob(filepath.Join("..", "modules", d.Name(), "*_test.go"))
		if len(tests) == 0 {
			continue
		}
		found := false
		for _, f := range tests {
			b, _ := os.ReadFile(f)
			if strings.Contains(string(b), "testhome.Run(m)") {
				found = true
			}
		}
		if !found {
			t.Errorf("modules/%s: func TestMain(m *testing.M) { testhome.Run(m) } is missing", d.Name())
		}
	}
}
