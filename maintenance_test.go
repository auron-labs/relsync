package main

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/backup"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/source"
)

// installedApp installs a portable app at v1.0.0 with a file of the person's own
// in its folder, then publishes v2.0.0 above it. It returns the Item and its folder.
func (f *installFixture) installedApp() (catalog.Entry, string) {
	f.t.Helper()
	v1 := f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1"), "data/a.dat": []byte("a1")})
	f.source.publish("app", "An app", v1)
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	folder := filepath.Join(f.apps, "app")
	write(f.t, filepath.Join(folder, "saves", "slot1.sav"), "my save")
	v2 := f.release("v2.0.0", 5, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v2"), "new.dll": []byte("n")})
	f.source.publish("app", "An app", v2, v1)
	f.refresh(entry.Item.ID)
	return f.entry(entry), folder
}

func (f *installFixture) updateToV2(e catalog.Entry) {
	f.t.Helper()
	if !f.screen.startInstall(e.Item.ID, "v2.0.0", nil) {
		f.t.Fatal("the update did not start")
	}
	f.finish()
}

func TestUpdatingAPortableAppBacksItUpAndTheDrawerRestoresIt(t *testing.T) {
	f := newInstallFixture(t)
	entry, folder := f.installedApp()
	f.updateToV2(entry)
	if got := f.entry(entry).InstalledVersion(); got != "v2.0.0" {
		t.Fatalf("installed %q", got)
	}

	backups := backup.ListApp(f.screen.appBackupDir(entry))
	if len(backups) != 1 || backups[0].Tag != "v1.0.0" || backups[0].Reason != backup.BeforeUpdate {
		t.Fatalf("backups = %+v", backups)
	}
	f.show(entry)
	f.finish()
	requireText(t, f.tt, "Previous versions", "Version v1.0.0, before an update", "Restore")

	f.fake.Running = []platform.Process{{ID: 7, Path: filepath.Join(folder, "app.exe")}}
	click(t, f.tt, "Restore")
	f.answer("Restore app from this backup?", "Restore backup")
	f.finish()

	want := []string{"app.exe", "data/a.dat", "saves/slot1.sav"}
	if got := filesIn(t, folder); !slices.Equal(got, want) {
		t.Fatalf("folder = %v, want %v", got, want)
	}
	if got := readFile(t, filepath.Join(folder, "app.exe")); got != string(gui("v1")) {
		t.Errorf("app.exe is not the backed up version")
	}
	got := f.entry(entry)
	in, _ := got.Installed()
	if got.InstalledVersion() != "v1.0.0" || in.Folder != folder || in.MainExe != "app.exe" {
		t.Errorf("install = %+v", in)
	}
	if files, err := f.screen.catalog.InstallFiles(t.Context(), entry.Item.ID); err != nil || len(files) != 2 {
		t.Errorf("recorded files = %+v, %v", files, err)
	}
	if closed := f.fake.Closed(); len(closed) != 1 || closed[0].ID != 7 {
		t.Errorf("closed = %+v: the running program must be closed first", closed)
	}
	if last := f.lastActivity(); last.Level != activity.Success || last.Message != "Restored acme/app to v1.0.0 from a backup" {
		t.Errorf("activity = %+v", last)
	}
	requireText(t, f.tt, "app restored to v1.0.0")
}

func TestDecliningARestoreChangesNothing(t *testing.T) {
	f := newInstallFixture(t)
	entry, folder := f.installedApp()
	f.updateToV2(entry)
	f.show(entry)
	f.finish()
	before := filesIn(t, folder)
	click(t, f.tt, "Restore")
	f.answer("Restore app from this backup?", "Cancel")
	f.finish()
	if got := filesIn(t, folder); !slices.Equal(got, before) || f.entry(entry).InstalledVersion() != "v2.0.0" || len(f.fake.Closed()) != 0 {
		t.Errorf("a declined restore changed %v, version %q", got, f.entry(entry).InstalledVersion())
	}
}

func TestBackingUpBeforeChangesCanBeTurnedOff(t *testing.T) {
	f := newInstallFixture(t)
	f.screen.prefs.Update(func(p *preferences) { p.BackupBeforeChanges = false })
	entry, _ := f.installedApp()
	f.updateToV2(entry)
	if got := backup.ListApp(f.screen.appBackupDir(entry)); len(got) != 0 {
		t.Fatalf("backups = %+v", got)
	}
}

func TestUninstallingAPortableAppKeepsABackupOfItsFolder(t *testing.T) {
	f := newInstallFixture(t)
	entry, folder := f.installedApp()
	f.screen.startUninstall(f.entry(entry), false)
	f.finish()
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("the folder is still there: %v", err)
	}
	backups := backup.ListApp(f.screen.appBackupDir(entry))
	if len(backups) != 1 || backups[0].Reason != backup.BeforeRemoval || backups[0].Tag != "v1.0.0" {
		t.Fatalf("backups = %+v", backups)
	}
	f.show(entry)
	f.finish()
	requireText(t, f.tt, "Version v1.0.0, before it was removed")
	if hasLabel(f.tt, "Restore") {
		t.Error("an app that is not installed offers Restore")
	}
}

