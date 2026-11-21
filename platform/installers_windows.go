//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// seeMaskNoCloseProcess keeps the process handle, to wait on it; noAsync
	// makes the call finish starting the program before it returns; noUI keeps
	// the shell from showing its own error boxes, as the caller reports them.
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskNoUI           = 0x00000400
	swShowNormal          = 1

	// processPoll is how often a running installer is checked for having ended.
	processPoll = 250 // Milliseconds.

	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall`
)

// shellExecuteInfo is SHELLEXECUTEINFOW, which x/sys/windows does not declare.
type shellExecuteInfo struct {
	Size          uint32
	Mask          uint32
	Hwnd          windows.Handle
	Verb          *uint16
	File          *uint16
	Parameters    *uint16
	Directory     *uint16
	Show          int32
	InstApp       windows.Handle
	IDList        uintptr
	Class         *uint16
	ClassKey      windows.Handle
	HotKey        uint32
	IconOrMonitor windows.Handle
	Process       windows.Handle
}

var shellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// Run starts the command through the shell, which is what raises the prompt for
// administrator rights, both for a command that asks for them and for a program
// whose manifest does, and waits for it to end. A program started directly
// would fail outright when its manifest asks for them.
func (windowsPlatform) Run(ctx context.Context, cmd Command) (int, error) {
	if cmd.NoPrompt {
		return runWithoutPrompt(ctx, cmd)
	}
	file, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return 0, fmt.Errorf("run %s: %w", cmd.Path, err)
	}
	args, err := windows.UTF16PtrFromString(cmd.Args)
	if err != nil {
		return 0, fmt.Errorf("run %s: %w", cmd.Path, err)
	}
	info := shellExecuteInfo{
		Mask: seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskNoUI, File: file, Parameters: args, Show: swShowNormal,
	}
	info.Size = uint32(unsafe.Sizeof(info))
	if cmd.Elevated {
		if info.Verb, err = windows.UTF16PtrFromString("runas"); err != nil {
			return 0, fmt.Errorf("run %s: %w", cmd.Path, err)
		}
	}
	if ok, _, callErr := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, ErrElevationDeclined
		}
		return 0, fmt.Errorf("run %s: %w", cmd.Path, callErr)
	}
	if info.Process == 0 {
		return 0, fmt.Errorf("run %s: the system did not start a process to wait for", cmd.Path)
	}
	defer func() { _ = windows.CloseHandle(info.Process) }()
	return waitForProcess(ctx, info.Process, cmd.Path)
}

// runWithoutPrompt starts the program directly, with the compatibility setting
// that makes a program whose manifest asks for administrator rights run with
// the caller's instead of raising the prompt. As with the shell, a canceled ctx
// stops the wait and leaves the program running.
func runWithoutPrompt(ctx context.Context, cmd Command) (int, error) {
	c := exec.Command(cmd.Path) // #nosec G204 -- the command is an installer the application downloaded and verified.
	// Args are passed exactly as written: some installers read them unquoted.
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + cmd.Path + `" ` + cmd.Args}
	c.Env = append(os.Environ(), "__COMPAT_LAYER=RunAsInvoker")
	if err := c.Start(); err != nil {
		return 0, fmt.Errorf("run %s: %w", cmd.Path, err)
	}
	ended := make(chan error, 1)
	go func() { ended <- c.Wait() }()
	select {
	case err := <-ended:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		if err != nil {
			return 0, fmt.Errorf("wait for %s: %w", cmd.Path, err)
		}
		return 0, nil
	case <-ctx.Done():
		return 0, fmt.Errorf("wait for %s: %w", cmd.Path, ctx.Err())
	}
}

