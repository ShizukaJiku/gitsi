package procx

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func flags(cmd *exec.Cmd) uint32 {
	if cmd.SysProcAttr == nil {
		return 0
	}
	return cmd.SysProcAttr.CreationFlags
}

// A captured child (buffers, nothing as input) gets no window; one that shares the console (live output) keeps it.
func TestBackgroundHidesOnlyWhenNoConsoleIsShared(t *testing.T) {
	var out bytes.Buffer
	c := exec.Command("cmd.exe", "/c", "echo", "hi")
	c.Stdout = &out
	Background(c)
	if flags(c)&Flags == 0 || !c.SysProcAttr.HideWindow {
		t.Fatal("a captured child must start without a console window")
	}
	if err := c.Run(); err != nil || !strings.Contains(out.String(), "hi") {
		t.Fatalf("the hidden child must still run and its output be captured: %v %q", err, out.String())
	}

	con, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		t.Skip("this process has no console to share")
	}
	defer con.Close()
	c = exec.Command("cmd.exe", "/c", "echo", "hi")
	c.Stdout = con
	Background(c)
	if flags(c)&Flags != 0 {
		t.Fatal("a child that writes to the console must share it (no CREATE_NO_WINDOW)")
	}
}

// Flags the caller already set are kept.
func TestBackgroundKeepsExistingAttributes(t *testing.T) {
	c := exec.Command("cmd.exe")
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
	Background(c)
	if flags(c) != 0x00000200|Flags {
		t.Fatalf("flags = %#x", flags(c))
	}
}

// Detached keeps the child out of this console and its Ctrl+C.
func TestDetached(t *testing.T) {
	c := Detached(exec.Command("cmd.exe"))
	if flags(c) != detachedProcess|createNewProcessGroup || !c.SysProcAttr.HideWindow {
		t.Fatalf("flags = %#x", flags(c))
	}
}

func TestJobStructLayouts(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("sizes checked for 64-bit Windows")
	}
	if n := unsafe.Sizeof(jobLimits{}); n != 144 {
		t.Fatalf("JOBOBJECT_EXTENDED_LIMIT_INFORMATION is 144 bytes on 64-bit Windows, got %d", n)
	}
	if n := unsafe.Sizeof(jobAccounting{}); n != 48 {
		t.Fatalf("JOBOBJECT_BASIC_ACCOUNTING_INFORMATION is 48 bytes, got %d", n)
	}
}

// Stopping a tree ends the child and the process it started (cmd.exe and its ping.exe, like gitsi and its ssh.exe).
func TestStartTreeStopsTheGrandchildren(t *testing.T) {
	c := Background(exec.Command("cmd.exe", "/c", "ping -n 60 127.0.0.1 >nul"))
	tr, err := newTree(c)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for tr.active() < 2 {
		if time.Now().After(deadline) {
			tr.stop()
			t.Fatalf("the grandchild must be in the job: %d process(es)", tr.active())
		}
		time.Sleep(20 * time.Millisecond)
	}
	tr.stop()
	tr.stop() // twice is harmless
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the child must end when the tree stops")
	}
}
