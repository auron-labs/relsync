package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/database"
	"github.com/auron-labs/mygo-framework/jobs"
	"github.com/auron-labs/mygo-framework/secrets/memory"
	"github.com/auron-labs/mygo-framework/state"

	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/theme"
)

// newTestScreen returns a screen whose preferences are never persisted, over a
// throwaway database and a fake source.
func newTestScreen(t *testing.T, initial preferences) *screen {
	t.Helper()
	return newEnv(t, initial).screen
}

func newTester(s *screen) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.view(c) }, windowWidth, windowHeight)
}

// persistentScreen returns a screen backed by the database in dir, as run builds it.
func persistentScreen(t *testing.T, dir string) (*screen, *database.Database) {
	t.Helper()
	db, values, err := openPreferences(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := loadPreferences(t.Context(), values)
	if err != nil {
		t.Fatal(err)
	}
	cell := state.NewCell(initial)
	saver := state.Autosave(cell, time.Hour, savePreferences(values))
	t.Cleanup(func() { _ = saver.Close(context.Background()) })
	manager := jobs.New(t.Context())
	d := dirs{config: dir, cache: dir}
	gh := source.NewGitHub(source.GitHubOptions{BaseURL: "http://127.0.0.1:1", UserAgent: "test"})
	svc, err := openServices(t.Context(), db.SQL, d, manager, gh, newFakeSource(), memory.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.avatars.Close)
	return newScreen(cell, saver, manager, d, svc), db
}

func TestNavigationSelectsTheRightPage(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	tt := newTester(s)
	tt.Frame()
	if !tt.HasText("Your library is empty") {
		t.Fatalf("library page = %q", tt.Texts())
	}
	for _, step := range []struct {
		nav  string
		want string
	}{
		{"Updates", "Check for updates to look for new releases."},
		{"Discover", "Projects gaining stars fastest right now. Movement arrows show how the ranking changed."},
		{"Try later", "Repositories you save to look at later will appear here."},
		{"Scan PC", "Look through installed programs"},
		{"Import & Export", "Your library is empty, so there is nothing to export."},
		{"Activity", "Installs, updates and checks will be recorded here."},
		{"Settings", "Search settings"},
	} {
		if err := tt.Click(step.nav); err != nil {
			t.Fatalf("%s: %v", step.nav, err)
		}
		tt.Frame()
		if !tt.HasText(step.want) {
			t.Fatalf("%s page = %q", step.nav, tt.Texts())
		}
	}
	if err := tt.Click("Library"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Your library is empty") {
		t.Fatalf("back to library = %q", tt.Texts())
	}
}

func TestNavigationBadgeShowsTheCount(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	tt := newTester(s)
	tt.Frame()
	s.updateCount.Set(3)
	s.tryLaterCount.Set(2)
	tt.Frame()
	if !tt.HasText("3") || !tt.HasText("2") {
		t.Fatalf("badges = %q", tt.Texts())
	}
}

func TestSidebarCollapsesToIconsBelowTheBreakpoint(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	tt := newTester(s)
	tt.Frame()
	if s.compact || !tt.HasText("Library") {
		t.Fatalf("expanded navigation = %q", tt.Texts())
	}
	tt.SetSize(compactBreakpoint-1, windowHeight)
	tt.Frame()
	// Icon-rail items keep their accessibility label, so a label occurs once
	// instead of twice (label plus visible text).
	if !s.compact || countText(tt, "Library") != 1 {
		t.Fatalf("compact navigation = %q", tt.Texts())
	}
	if tt.HasText("Not signed in") {
		t.Fatal("the wide footer card did not collapse")
	}
	if err := tt.Click("Discover"); err != nil {
		t.Fatal("icon rail items stay clickable:", err)
	}
	tt.Frame()
	if !tt.HasText("Projects gaining stars fastest right now. Movement arrows show how the ranking changed.") {
		t.Fatalf("compact discover page = %q", tt.Texts())
	}
	tt.SetSize(windowWidth, windowHeight)
	tt.Frame()
	if s.compact || countText(tt, "Library") != 2 || !tt.HasText("Not signed in") {
		t.Fatalf("navigation did not expand = %q", tt.Texts())
	}
}

func countText(tt *ui.Tester, s string) int {
	count := 0
	for _, text := range tt.Texts() {
		if text == s {
			count++
		}
	}
	return count
}

func TestEscapeClosesTheTopMostOverlay(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	tt := newTester(s)
	s.openDrawer("Details", func(c *ui.Context) {
		components.Text(c, "Detail body").Wrap()
	})
	s.showDialog("Confirm", func(c *ui.Context) {
		components.Text(c, "Dialog body").Wrap()
	})
	tt.Frame()
	if !tt.HasText("Detail body") || !tt.HasText("Dialog body") {
		t.Fatalf("overlays = %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if tt.HasText("Dialog body") {
		t.Fatal("Escape did not close the dialog first")
	}
	if !s.drawer.open || !tt.HasText("Detail body") {
		t.Fatal("Escape closed the drawer under the dialog")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if s.drawer.open || tt.HasText("Detail body") {
		t.Fatal("Escape did not close the drawer")
	}
}

func TestQueuedToastShowsNextFrame(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	tt := newTester(s)
	tt.Frame()
	s.queueToast(ui.Toast{Title: "Support bundle saved.", Type: "success"})
	tt.Frame()
	if !tt.HasText("Support bundle saved.") {
		t.Fatalf("toast = %q", tt.Texts())
	}
}

func TestThemeChoicePersistsAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	s, db := persistentScreen(t, dir)
	tt := newTester(s)
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Appearance"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Dark"); err != nil {
		t.Fatal(err)
	}
	if err := s.saver.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, db := persistentScreen(t, dir)
	t.Cleanup(func() { _ = db.Close() })
	if got := restarted.prefs.Get(); got.Theme != themeDark {
		t.Fatalf("restarted preferences = %+v", got)
	}
}

func TestChoosingAThemeAppliesIt(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	var applied []string
	defer watchTheme(s.prefs, func(name string) { applied = append(applied, name) })()
	tt := newTester(s)
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Appearance"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Dark"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(applied, []string{themeDark}) {
		t.Fatalf("applied themes = %q, want only dark", applied)
	}
}

func TestOpenPreferencesMigratesAndPersists(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")

	db, values, err := openPreferences(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := db.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM goose_db_version WHERE version_id = 1 AND is_applied").Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("settings migration applied = %d, %v", applied, err)
	}
	defaults, err := loadPreferences(ctx, values)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults, defaultPreferences()) {
		t.Fatalf("defaults = %+v", defaults)
	}
	want := defaultPreferences()
	want.Theme, want.ErrorReports, want.ParallelChecks, want.LastFullCheck, want.UseGitHubCLI = themeDark, false, 6, 1_800_000_000, true
	want.DownloadFolder, want.KeepDownloads, want.VerifyDownloads = filepath.Join(t.TempDir(), "downloads"), true, false
	want.InstallAfterAdding, want.StartMenuShortcuts, want.ConfirmRemove = false, false, false
	want.PortableFolder, want.ToolsFolder = filepath.Join(t.TempDir(), "apps"), filepath.Join(t.TempDir(), "tools")
	want.SilentInstalls, want.InstallForEveryone = false, true
	want.StartAtLogin, want.StartMinimized, want.ShowTrayIcon, want.CloseToTray, want.TrayNoticeShown = true, false, false, false, true
	want.CheckInterval, want.NotifyUpdates, want.AutoInstall = "30m", false, true
	want.ScanFolders = []string{filepath.Join(t.TempDir(), "portable")}
	want.LibraryView, want.AutoGroups, want.GroupSections = viewList, false, false
	want.ScanDownloads, want.CheckProvenance, want.UseVirusTotal, want.SafetyHints = false, false, true, false
	want.BackupBeforeChanges, want.LibraryBackupEvery, want.LibraryBackupFolder = false, "3d", filepath.Join(t.TempDir(), "backups")
	want.LibraryBackupInclude, want.LibraryBackupKeep, want.LastLibraryBackup = "full", 0, 1_800_000_000
	if err := want.save(ctx, values); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, values, err = openPreferences(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	got, err := loadPreferences(ctx, values)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded = %+v, %v; want %+v", got, err, want)
	}
}

