package procx

import (
	"os/exec"
	"syscall"
)

func creationFlags(cmd *exec.Cmd) uint32 { return cmd.SysProcAttr.CreationFlags }

func setFlags(cmd *exec.Cmd, f uint32) { cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: f} }
