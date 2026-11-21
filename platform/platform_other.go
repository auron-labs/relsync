//go:build !windows

package platform

import "context"

// stub is the Platform of systems with no implementation yet. Its signature
// checks report that nothing was checked, which never blocks; everything else
// fails with ErrNotSupported.
type stub struct{}

func current() Platform { return stub{} }

func (stub) CheckSignature(context.Context, string) (Signature, error) {
	return Signature{State: SignatureNotChecked}, nil
}

func (stub) Scan(context.Context, string) (ScanResult, error) {
	return ScanResult{State: ScanUnavailable}, nil
}

func (stub) Folders() (Folders, error)                              { return Folders{}, ErrNotSupported }
func (stub) CreateShortcut(Shortcut) error                          { return ErrNotSupported }
func (stub) RemoveShortcut(string) error                            { return ErrNotSupported }
func (stub) AddToUserPath(string) (bool, error)                     { return false, ErrNotSupported }
func (stub) ProcessesIn(context.Context, string) ([]Process, error) { return nil, ErrNotSupported }
func (stub) CloseProcesses(context.Context, []Process) error        { return ErrNotSupported }
func (stub) Launch(string) error                                    { return ErrNotSupported }
func (stub) FreeSpace(string) (uint64, error)                       { return 0, ErrNotSupported }
func (stub) RetargetShortcuts(context.Context, string, string) (int, error) {
	return 0, ErrNotSupported
}

func (stub) Run(context.Context, Command) (int, error)              { return 0, ErrNotSupported }
func (stub) Programs(context.Context) ([]Program, error)            { return nil, ErrNotSupported }
func (stub) FileVersion(string) (string, error)                     { return "", ErrNotSupported }
func (stub) VersionInfo(string) (VersionInfo, error)                { return VersionInfo{}, ErrNotSupported }
func (stub) ListShortcuts(context.Context) ([]Shortcut, error)      { return nil, ErrNotSupported }
func (stub) Winget(context.Context, ...string) (string, error)      { return "", ErrNotSupported }
func (stub) InstallPackage(context.Context, string) (string, error) { return "", ErrNotSupported }
func (stub) RemovePackage(context.Context, string) error            { return ErrNotSupported }
