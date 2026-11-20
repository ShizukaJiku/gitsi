//go:build windows

package provision

import "syscall"

// IsAdmin reports whether the process runs with administrator (elevated) rights.
func IsAdmin() bool {
	r, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("IsUserAnAdmin").Call()
	return r != 0
}
