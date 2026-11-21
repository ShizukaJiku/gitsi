package layout

import (
	"path/filepath"
	"syscall"
)

// shortPath asks Windows for the 8.3 name (GetShortPathNameW). It returns "" when the path does not exist or the volume has no
// short names: the caller then keeps the long one.
func shortPath(p string) string {
	in, err := syscall.UTF16PtrFromString(filepath.FromSlash(p))
	if err != nil {
		return ""
	}
	n, err := syscall.GetShortPathName(in, nil, 0) // with no buffer it returns the size needed, terminator included
	if err != nil || n == 0 {
		return ""
	}
	buf := make([]uint16, n)
	n, err = syscall.GetShortPathName(in, &buf[0], n)
	if err != nil || n == 0 || int(n) >= len(buf) {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}
