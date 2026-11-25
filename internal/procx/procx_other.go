//go:build !windows

package procx

import (
	"os/exec"
	"sync"
)

// Flags are the creation flags Background adds (none outside Windows: there are no console windows to hide).
const Flags = 0

func hide(*exec.Cmd) {}

func detach(*exec.Cmd) {}

func startTree(cmd *exec.Cmd) (func(), error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = cmd.Process.Kill() }) }, nil
}
