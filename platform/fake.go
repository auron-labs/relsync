package platform

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
)

// Fake is a Platform for tests. It answers every signature check with Result,
// or Err when set, and keeps shortcuts, PATH entries and running programs in
// memory, touching nothing of the machine it runs on.
type Fake struct {
	Result Signature
	Err    error
	// Scanned is what Scan answers, ScanClean when unset; ScanErr makes it fail.
	Scanned ScanResult
	ScanErr error
	// SpaceFunc answers FreeSpace; unset, there is always room.
	SpaceFunc func(dir string) (uint64, error)
	// Known answers Folders.
	Known Folders
	// Running are the programs that are running; closing them removes them.
	Running []Process
	// ShortcutErr, PathErr and CloseErr make that operation fail when set.
	ShortcutErr, PathErr, CloseErr error
	// RunFunc is what running a command does, such as an installer adding an
	// entry with AddProgram; unset, a command ends with exit code 0 and no effect.
	RunFunc func(Command) (exitCode int, err error)
	// ProgramsErr makes listing the installed programs fail when set.
	ProgramsErr error
	// Versions maps a program's path to its product version.
	Versions map[string]string
	// Info maps a program's path to its version information; ProductVersion
	// is taken from Versions when Info leaves it empty.
	Info map[string]VersionInfo
	// WingetOutputs maps winget's arguments, joined with spaces, to what it
	// prints; WingetErr makes every call fail, and a call with no output set
	// fails too.
	WingetOutputs map[string]string
	WingetErr     error
	// PackageName is the full name InstallPackage reports; PackageErr makes
	// installing or removing a package fail.
	PackageName string
	PackageErr  error

	mu        sync.Mutex
	checked   []string
	scanned   []string
	shortcuts map[string]Shortcut
	path      []string
	closed    []Process
	launched  []string
	commands  []Command
	programs  []Program
	packages  []string
	installed []string
}

var _ Platform = (*Fake)(nil)

// CheckSignature records path and returns the configured answer.
func (f *Fake) CheckSignature(_ context.Context, path string) (Signature, error) {
	f.mu.Lock()
	f.checked = append(f.checked, path)
	f.mu.Unlock()
	return f.Result, f.Err
}

// Scan records path and returns the configured answer.
func (f *Fake) Scan(_ context.Context, path string) (ScanResult, error) {
	f.mu.Lock()
	f.scanned = append(f.scanned, path)
	f.mu.Unlock()
	if f.ScanErr != nil {
		return ScanResult{}, f.ScanErr
	}
	if f.Scanned.State == "" {
		return ScanResult{State: ScanClean}, nil
	}
	return f.Scanned, nil
}

// ScannedPaths lists the paths that were scanned, in order.
func (f *Fake) ScannedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.scanned...)
}

// FreeSpace asks SpaceFunc.
func (f *Fake) FreeSpace(dir string) (uint64, error) {
	if f.SpaceFunc == nil {
		return math.MaxUint64, nil
	}
	return f.SpaceFunc(dir)
}

// RetargetShortcuts moves the target of every shortcut that leads into from.
func (f *Fake) RetargetShortcuts(_ context.Context, from, to string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ShortcutErr != nil {
		return 0, f.ShortcutErr
	}
	changed := 0
	for path, s := range f.shortcuts {
		if target, ok := Rebase(s.Target, from, to); ok {
			s.Target = target
			f.shortcuts[path] = s
			changed++
		}
	}
	return changed, nil
}

// Checked lists the paths asked about, in order.
func (f *Fake) Checked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.checked...)
}

// Folders returns Known.
func (f *Fake) Folders() (Folders, error) { return f.Known, nil }

// CreateShortcut remembers the shortcut under its path.
func (f *Fake) CreateShortcut(s Shortcut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ShortcutErr != nil {
		return f.ShortcutErr
	}
	if f.shortcuts == nil {
		f.shortcuts = map[string]Shortcut{}
	}
	f.shortcuts[s.Path] = s
	return nil
}

// RemoveShortcut forgets the shortcut.
func (f *Fake) RemoveShortcut(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ShortcutErr != nil {
		return f.ShortcutErr
	}
	delete(f.shortcuts, path)
	return nil
}

// Shortcuts lists the shortcuts that exist, by path.
func (f *Fake) Shortcuts() map[string]Shortcut {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]Shortcut, len(f.shortcuts))
	for path, s := range f.shortcuts {
		out[path] = s
	}
	return out
}

