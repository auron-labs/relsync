package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
)

// fastTiming makes the waits after an installer ends short.
var fastTiming = Timing{Interval: time.Millisecond, Appear: 30 * time.Millisecond, Vanish: 30 * time.Millisecond}

// installerFixture is a download, a fake platform whose installer adds an
// entry, and the request that installs it.
type installerFixture struct {
	t        *testing.T
	fake     *platform.Fake
	location string
	req      InstallerRequest
}

func newInstallerFixture(t *testing.T, kind selection.ArtifactKind, name, content string) *installerFixture {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &installerFixture{t: t, fake: &platform.Fake{}, location: filepath.Join(dir, "Program Files", "DeckCraft")}
	write(t, filepath.Join(f.location, "deckcraft.exe"), guiExe("app"))
	f.req = InstallerRequest{
		File: file, Asset: name, Kind: kind, Item: deckCraft, Silent: true, Log: filepath.Join(dir, "deckcraft-install.log"),
		Platform: f.fake, Timing: fastTiming,
	}
	return f
}

func (f *installerFixture) entry(version string) platform.Program {
	return platform.Program{
		Key: "LocalMachine|64|{DECK}", Scope: platform.ScopeMachine, Name: "DeckCraft", Publisher: "Acme", Version: version,
		UninstallCommand: `"` + filepath.Join(f.location, "unins000.exe") + `"`, InstallLocation: f.location,
		DisplayIcon: filepath.Join(f.location, "deckcraft.exe") + ",0",
	}
}

// installs makes the installer add the entry, and end with code.
func (f *installerFixture) installs(version string, code int) {
	f.fake.RunFunc = func(platform.Command) (int, error) {
		f.fake.AddProgram(f.entry(version))
		return code, nil
	}
}

func TestASetupProgramIsRunWithItsFrameworksSwitchesAndItsEntryIsRecorded(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Nullsoft Install System"))
	f.installs("1.0.0", 0)
	f.req.Folder = `D:\Apps\DeckCraft`

	out, err := ApplyInstaller(t.Context(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	cmds := f.fake.Commands()
	if len(cmds) != 1 || cmds[0].Path != f.req.File || cmds[0].Args != `/S /D=D:\Apps\DeckCraft` || cmds[0].Elevated {
		t.Errorf("commands = %+v", cmds)
	}
	rec := out.Record
	if rec.Method != MethodInstaller || rec.Asset != "deckcraft-setup.exe" || rec.UninstallKey != "LocalMachine|64|{DECK}" ||
		rec.Scope != platform.ScopeMachine || rec.EntryVersion != "1.0.0" || rec.Folder != f.location || rec.MainExe != "deckcraft.exe" {
		t.Errorf("record = %+v", rec)
	}
	if out.Warning != nil || out.RestartNeeded {
		t.Errorf("outcome = %+v", out)
	}
	if !rec.Removable() {
		t.Error("a listed program should be removable")
	}
}

func TestASetupProgramOfNoKnownFrameworkShowsItsOwnInterface(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("custom"))
	f.installs("1.0.0", 0)
	if _, err := ApplyInstaller(t.Context(), f.req); err != nil {
		t.Fatal(err)
	}
	if cmds := f.fake.Commands(); cmds[0].Args != "" {
		t.Errorf("arguments = %q", cmds[0].Args)
	}
}

func TestTheItemsOwnArgumentsReplaceTheSilentSwitches(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Inno Setup"))
	f.installs("1.0.0", 0)
	f.req.Args = "/SILENT /LOADINF=x.inf"
	if _, err := ApplyInstaller(t.Context(), f.req); err != nil {
		t.Fatal(err)
	}
	if cmds := f.fake.Commands(); cmds[0].Args != "/SILENT /LOADINF=x.inf" {
		t.Errorf("arguments = %q", cmds[0].Args)
	}
}

func TestSetupExitCodes(t *testing.T) {
	for _, tc := range []struct {
		code    int
		restart bool
		err     error
	}{
		{0, false, nil}, {3010, true, nil}, {1641, true, nil}, {1602, false, ErrCancelled},
	} {
		f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Nullsoft"))
		f.installs("1.0.0", tc.code)
		out, err := ApplyInstaller(t.Context(), f.req)
		if !errors.Is(err, tc.err) || out.RestartNeeded != tc.restart {
			t.Errorf("code %d: restart %v, error %v", tc.code, out.RestartNeeded, err)
		}
	}
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Nullsoft"))
	f.installs("1.0.0", 5)
	var failed *SetupError
	if _, err := ApplyInstaller(t.Context(), f.req); !errors.As(err, &failed) || failed.Code != 5 {
		t.Errorf("error = %v", err)
	}
}

func TestDecliningTheAdministratorPromptIsACancellationNotAFailure(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Nullsoft"))
	f.fake.RunFunc = func(platform.Command) (int, error) { return 0, platform.ErrElevationDeclined }
	_, err := ApplyInstaller(t.Context(), f.req)
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("error = %v", err)
	}
	if programs, _ := f.fake.Programs(t.Context()); len(programs) != 0 {
		t.Errorf("programs = %+v", programs)
	}
}

