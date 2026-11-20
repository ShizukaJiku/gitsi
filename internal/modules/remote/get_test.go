package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

func TestGetPathMustStayInsideTheWorkingClone(t *testing.T) {
	for _, ok := range []string{"TestResults", "bin/Release/app.zip", `out\report.xml`, "./a/../b"} {
		if err := checkGetPath(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"..", "../out", `..\out`, "a/../../out", "C:/Windows", `C:\x`, "/etc"} {
		if err := checkGetPath(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

// The hub's script, really in PowerShell: with the path present it calls the hub's gitsi (here, a .cmd that answers like it);
// without it, it says NOPATH.
func TestGetScriptRealPowerShell(t *testing.T) {
	needWindows(t)
	dir := filepath.ToSlash(t.TempDir())
	bin := dir + "/gitsi.cmd"
	os.WriteFile(filepath.FromSlash(bin), []byte("@echo Left for the edge: %2 (file, 1 B)  id 0123456789abcdef\r\n"), 0o755)
	os.WriteFile(filepath.FromSlash(dir+"/report.xml"), []byte("x"), 0o644)
	if out := runPS(t, getScript(bin, dir+"/report.xml")); !strings.Contains(out, "id 0123456789abcdef") || !strings.Contains(out, "report.xml") {
		t.Fatalf("%s", out)
	}
	if out := runPS(t, getScript(bin, dir+"/missing")); !strings.Contains(out, "NOPATH") {
		t.Fatalf("%s", out)
	}
}

// getHub prepares the real clone (localHub), simulates the run with an exit code and answers -get's script.
type getHub struct {
	jobsHub
	code   int
	getRan string
}

func (h *getHub) Run(ctx context.Context, script string) (transport.Result, error) {
	if strings.Contains(script, "' send $p") {
		h.getRan = script
		return transport.Result{Stdout: "Left for the edge: TestResults (dir, 2 KiB)  id 0123456789abcdef\r\n"}, nil
	}
	return h.jobsHub.Run(ctx, script)
}
func (h *getHub) Exec(_ context.Context, command string, _, _ io.Writer) (int, error) {
	h.exec = command
	return h.code, nil
}

func TestRunRepoGetBringsTheResultEvenIfTheCommandFails(t *testing.T) {
	needWindows(t)
	hub := &getHub{code: 3}
	e, _, work := repoRig(t, hub)
	e.Config.Policy.AllowTake = true
	var gotID, gotDir string
	take := func(_ context.Context, _ *module.Env, id, dir string) (string, error) {
		gotID, gotDir = id, dir
		return filepath.Join(dir, "TestResults"), nil
	}
	err := runWith(context.Background(), e, []string{"-repo", "-get", "TestResults", "dotnet", "test"}, take)
	var xe *module.ExitError
	if !errors.As(err, &xe) || xe.Code != 3 {
		t.Fatalf("the command's exit code is returned: %v", err)
	}
	if !strings.Contains(hub.getRan, "'"+work+"/TestResults'") || gotID != "0123456789abcdef" || !strings.HasSuffix(gotDir, filepath.Join("Downloads", "gitsi")) {
		t.Fatalf("it leaves the clone's path in the outbox and brings it to Downloads: %q %q %q", hub.getRan, gotID, gotDir)
	}
}

func TestRunGetChecks(t *testing.T) {
	take := func(context.Context, *module.Env, string, string) (string, error) { return "", nil }
	e, _ := env(t, &getHub{})
	if err := runWith(context.Background(), e, []string{"-get", "x", "make"}, take); err == nil || !strings.Contains(err.Error(), "goes with -repo") {
		t.Fatalf("%v", err)
	}
	if err := runWith(context.Background(), e, []string{"-repo", "-get", "../x", "make"}, take); err == nil || !strings.Contains(err.Error(), "relative path") {
		t.Fatalf("%v", err)
	}
	hub := &getHub{}
	e, _, _ = repoRig(t, hub)
	e.Config.Policy.AllowTake = false
	if err := runWith(context.Background(), e, []string{"-repo", "-get", "TestResults", "make"}, take); err == nil || !strings.Contains(err.Error(), "take") || hub.exec != "" {
		t.Fatalf("without take nothing runs: %v (exec=%q)", err, hub.exec)
	}
}