// waitForProcess waits for the process to end, giving up when ctx ends; the
// process is left running.
func waitForProcess(ctx context.Context, process windows.Handle, name string) (int, error) {
	for {
		event, err := windows.WaitForSingleObject(process, processPoll)
		if err != nil {
			return 0, fmt.Errorf("wait for %s: %w", name, err)
		}
		if event == windows.WAIT_OBJECT_0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("wait for %s: %w", name, err)
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil {
		return 0, fmt.Errorf("read the exit code of %s: %w", name, err)
	}
	return int(code), nil
}

// Programs reads the uninstall entries of the machine, in the 64-bit and
// 32-bit views, and of the current user.
func (windowsPlatform) Programs(ctx context.Context) ([]Program, error) {
	roots := []struct {
		root  registry.Key
		view  uint32
		name  string
		scope Scope
	}{
		{registry.LOCAL_MACHINE, registry.WOW64_64KEY, "LocalMachine|64", ScopeMachine},
		{registry.LOCAL_MACHINE, registry.WOW64_32KEY, "LocalMachine|32", ScopeMachine},
		{registry.CURRENT_USER, registry.WOW64_64KEY, "CurrentUser|64", ScopeUser},
	}
	var found []Program
	for _, r := range roots {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("list installed programs: %w", err)
		}
		list, err := readPrograms(r.root, r.view, r.name, r.scope)
		if err != nil {
			return nil, err
		}
		found = append(found, list...)
	}
	return found, nil
}