func TestAProgramTheSystemDoesNotListIsRecordedWithoutAnEntry(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Nullsoft"))
	out, err := ApplyInstaller(t.Context(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Record.Method != MethodInstaller || out.Record.UninstallKey != "" || out.Record.Removable() || out.Record.MainExe != "" {
		t.Errorf("record = %+v", out.Record)
	}
}

func TestAnEntryThatAppearsAfterTheInstallerEndsIsFound(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Nullsoft"))
	f.timing(Timing{Interval: time.Millisecond, Appear: 2 * time.Second})
	f.fake.RunFunc = func(platform.Command) (int, error) {
		// The installer hands off to another process, which finishes later.
		go func() {
			time.Sleep(20 * time.Millisecond)
			f.fake.AddProgram(f.entry("1.0.0"))
		}()
		return 0, nil
	}
	out, err := ApplyInstaller(t.Context(), f.req)
	if err != nil || out.Record.UninstallKey == "" {
		t.Errorf("record = %+v, %v", out.Record, err)
	}
}

func (f *installerFixture) timing(t Timing) { f.req.Timing = t }

func TestAnUpdateKeepsTheEntryWhoseVersionDidNotChange(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Inno Setup"))
	f.fake.AddProgram(f.entry("1.0.0"))
	f.req.Previous = &Record{Method: MethodInstaller, UninstallKey: "LocalMachine|64|{DECK}", Scope: platform.ScopeMachine, Folder: f.location}
	f.req.Folder = f.location
	out, err := ApplyInstaller(t.Context(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Record.UninstallKey != "LocalMachine|64|{DECK}" || out.Record.EntryVersion != "1.0.0" {
		t.Errorf("record = %+v", out.Record)
	}
	if args := f.fake.Commands()[0].Args; !strings.Contains(args, `/DIR="`+f.location+`"`) {
		t.Errorf("the recorded location should be passed: %q", args)
	}
}

func TestAnUpdateRefreshesTheRecordFromTheChangedEntry(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Nullsoft"))
	f.fake.AddProgram(f.entry("1.0.0"))
	f.req.Previous = &Record{Method: MethodInstaller, UninstallKey: "LocalMachine|64|{DECK}"}
	f.installs("1.1.0", 0)
	out, err := ApplyInstaller(t.Context(), f.req)
	if err != nil || out.Record.EntryVersion != "1.1.0" {
		t.Errorf("record = %+v, %v", out.Record, err)
	}
}

func TestAnMSIIsInstalledSilentlyElevatedAndItsLogIsDeletedOnSuccess(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactMSI, "deckcraft.msi", "msi")
	f.req.Everyone = true
	f.fake.RunFunc = func(platform.Command) (int, error) {
		write(t, f.req.Log, "log")
		f.fake.AddProgram(f.entry("1.0.0"))
		return 0, nil
	}
	out, err := ApplyInstaller(t.Context(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := f.fake.Commands()[0]
	want := `/i "` + f.req.File + `" /qn /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown /L*V "` + f.req.Log + `" ALLUSERS=1`
	if cmd.Path != "msiexec.exe" || cmd.Args != want || !cmd.Elevated {
		t.Errorf("command = %+v", cmd)
	}
	if _, err := os.Stat(f.req.Log); !os.IsNotExist(err) {
		t.Errorf("the log of a successful install was kept: %v", err)
	}
	if out.Record.UninstallKey == "" {
		t.Errorf("record = %+v", out.Record)
	}
}

func TestAnUpdateOfAnMSIMatchesTheScopeOfTheInstalledCopy(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactMSI, "deckcraft.msi", "msi")
	f.req.Everyone = false
	f.req.Previous = &Record{Method: MethodInstaller, Scope: platform.ScopeMachine}
	if _, err := ApplyInstaller(t.Context(), f.req); err != nil {
		t.Fatal(err)
	}
	if args := f.fake.Commands()[0].Args; !strings.HasSuffix(args, "ALLUSERS=1") {
		t.Errorf("arguments = %q", args)
	}
}

func TestAFailedMSIIsExplainedFromItsLogAndTheLogIsKept(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactMSI, "deckcraft.msi", "msi")
	f.fake.RunFunc = func(platform.Command) (int, error) {
		write(t, f.req.Log, "Action start\nError 1925. You do not have sufficient privileges\n")
		return 1603, nil
	}
	_, err := ApplyInstaller(t.Context(), f.req)
	var failed *MSIError
	if !errors.As(err, &failed) || failed.Code != 1603 || failed.Diagnosis.Cause != CauseAdministrator || failed.Log != f.req.Log {
		t.Fatalf("error = %#v", err)
	}
	if _, statErr := os.Stat(f.req.Log); statErr != nil {
		t.Errorf("the log of a failed install was not kept: %v", statErr)
	}
}

func TestAFailedMSIWithNoLogStillReportsItsCode(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactMSI, "deckcraft.msi", "msi")
	f.fake.RunFunc = func(platform.Command) (int, error) { return 1603, nil }
	_, err := ApplyInstaller(t.Context(), f.req)
	var failed *MSIError
	if !errors.As(err, &failed) || failed.Code != 1603 || failed.Diagnosis != (MSIDiagnosis{}) || failed.Log != "" {
		t.Errorf("error = %#v", err)
	}
}

func TestAnMSIThatNeedsItsLicenceAcceptedAsksAndIsNeverAnsweredForTheUser(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactMSI, "deckcraft.msi", "msi")
	f.fake.RunFunc = func(platform.Command) (int, error) {
		write(t, f.req.Log, "Product: DeckCraft -- Please accept the license agreement by passing ACCEPTLICENSE=YES during installation.\n")
		return 1603, nil
	}
	_, err := ApplyInstaller(t.Context(), f.req)
	var asked *LicenseRequiredError
	if !errors.As(err, &asked) || asked.Setting != "ACCEPTLICENSE=YES" {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(f.req.Log); !os.IsNotExist(statErr) {
		t.Errorf("the log of a licence question was kept: %v", statErr)
	}
	if args := f.fake.Commands()[0].Args; strings.Contains(args, "ACCEPTLICENSE") {
		t.Errorf("the licence was accepted for the user: %q", args)
	}

	// With the setting already given, the failure is the installer's own.
	f.req.Args = "ACCEPTLICENSE=YES"
	_, err = ApplyInstaller(t.Context(), f.req)
	var failed *MSIError
	if !errors.As(err, &failed) {
		t.Errorf("an accepted licence asked again: %v", err)
	}
}

func TestAnMSIThatIsCancelledIsNotAFailure(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactMSI, "deckcraft.msi", "msi")
	f.fake.RunFunc = func(platform.Command) (int, error) { return 1602, nil }
	if _, err := ApplyInstaller(t.Context(), f.req); !errors.Is(err, ErrCancelled) {
		t.Errorf("error = %v", err)
	}
}

func TestAnMSIXPackageIsInstalledAndRecordedByItsName(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactMSIX, "deckcraft.msix", "msix")
	f.fake.PackageName = "Acme.DeckCraft_1.0.0.0_x64__abc"
	out, err := ApplyInstaller(t.Context(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Record.Package != f.fake.PackageName || out.Record.Scope != platform.ScopeUser || !out.Record.Removable() {
		t.Errorf("record = %+v", out.Record)
	}
	if got := f.fake.PackageFiles(); len(got) != 1 || got[0] != f.req.File {
		t.Errorf("packages installed = %v", got)
	}
	if len(f.fake.Commands()) != 0 {
		t.Errorf("an MSIX needs no setup command: %+v", f.fake.Commands())
	}

	f.fake.PackageErr = errors.New("0x80073CF3")
	if _, err := ApplyInstaller(t.Context(), f.req); err == nil || !strings.Contains(err.Error(), "0x80073CF3") {
		t.Errorf("error = %v", err)
	}
}

func TestAFileThatIsNotAnInstallerCannotBeApplied(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactArchive, "deckcraft.zip", "zip")
	if _, err := ApplyInstaller(t.Context(), f.req); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("error = %v", err)
	}
}

