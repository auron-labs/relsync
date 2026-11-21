//go:build windows

package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestSignedSystemBinaryIsTrusted(t *testing.T) {
	notepad := filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe")
	got, err := Current().CheckSignature(t.Context(), notepad)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != SignatureTrusted || !strings.Contains(got.Signer, "Microsoft") {
		t.Fatalf("notepad.exe = %+v", got)
	}
}

// The test binary itself is an unsigned executable.
func TestUnsignedExecutableIsReported(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "unsigned fixture.exe")
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Current().CheckSignature(t.Context(), copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != SignatureUnsigned {
		t.Fatalf("unsigned fixture = %+v", got)
	}
}

func TestShortcutIsCreatedAndRemoved(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Release Sync Test")
	link := filepath.Join(dir, "Example.lnk")
	target, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := Current()
	if err := p.CreateShortcut(Shortcut{Path: link, Target: target, Description: "Example"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(link); err != nil {
		t.Fatalf("the shortcut was not written: %v", err)
	}
	if err := p.RemoveShortcut(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the emptied shortcut folder is still there: %v", err)
	}
	if err := p.RemoveShortcut(link); err != nil {
		t.Errorf("removing a shortcut that is gone failed: %v", err)
	}
}

// The test edits the real user PATH and puts back the value it found.
func TestUserPathIsEditedOnceAndKeepsItsType(t *testing.T) {
	key, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = key.Close() }()
	before, kind, err := key.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		t.Fatal(err)
	}
	existed := err == nil
	t.Cleanup(func() {
		if !existed {
			_ = key.DeleteValue("Path")
		} else if kind == registry.SZ {
			_ = key.SetStringValue("Path", before)
		} else {
			_ = key.SetExpandStringValue("Path", before)
		}
	})
	dir := filepath.Join(t.TempDir(), "tools")
	p := Current()
	for i, want := range []bool{true, false} {
		changed, err := p.AddToUserPath(dir)
		if err != nil || changed != want {
			t.Fatalf("AddToUserPath #%d = %v, %v; want %v", i+1, changed, err, want)
		}
	}
	after, afterKind, err := key.GetStringValue("Path")
	if err != nil || !strings.HasSuffix(after, dir) || (kind != 0 && afterKind != kind) {
		t.Errorf("PATH = %q (type %d), %v", after, afterKind, err)
	}
}

func TestRunningProgramsAreFoundAndClosed(t *testing.T) {
	// The system reports the long form of a path; a temporary folder may be named in the short one.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ping := filepath.Join(dir, "ping.exe")
	data, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ping, data, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ping, "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	p := Current()
	found, err := p.ProcessesIn(t.Context(), dir)
	if err != nil || len(found) != 1 || found[0].ID != cmd.Process.Pid {
		t.Fatalf("ProcessesIn = %+v, %v", found, err)
	}
	if err := p.CloseProcesses(t.Context(), found); err != nil {
		t.Fatal(err)
	}
	if left, _ := p.ProcessesIn(t.Context(), dir); len(left) != 0 {
		t.Errorf("still running: %+v", left)
	}
}

func TestRunWaitsForTheProgramAndReportsItsExitCode(t *testing.T) {
	code, err := Current().Run(t.Context(), Command{
		Path: filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), Args: "/c exit 7",
	})
	if err != nil || code != 7 {
		t.Fatalf("Run = %d, %v; want 7", code, err)
	}
}

func TestRunWithoutPromptWaitsForTheProgramAndReportsItsExitCode(t *testing.T) {
	code, err := Current().Run(t.Context(), Command{
		Path: filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), Args: "/c exit 7", NoPrompt: true,
	})
	if err != nil || code != 7 {
		t.Fatalf("Run = %d, %v; want 7", code, err)
	}
}

func TestProgramsListsOnlyEntriesWithANameAndAnUninstallCommand(t *testing.T) {
	programs, err := Current().Programs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range programs {
		if p.Name == "" || p.UninstallCommand == "" && p.QuietUninstallCommand == "" || p.Key == "" {
			t.Errorf("program = %+v", p)
		}
	}
}

func TestFileVersionReadsTheVersionResource(t *testing.T) {
	notepad := filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe")
	got, err := Current().FileVersion(notepad)
	if err != nil || strings.Count(got, ".") != 3 {
		t.Fatalf("FileVersion = %q, %v", got, err)
	}
}

func TestVersionInfoReadsTheStringsOfTheVersionResource(t *testing.T) {
	notepad := filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe")
	got, err := Current().VersionInfo(notepad)
	if err != nil || strings.Count(got.ProductVersion, ".") != 3 || !strings.Contains(got.CompanyName, "Microsoft") {
		t.Fatalf("VersionInfo = %+v, %v", got, err)
	}
}

func TestShortcutsListedIncludeOnesThatExist(t *testing.T) {
	shortcuts, err := Current().ListShortcuts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range shortcuts {
		if !strings.HasSuffix(strings.ToLower(s.Path), ".lnk") {
			t.Errorf("shortcut = %+v", s)
		}
	}
}

// A benign file is clean, or unavailable where Windows Security is not the
// antivirus in charge; it is never a threat.
func TestScanOfABenignFileIsNeverAThreat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "benign.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Current().Scan(t.Context(), path)
	if err != nil || (got.State != ScanClean && got.State != ScanUnavailable) {
		t.Fatalf("Scan = %+v, %v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := Current().Scan(t.Context(), path); err != nil || got.State != ScanThreat || !got.Removed {
		t.Fatalf("a file that is gone: %+v, %v", got, err)
	}
}

func TestFreeSpaceOfTheTemporaryFolder(t *testing.T) {
	free, err := Current().FreeSpace(t.TempDir())
	if err != nil || free == 0 {
		t.Fatalf("FreeSpace = %d, %v", free, err)
	}
}
