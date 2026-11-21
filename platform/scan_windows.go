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
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// scanTimeout is how long a scan may take before it is given up.
const scanTimeout = 3 * time.Minute

// defender is Windows Security's command-line scanner.
var defender = scanner{locate: locateDefender, run: runDefender, timeout: scanTimeout}

// Scan runs Windows Security's scanner over the file with remediation off, so
// the file is left for the caller to delete.
func (windowsPlatform) Scan(ctx context.Context, path string) (ScanResult, error) {
	return defender.scan(ctx, path)
}

// locateDefender finds the newest MpCmdRun.exe: the platform folders under
// ProgramData hold the current one, and Program Files the one that ships with
// Windows.
func locateDefender() (string, bool) {
	if data, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT); err == nil {
		root := filepath.Join(data, "Microsoft", "Windows Defender", "Platform")
		if entries, err := os.ReadDir(root); err == nil {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() {
					names = append(names, e.Name())
				}
			}
			slices.SortFunc(names, func(a, b string) int { return newerScannerDir(b, a) })
			for _, name := range names {
				if program := filepath.Join(root, name, "MpCmdRun.exe"); isFile(program) {
					return program, true
				}
			}
		}
	}
	if files, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT); err == nil {
		if program := filepath.Join(files, "Windows Defender", "MpCmdRun.exe"); isFile(program) {
			return program, true
		}
	}
	return "", false
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// runDefender scans one file (scan type 3 is a custom scan) without remediation.
func runDefender(ctx context.Context, program, path string) (int, string, error) {
	cmd := exec.CommandContext(ctx, program, "-Scan", "-ScanType", "3", "-File", path, "-DisableRemediation") // #nosec G204 -- the program is Windows Security's own and the file is the download.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), out.String(), nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("run the Windows Security scanner: %w", err)
	}
	return 0, out.String(), nil
}

// FreeSpace asks Windows how much the current user can write below dir.
func (windowsPlatform) FreeSpace(dir string) (uint64, error) {
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, fmt.Errorf("measure free space in %s: %w", dir, err)
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(name, &free, nil, nil); err != nil {
		return 0, fmt.Errorf("measure free space in %s: %w", dir, err)
	}
	return free, nil
}

// linksScript lists the shortcuts below the folders named in the environment
// with the paths a move can change.
const linksScript = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$shell = New-Object -ComObject WScript.Shell
$found = @(foreach ($dir in $env:RELEASE_SYNC_SHORTCUT_DIRS -split '\|') {
  if (-not $dir -or -not (Test-Path -LiteralPath $dir)) { continue }
  Get-ChildItem -LiteralPath $dir -Filter *.lnk -Recurse -File -ErrorAction SilentlyContinue | ForEach-Object {
    $link = $shell.CreateShortcut($_.FullName)
    [pscustomobject]@{ path = $_.FullName; target = $link.TargetPath; workdir = $link.WorkingDirectory; icon = $link.IconLocation }
  }
})
ConvertTo-Json -InputObject $found -Compress`

// retargetScript rewrites the paths of the shortcuts in the JSON the
// environment holds, leaving everything else about them as it is.
const retargetScript = `$shell = New-Object -ComObject WScript.Shell
foreach ($item in ($env:RELEASE_SYNC_LINKS | ConvertFrom-Json)) {
  $link = $shell.CreateShortcut($item.path)
  $link.TargetPath = $item.target
  $link.WorkingDirectory = $item.workdir
  $link.IconLocation = $item.icon
  $link.Save()
}`

// shortcutFolders are where the user's shortcuts to a program live: the
// Desktop and Start menu, for the user and for everyone, and the folders the
// taskbar and Start menu keep their pins in.
func shortcutFolders() []string {
	var dirs []string
	for _, id := range []*windows.KNOWNFOLDERID{
		windows.FOLDERID_Desktop, windows.FOLDERID_PublicDesktop, windows.FOLDERID_Programs, windows.FOLDERID_CommonPrograms,
	} {
		if path, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil {
			dirs = append(dirs, path)
		}
	}
	if roaming, err := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData, windows.KF_FLAG_DEFAULT); err == nil {
		dirs = append(dirs, filepath.Join(roaming, "Microsoft", "Internet Explorer", "Quick Launch", "User Pinned"))
	}
	return dirs
}

// RetargetShortcuts reads the shortcuts, works out in Go which lead into from,
// and has them saved with the new paths.
func (windowsPlatform) RetargetShortcuts(ctx context.Context, from, to string) (int, error) {
	out, err := powershell(ctx, linksScript, "RELEASE_SYNC_SHORTCUT_DIRS="+strings.Join(shortcutFolders(), "|"))
	if err != nil {
		return 0, fmt.Errorf("list shortcuts: %w", err)
	}
	changes, err := retargets(out, from, to)
	if err != nil || len(changes) == 0 {
		return 0, err
	}
	payload, err := json.Marshal(changes)
	if err != nil {
		return 0, fmt.Errorf("write the shortcut changes: %w", err)
	}
	if _, err := powershell(ctx, retargetScript, "RELEASE_SYNC_LINKS="+string(payload)); err != nil {
		return 0, fmt.Errorf("repoint shortcuts: %w", err)
	}
	return len(changes), nil
}