func TestNothingIsRunWhenTheInstalledProgramsCannotBeListed(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Nullsoft"))
	f.fake.ProgramsErr = errors.New("registry unavailable")
	if _, err := ApplyInstaller(t.Context(), f.req); err == nil {
		t.Error("no error")
	}
	if len(f.fake.Commands()) != 0 {
		t.Errorf("the installer was run without a before list: %+v", f.fake.Commands())
	}
}

// uninstallFixture is an installed program whose uninstaller removes its entry.
func (f *installerFixture) uninstaller(removes bool) RemoveRequest {
	f.fake.AddProgram(f.entry("1.0.0"))
	f.fake.RunFunc = func(platform.Command) (int, error) {
		if removes {
			f.fake.RemoveProgram("LocalMachine|64|{DECK}")
		}
		return 0, nil
	}
	return RemoveRequest{
		Record: Record{Method: MethodInstaller, UninstallKey: "LocalMachine|64|{DECK}"}, Silent: true,
		Platform: f.fake, Timing: fastTiming,
	}
}

func TestUninstallingRunsTheProgramsOwnUninstallerUntilItsEntryIsGone(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "x.exe", guiExe(""))
	req := f.uninstaller(true)
	if err := RemoveInstalled(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	cmds := f.fake.Commands()
	if len(cmds) != 1 || cmds[0].Path != filepath.Join(f.location, "unins000.exe") {
		t.Errorf("commands = %+v", cmds)
	}
}