func TestMovingAPortableAppRepointsItsShortcutsAndKeepsItsVersion(t *testing.T) {
	f := newInstallFixture(t)
	entry, folder := f.installedApp()
	link := filepath.Join(f.menu, appName, "app.lnk")
	dest := filepath.Join(t.TempDir(), "elsewhere", "app")
	f.fake.Running = []platform.Process{{ID: 9, Path: filepath.Join(folder, "app.exe")}}

	f.show(entry)
	requireText(t, f.tt, "Move to another folder…")
	f.screen.startMove(entry, dest)
	f.answer("app is running", "Close it and move")
	f.finish()

	want := []string{"app.exe", "data/a.dat", "saves/slot1.sav"}
	if got := filesIn(t, dest); !slices.Equal(got, want) {
		t.Fatalf("moved folder = %v, want %v", got, want)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the old folder is still there: %v", err)
	}
	got := f.entry(entry)
	in, _ := got.Installed()
	if in.Folder != dest || in.Dedicated || got.InstalledVersion() != "v1.0.0" || in.MainExe != "app.exe" {
		t.Errorf("install = %+v at %q", in, got.InstalledVersion())
	}
	if shortcut := f.fake.Shortcuts()[link]; shortcut.Target != filepath.Join(dest, "app.exe") {
		t.Errorf("shortcut target = %q", shortcut.Target)
	}
	if closed := f.fake.Closed(); len(closed) != 1 || closed[0].ID != 9 {
		t.Errorf("closed = %+v", closed)
	}
	if last := f.lastActivity(); last.Level != activity.Success || !strings.HasPrefix(last.Message, "Moved acme/app from "+folder+" to "+dest) {
		t.Errorf("activity = %+v", last)
	}
	// The next update goes where the app is now.
	f.updateToV2(f.entry(entry))
	if got := readFile(t, filepath.Join(dest, "app.exe")); got != string(gui("v2")) {
		t.Errorf("the update did not land in the new folder")
	}
}

func TestAMoveToAFolderThatCannotTakeTheAppIsRefusedWithTheReason(t *testing.T) {
	f := newInstallFixture(t)
	entry, folder := f.installedApp()
	occupied := filepath.Join(t.TempDir(), "occupied")
	write(t, filepath.Join(occupied, "readme.txt"), "x")
	for dest, want := range map[string]string{
		folder:                              "It's already in that folder.",
		filepath.Join(folder, "data", "in"): "That folder is inside the app's current folder. Choose a folder somewhere else.",
		occupied:                            "That folder already has files in it. Choose an empty folder.",
	} {
		f.screen.startMove(entry, dest)
		f.settle()
		f.tt.Frame()
		requireText(t, f.tt, want)
	}
	f.fake.SpaceFunc = func(string) (uint64, error) { return 1, nil }
	f.screen.startMove(entry, filepath.Join(t.TempDir(), "new"))
	f.settle()
	requireText(t, f.tt, "There isn't enough free space there.")
	if got := filesIn(t, folder); !slices.Equal(got, []string{"app.exe", "data/a.dat", "saves/slot1.sav"}) {
		t.Errorf("a refused move changed the app: %v", got)
	}
	if _, working := f.screen.opOf(entry.Item.ID); working {
		t.Error("a refused move left an operation running")
	}
}

