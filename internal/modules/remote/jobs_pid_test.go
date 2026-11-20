package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// These tests run the hub's scripts with a real powershell.exe and real processes (what the hub does).

func runPS(t *testing.T, script string) string {
	t.Helper()
	out, err := winps.Command(context.Background(), "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

func needWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell scripts")
	}
	if testing.Short() {
		t.Skip("starts real processes")
	}
}

// fakeJob creates the folder of a "running" job with the pid and, when given, the start time.
func fakeJob(t *testing.T, jobs, id string, pid int, start string) {
	t.Helper()
	d := filepath.Join(jobs, id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "pid"), []byte(strconv.Itoa(pid)), 0o644)
	os.WriteFile(filepath.Join(d, "meta"), []byte(time.Now().Format(time.RFC3339)+"|test"), 0o644)
	if start != "" {
		os.WriteFile(filepath.Join(d, "pidstart"), []byte(start), 0o644)
	}
}

func stateOf(t *testing.T, jobs, id string) string {
	t.Helper()
	for _, j := range parseJobs(runPS(t, listScript(filepath.ToSlash(jobs)))) {
		if j.ID == id {
			return j.State
		}
	}
	t.Fatalf("job %s does not show up", id)
	return ""
}

func startTicks(t *testing.T, pid int) string {
	return strings.TrimSpace(runPS(t, "(Get-Process -Id "+strconv.Itoa(pid)+").StartTime.ToUniversalTime().Ticks"))
}

func alive(pid int) bool {
	out, _ := winps.Command(context.Background(), "-NoProfile", "-Command", "[bool](Get-Process -Id "+strconv.Itoa(pid)+" -ErrorAction SilentlyContinue)").Output()
	return strings.TrimSpace(string(out)) == "True"
}

// A PID reused by another process (e.g. after restarting the hub) is not the job: it does not show as running and stop does NOT kill it.
func TestReusedPIDIsNotTheJobAndStopDoesNotKillIt(t *testing.T) {
	needWindows(t)
	other := winps.Command(context.Background(), "-NoProfile", "-Command", "Start-Sleep 60")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer other.Process.Kill()
	pid := other.Process.Pid
	time.Sleep(500 * time.Millisecond)
	jobs := t.TempDir()

	fakeJob(t, jobs, "aaaaaaaa", pid, startTicks(t, pid)) // the real one: same start time
	if s := stateOf(t, jobs, "aaaaaaaa"); s != "running" {
		t.Fatalf("pid and start time match: it must be running, it is %s", s)
	}

	fakeJob(t, jobs, "bbbbbbbb", pid, "638000000000000000") // same pid, another time: another process
	if s := stateOf(t, jobs, "bbbbbbbb"); s != "lost" {
		t.Fatalf("reused pid: it must be lost, it is %s", s)
	}
	out := runPS(t, stopScript(filepath.ToSlash(jobs), "bbbbbbbb"))
	if !strings.Contains(out, "NOTRUNNING") {
		t.Fatalf("stop of a reused pid: %s", out)
	}
	time.Sleep(300 * time.Millisecond)
	if !alive(pid) {
		t.Fatal("stop killed an unrelated process that had the reused pid")
	}
}

// Without a start time (pidstart) there is no way to know whether the PID is the job's: it does not count as alive.
func TestJobWithoutStartTimeIsNotRunning(t *testing.T) {
	needWindows(t)
	jobs := t.TempDir()
	ps := winps.Command(context.Background(), "-NoProfile", "-Command", "Start-Sleep 60")
	if err := ps.Start(); err != nil {
		t.Fatal(err)
	}
	defer ps.Process.Kill()
	time.Sleep(500 * time.Millisecond)
	fakeJob(t, jobs, "cccccccc", ps.Process.Pid, "")
	if s := stateOf(t, jobs, "cccccccc"); s != "lost" {
		t.Fatalf("without pidstart it must be lost, it is %s", s)
	}
}

// End to end with the real script: start saves the start time, the job shows as running and stop stops it (137).
func TestStartRecordsStartTimeAndStopStillWorks(t *testing.T) {
	needWindows(t)
	jobs := filepath.ToSlash(t.TempDir())
	out := runPS(t, startScript(jobs, "eeeeeeee", "", "Start-Sleep 60"))
	if !strings.Contains(out, "STARTED|eeeeeeee|") {
		t.Fatalf("%s", out)
	}
	b, err := os.ReadFile(filepath.Join(jobs, "eeeeeeee", "pidstart"))
	if err != nil || len(bytes.TrimSpace(b)) < 10 {
		t.Fatalf("start must save the start time: %q %v", b, err)
	}
	pidB, _ := os.ReadFile(filepath.Join(jobs, "eeeeeeee", "pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidB)))
	t.Cleanup(func() {
		exec.Command("taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
		// Windows releases the ended process's out.log a moment later: wait before the temporary folder is removed
		log := filepath.Join(jobs, "eeeeeeee", "out.log")
		for i := 0; i < 50; i++ {
			if err := os.Remove(log); err == nil || errors.Is(err, os.ErrNotExist) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	if s := stateOf(t, jobs, "eeeeeeee"); s != "running" {
		t.Fatalf("just started it must be running, it is %s", s)
	}
	if out := runPS(t, stopScript(jobs, "eeeeeeee")); !strings.Contains(out, "STOPPED") {
		t.Fatalf("%s", out)
	}
	time.Sleep(500 * time.Millisecond)
	if alive(pid) {
		t.Fatal("stop must end the job")
	}
	for _, j := range parseJobs(runPS(t, listScript(jobs))) {
		if j.ID == "eeeeeeee" && (j.State != "exited" || j.Exit != "137") {
			t.Fatalf("stopped: it must end up exited 137, it is %s %s", j.State, j.Exit)
		}
	}
}
