//go:build !windows

package desktop

// Desktop is not available outside Windows.
type Desktop struct{ HotkeyErr error }

// Start reports ErrUnsupported.
func Start(Options) (*Desktop, error) { return nil, ErrUnsupported }

// SetTooltip does nothing outside Windows.
func (*Desktop) SetTooltip(string) {}

// SetMenu does nothing outside Windows.
func (*Desktop) SetMenu([]MenuItem) {}

// Notify does nothing outside Windows.
func (*Desktop) Notify(string, string, bool) {}

// Done is never closed outside Windows.
func (*Desktop) Done() <-chan struct{} { return nil }

// Close does nothing outside Windows.
func (*Desktop) Close() {}

// WriteText reports ErrUnsupported.
func (*Desktop) WriteText(string) error { return ErrUnsupported }

// ReadText reports that there is no direct reading outside Windows.
func ReadText() (string, bool, error) { return "", false, nil }

// Seq returns 0 outside Windows.
func Seq() uint32 { return 0 }

// Private reports false outside Windows.
func Private() bool { return false }

// Lock always succeeds outside Windows (there is no tray instance to protect).
func Lock(string) (func(), error) { return func() {}, nil }

// Set reports ErrUnsupported.
func (Autostart) Set(string) error { return ErrUnsupported }

// Remove reports ErrUnsupported.
func (Autostart) Remove() error { return ErrUnsupported }

// Get reports ErrUnsupported.
func (Autostart) Get() (string, bool, error) { return "", false, ErrUnsupported }