func TestAnInstallerBasedAppSaysWhyItCannotBeMoved(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "An app", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	rec := install.Record{Method: install.MethodInstaller, Asset: "setup.exe", Folder: filepath.Join(f.apps, "elsewhere"), UninstallKey: "key"}
	if err := f.screen.catalog.RecordInstall(t.Context(), entry.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: rec}); err != nil {
		t.Fatal(err)
	}
	f.show(entry)
	requireText(t, f.tt, "Apps installed with a setup program can't be moved: the setup program decides where its files go.")
	if hasLabel(f.tt, "Move to another folder…") {
		t.Error("an installer-based app offers to be moved")
	}
	f.screen.startMove(f.entry(entry), filepath.Join(t.TempDir(), "new"))
	f.settle()
	requireText(t, f.tt, "was installed with a setup program, so it can't be moved.")
}

// backupFixture is an install fixture whose desktop shows nothing, with the
// Library backup folder set.
func newBackupFixture(t *testing.T) (*autoFixture, string) {
	t.Helper()
	f := newAutoFixture(t)
	folder := filepath.Join(t.TempDir(), "backups")
	f.screen.prefs.Update(func(p *preferences) { p.LibraryBackupFolder = folder })
	return f, folder
}

func zipEntries(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	names := make([]string, 0, len(zr.File))
	for _, e := range zr.File {
		names = append(names, e.Name)
	}
	slices.Sort(names)
	return names
}

func (f *autoFixture) backupFiles(folder string) []string { return filesIn(f.t, folder) }

func TestBackingUpTheLibraryNowWritesAnArchiveAndRemembersWhen(t *testing.T) {
	f, folder := newBackupFixture(t)
	f.publishPortable()
	f.screen.startLibraryBackup(false)
	f.settle()

	files := f.backupFiles(folder)
	if len(files) != 1 || !strings.HasPrefix(files[0], "release-sync-library-") {
		t.Fatalf("backups = %v", files)
	}
	if got := zipEntries(t, filepath.Join(folder, files[0])); !slices.Equal(got, []string{"library/app.db", "manifest.json"}) {
		t.Errorf("archive holds %v", got)
	}
	if last, ok := f.screen.prefs.Get().lastLibraryBackup(); !ok || time.Since(last) > time.Minute {
		t.Errorf("last backup = %v, %v", last, ok)
	}
	if got := f.lastActivity(); got.Level != activity.Success || !strings.HasPrefix(got.Message, "Backed up the library to "+folder) {
		t.Errorf("activity = %+v", got)
	}
}

func TestALibraryBackupCanIncludeThePortableAppsFiles(t *testing.T) {
	f, folder := newBackupFixture(t)
	f.screen.prefs.Update(func(p *preferences) { p.LibraryBackupInclude = string(backup.WithApps) })
	entry := f.publishPortable()
	f.install(entry)
	f.screen.startLibraryBackup(false)
	f.settle()
	files := f.backupFiles(folder)
	if len(files) != 1 || !strings.Contains(files[0], "-full-") {
		t.Fatalf("backups = %v", files)
	}
	if got := zipEntries(t, filepath.Join(folder, files[0])); !slices.Equal(got, []string{"apps/1.zip", "library/app.db", "manifest.json"}) {
		t.Errorf("archive holds %v", got)
	}
	m, err := backup.ReadManifest(filepath.Join(folder, files[0]))
	if err != nil || len(m.Apps) != 1 || m.Apps[0].Folder != filepath.Join(f.apps, "app") {
		t.Errorf("manifest = %+v, %v", m, err)
	}
}

func TestOnlyTheChosenNumberOfLibraryBackupsAreKept(t *testing.T) {
	f, folder := newBackupFixture(t)
	f.screen.prefs.Update(func(p *preferences) { p.LibraryBackupKeep = 2 })
	var first string
	for i := range 3 {
		if err := f.screen.backUpLibrary(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = f.backupFiles(folder)[0]
		}
		if i < 2 {
			// Backups are named by the second they were made.
			time.Sleep(1100 * time.Millisecond)
		}
	}
	if got := f.backupFiles(folder); len(got) != 2 || slices.Contains(got, first) {
		t.Fatalf("kept %v, the oldest was %s", got, first)
	}
}