// AddToUserPath remembers dir, reporting whether it was new.
func (f *Fake) AddToUserPath(dir string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PathErr != nil {
		return false, f.PathErr
	}
	if slices.Contains(f.path, dir) {
		return false, nil
	}
	f.path = append(f.path, dir)
	return true, nil
}

// UserPath lists the folders added to the PATH, in order.
func (f *Fake) UserPath() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.path...)
}

// ProcessesIn lists the running programs inside dir.
func (f *Fake) ProcessesIn(_ context.Context, dir string) ([]Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []Process
	for _, p := range f.Running {
		if inside(dir, p.Path) {
			found = append(found, p)
		}
	}
	return found, nil
}

// CloseProcesses stops the programs: they are no longer running.
func (f *Fake) CloseProcesses(_ context.Context, procs []Process) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CloseErr != nil {
		return f.CloseErr
	}
	f.closed = append(f.closed, procs...)
	f.Running = slices.DeleteFunc(f.Running, func(p Process) bool { return slices.Contains(procs, p) })
	return nil
}

// Closed lists the programs that were closed, in order.
func (f *Fake) Closed() []Process {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Process(nil), f.closed...)
}

// Launch remembers the program that was started.
func (f *Fake) Launch(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launched = append(f.launched, path)
	return nil
}

// Launched lists the programs that were started, in order.
func (f *Fake) Launched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.launched...)
}

// Run records the command and does what RunFunc says.
func (f *Fake) Run(_ context.Context, cmd Command) (int, error) {
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	run := f.RunFunc
	f.mu.Unlock()
	if run == nil {
		return 0, nil
	}
	return run(cmd)
}

// Commands lists the commands that were run, in order.
func (f *Fake) Commands() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Command(nil), f.commands...)
}

// Programs lists the entries added with AddProgram.
func (f *Fake) Programs(context.Context) ([]Program, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ProgramsErr != nil {
		return nil, f.ProgramsErr
	}
	return append([]Program(nil), f.programs...), nil
}

// AddProgram adds an entry to the installed programs, or replaces the one with
// the same key.
func (f *Fake) AddProgram(p Program) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := slices.IndexFunc(f.programs, func(q Program) bool { return q.Key == p.Key }); i >= 0 {
		f.programs[i] = p
		return
	}
	f.programs = append(f.programs, p)
}

// RemoveProgram removes the entry with the key.
func (f *Fake) RemoveProgram(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.programs = slices.DeleteFunc(f.programs, func(p Program) bool { return p.Key == key })
}

// FileVersion returns the version set for path, empty for none.
func (f *Fake) FileVersion(path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Versions[path], nil
}

// VersionInfo returns the information set for path with Info, or the
// version set with Versions alone.
func (f *Fake) VersionInfo(path string) (VersionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info := f.Info[path]
	if info.ProductVersion == "" {
		info.ProductVersion = f.Versions[path]
	}
	return info, nil
}

// ListShortcuts lists the shortcuts that exist, in the order of their paths.
func (f *Fake) ListShortcuts(context.Context) ([]Shortcut, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]Shortcut, 0, len(f.shortcuts))
	for _, s := range f.shortcuts {
		list = append(list, s)
	}
	slices.SortFunc(list, func(a, b Shortcut) int { return strings.Compare(a.Path, b.Path) })
	return list, nil
}

// Winget answers with the output set for the arguments.
func (f *Fake) Winget(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.WingetErr != nil {
		return "", f.WingetErr
	}
	key := strings.Join(args, " ")
	out, ok := f.WingetOutputs[key]
	if !ok {
		return "", fmt.Errorf("fake platform: no winget output for %q", key)
	}
	return out, nil
}

// InstallPackage records the package and reports PackageName.
func (f *Fake) InstallPackage(_ context.Context, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PackageErr != nil {
		return "", f.PackageErr
	}
	f.installed = append(f.installed, path)
	f.packages = append(f.packages, f.PackageName)
	return f.PackageName, nil
}

// RemovePackage forgets the package.
func (f *Fake) RemovePackage(_ context.Context, fullName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PackageErr != nil {
		return f.PackageErr
	}
	f.packages = slices.DeleteFunc(f.packages, func(name string) bool { return name == fullName })
	return nil
}

// Packages lists the full names of the packages that are installed.
func (f *Fake) Packages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.packages...)
}

// PackageFiles lists the package files that were installed, in order.
func (f *Fake) PackageFiles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.installed...)
}
