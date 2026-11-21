// Release Sync installs, updates and removes software published as release
// assets on code-hosting sites.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/auron-labs/mygo-framework/database"
	"github.com/auron-labs/mygo-framework/errorreport"
	"github.com/auron-labs/mygo-framework/jobs"
	"github.com/auron-labs/mygo-framework/logging"
	"github.com/auron-labs/mygo-framework/settings"
	sqlitesettings "github.com/auron-labs/mygo-framework/settings/sqlite"
	"github.com/auron-labs/mygo-framework/state"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/backup"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/database/migrations"
	"github.com/aaronflorey/release-sync/source"

	mygomcp "github.com/auron-labs/mygo-mcp"
	"github.com/egoist/mygo"
)

const (
	// shutdownTimeout bounds how long quitting waits for background jobs to stop.
	shutdownTimeout = 5 * time.Second
	// saveDelay is how long preferences must stay unchanged before they are saved.
	saveDelay = 300 * time.Millisecond
	// saveTimeout bounds how long closing waits for unsaved preferences.
	saveTimeout = 2 * time.Second
	// logDirectory is where the rotating logs live, inside the cache directory.
	logDirectory = "logs"
)

func main() {
	if err := errorreport.Init(mygo.App.Version()); err != nil {
		log.Print(err)
	}
	if err := start(); err != nil {
		log.Fatal(err)
	}
}

func start() error {
	defer errorreport.Flush()
	defer errorreport.RecoverAndRepanic()
	mcpAddress := flag.String("mcp", "", "Serve an isolated headless UI over MCP at a loopback IP:port instead of opening a window")
	dataDir := flag.String("data-dir", "", "Directory for the database and logs; defaults to a directory under the user configuration and cache directories")
	flag.Parse()
	if *mcpAddress != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		err := mygomcp.Run(ctx, *mcpAddress, []mygomcp.Fixture{nativeFixture()})
		stop()
		if err != nil {
			return fmt.Errorf("serve MCP: %w", err)
		}
		return nil
	}
	err := run(*dataDir)
	if err != nil {
		errorreport.Capture(err, "run", "runtime")
	}
	return err
}

func run(dataDir string) (err error) {
	// A second launch with the same directories would open the database
	// twice; -data-dir runs a separate instance on purpose.
	useDefaultDirs := dataDir == ""
	d, err := resolveDirs(dataDir)
	if err != nil {
		return err
	}
	if useDefaultDirs && !mygo.App.RequestSingleInstanceLock() {
		return nil
	}
	logger, closeLogs, err := logging.New(logging.DefaultOptions(mygo.IsDev(), filepath.Join(d.cache, logDirectory)))
	if err != nil {
		return fmt.Errorf("open logs: %w", err)
	}
	defer func() { err = errors.Join(err, closeLogs.Close()) }()
	// The standard log package writes through the default logger, so the
	// existing log calls reach the rotating file.
	slog.SetDefault(logger)
	ctx := context.Background()
	// A restore the last run staged replaces the database before it is opened.
	// One that cannot be applied leaves the current database as it is.
	if applied, err := backup.ApplyPendingRestore(ctx, d.restore, d.database()); err != nil {
		log.Print(err)
		errorreport.Capture(err, "restore.apply", "runtime")
	} else if applied {
		log.Print("restored the library from a backup")
	}
	// Each part is added as it opens, so a failure partway closes what exists.
	var quit closers
	defer func() { err = errors.Join(err, quit.close()) }()
	db, values, err := openPreferences(ctx, d.config)
	if err != nil {
		return err
	}
	quit.database = db
	prefs, err := loadPreferences(ctx, values)
	if err != nil {
		return fmt.Errorf("load preferences: %w", err)
	}
	cell := state.NewCell(prefs)
	saver := state.Autosave(cell, saveDelay, savePreferences(values))
	quit.saver = saver
	jobManager := jobs.New(ctx)
	quit.jobs = jobManager
	tokens, err := newTokenStore()
	if err != nil {
		return err
	}
	svc, err := newServices(ctx, db.SQL, d, jobManager, tokens)
	if err != nil {
		return err
	}
	defer svc.avatars.Close()
	s := newScreen(cell, saver, jobManager, d, svc)
	errorreport.SetEnabled(prefs.ErrorReports)
	defer state.Select(cell, func(p preferences) bool { return p.ErrorReports }).Subscribe(errorreport.SetEnabled)()
	mygo.App.OnSecondInstance(func([]string, string) { s.showWindow() })
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { s.presence.quitting.Store(true) })
	mygo.App.OnNotificationClick(func(id string) { mygo.App.WhenReady(func() { s.notificationClicked(id) }) })
	mygo.App.SetName(appName)
	mygo.Theme.SetSource(mygo.ThemeSource(prefs.Theme))
	defer watchTheme(cell, setTheme)()
	mygo.App.WhenReady(func() {
		win := s.openWindow()
		s.win = win
		// Invalidate is safe from any goroutine; the view reads the job status and preferences itself.
		jobManager.Subscribe(func(jobs.Status) { win.Invalidate() })
		mygo.App.SetMenu(s.menu())
		win.OnClosed(cell.Subscribe(func(preferences) { win.Invalidate() }))
		win.OnClosed(saver.Err().Subscribe(func(error) { win.Invalidate() }))
		win.OnClosed(s.updateCount.Subscribe(func(int) { win.Invalidate() }))
		win.OnClosed(s.tryLaterCount.Subscribe(func(int) { win.Invalidate() }))
		win.OnClosed(s.catalog.Entries().Subscribe(func([]catalog.Entry) { win.Invalidate() }))
		win.OnClosed(s.catalog.Groups().Subscribe(func([]catalog.Group) { win.Invalidate() }))
		win.OnClosed(s.catalog.TryLaterEntries().Subscribe(func([]catalog.TryLater) { win.Invalidate() }))
		win.OnClosed(s.activity.Entries().Subscribe(func([]activity.Entry) { win.Invalidate() }))
		win.OnClosed(s.account.Subscribe(func(account) { win.Invalidate() }))
		win.OnClosed(s.devices.status.Subscribe(func(syncStatus) { win.Invalidate() }))
		win.OnClosed(s.transfers.Subscribe(func([]transfer) { win.Invalidate() }))
		win.OnClosed(s.operations.Subscribe(func([]operation) { win.Invalidate() }))
		win.OnClosed(s.github.AllowanceState().Subscribe(func(source.Allowance) { win.Invalidate() }))
		win.OnClosed(s.scanPage.results.Subscribe(func(scanResults) { win.Invalidate() }))
		s.avatars.SetNotify(win.Invalidate)
		// MyGo also calls OnClose during application quit, before destroying the window.
		win.OnClose(s.beforeClose)
		win.OnClosed(func() { mygo.App.Quit() })
		quit.background = s.startBackground(!useDefaultDirs)
		if !s.startsHidden() {
			win.OnReadyToShow(win.Show)
		}
		s.checkForUpdates()
		s.cleanDownloads()
		s.cleanTools()
		s.refreshAccount()
		s.refreshVirusTotalKey()
		s.scheduleStartupCheck(startupCheckDelay)
	})
	// A link that launched the app finds the window once it exists.
	mygo.App.OnOpenURL(func(string) { mygo.App.WhenReady(s.showWindow) })
	if err := mygo.App.Run(); err != nil {
		return fmt.Errorf("run native app: %w", err)
	}
	return nil
}