func TestAnUnreachableBackupFolderIsRetriedLaterWithOneNotice(t *testing.T) {
	f, folder := newBackupFixture(t)
	blocker := filepath.Join(t.TempDir(), "not-a-folder")
	write(t, blocker, "x")
	f.screen.prefs.Update(func(p *preferences) { p.LibraryBackupFolder = filepath.Join(blocker, "backups") })

	for range 3 {
		if err := f.screen.backUpLibrary(t.Context(), true); err != nil {
			t.Fatalf("an unreachable folder is routine and not reported: %v", err)
		}
	}
	if got := f.desktop.Notices(); len(got) != 1 || got[0].ID != noticeBackup {
		t.Fatalf("notices = %+v, want one", got)
	}
	var warnings int
	for _, e := range f.screen.activity.Entries().Get() {
		if e.Level == activity.Warning && strings.HasPrefix(e.Message, "Couldn't reach the backup folder") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("%d activity warnings, want 1", warnings)
	}

	// The folder comes back: the backup works and the next failure is announced again.
	f.screen.prefs.Update(func(p *preferences) { p.LibraryBackupFolder = folder })
	if err := f.screen.backUpLibrary(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if f.screen.backupNotified.Load() {
		t.Error("a successful backup did not clear the notice")
	}
	f.screen.prefs.Update(func(p *preferences) { p.LibraryBackupFolder = filepath.Join(blocker, "backups") })
	if err := f.screen.backUpLibrary(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if got := f.desktop.Notices(); len(got) != 2 {
		t.Errorf("notices = %d, want a new one for a new failure", len(got))
	}
}

func TestABackupTheScheduleAsksForIsSilentButOneYouAskForSaysSo(t *testing.T) {
	f, _ := newBackupFixture(t)
	f.screen.startLibraryBackup(true)
	f.settle()
	f.tt.Frame()
	requireNoText(t, f.tt, "Backed up to")
	f.screen.startLibraryBackup(false)
	f.settle()
	requireText(t, f.tt, "Backed up to release-sync-library-")
}

func TestABackupIsOverdueOnlyWhenBackupsAreOnAndTheLastIsOld(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		every string
		last  time.Time
		want  bool
	}{
		"off, never run":        {background.BackupOff, time.Time{}, false},
		"on, never run":         {"1d", time.Time{}, true},
		"daily, a day ago":      {"1d", now.Add(-24 * time.Hour), true},
		"daily, an hour ago":    {"1d", now.Add(-time.Hour), false},
		"weekly, three days":    {"7d", now.Add(-72 * time.Hour), false},
		"off, long ago":         {background.BackupOff, now.AddDate(-1, 0, 0), false},
		"monthly, a month ago":  {"30d", now.Add(-30 * 24 * time.Hour), true},
		"six hourly, 5h59m ago": {"6h", now.Add(-6*time.Hour + time.Minute), false},
	} {
		p := defaultPreferences()
		p.LibraryBackupEvery = tc.every
		if !tc.last.IsZero() {
			p.LastLibraryBackup = tc.last.Unix()
		}
		if got := libraryBackupOverdue(p, now); got != tc.want {
			t.Errorf("%s: overdue = %v, want %v", name, got, tc.want)
		}
	}
}

func TestRestoringALibraryBackupStagesItAndRestartsAfterTheAnswer(t *testing.T) {
	f, folder := newBackupFixture(t)
	var relaunches atomic.Int32
	f.screen.relaunch = func() { relaunches.Add(1) }
	f.screen.startLibraryBackup(false)
	f.settle()
	path := filepath.Join(folder, f.backupFiles(folder)[0])

	// Saying no leaves everything as it is.
	f.screen.startLibraryRestore(path)
	f.answer("Restore from this backup?", "Cancel")
	f.settle()
	if _, err := os.Stat(f.screen.dirs.restore); !os.IsNotExist(err) || relaunches.Load() != 0 {
		t.Fatalf("a declined restore staged %v and restarted %d times", f.screen.dirs.restore, relaunches.Load())
	}

	f.screen.startLibraryRestore(path)
	f.waitFor("A copy of the current ones is kept first")
	f.answer("Restore from this backup?", "Restore and restart")
	f.settle()
	f.tt.Frame()
	if _, err := os.Stat(filepath.Join(f.screen.dirs.restore, "app.db")); err != nil {
		t.Fatalf("the backup's database was not staged: %v", err)
	}
	if relaunches.Load() != 1 {
		t.Errorf("restarted %d times", relaunches.Load())
	}
	// The running Library is untouched until the application starts again.
	if _, ok := f.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "app"}); ok {
		t.Error("the running library changed")
	}
}

