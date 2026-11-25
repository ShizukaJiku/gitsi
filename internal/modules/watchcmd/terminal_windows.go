package watchcmd

import (
	"context"
	"os/exec"
	"strings"
	"syscall"

	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// createNewConsole is CREATE_NEW_CONSOLE: the terminal gets a window of its own (watch in the tray has none to share).
const createNewConsole = 0x00000010

// openTerminal opens a terminal in a folder: Windows Terminal when it is installed, PowerShell in a console window otherwise (a
// variable in tests). The folder goes as the working directory, never inside a command line.
var openTerminal = func(dir string) error {
	if wt, err := exec.LookPath("wt.exe"); err == nil && !strings.Contains(dir, ";") { // wt reads ";" as a new command
		return exec.Command(wt, "-d", dir).Start()
	}
	cmd := winps.Command(context.Background(), "-NoLogo", "-NoExit")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole}
	return cmd.Start()
}