// savePreferences returns the autosave function for values. It reports each
// failure once; callers that receive the error from Flush must not report it
// again.
func savePreferences(values *settings.Values) func(context.Context, preferences) error {
	return func(ctx context.Context, p preferences) error {
		if err := p.save(ctx, values); err != nil {
			err = fmt.Errorf("save preferences: %w", err)
			log.Print(err)
			errorreport.Capture(err, "preferences.save", "ui")
			return err
		}
		return nil
	}
}

// beforeClose saves pending preferences and keeps the window open, showing the
// error, when that fails. When the save works and the window is closed rather
// than the application quit, it hides the window instead if the tray icon is
// there to bring it back; the saved preferences are the same either way. A
// quit sets quitting first, so it is never mistaken for a close.
func (s *screen) beforeClose(event *mygo.CloseEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	defer cancel()
	if err := s.saver.Flush(ctx); err != nil {
		// A failed save also cancels the quit this close was part of.
		s.presence.quitting.Store(false)
		event.PreventDefault()
		if s.win != nil {
			s.win.Invalidate()
		}
		return
	}
	if s.closesToTray() {
		event.PreventDefault()
		s.hide()
		s.tellAboutTray()
	}
}

// watchTheme calls apply with the preferred appearance whenever it changes.
func watchTheme(prefs *state.Cell[preferences], apply func(name string)) (stop func()) {
	return state.Select(prefs, func(p preferences) string { return p.Theme }).Subscribe(apply)
}

// setTheme switches the running application's appearance. SetSource is not
// documented as safe from other goroutines, so it runs on the main thread.
func setTheme(name string) {
	mygo.RunOnMain(func() { mygo.Theme.SetSource(mygo.ThemeSource(name)) })
}

// openPreferences opens the SQLite database in dir, applies pending
// migrations, and binds the typed preferences to it.
func openPreferences(ctx context.Context, dir string) (*database.Database, *settings.Values, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := database.Open(ctx, filepath.Join(dir, databaseFile), migrations.FS())
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	store, err := sqlitesettings.New(db.SQL)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("open preferences: %w", err), db.Close())
	}
	values, err := settings.New(store, themeKey, errorReportsKey, checkOnStartupKey, parallelChecksKey,
		defaultPrereleasesKey, lastFullCheckKey, useGitHubCLIKey, architectureKey, preferInstallersKey,
		downloadFolderKey, keepDownloadsKey, verifyDownloadsKey, installAfterAddingKey, portableFolderKey, toolsFolderKey,
		startMenuShortcutsKey, confirmRemoveKey, silentInstallsKey, installForEveryoneKey,
		startAtLoginKey, startMinimizedKey, showTrayIconKey, closeToTrayKey, trayNoticeShownKey,
		checkIntervalKey, notifyUpdatesKey, autoInstallKey, scanFoldersKey,
		discoverLightKey, discoverDownloadsOnlyKey, discoverIntroSeenKey, discoverTabKey, discoverCategoryKey, discoverCompareKey,
		libraryViewKey, autoGroupsKey, groupSectionsKey, scanDownloadsKey, checkProvenanceKey, useVirusTotalKey, safetyHintsKey,
		backupBeforeKey, libraryBackupKey, libraryFolderKey, libraryIncludeKey, libraryKeepKey, lastLibraryBackupKey,
		syncModeKey, syncGistKey, syncFolderKey, syncEncryptKey, syncDeviceIDKey, syncDeviceNameKey)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("register preferences: %w", err), db.Close())
	}
	return db, values, nil
}