func TestAFileThatIsNotABackupIsRefusedWithoutAskingOrRestarting(t *testing.T) {
	f, _ := newBackupFixture(t)
	var relaunches atomic.Int32
	f.screen.relaunch = func() { relaunches.Add(1) }
	junk := filepath.Join(t.TempDir(), "junk.zip")
	write(t, junk, "not a zip")
	f.screen.startLibraryRestore(junk)
	f.settle()
	requireText(t, f.tt, "That file isn't a Release Sync backup.")
	if relaunches.Load() != 0 {
		t.Error("restarted for a file that is not a backup")
	}
	if _, err := os.Stat(f.screen.dirs.restore); !os.IsNotExist(err) {
		t.Error("something was staged")
	}
}

func TestRestoringAFullBackupClosesTheAppsAndPutsTheirFilesBack(t *testing.T) {
	f, folder := newBackupFixture(t)
	f.screen.relaunch = func() {}
	f.screen.prefs.Update(func(p *preferences) { p.LibraryBackupInclude = string(backup.WithApps) })
	entry := f.publishPortable()
	f.install(entry)
	appFolder := filepath.Join(f.apps, "app")
	f.screen.startLibraryBackup(false)
	f.settle()
	path := filepath.Join(folder, f.backupFiles(folder)[0])
	write(t, filepath.Join(appFolder, "app.exe"), "damaged")
	f.fake.Running = []platform.Process{{ID: 3, Path: filepath.Join(appFolder, "app.exe")}}

	f.screen.startLibraryRestore(path)
	f.waitFor("It also holds the files of 1 portable app. Any that are running are closed first.")
	f.answer("Restore from this backup?", "Restore and restart")
	f.settle()
	if got := readFile(t, filepath.Join(appFolder, "app.exe")); got != string(gui("v1.0.0")) {
		t.Errorf("app.exe = %q", got)
	}
	if closed := f.fake.Closed(); len(closed) != 1 || closed[0].ID != 3 {
		t.Errorf("closed = %+v", closed)
	}
}

func TestTheDataSectionOffersTheBackupSettings(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	tt.SetSize(windowWidth, 6000)
	e.screen.openSettings("data")
	requireText(t, tt, "Back up an app before changing it", "Back up automatically", "Off", "Where to save backups", "What to include",
		"The library and settings", "How many to keep", "Newest 10", "Back up now", "Restore from a backup file…", "No backup has been made yet.")

	click(t, tt, "How often to back up")
	for _, label := range []string{"Every 6 hours", "Every 12 hours", "Daily", "Every 3 days", "Weekly", "Every 2 weeks", "Monthly"} {
		requireText(t, tt, label)
	}
	click(t, tt, "Every 3 days")
	click(t, tt, "How many backups to keep")
	for _, label := range []string{"Newest 5", "Newest 30", "Newest 100", "All"} {
		requireText(t, tt, label)
	}
	click(t, tt, "All")
	click(t, tt, "What a backup includes")
	click(t, tt, "The library, settings and portable apps' files")
	click(t, tt, "Toggle backing up an app before changing it")
	got := e.screen.prefs.Get()
	if got.LibraryBackupEvery != "3d" || got.LibraryBackupKeep != 0 || got.LibraryBackupInclude != string(backup.WithApps) || got.BackupBeforeChanges {
		t.Errorf("preferences = %+v", got)
	}
}

func TestRestoringBackedUpAppsRefusesATooBroadFolderBeforeClosingAnything(t *testing.T) {
	f, _ := newBackupFixture(t)
	root := string(filepath.Separator)
	f.fake.Running = []platform.Process{{ID: 3, Path: filepath.Join(root, "usr", "bin", "other")}}
	m := backup.Manifest{Apps: []backup.ManifestApp{{Name: "acme/app", Folder: root}}}
	err := f.screen.restoreBackedUpApps(t.Context(), "unused.zip", m)
	if !errors.Is(err, install.ErrTooBroad) {
		t.Fatalf("restoreBackedUpApps = %v, want ErrTooBroad", err)
	}
	if closed := f.fake.Closed(); len(closed) != 0 {
		t.Errorf("closed %v before the folder was refused", closed)
	}
}
