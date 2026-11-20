package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/version"
)

func TestParseGlobalOnlyLeadingFlags(t *testing.T) {
	t.Setenv("GITSI_VERBOSE", "")
	t.Setenv("GITSI_QUIET", "")
	cases := []struct {
		in      []string
		rest    []string
		v, q, j bool
	}{
		{[]string{"-v", "check"}, []string{"check"}, true, false, false},
		{[]string{"--verbose", "--json", "check", "--active"}, []string{"check", "--active"}, true, false, true},
		{[]string{"-q", "send", "-text", "x"}, []string{"send", "-text", "x"}, false, true, false},
		{[]string{"--json", "svc", "ls"}, []string{"svc", "ls"}, false, false, true},
		// after the command they belong to it: they are not interpreted (they never clash with its flags)
		{[]string{"check", "-v"}, []string{"check", "-v"}, false, false, false},
		{[]string{"send", "-q"}, []string{"send", "-q"}, false, false, false},
		{nil, nil, false, false, false},
		{[]string{"-v"}, []string{}, true, false, false},
	}
	for _, c := range cases {
		rest, m, err := parseGlobal(c.in)
		if err != nil || m.verbose != c.v || m.quiet != c.q || m.json != c.j || !(len(rest) == 0 && len(c.rest) == 0 || reflect.DeepEqual(rest, c.rest)) {
			t.Errorf("%v => %v %+v %v", c.in, rest, m, err)
		}
	}
}

func TestParseGlobalEnvVarsAndConflict(t *testing.T) {
	t.Setenv("GITSI_VERBOSE", "1")
	t.Setenv("GITSI_QUIET", "")
	if _, m, err := parseGlobal([]string{"check"}); err != nil || !m.verbose {
		t.Fatalf("GITSI_VERBOSE=1 must turn on -v: %+v %v", m, err)
	}
	t.Setenv("GITSI_VERBOSE", "")
	t.Setenv("GITSI_QUIET", "1")
	if _, m, err := parseGlobal([]string{"check"}); err != nil || !m.quiet {
		t.Fatalf("%+v %v", m, err)
	}
	if _, _, err := parseGlobal([]string{"-v"}); err == nil {
		t.Fatal("-v with GITSI_QUIET=1 is a conflict")
	}
	if code, _, errb := run(t, "-v", "-q", "check"); code != 2 || !strings.Contains(errb, "do not combine") {
		t.Fatalf("-v and -q together: %d %q", code, errb)
	}
}

func setConfig(t *testing.T, m map[string]any) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".gitsi"), 0o700)
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(home, ".gitsi", "config.json"), b, 0o600)
}

// --json: stdout is valid JSON and nothing else (to consume it from another program).
func TestJSONOutputIsPureJSON(t *testing.T) {
	t.Setenv("GITSI_VERBOSE", "")
	t.Setenv("GITSI_QUIET", "")
	setConfig(t, map[string]any{"version": 1, "role": "edge", "services": []map[string]any{{"name": "pg", "remote_port": 5432, "local_port": 15432}}})

	code, out, _ := run(t, "--json", "svc", "ls")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || code != 0 || len(rows) != 1 || rows[0]["name"] != "pg" || rows[0]["local_port"].(float64) != 15432 {
		t.Fatalf("svc ls --json: %d %v\n%s", code, err, out)
	}

	// empty lists are [], not null or text
	setConfig(t, map[string]any{"version": 1, "role": "edge"})
	_, out, _ = run(t, "--json", "svc", "ls")
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("an empty catalog must be []: %q", out)
	}
}

func TestJSONCheckIsPureJSONAndKeepsExitCode(t *testing.T) {
	t.Setenv("GITSI_VERBOSE", "")
	t.Setenv("GITSI_QUIET", "")
	// edge without a configured hub: the diagnosis fails (FAIL) and the exit code reflects it, in JSON
	setConfig(t, map[string]any{"version": 1, "role": "edge"})
	code, out, _ := run(t, "--json", "check")
	var rep struct {
		Results []struct{ Level, Name, Detail string }
		Summary map[string]int
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("the output must be JSON only: %v\n%s", err, out)
	}
	if rep.Summary["fail"] < 1 || code == 0 {
		t.Fatalf("the failure must show in the summary and the exit code: code=%d %+v", code, rep.Summary)
	}
	for _, r := range rep.Results {
		if r.Level != "ok" && r.Level != "info" && r.Level != "warn" && r.Level != "fail" {
			t.Errorf("unexpected level: %q", r.Level)
		}
	}
}

// -v shows what really runs against the hub (here a real ssh that fails) and does NOT clutter stdout.
func TestVerboseTracesRealSSHCalls(t *testing.T) {
	t.Setenv("GITSI_VERBOSE", "")
	t.Setenv("GITSI_QUIET", "")
	setConfig(t, map[string]any{"version": 1, "role": "edge", "repos_dir": "C:/x/repos",
		"hub": map[string]any{"host": "127.0.0.1", "user": "u", "port": 1, "key_file": "C:/does/not/exist"}})
	code, out, errb := run(t, "-v", "repo", "list")
	if code == 0 {
		t.Fatal("with port 1 closed it must fail")
	}
	// the first thing a repos command does is ask the hub for its layout (with a greeting): -v makes it visible
	if !strings.Contains(errb, "[ssh] ssh (channel) u@127.0.0.1") || !strings.Contains(errb, "ConvertFrom-Json") || !strings.Contains(errb, "ms]") {
		t.Fatalf("-v must show the remote ssh command and its duration:\n%s", errb)
	}
	if !strings.Contains(errb, "[v] gitsi") {
		t.Errorf("-v must identify the version and the role:\n%s", errb)
	}
	if strings.Contains(out, "[ssh]") || strings.Contains(out, "[v]") {
		t.Errorf("the detail goes to stderr, not stdout: %q", out)
	}
	// without -v there are no traces
	_, _, errb = run(t, "repo", "list")
	if strings.Contains(errb, "[ssh]") || strings.Contains(errb, "[v]") {
		t.Errorf("without -v there must be no traces:\n%s", errb)
	}
}

func TestVersionShowsBuildInfoWhenSet(t *testing.T) {
	setRole(t, config.RoleEdge)
	_, out, _ := run(t, "version")
	if !strings.HasPrefix(strings.TrimSpace(out), "gitsi "+version.Version) {
		t.Fatalf("%q", out)
	}
}
