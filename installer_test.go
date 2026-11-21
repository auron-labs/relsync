package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

const (
	nsisSetup = "Nullsoft Install System v3.08"
	appKey    = "LocalMachine|64|{APP}"
)

// setupApp is a program the fake platform's installer puts on the machine:
// running its setup program adds an entry and the program's files; running its
// uninstaller takes both away.
type setupApp struct {
	f        *installFixture
	location string
	version  string
	// listed says the installer adds its entry to the list of installed programs.
	listed bool
	// code is what the setup program ends with.
	code int
	// scope is whom the installed entry says it was installed for.
	scope platform.Scope
}

func (f *installFixture) setupApp() *setupApp {
	a := &setupApp{f: f, location: filepath.Join(filepath.Dir(f.apps), "Program Files", "app"), version: "1.0.0", listed: true, scope: platform.ScopeMachine}
	f.fake.RunFunc = a.run
	return a
}

func (a *setupApp) entry() platform.Program {
	return platform.Program{
		Key: appKey, Scope: a.scope, Name: "app", Publisher: "acme", Version: a.version,
		UninstallCommand: filepath.Join(a.location, "unins000.exe"), InstallLocation: a.location,
		DisplayIcon: filepath.Join(a.location, "app.exe"),
	}
}

func (a *setupApp) run(cmd platform.Command) (int, error) {
	t := a.f.t
	if strings.HasSuffix(cmd.Path, "unins000.exe") || strings.Contains(cmd.Args, "/x ") {
		a.f.fake.RemoveProgram(appKey)
		if err := os.RemoveAll(a.location); err != nil {
			t.Error(err)
			return 1, nil
		}
		return 0, nil
	}
	if a.code != 0 && a.code != 3010 {
		return a.code, nil
	}
	write(t, filepath.Join(a.location, "app.exe"), string(gui(a.version)))
	if a.listed {
		a.f.fake.AddProgram(a.entry())
	}
	return a.code, nil
}

// setupRelease is a release whose one file is a setup program of the given toolkit.
func (f *installFixture) setupRelease(tag string, days int, name, toolkit string) source.Release {
	return f.releaseOf(tag, days, name, gui(toolkit))
}

func (f *installFixture) trackApp(release ...source.Release) catalog.Entry {
	f.t.Helper()
	f.source.publish("app", "An app", release...)
	return f.track("app", catalog.AddOptions{Kind: selection.ItemApp})
}

func (f *installFixture) activityMessages() []string {
	entries := f.screen.activity.Entries().Get()
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Message
	}
	return out
}

func TestASetupProgramIsInstalledQuietlyAndTheAppCanBeLaunchedAndUninstalled(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.show(entry)
	requireText(t, f.tt, "Install", "Setup program built for x64.")
	requireNoText(t, f.tt, "can't be installed from here")

	click(t, f.tt, "Install")
	f.finish()

	cmds := f.fake.Commands()
	if len(cmds) != 1 || !strings.HasSuffix(cmds[0].Path, "app-1.0.0-x64-setup.exe") || cmds[0].Args != "/S" {
		t.Fatalf("commands = %+v", cmds)
	}
	in, ok := f.entry(entry).Installed()
	if !ok || in.Method != install.MethodInstaller || in.Tag != "v1.0.0" || in.UninstallKey != appKey || in.Scope != platform.ScopeMachine ||
		in.Folder != app.location || in.MainExe != "app.exe" || in.EntryVersion != "1.0.0" {
		t.Fatalf("install = %+v, %v", in, ok)
	}
	if last := f.lastActivity(); last.Level != activity.Success || last.Message != "Installed acme/app v1.0.0" {
		t.Errorf("activity = %+v", last)
	}
	requireText(t, f.tt, "app installed: v1.0.0", "Launch", "See file location", "Uninstall from this PC", "Installed in", app.location)
	requireNoText(t, f.tt, "Windows doesn't list this program")

	click(t, f.tt, "Launch")
	f.finish()
	if got := f.fake.Launched(); !slices.Equal(got, []string{filepath.Join(app.location, "app.exe")}) {
		t.Errorf("launched = %v", got)
	}

	click(t, f.tt, "Uninstall from this PC")
	requireText(t, f.tt, "Uninstall app from this PC?", "app is removed with its own uninstaller. Windows may ask for permission.")
	click(t, f.tt, "Uninstall")
	f.finish()

	if programs, _ := f.fake.Programs(t.Context()); len(programs) != 0 {
		t.Errorf("programs left = %+v", programs)
	}
	if got := f.entry(entry); got.Install != nil {
		t.Errorf("install = %+v", got.Install)
	}
	if last := f.lastActivity(); last.Level != activity.Success || last.Message != "Uninstalled acme/app" {
		t.Errorf("activity = %+v", last)
	}
	requireText(t, f.tt, "app uninstalled")
}

