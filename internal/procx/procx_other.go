//go:build !windows

package procx

import "os/exec"

// Flags are the creation flags Background adds (none outside Windows: there are no console windows to hide).
const Flags = 0

func hide(*exec.Cmd) {}
