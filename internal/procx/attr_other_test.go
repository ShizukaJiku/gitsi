//go:build !windows

package procx

import "os/exec"

func creationFlags(*exec.Cmd) uint32 { return 0 }

func setFlags(*exec.Cmd, uint32) {}