func TestAnUninstallerThatLeavesTheEntryIsReported(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "x.exe", guiExe(""))
	if err := RemoveInstalled(t.Context(), f.uninstaller(false)); !errors.Is(err, ErrStillListed) {
		t.Errorf("error = %v", err)
	}
}

func TestAnEntryThatIsAlreadyGoneIsAlreadyUninstalled(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "x.exe", guiExe(""))
	req := f.uninstaller(true)
	f.fake.RemoveProgram("LocalMachine|64|{DECK}")
	if err := RemoveInstalled(t.Context(), req); err != nil || len(f.fake.Commands()) != 0 {
		t.Errorf("error %v, commands %+v", err, f.fake.Commands())
	}
}

func TestAnUninstallerWhoseAdministratorPromptIsDeclinedIsCancelled(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "x.exe", guiExe(""))
	req := f.uninstaller(true)
	f.fake.RunFunc = func(platform.Command) (int, error) { return 0, platform.ErrElevationDeclined }
	if err := RemoveInstalled(t.Context(), req); !errors.Is(err, ErrCancelled) {
		t.Errorf("error = %v", err)
	}
}

func TestAnMSIXPackageIsRemovedByItsName(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactMSIX, "x.msix", "x")
	f.fake.PackageName = "Acme.DeckCraft_1.0.0.0_x64__abc"
	out, err := ApplyInstaller(t.Context(), f.req)
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveInstalled(t.Context(), RemoveRequest{Record: out.Record, Platform: f.fake}); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.Packages(); len(got) != 0 {
		t.Errorf("packages left = %v", got)
	}
}

func TestAnUnlistedProgramCannotBeUninstalledFromHere(t *testing.T) {
	f := newInstallerFixture(t, selection.ArtifactSetupExe, "x.exe", guiExe(""))
	err := RemoveInstalled(t.Context(), RemoveRequest{Record: Record{Method: MethodInstaller}, Platform: f.fake})
	if err == nil {
		t.Error("no error")
	}
}

func TestWaitingForProgramsStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := waitFor(ctx, &platform.Fake{}, time.Minute, time.Hour, func([]platform.Program) bool { return false })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v", err)
	}
}

func TestAnUnattendedInstallerIsQuietAndNeverRaisesThePrompt(t *testing.T) {
	for _, c := range []struct {
		name    string
		kind    selection.ArtifactKind
		file    string
		content string
		silent  bool
		wantArg string
	}{
		{"an MSI, which a quiet install would elevate", selection.ArtifactMSI, "deckcraft.msi", "msi", false, "/qn"},
		{"an MSI with the silent setting off", selection.ArtifactMSI, "deckcraft.msi", "msi", true, "/qn"},
		{"a setup program with the silent setting off", selection.ArtifactSetupExe, "deckcraft-setup.exe", guiExe("Nullsoft Install System"), false, "/S"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newInstallerFixture(t, c.kind, c.file, c.content)
			f.req.Silent, f.req.Unattended = c.silent, true
			f.req.Previous = &Record{Method: MethodInstaller, Scope: platform.ScopeUser}
			f.installs("1.0.0", 0)
			if _, err := ApplyInstaller(t.Context(), f.req); err != nil {
				t.Fatal(err)
			}
			cmd := f.fake.Commands()[0]
			if cmd.Elevated || !cmd.NoPrompt || !strings.Contains(cmd.Args, c.wantArg) {
				t.Errorf("command = %+v, want quiet, not elevated, and without the prompt", cmd)
			}
		})
	}
}