func TestAnUninstallerThatLeavesTheProgramListedIsReportedAndTheInstallKept(t *testing.T) {
	f := newInstallFixture(t)
	f.setupApp()
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.install(entry)
	f.fake.RunFunc = func(platform.Command) (int, error) { return 0, nil } // The uninstaller is cancelled.
	f.show(entry)

	click(t, f.tt, "Uninstall from this PC")
	click(t, f.tt, "Uninstall")
	f.finish()

	if _, ok := f.entry(entry).Installed(); !ok {
		t.Error("the install was forgotten though the program is still there")
	}
	const reason = "The uninstaller finished, but the program still appears installed. It may have been cancelled."
	requireText(t, f.tt, reason)
	if last := f.lastActivity(); last.Level != activity.Error || !strings.Contains(last.Message, reason) {
		t.Errorf("activity = %+v", last)
	}
}

func TestAProgramWithNoKnownSetupToolkitAndNoSetupInItsNameIsAStandaloneProgram(t *testing.T) {
	f := newInstallFixture(t)
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-x64.exe", "just a program"))
	f.install(entry)

	if len(f.fake.Commands()) != 0 {
		t.Errorf("commands = %+v: a standalone program is not run", f.fake.Commands())
	}
	in, _ := f.entry(entry).Installed()
	if in.Method != install.MethodPortable || in.MainExe != "app-x64.exe" {
		t.Errorf("install = %+v", in)
	}
}

func TestTheInstallMethodOverridesTheInspectionOfTheFile(t *testing.T) {
	f := newInstallFixture(t)
	f.setupApp()
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-x64.exe", "custom installer"))
	if err := f.screen.catalog.SetMethod(t.Context(), entry.Item.ID, selection.MethodInstaller); err != nil {
		t.Fatal(err)
	}
	f.install(entry)

	cmds := f.fake.Commands()
	if len(cmds) != 1 || cmds[0].Args != "" {
		t.Fatalf("commands = %+v: an installer of no known toolkit shows its own interface", cmds)
	}
	if in, _ := f.entry(entry).Installed(); in.Method != install.MethodInstaller {
		t.Errorf("install = %+v", in)
	}
}

func TestAnItemsInstallerArgumentsAndFolderShapeTheCommand(t *testing.T) {
	f := newInstallFixture(t)
	f.setupApp()
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", "Inno Setup"))
	folder := filepath.Join(filepath.Dir(f.apps), "mine")
	ctx := t.Context()
	if err := f.screen.catalog.SetInstallerArgs(ctx, entry.Item.ID, "/SILENT /TASKS=desktop"); err != nil {
		t.Fatal(err)
	}
	if err := f.screen.catalog.SetInstallFolder(ctx, entry.Item.ID, folder); err != nil {
		t.Fatal(err)
	}
	f.install(entry)

	if got := f.fake.Commands()[0].Args; got != `/SILENT /TASKS=desktop /DIR="`+folder+`"` {
		t.Errorf("arguments = %q", got)
	}
}

func TestSilentInstallsCanBeTurnedOff(t *testing.T) {
	f := newInstallFixture(t)
	f.setupApp()
	f.screen.prefs.Update(func(p *preferences) { p.SilentInstalls = false })
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.install(entry)
	if got := f.fake.Commands()[0].Args; got != "" {
		t.Errorf("arguments = %q", got)
	}
}

