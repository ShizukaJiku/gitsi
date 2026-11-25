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
