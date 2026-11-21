//go:build windows

package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// signatureScript reads the path from the environment, so no file name needs
// quoting. Output is forced to UTF-8 so a publisher's name survives intact.
const signatureScript = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$s = Get-AuthenticodeSignature -LiteralPath $env:RELEASE_SYNC_SIGN_PATH
$signer = ''
if ($s.SignerCertificate) { $signer = $s.SignerCertificate.GetNameInfo('SimpleName', $false) }
@{ status = [string]$s.Status; signer = $signer } | ConvertTo-Json -Compress`

// shortcutScript writes a shortcut from values in the environment, for the
// same reason.
const shortcutScript = `$link = (New-Object -ComObject WScript.Shell).CreateShortcut($env:RELEASE_SYNC_LINK)
$link.TargetPath = $env:RELEASE_SYNC_TARGET
$link.WorkingDirectory = Split-Path -Parent $env:RELEASE_SYNC_TARGET
$link.Description = $env:RELEASE_SYNC_DESCRIPTION
$link.Save()`

const (
	// createNoWindow keeps a console window from flashing up.
	createNoWindow = 0x08000000
	// detachedProcess starts a program that does not belong to this one's console.
	detachedProcess = 0x00000008

	// politeCloseWait is how long programs get to quit after being asked to.
	politeCloseWait = 10 * time.Second
	// forcedCloseWait is how long to wait for programs that were ended.
	forcedCloseWait = 5 * time.Second
	closePoll       = 250 * time.Millisecond

	environmentKey = "Environment"
	pathValue      = "Path"
	// hwndBroadcast, wmSettingChange and smtoAbortIfHung are the parts of the
	// message that tells running programs the environment changed.
	hwndBroadcast   = 0xffff
	wmSettingChange = 0x001A
	smtoAbortIfHung = 0x0002
	settingTimeout  = 5000
)

type windowsPlatform struct{}

func current() Platform { return windowsPlatform{} }

// powershell runs a script with extra environment variables and returns what
// it printed.
func powershell(ctx context.Context, script string, env ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}

// CheckSignature asks Windows for the file's Authenticode trust state, which
// covers embedded signatures and signature catalogs.
func (windowsPlatform) CheckSignature(ctx context.Context, path string) (Signature, error) {
	out, err := powershell(ctx, signatureScript, "RELEASE_SYNC_SIGN_PATH="+path)
	if err != nil {
		return Signature{}, fmt.Errorf("check signature of %s: %w", path, err)
	}
	var result struct {
		Status string `json:"status"`
		Signer string `json:"signer"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return Signature{}, fmt.Errorf("read signature of %s: %w", path, err)
	}
	return signatureFromStatus(result.Status, result.Signer), nil
}

// Folders asks Windows for the folders by their identity, so a redirected
// Documents or Downloads folder is found where it really is.
func (windowsPlatform) Folders() (Folders, error) {
	startMenu, err := windows.KnownFolderPath(windows.FOLDERID_Programs, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return Folders{}, fmt.Errorf("locate the Start menu: %w", err)
	}
	f := Folders{StartMenu: startMenu}
	for _, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Windows, windows.FOLDERID_ProgramFiles, windows.FOLDERID_ProgramFilesX86} {
		if path, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil {
			f.System = append(f.System, path)
		}
	}
	if local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT); err == nil {
		f.System = append(f.System, filepath.Join(local, "Programs"), filepath.Join(local, "Microsoft"))
	}
	for _, id := range []*windows.KNOWNFOLDERID{
		windows.FOLDERID_Profile, windows.FOLDERID_Desktop, windows.FOLDERID_Documents, windows.FOLDERID_Downloads,
		windows.FOLDERID_ProgramFiles, windows.FOLDERID_ProgramFilesX86, windows.FOLDERID_Windows,
		windows.FOLDERID_LocalAppData, windows.FOLDERID_RoamingAppData, windows.FOLDERID_ProgramData,
	} {
		// A folder this system does not have is one nobody can choose.
		if path, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil {
			f.Protected = append(f.Protected, path)
		}
	}
	return f, nil
}

