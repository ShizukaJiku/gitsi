// Package procx starts helper processes (powershell.exe, git, ssh, gitsi itself) without a console window of their own.
//
// A console program started by a process that has no console (a tray process, an editor or agent running gitsi or its tests,
// a job started through WMI) gets a NEW console, and Windows shows it: a window flashes on the user's screen for every
// PowerShell or git call. Background asks Windows not to create that window when the child's input and output are not a console
// anyway (pipes, buffers, files or nothing), which is every case where gitsi captures the output.
package procx

import (
	"os"
	"os/exec"
)

// Background prepares cmd to run without a console window of its own and returns it. Call it after setting Stdin, Stdout and
// Stderr (or before Output/CombinedOutput, which set them to buffers): when any of them is a console, the child must share it
// (live output, prompts) and cmd is left as it is.
func Background(cmd *exec.Cmd) *exec.Cmd {
	for _, s := range []any{cmd.Stdin, cmd.Stdout, cmd.Stderr} {
		if f, ok := s.(*os.File); ok && isConsole(f) {
			return cmd
		}
	}
	hide(cmd)
	return cmd
}

// isConsole reports whether f is a terminal (a character device): a child with no console of its own could not use it.
func isConsole(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// StartTree starts cmd so that stop ends it together with every process it starts: killing a gitsi child alone would leave
// its ssh.exe running (a tunnel nobody can close). On Windows the child goes into a job object that also ends when this process
// does. The child is placed in it right after it starts, before it gets to start processes of its own in practice (a gitsi child
// reads its configuration first). stop is safe to call more than once.
func StartTree(cmd *exec.Cmd) (stop func(), err error) {
	return startTree(cmd)
}

// Detached prepares cmd to keep running on its own after this process and its terminal end (the tray): no console, and its own
// process group, so a Ctrl+C in the terminal that started it does not reach it.
func Detached(cmd *exec.Cmd) *exec.Cmd {
	detach(cmd)
	return cmd
}
