package procx

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func flags(cmd *exec.Cmd) uint32 {
	if cmd.SysProcAttr == nil {
		return 0
	}
	return creationFlags(cmd)
}

// A captured child (buffers, nothing as input) gets no window; one that shares the console (live output) keeps it.
func TestBackgroundHidesOnlyWhenNoConsoleIsShared(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("console windows exist only on Windows")
	}
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
	if runtime.GOOS != "windows" {
		t.Skip("console windows exist only on Windows")
	}
	c := exec.Command("cmd.exe")
	setFlags(c, 0x00000200) // CREATE_NEW_PROCESS_GROUP
	Background(c)
	if flags(c) != 0x00000200|Flags {
		t.Fatalf("flags = %#x", flags(c))
	}
}
