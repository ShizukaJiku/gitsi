package desktop

import (
	"errors"
	"syscall"
	"unsafe"
)

var (
	advapi32             = syscall.NewLazyDLL("advapi32.dll")
	procRegCreateKeyExW  = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW   = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW  = advapi32.NewProc("RegDeleteValueW")
	procRegQueryValueExW = advapi32.NewProc("RegQueryValueExW")
	procRegDeleteKeyW    = advapi32.NewProc("RegDeleteKeyW")
)

const (
	hkeyCurrentUser = 0x80000001
	keyQueryValue   = 0x0001
	keySetValue     = 0x0002
	regSZ           = 1
	errFileNotFound = 2
)

func (a Autostart) open() (syscall.Handle, error) {
	k, err := syscall.UTF16PtrFromString(a.Key)
	if err != nil {
		return 0, err
	}
	var h syscall.Handle
	if r, _, _ := procRegCreateKeyExW.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(k)), 0, 0, 0, keyQueryValue|keySetValue, 0,
		uintptr(unsafe.Pointer(&h)), 0); r != 0 {
		return 0, syscall.Errno(r)
	}
	return h, nil
}

// Set writes the entry: Windows runs command when the user logs on.
func (a Autostart) Set(command string) error {
	h, err := a.open()
	if err != nil {
		return err
	}
	defer syscall.RegCloseKey(h)
	name, _ := syscall.UTF16PtrFromString(a.Name)
	data, err := syscall.UTF16FromString(command)
	if err != nil {
		return err
	}
	if r, _, _ := procRegSetValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(name)), 0, regSZ, uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)*2)); r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// Remove deletes the entry; one that does not exist is not an error.
func (a Autostart) Remove() error {
	h, err := a.open()
	if err != nil {
		return err
	}
	defer syscall.RegCloseKey(h)
	name, _ := syscall.UTF16PtrFromString(a.Name)
	if r, _, _ := procRegDeleteValueW.Call(uintptr(h), uintptr(unsafe.Pointer(name))); r != 0 && r != errFileNotFound {
		return syscall.Errno(r)
	}
	return nil
}

// Get returns the entry's command, and whether it exists.
func (a Autostart) Get() (string, bool, error) {
	h, err := a.open()
	if err != nil {
		return "", false, err
	}
	defer syscall.RegCloseKey(h)
	name, _ := syscall.UTF16PtrFromString(a.Name)
	var typ, size uint32
	r, _, _ := procRegQueryValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&typ)), 0, uintptr(unsafe.Pointer(&size)))
	if r == errFileNotFound {
		return "", false, nil
	}
	if r != 0 {
		return "", false, syscall.Errno(r)
	}
	if typ != regSZ || size < 2 {
		return "", true, errors.New("the logon entry is not a text value")
	}
	buf := make([]uint16, size/2)
	if r, _, _ := procRegQueryValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r != 0 {
		return "", false, syscall.Errno(r)
	}
	return syscall.UTF16ToString(buf), true, nil
}

// deleteKey removes the (empty) key itself: only tests, which use a temporary key, need it.
func (a Autostart) deleteKey() error {
	k, _ := syscall.UTF16PtrFromString(a.Key)
	if r, _, _ := procRegDeleteKeyW.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(k))); r != 0 && r != errFileNotFound {
		return syscall.Errno(r)
	}
	return nil
}