// readPrograms lists the entries of one registry view. A view with no
// uninstall key has no programs. An entry that cannot be read is not shown by
// Windows either.
func readPrograms(root registry.Key, view uint32, name string, scope Scope) ([]Program, error) {
	parent, err := registry.OpenKey(root, uninstallKey, registry.ENUMERATE_SUB_KEYS|view)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open the list of installed programs: %w", err)
	}
	defer func() { _ = parent.Close() }()
	subs, err := parent.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("read the list of installed programs: %w", err)
	}
	var found []Program
	for _, sub := range subs {
		entry, err := registry.OpenKey(root, uninstallKey+`\`+sub, registry.QUERY_VALUE|view)
		if err != nil {
			continue
		}
		if p, ok := programFrom(name+"|"+sub, scope, readValues(entry)); ok {
			found = append(found, p)
		}
		_ = entry.Close()
	}
	return found, nil
}

// readValues reads the values programFrom needs, expanding the variables some
// installers leave in them. A value that is missing or of another type is empty.
func readValues(k registry.Key) uninstallValues {
	text := func(name string) string {
		value, _, err := k.GetStringValue(name)
		if err != nil {
			return ""
		}
		if expanded, err := registry.ExpandString(value); err == nil {
			return expanded
		}
		return value
	}
	system, _, _ := k.GetIntegerValue("SystemComponent")
	return uninstallValues{
		DisplayName: text("DisplayName"), Publisher: text("Publisher"), DisplayVersion: text("DisplayVersion"),
		UninstallString: text("UninstallString"), QuietUninstallString: text("QuietUninstallString"),
		InstallLocation: text("InstallLocation"), DisplayIcon: text("DisplayIcon"), ParentKeyName: text("ParentKeyName"),
		ReleaseType: text("ReleaseType"), SystemComponent: system == 1,
		URLInfoAbout: text("URLInfoAbout"), URLUpdateInfo: text("URLUpdateInfo"), HelpLink: text("HelpLink"), Comments: text("Comments"),
	}
}

// versionBlock is a program's version resource, read whole.
type versionBlock []byte

// readVersionBlock reads the version resource of a file. A file with none
// returns a nil block and no error.
func readVersionBlock(path string) (versionBlock, error) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil {
		if errors.Is(err, windows.ERROR_RESOURCE_DATA_NOT_FOUND) || errors.Is(err, windows.ERROR_RESOURCE_TYPE_NOT_FOUND) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the version of %s: %w", path, err)
	}
	block := make(versionBlock, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&block[0])); err != nil {
		return nil, fmt.Errorf("read the version of %s: %w", path, err)
	}
	return block, nil
}

// productVersion is the fixed product version as four numbers, empty when the
// resource has no fixed version information.
func (b versionBlock) productVersion() string {
	var fixed *windows.VS_FIXEDFILEINFO
	var length uint32
	if len(b) == 0 || windows.VerQueryValue(unsafe.Pointer(&b[0]), `\`, unsafe.Pointer(&fixed), &length) != nil || fixed == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", fixed.ProductVersionMS>>16, fixed.ProductVersionMS&0xffff,
		fixed.ProductVersionLS>>16, fixed.ProductVersionLS&0xffff)
}

// text reads the string named name in the resource's first language, empty
// when there is none.
func (b versionBlock) text(name string) string {
	if len(b) == 0 {
		return ""
	}
	var translations *[2]uint16
	var length uint32
	if windows.VerQueryValue(unsafe.Pointer(&b[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&translations), &length) != nil ||
		translations == nil || length < 4 {
		return ""
	}
	var value *uint16
	sub := fmt.Sprintf(`\StringFileInfo\%04x%04x\%s`, translations[0], translations[1], name)
	if windows.VerQueryValue(unsafe.Pointer(&b[0]), sub, unsafe.Pointer(&value), &length) != nil || value == nil || length == 0 {
		return ""
	}
	return strings.TrimSpace(windows.UTF16ToString(unsafe.Slice(value, length)))
}

// FileVersion reads the fixed product version of a program's version
// resource, as four numbers.
func (windowsPlatform) FileVersion(path string) (string, error) {
	block, err := readVersionBlock(path)
	return block.productVersion(), err
}

// VersionInfo reads the version resource of a program: its product version and
// the strings that describe it. Nothing is run.
func (windowsPlatform) VersionInfo(path string) (VersionInfo, error) {
	block, err := readVersionBlock(path)
	if err != nil || block == nil {
		return VersionInfo{}, err
	}
	return VersionInfo{
		ProductVersion: block.productVersion(), Comments: block.text("Comments"), LegalCopyright: block.text("LegalCopyright"),
		LegalTrademarks: block.text("LegalTrademarks"), ProductName: block.text("ProductName"), CompanyName: block.text("CompanyName"),
	}, nil
}

// packageScript installs the package and prints the full name of the newest
// installed package of its identity, which the package's own manifest names;
// a bundle names it in its bundle manifest. The path comes from the
// environment, so no file name needs quoting.
const packageScript = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression.FileSystem
$path = $env:RELEASE_SYNC_PACKAGE
$zip = [System.IO.Compression.ZipFile]::OpenRead($path)
try {
  $entry = $zip.Entries | Where-Object { $_.FullName -in 'AppxManifest.xml', 'AppxMetadata/AppxBundleManifest.xml' } | Select-Object -First 1
  $reader = New-Object System.IO.StreamReader($entry.Open())
  $xml = [xml]$reader.ReadToEnd()
} finally { $zip.Dispose() }
$name = $xml.DocumentElement.Identity.Name
Add-AppxPackage -Path $path
(Get-AppxPackage -Name $name | Sort-Object { [version]$_.Version } -Descending | Select-Object -First 1).PackageFullName`

// InstallPackage installs the package for the current user through the
// system's package installer.
func (windowsPlatform) InstallPackage(ctx context.Context, path string) (string, error) {
	out, err := powershell(ctx, packageScript, "RELEASE_SYNC_PACKAGE="+path)
	if err != nil {
		return "", fmt.Errorf("install package %s: %w", path, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RemovePackage removes the package by its full name.
func (windowsPlatform) RemovePackage(ctx context.Context, fullName string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := powershell(ctx, `$ErrorActionPreference = 'Stop'
Remove-AppxPackage -Package $env:RELEASE_SYNC_PACKAGE_FULL_NAME`, "RELEASE_SYNC_PACKAGE_FULL_NAME="+fullName); err != nil {
		return fmt.Errorf("remove package %s: %w", fullName, err)
	}
	return nil
}