func TestPreferencesRejectInvalidValues(t *testing.T) {
	ctx := context.Background()
	db, values, err := openPreferences(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	invalidTheme := defaultPreferences()
	invalidTheme.Theme = "neon"
	if err := invalidTheme.save(ctx, values); err == nil {
		t.Error("an invalid theme was saved")
	}
	for name, set := range map[string]func(*preferences){
		"download": func(p *preferences) { p.DownloadFolder = "downloads" },
		"portable": func(p *preferences) { p.PortableFolder = "apps" },
		"tools":    func(p *preferences) { p.ToolsFolder = "tools" },
	} {
		relative := defaultPreferences()
		set(&relative)
		if err := relative.save(ctx, values); err == nil {
			t.Errorf("a relative %s folder was saved", name)
		}
	}
	badView := defaultPreferences()
	badView.LibraryView = "table"
	if err := badView.save(ctx, values); err == nil {
		t.Error("a Library view that is not one of the choices was saved")
	}
	badInterval := defaultPreferences()
	badInterval.CheckInterval = "2h"
	if err := badInterval.save(ctx, values); err == nil {
		t.Error("a check interval that is not one of the choices was saved")
	}
	badBackup := defaultPreferences()
	badBackup.LibraryBackupEvery = "hourly"
	if err := badBackup.save(ctx, values); err == nil {
		t.Error("a backup interval that is not one of the choices was saved")
	}
	badKeep := defaultPreferences()
	badKeep.LibraryBackupKeep = 7
	if err := badKeep.save(ctx, values); err == nil {
		t.Error("a number of backups to keep that is not one of the choices was saved")
	}
	badInclude := defaultPreferences()
	badInclude.LibraryBackupInclude = "everything"
	if err := badInclude.save(ctx, values); err == nil {
		t.Error("a backup content that is not one of the choices was saved")
	}
	for _, n := range []int{0, 9} {
		tooMany := defaultPreferences()
		tooMany.ParallelChecks = n
		if err := tooMany.save(ctx, values); err == nil {
			t.Errorf("%d parallel checks were saved", n)
		}
	}
}

func TestSaveFailureIsVisibleAndBlocksClose(t *testing.T) {
	s, db := persistentScreen(t, t.TempDir())
	tt := newTester(s)
	tt.Frame()
	if tt.HasText("Could not save your preferences.") {
		t.Fatal("save error shown before any failure")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s.prefs.Update(func(p *preferences) { p.Theme = themeDark })
	var event mygo.CloseEvent
	s.beforeClose(&event)
	if !event.DefaultPrevented() {
		t.Fatal("a failed save must keep the window open")
	}
	tt.Frame()
	if !tt.HasText("Could not save your preferences.") {
		t.Fatalf("save error is not visible: %q", tt.Texts())
	}
}

// requestCounter stands in for an update server and counts what reaches it.
type requestCounter struct{ requests atomic.Int32 }

func (r *requestCounter) Manifest(context.Context, bool) (io.ReadCloser, error) {
	r.requests.Add(1)
	return nil, errors.New("unexpected update request")
}

func (r *requestCounter) Open(context.Context, string) (io.ReadCloser, error) {
	r.requests.Add(1)
	return nil, errors.New("unexpected update request")
}

func TestUpdatesAreOffUnlessTheBuildConfiguresThem(t *testing.T) {
	server := &requestCounter{}
	mygo.Updater.SetSource(server)
	t.Cleanup(func() { mygo.Updater.SetSource(nil) })
	s := newTestScreen(t, preferences{})

	s.checkForUpdates()

	if _, started := s.jobs.Status(updateCheckJob); started {
		t.Fatal("an update check started in a build without updates")
	}
	if server.requests.Load() != 0 {
		t.Fatal("the update source was contacted")
	}
	if _, err := mygo.Updater.Check(t.Context()); !errors.Is(err, mygo.ErrUpdatesDisabled) {
		t.Fatalf("Check = %v, want ErrUpdatesDisabled", err)
	}
	tt := newTester(s)
	tt.Frame()
	if tt.HasText("Update available") {
		t.Fatalf("update controls shown without an update: %q", tt.Texts())
	}
}

func TestFoundUpdateOffersInstallAndReportsFailure(t *testing.T) {
	s := newTestScreen(t, preferences{})
	s.update = &mygo.Update{Version: "2.0.0"}
	finished := make(chan jobs.Status, 8)
	s.jobs.Subscribe(func(st jobs.Status) {
		if st.State != jobs.Running {
			finished <- st
		}
	})
	tt := newTester(s)
	tt.Frame()
	if !tt.HasText("Update available") {
		t.Fatalf("view = %q", tt.Texts())
	}
	// An update that was not made by Check has no signed manifest, so it cannot install.
	if err := tt.Click("Update available"); err != nil {
		t.Fatal(err)
	}
	if st := <-finished; st.State != jobs.Failed {
		t.Fatalf("final = %+v", st)
	}
	tt.Frame()
	if !tt.HasText("The update could not be installed.") || !tt.HasText("Update available") {
		t.Fatalf("failed view = %q", tt.Texts())
	}
}

func TestErrorReportSettingIsHiddenWithoutADSN(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	tt := newTester(s)
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("General"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("Send error reports") {
		t.Fatalf("error report setting shown without a DSN: %q", tt.Texts())
	}
}