func TestAnUpdateRunsTheNewSetupOverTheInstalledCopyWhereItIsAfterClosingTheApp(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.install(entry)

	app.version = "2.0.0"
	f.source.publish("app", "", f.setupRelease("v2.0.0", 5, "app-2.0.0-x64-setup.exe", nsisSetup+" 2"),
		f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	updated := f.refresh(entry.Item.ID)
	f.fake.Running = []platform.Process{{ID: 7, Path: filepath.Join(app.location, "app.exe")}}
	if !f.screen.startInstall(updated.Item.ID, "v2.0.0", nil) {
		t.Fatal("the update did not start")
	}
	f.answer("app is running", "Close it and update")
	f.finish()

	cmds := f.fake.Commands()
	if len(cmds) != 2 || cmds[1].Args != "/S /D="+app.location {
		t.Errorf("commands = %+v: the update should land where the app already is", cmds)
	}
	if got := f.fake.Closed(); len(got) != 1 || got[0].ID != 7 {
		t.Errorf("closed = %+v", got)
	}
	if got := f.fake.Launched(); !slices.Equal(got, []string{filepath.Join(app.location, "app.exe")}) {
		t.Errorf("the app was not opened again: %v", got)
	}
	in, _ := f.entry(entry).Installed()
	if in.Tag != "v2.0.0" || in.EntryVersion != "2.0.0" || in.UninstallKey != appKey {
		t.Errorf("install = %+v", in)
	}
	if last := f.lastActivity(); last.Message != "Updated acme/app to v2.0.0" {
		t.Errorf("activity = %+v", last)
	}
}

func TestAnUpdateDoesNotPassAFolderThatIsGone(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.install(entry)
	if err := os.RemoveAll(app.location); err != nil {
		t.Fatal(err)
	}
	app.version = "1.0.1"
	f.source.publish("app", "", f.setupRelease("v1.0.1", 5, "app-1.0.1-x64-setup.exe", nsisSetup+" 2"),
		f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.refresh(entry.Item.ID)
	if !f.screen.startInstall(entry.Item.ID, "v1.0.1", nil) {
		t.Fatal("the update did not start")
	}
	f.finish()
	if got := f.fake.Commands()[1].Args; got != "/S" {
		t.Errorf("arguments = %q", got)
	}
}

func TestDecliningTheAdministratorPromptCancelsTheInstallWithoutAnError(t *testing.T) {
	f := newInstallFixture(t)
	f.setupApp()
	f.fake.RunFunc = func(platform.Command) (int, error) { return 0, platform.ErrElevationDeclined }
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.show(entry)
	click(t, f.tt, "Install")
	f.finish()

	got := f.entry(entry)
	if got.Install != nil || got.Item.InstallError != "" {
		t.Errorf("install %+v, error %q", got.Install, got.Item.InstallError)
	}
	if last := f.lastActivity(); last.Level != activity.Info || last.Message != "Canceled installing acme/app v1.0.0" {
		t.Errorf("activity = %+v", last)
	}
	requireText(t, f.tt, "Installation was cancelled.", "Install")
}

func TestASetupProgramThatFailsIsExplainedWithItsExitCode(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	app.code = 2
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.show(entry)
	click(t, f.tt, "Install")
	f.finish()

	const reason = "The installer closed with code 2. If you cancelled it, you can try again."
	if got := f.entry(entry).Item.InstallError; got != reason {
		t.Errorf("install error = %q", got)
	}
	requireText(t, f.tt, reason)
}

func TestARestartTheInstallerAsksForIsReported(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	app.code = 3010
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.install(entry)
	if _, ok := f.entry(entry).Installed(); !ok {
		t.Error("an install that needs a restart is still installed")
	}
	requireText(t, f.tt, "app needs your PC to restart to finish installing.")
	if !slices.Contains(f.activityMessages(), "acme/app needs your PC to restart to finish installing.") {
		t.Errorf("activity = %q", f.activityMessages())
	}
}

func TestAProgramTheSystemDoesNotListIsInstalledButCannotBeLaunchedOrUninstalledUntilLocated(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	app.listed = false
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.show(entry)
	click(t, f.tt, "Install")
	f.finish()

	if _, ok := f.entry(entry).Installed(); !ok {
		t.Fatal("the Item is not installed")
	}
	requireText(t, f.tt, "Windows doesn't list this program. To launch it, locate it.", "Locate the program…")
	for _, label := range []string{"Launch", "Uninstall from this PC", "See file location"} {
		if hasLabel(f.tt, label) {
			t.Errorf("%q is offered for a program the system does not list", label)
		}
	}
	if !slices.Contains(f.activityMessages(), "Windows doesn't list acme/app after installing it, so it can't be uninstalled from here.") {
		t.Errorf("activity = %q", f.activityMessages())
	}

	f.screen.locateProgram(f.entry(entry), filepath.Join(app.location, "app.exe"))
	f.finish()
	f.tt.Frame()
	requireText(t, f.tt, "Launch", "See file location")
	requireNoText(t, f.tt, "Locate the program…")
	in, _ := f.entry(entry).Installed()
	if in.Folder != app.location || in.MainExe != "app.exe" {
		t.Errorf("install = %+v", in)
	}
}

// msiApp is an MSI whose installer writes the log msiexec was asked for.
func (f *installFixture) msiApp(app *setupApp, outcome func(cmd platform.Command, calls int) (code int, log string)) {
	calls := 0
	f.fake.RunFunc = func(cmd platform.Command) (int, error) {
		if !strings.Contains(cmd.Args, "/i ") {
			return app.run(cmd)
		}
		calls++
		code, log := outcome(cmd, calls)
		if i := strings.Index(cmd.Args, `/L*V "`); i >= 0 {
			path, _, _ := strings.Cut(cmd.Args[i+len(`/L*V "`):], `"`)
			write(f.t, path, log)
		}
		if code == 0 {
			return app.run(platform.Command{Path: "setup"})
		}
		return code, nil
	}
}

func TestAnMSIIsInstalledByWindowsInstallerAndItsLogIsDeleted(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	f.screen.prefs.Update(func(p *preferences) { p.InstallForEveryone = true })
	f.msiApp(app, func(platform.Command, int) (int, string) { return 0, "log" })
	entry := f.trackApp(f.releaseOf("v1.0.0", 1, "app-x64.msi", []byte("msi")))
	f.install(entry)

	cmd := f.fake.Commands()[0]
	if cmd.Path != "msiexec.exe" || !cmd.Elevated || !strings.HasPrefix(cmd.Args, "/i ") || !strings.Contains(cmd.Args, "/qn /norestart") ||
		!strings.HasSuffix(cmd.Args, "ALLUSERS=1") {
		t.Errorf("command = %+v", cmd)
	}
	logs, _ := filepath.Glob(filepath.Join(f.screen.dirs.cache, logDirectory, "*.log"))
	if len(logs) != 0 {
		t.Errorf("logs left after a successful install: %v", logs)
	}
	if in, _ := f.entry(entry).Installed(); in.Method != install.MethodInstaller || in.UninstallKey != appKey {
		t.Errorf("install = %+v", in)
	}
}

func TestAnMSIThatFailsIsExplainedFromItsLogWhichIsKept(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	f.msiApp(app, func(platform.Command, int) (int, string) {
		return 1603, "Action start\nError 1925. You do not have sufficient privileges\n"
	})
	entry := f.trackApp(f.releaseOf("v1.0.0", 1, "app-x64.msi", []byte("msi")))
	f.show(entry)
	click(t, f.tt, "Install")
	f.finish()

	logPath := filepath.Join(f.screen.dirs.cache, logDirectory, "app-install.log")
	want := "app couldn't be installed. This installer needs an administrator account. Details: " + logPath
	if got := f.entry(entry).Item.InstallError; got != want {
		t.Errorf("install error = %q, want %q", got, want)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("the log of a failed install was not kept: %v", err)
	}
	if last := f.lastActivity(); last.Level != activity.Error || !strings.Contains(last.Message, "administrator") {
		t.Errorf("activity = %+v", last)
	}
	requireText(t, f.tt, "This installer needs an administrator account.")
}

func TestAnMSIThatAsksForItsLicenseIsRunAgainOnlyOnceThePersonAccepts(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	f.msiApp(app, func(cmd platform.Command, _ int) (int, string) {
		if strings.Contains(cmd.Args, "ACCEPTLICENSE=YES") {
			return 0, ""
		}
		return 1603, "Product: app -- Please accept the license agreement by passing ACCEPTLICENSE=YES during installation.\n"
	})
	entry := f.trackApp(f.releaseOf("v1.0.0", 1, "app-x64.msi", []byte("msi")))
	f.show(entry)
	click(t, f.tt, "Install")
	f.answer("Accept the license of app?", "I accept the license")
	f.finish()

	cmds := f.fake.Commands()
	if len(cmds) != 2 || strings.Contains(cmds[0].Args, "ACCEPTLICENSE") || !strings.Contains(cmds[1].Args, "ACCEPTLICENSE=YES") {
		t.Fatalf("commands = %+v", cmds)
	}
	if got := f.entry(entry).Item.InstallerArgs; got != "ACCEPTLICENSE=YES" {
		t.Errorf("installer arguments = %q", got)
	}
	if _, ok := f.entry(entry).Installed(); !ok {
		t.Error("not installed")
	}
}

func TestDecliningALicenseAddsNothingAndCancelsTheInstall(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	f.msiApp(app, func(platform.Command, int) (int, string) {
		return 1603, "Product: app -- Please accept the license agreement by passing ACCEPTLICENSE=YES during installation.\n"
	})
	entry := f.trackApp(f.releaseOf("v1.0.0", 1, "app-x64.msi", []byte("msi")))
	f.show(entry)
	click(t, f.tt, "Install")
	f.answer("Accept the license of app?", "Cancel")
	f.finish()

	got := f.entry(entry)
	if got.Item.InstallerArgs != "" || got.Install != nil || len(f.fake.Commands()) != 1 {
		t.Errorf("arguments %q, install %+v, commands %+v", got.Item.InstallerArgs, got.Install, f.fake.Commands())
	}
	if last := f.lastActivity(); last.Message != "Canceled installing acme/app v1.0.0" {
		t.Errorf("activity = %+v", last)
	}
}

func TestReinstallingAnMSIUninstallsItFirstAndOffersToInstallForEveryone(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	f.msiApp(app, func(platform.Command, int) (int, string) { return 0, "" })
	entry := f.trackApp(f.releaseOf("v1.0.0", 1, "app-x64.msi", []byte("msi")))
	f.install(entry)
	// The entry of an MSI is removed with msiexec and its product code.
	listed := app.entry()
	listed.UninstallCommand = "MsiExec.exe /X{6D0C3F9E-0000-0000-0000-000000000001}"
	f.fake.AddProgram(listed)
	f.show(entry)

	click(t, f.tt, "Reinstall this version")
	requireText(t, f.tt, "Reinstall app?", "app is uninstalled first, then v1.0.0 is installed again.", "Install for everyone on this PC")
	click(t, f.tt, "Install for everyone on this PC")
	click(t, f.tt, "Reinstall")
	f.finish()

	cmds := f.fake.Commands()
	if len(cmds) != 3 || !strings.HasPrefix(cmds[1].Args, "/x {6D0C3F9E-0000-0000-0000-000000000001}") || !strings.HasPrefix(cmds[2].Args, "/i ") || !strings.HasSuffix(cmds[2].Args, "ALLUSERS=1") {
		t.Fatalf("commands = %+v", cmds)
	}
	if last := f.lastActivity(); last.Message != "Reinstalled acme/app v1.0.0" {
		t.Errorf("activity = %+v", last)
	}
}

func TestAnMSIUpdateMatchesTheScopeOfTheInstalledCopy(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	f.msiApp(app, func(platform.Command, int) (int, string) { return 0, "" })
	entry := f.trackApp(f.releaseOf("v1.0.0", 1, "app-1.0.0-x64.msi", []byte("msi")))
	f.install(entry)
	if in, _ := f.entry(entry).Installed(); in.Scope != platform.ScopeMachine {
		t.Fatalf("install = %+v", in)
	}

	app.version = "2.0.0"
	f.source.publish("app", "", f.releaseOf("v2.0.0", 5, "app-2.0.0-x64.msi", []byte("msi 2")), f.releaseOf("v1.0.0", 1, "app-1.0.0-x64.msi", []byte("msi")))
	f.refresh(entry.Item.ID)
	if !f.screen.startInstall(entry.Item.ID, "v2.0.0", nil) {
		t.Fatal("the update did not start")
	}
	f.finish()
	cmds := f.fake.Commands()
	if len(cmds) != 2 || !strings.HasSuffix(cmds[1].Args, "ALLUSERS=1") || !strings.Contains(cmds[1].Args, `INSTALLDIR="`+app.location+`"`) {
		t.Errorf("commands = %+v", cmds)
	}
}

func TestAnMSIXPackageIsInstalledForTheUserAndUninstalledByItsName(t *testing.T) {
	f := newInstallFixture(t)
	f.fake.PackageName = "Acme.App_1.0.0.0_x64__abc"
	entry := f.trackApp(f.releaseOf("v1.0.0", 1, "app-x64.msix", []byte("msix")))
	f.show(entry)
	click(t, f.tt, "Install")
	f.finish()

	in, _ := f.entry(entry).Installed()
	if in.Method != install.MethodInstaller || in.Package != f.fake.PackageName || in.Scope != platform.ScopeUser {
		t.Fatalf("install = %+v", in)
	}
	requireText(t, f.tt, "Open it from the Start menu.", "Uninstall from this PC")
	for _, label := range []string{"Launch", "See file location"} {
		if hasLabel(f.tt, label) {
			t.Errorf("%q is offered for an MSIX app", label)
		}
	}

	click(t, f.tt, "Uninstall from this PC")
	click(t, f.tt, "Uninstall")
	f.finish()
	if got := f.fake.Packages(); len(got) != 0 {
		t.Errorf("packages left = %v", got)
	}
	if f.entry(entry).Install != nil {
		t.Error("the install was not forgotten")
	}
}

func TestAnArchiveWhoseOnlyProgramIsAnMSIIsUnpackedAndInstalled(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	f.msiApp(app, func(platform.Command, int) (int, string) { return 0, "" })
	entry := f.trackApp(f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app/app.msi": []byte("msi"), "app/readme.txt": []byte("hi")}))
	f.install(entry)

	cmds := f.fake.Commands()
	if len(cmds) != 1 || cmds[0].Path != "msiexec.exe" || !strings.Contains(cmds[0].Args, `app.msi"`) {
		t.Fatalf("commands = %+v", cmds)
	}
	if in, _ := f.entry(entry).Installed(); in.Method != install.MethodInstaller {
		t.Errorf("install = %+v", in)
	}
	if left := filesIn(t, f.apps); len(left) != 0 {
		t.Errorf("unpacked files left behind: %v", left)
	}
}

func TestAnAppUpdatedOutsideTheApplicationIsMovedToThatRelease(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.install(entry)
	f.source.publish("app", "", f.setupRelease("v1.1.0", 5, "app-1.1.0-x64-setup.exe", nsisSetup+" 2"),
		f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.refresh(entry.Item.ID)

	check := func() { f.t.Helper(); f.screen.checkOutsideUpdates(f.t.Context()) }
	check() // Still at 1.0.0.
	if got := f.entry(entry).InstalledVersion(); got != "v1.0.0" {
		t.Fatalf("installed version = %q", got)
	}
	app.version = "1.1.0.0"
	f.fake.AddProgram(app.entry())
	check()

	in, _ := f.entry(entry).Installed()
	if in.Tag != "v1.1.0" || in.EntryVersion != "1.1.0.0" {
		t.Errorf("install = %+v", in)
	}
	if last := f.lastActivity(); last.Message != "acme/app was updated outside Release Sync" {
		t.Errorf("activity = %+v", last)
	}
}

func TestWhenTheEntryHasNoUsableVersionTheProgramsOwnVersionIsTried(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	app.version = ""
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.install(entry)
	f.source.publish("app", "", f.setupRelease("v1.1.0", 5, "app-1.1.0-x64-setup.exe", nsisSetup+" 2"),
		f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.refresh(entry.Item.ID)

	f.fake.Versions = map[string]string{filepath.Join(app.location, "app.exe"): "1.1.0.0"}
	f.screen.checkOutsideUpdates(t.Context())
	if got := f.entry(entry).InstalledVersion(); got != "v1.1.0" {
		t.Errorf("installed version = %q", got)
	}
}

func TestAnEntryWhoseVersionMatchesNoNewerReleaseLeavesTheInstalledVersionAlone(t *testing.T) {
	f := newInstallFixture(t)
	app := f.setupApp()
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.install(entry)
	app.version = "9.9.9"
	f.fake.AddProgram(app.entry())
	f.fake.ProgramsErr = nil
	f.screen.checkOutsideUpdates(t.Context())
	if got := f.entry(entry).InstalledVersion(); got != "v1.0.0" {
		t.Errorf("installed version = %q", got)
	}
	// A failure to read the list changes nothing either.
	f.fake.ProgramsErr = errors.New("registry unavailable")
	f.screen.checkOutsideUpdates(t.Context())
	if got := f.entry(entry).InstalledVersion(); got != "v1.0.0" {
		t.Errorf("installed version = %q", got)
	}
}

func TestTheDrawerOffersTheInstallerOptionsOfAnApp(t *testing.T) {
	f := newInstallFixture(t)
	entry := f.trackApp(f.setupRelease("v1.0.0", 1, "app-1.0.0-x64-setup.exe", nsisSetup))
	f.show(entry)
	requireText(t, f.tt, "Install for everyone", "Custom installer arguments", "Install folder", "Default location",
		"Used the next time it's installed or updated. The copy that's installed now isn't moved.")

	click(t, f.tt, "Install for everyone")
	typeInto(t, f.tt, "Installer arguments for this app", "/S /NORESTART")
	f.waitFor("Save")
	click(t, f.tt, "Save")
	f.finish()
	got := f.entry(entry).Item
	if !got.InstallForEveryone || got.InstallerArgs != "/S /NORESTART" {
		t.Errorf("item = %+v", got)
	}
}

func TestACommandLineToolHasNoInstallerOptions(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("tool", "", f.releaseOf("v1.0.0", 1, "tool-windows-x64.exe", console("tool")))
	entry := f.track("tool", catalog.AddOptions{Kind: selection.ItemTool})
	f.show(entry)
	requireNoText(t, f.tt, "Custom installer arguments", "Install for everyone")
}

func TestTheInstallationSettingsHaveSilentInstallsAndInstallForEveryone(t *testing.T) {
	f := newInstallFixture(t)
	click(t, f.tt, "Settings")
	f.tt.Frame()
	click(t, f.tt, "Installation")
	requireText(t, f.tt, "Silent installs", "Install apps for everyone on this PC")
	if p := f.screen.prefs.Get(); !p.SilentInstalls || p.InstallForEveryone {
		t.Errorf("defaults = %+v", p)
	}
	click(t, f.tt, "Toggle silent installs")
	click(t, f.tt, "Toggle installing apps for everyone on this PC")
	f.tt.Frame()
	if p := f.screen.prefs.Get(); p.SilentInstalls || !p.InstallForEveryone {
		t.Errorf("after toggling = %+v", p)
	}
	if !f.screen.catalog.Policy().InstallForEveryone {
		t.Error("downloads are not chosen for everyone")
	}
}