// listShortcutsScript reads the shortcuts below the folders named in the
// environment, a few at most from each so a folder of thousands is not walked.
const listShortcutsScript = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$shell = New-Object -ComObject WScript.Shell
$found = @(foreach ($dir in $env:RELEASE_SYNC_SHORTCUT_DIRS -split '\|') {
  if (-not $dir -or -not (Test-Path -LiteralPath $dir)) { continue }
  Get-ChildItem -LiteralPath $dir -Filter *.lnk -Recurse -File -ErrorAction SilentlyContinue | Select-Object -First 1500 | ForEach-Object {
    $link = $shell.CreateShortcut($_.FullName)
    [pscustomobject]@{ path = $_.FullName; target = $link.TargetPath; description = $link.Description }
  }
})
ConvertTo-Json -InputObject $found -Compress`

// ListShortcuts lists the shortcuts on the Desktop and in the Start menu, for
// the current user and for everyone.
func (windowsPlatform) ListShortcuts(ctx context.Context) ([]Shortcut, error) {
	var dirs []string
	for _, id := range []*windows.KNOWNFOLDERID{
		windows.FOLDERID_Desktop, windows.FOLDERID_PublicDesktop, windows.FOLDERID_Programs, windows.FOLDERID_CommonPrograms,
	} {
		if path, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil {
			dirs = append(dirs, path)
		}
	}
	out, err := powershell(ctx, listShortcutsScript, "RELEASE_SYNC_SHORTCUT_DIRS="+strings.Join(dirs, "|"))
	if err != nil {
		return nil, fmt.Errorf("list shortcuts: %w", err)
	}
	return parseShortcuts(out)
}

// Winget runs winget and returns its output, which it prints in UTF-8.
func (windowsPlatform) Winget(ctx context.Context, args ...string) (string, error) {
	path, err := exec.LookPath("winget.exe")
	if err != nil {
		return "", fmt.Errorf("find winget: %w", ErrNotSupported)
	}
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- the arguments are package identifiers read from winget's own list.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run winget %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.String(), nil
}

// CreateShortcut writes the shortcut through the Windows scripting host.
func (windowsPlatform) CreateShortcut(s Shortcut) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o750); err != nil {
		return fmt.Errorf("create shortcut folder: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := powershell(ctx, shortcutScript, "RELEASE_SYNC_LINK="+s.Path, "RELEASE_SYNC_TARGET="+s.Target,
		"RELEASE_SYNC_DESCRIPTION="+s.Description); err != nil {
		return fmt.Errorf("create shortcut %s: %w", s.Path, err)
	}
	return nil
}

// RemoveShortcut deletes the shortcut and its folder when that is now empty.
func (windowsPlatform) RemoveShortcut(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove shortcut %s: %w", path, err)
	}
	_ = os.Remove(filepath.Dir(path)) // Fails, harmlessly, while anything else is in it.
	return nil
}

// AddToUserPath edits the user's own PATH in the registry, keeping its type so
// a value that refers to other variables still does, then tells running
// programs the environment changed.
func (windowsPlatform) AddToUserPath(dir string) (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, environmentKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, fmt.Errorf("open the user environment: %w", err)
	}
	defer func() { _ = key.Close() }()
	current, kind, err := key.GetStringValue(pathValue)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, fmt.Errorf("read the user PATH: %w", err)
	}
	updated, changed := pathWithEntry(current, dir)
	if !changed {
		return false, nil
	}
	if kind == registry.SZ {
		err = key.SetStringValue(pathValue, updated)
	} else {
		err = key.SetExpandStringValue(pathValue, updated)
	}
	if err != nil {
		return false, fmt.Errorf("write the user PATH: %w", err)
	}
	broadcastEnvironmentChange()
	return true, nil
}

// broadcastEnvironmentChange makes Explorer and other programs reread the
// environment; failing to is harmless, new programs read it anyway.
func broadcastEnvironmentChange() {
	name, err := windows.UTF16PtrFromString(environmentKey)
	if err != nil {
		return
	}
	var result uintptr
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	_, _, _ = proc.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(name)), smtoAbortIfHung, settingTimeout,
		uintptr(unsafe.Pointer(&result)))
}

// ProcessesIn lists the running programs whose file is below dir. Programs
// this user may not inspect are not the user's to close and are left out.
func (windowsPlatform) ProcessesIn(ctx context.Context, dir string) ([]Process, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("list running programs: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var found []Process
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("list running programs: %w", ctx.Err())
		}
		path, ok := processPath(entry.ProcessID)
		if ok && inside(dir, path) && int(entry.ProcessID) != os.Getpid() {
			found = append(found, Process{ID: int(entry.ProcessID), Path: path})
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("list running programs: %w", err)
	}
	return found, nil
}

// processPath is the file a process runs from, false when it cannot be read.
func processPath(id uint32) (string, bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, id)
	if err != nil {
		return "", false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", false
	}
	return windows.UTF16ToString(buf[:size]), true
}

// CloseProcesses asks each program to close its windows, as clicking the X
// does, and ends the ones still running after a wait.
func (windowsPlatform) CloseProcesses(ctx context.Context, procs []Process) error {
	for _, p := range procs {
		_ = taskkill(ctx, p, false) // Programs with no window refuse; the forced pass handles them.
	}
	remaining := waitForExit(ctx, procs, politeCloseWait)
	for _, p := range remaining {
		_ = taskkill(ctx, p, true)
	}
	remaining = waitForExit(ctx, remaining, forcedCloseWait)
	if len(remaining) > 0 {
		return fmt.Errorf("could not close %s", remaining[0].Path)
	}
	return ctx.Err()
}

func taskkill(ctx context.Context, p Process, force bool) error {
	args := []string{"/PID", strconv.Itoa(p.ID)}
	if force {
		args = append(args, "/F", "/T")
	}
	cmd := exec.CommandContext(ctx, "taskkill.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd.Run()
}

// waitForExit polls until none of procs is running or the wait is over, and
// returns the ones still running.
func waitForExit(ctx context.Context, procs []Process, wait time.Duration) []Process {
	deadline := time.Now().Add(wait)
	for {
		var running []Process
		for _, p := range procs {
			if isRunning(p) {
				running = append(running, p)
			}
		}
		if len(running) == 0 || ctx.Err() != nil || time.Now().After(deadline) {
			return running
		}
		procs = running
		select {
		case <-time.After(closePoll):
		case <-ctx.Done():
		}
	}
}

// isRunning reports whether the process is still there. A program this user
// may not open is assumed to be running.
func isRunning(p Process) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(p.ID))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	event, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && event == uint32(windows.WAIT_TIMEOUT)
}

// Launch starts the program detached from this one, in its own folder, as the
// shell would.
func (windowsPlatform) Launch(path string) error {
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", path, err)
	}
	return cmd.Process.Release()
}
