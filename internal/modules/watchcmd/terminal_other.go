//go:build !windows

package watchcmd

import "errors"

// openTerminal is not available outside Windows (the tray needs Windows).
var openTerminal = func(string) error { return errors.New("opening a terminal needs Windows") }
