//go:build windows

package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
)

// fixtureScript is a minimal per-user NSIS installer that lists itself with
// the system, the way real ones do, and removes itself again.
const fixtureScript = `Unicode true
Name "Release Sync Fixture"
OutFile "fixture-setup.exe"
InstallDir "$LOCALAPPDATA\ReleaseSyncFixture"
RequestExecutionLevel user
SilentInstall silent
SilentUnInstall silent

!define UNINSTALL "Software\Microsoft\Windows\CurrentVersion\Uninstall\ReleaseSyncFixture"

Section
  SetOutPath $INSTDIR
  File /oname=fixture.exe "$%SystemRoot%\System32\notepad.exe"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKCU "${UNINSTALL}" "DisplayName" "Release Sync Fixture"
  WriteRegStr HKCU "${UNINSTALL}" "DisplayVersion" "${FIXTURE_VERSION}"
  WriteRegStr HKCU "${UNINSTALL}" "Publisher" "Release Sync Tests"
  WriteRegStr HKCU "${UNINSTALL}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL}" "DisplayIcon" "$INSTDIR\fixture.exe"
  WriteRegStr HKCU "${UNINSTALL}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINSTALL}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
SectionEnd

Section Uninstall
  Delete "$INSTDIR\fixture.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKCU "${UNINSTALL}"
SectionEnd
`

// makensis finds the NSIS compiler, or skips the test where it is not installed.
func makensis(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("makensis"); err == nil {
		return path
	}
	for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		if path := filepath.Join(root, "NSIS", "makensis.exe"); root != "" && fileExists(path) {
			return path
		}
	}
	t.Skip("makensis is not installed")
	return ""
}

// buildFixture compiles the installer of a version of the fixture.
func buildFixture(t *testing.T, compiler, version string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fixture.nsi")
	if err := os.WriteFile(script, []byte(fixtureScript), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), compiler, "/DFIXTURE_VERSION="+version, "/V1", script) // #nosec G204 -- The compiler is found on this machine.
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("makensis: %v\n%s", err, out)
	}
	return filepath.Join(dir, "fixture-setup.exe")
}

// The whole of an installer's life on a real system: installed quietly into a
// chosen folder, its entry bound and its program found, updated in place, and
// uninstalled until its entry is gone.
func TestANSISInstallerIsInstalledUpdatedAndUninstalledThroughTheSystem(t *testing.T) {
	compiler := makensis(t)
	p := platform.Current()
	folder := filepath.Join(t.TempDir(), "Fixture")
	identity := Identity{Owner: "tests", Repo: "release-sync-fixture", Title: "Release Sync Fixture"}
	request := func(version string, previous *Record) InstallerRequest {
		return InstallerRequest{
			File: buildFixture(t, compiler, version), Asset: "fixture-setup.exe", Kind: selection.ArtifactSetupExe, Item: identity,
			Silent: true, Folder: folder, Previous: previous, Platform: p,
		}
	}
	t.Cleanup(func() {
		programs, _ := p.Programs(t.Context())
		for _, entry := range programs {
			if entry.Name == identity.Title {
				_ = RemoveInstalled(t.Context(), RemoveRequest{Record: Record{Method: MethodInstaller, UninstallKey: entry.Key}, Silent: true, Platform: p})
			}
		}
	})

	first, err := ApplyInstaller(t.Context(), request("1.0.0", nil))
	if err != nil {
		t.Fatal(err)
	}
	rec := first.Record
	if rec.UninstallKey == "" || rec.Scope != platform.ScopeUser || rec.EntryVersion != "1.0.0" || rec.MainExe != "fixture.exe" ||
		!strings.EqualFold(rec.Folder, folder) {
		t.Fatalf("record = %+v", rec)
	}

	second, err := ApplyInstaller(t.Context(), request("1.1.0", &rec))
	if err != nil {
		t.Fatal(err)
	}
	if second.Record.UninstallKey != rec.UninstallKey || second.Record.EntryVersion != "1.1.0" {
		t.Errorf("updated record = %+v", second.Record)
	}

	if err := RemoveInstalled(t.Context(), RemoveRequest{Record: second.Record, Silent: true, Platform: p}); err != nil {
		t.Fatal(err)
	}
	programs, err := p.Programs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, still := Entry(programs, rec.UninstallKey); still {
		t.Error("the entry is still listed after uninstalling")
	}
}
