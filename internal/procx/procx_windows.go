package procx

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: the child is a console program that runs without a console window.
const createNoWindow = 0x08000000

// Flags are the creation flags Background adds (exported for tests).
const Flags = createNoWindow

// detachedProcess and createNewProcessGroup are DETACHED_PROCESS and CREATE_NEW_PROCESS_GROUP.
const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= detachedProcess | createNewProcessGroup
}

func hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
