package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/backup"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

const (
	libraryBackupJob  = "library-backup"
	libraryRestoreJob = "library-restore"
	// noticeBackup is the notification of a backup that could not be made.
	noticeBackup = "backup-unreachable"
	// backupStartDelay lets the application settle before an overdue backup runs.
	backupStartDelay = time.Minute
	// backupRetryAfter is how long a backup to a folder that cannot be reached waits to try again.
	backupRetryAfter = time.Hour
)

// databaseFile is the name of the database in the configuration directory.
const databaseFile = "app.db"

// database is the path of the database, which is opened by the application.
func (d dirs) database() string { return filepath.Join(d.config, databaseFile) }

// backupFolder is where Library backups go.
func (s *screen) backupFolder(p preferences) string {
	if p.LibraryBackupFolder != "" {
		return p.LibraryBackupFolder
	}
	return s.dirs.libraryBackups
}

// libraryBackupOverdue reports that a scheduled backup is due now: backups are
// on, and the last one is older than the interval or there has been none.
func libraryBackupOverdue(p preferences, now time.Time) bool {
	every := p.backupEvery()
	if every <= 0 {
		return false
	}
	last, ok := p.lastLibraryBackup()
	return !ok || now.Sub(last) >= every
}

// scheduledBackup is what the backup schedule runs when a backup is due. It
// does not block.
func (s *screen) scheduledBackup() { s.startLibraryBackup(true) }

// startLibraryBackup backs the Library up in the background. scheduled says
// the schedule asked for it, so nobody is told of a success.
func (s *screen) startLibraryBackup(scheduled bool) {
	err := s.jobs.Start(libraryBackupJob, func(ctx context.Context, _ jobs.Report) error {
		return s.backUpLibrary(ctx, scheduled)
	})
	if err != nil && !errors.Is(err, jobs.ErrRunning) && !errors.Is(err, jobs.ErrStopped) {
		log.Print(err)
	}
}

// portableApps lists the Portable apps whose files go in a backup that includes them.
func (s *screen) portableApps(ctx context.Context) ([]backup.AppFiles, error) {
	var apps []backup.AppFiles
	for _, e := range s.catalog.Entries().Get() {
		rec, ok, err := s.previousInstall(ctx, e)
		if err != nil {
			return nil, err
		}
		if ok && rec.Method == install.MethodPortable {
			apps = append(apps, backup.AppFiles{Name: e.Title(), Record: rec, Tag: e.InstalledVersion()})
		}
	}
	return apps, nil
}

// backUpLibrary makes one Library backup. A folder that cannot be reached is
// routine, so it is noted, tried again later and not reported; any other
// failure is returned to be.
func (s *screen) backUpLibrary(ctx context.Context, scheduled bool) error {
	p := s.prefs.Get()
	req := backup.LibraryRequest{
		Database: s.dirs.database(), Folder: s.backupFolder(p), Prefix: appSlug, Include: backup.Include(p.LibraryBackupInclude),
		Keep: p.LibraryBackupKeep, AppVersion: mygo.App.Version(), Now: time.Now(),
	}
	if req.Include == backup.WithApps {
		var err error
		if req.Apps, err = s.portableApps(ctx); err != nil {
			return fmt.Errorf("list the portable apps to back up: %w", err)
		}
	}
	res, err := backup.CreateLibrary(ctx, req)
	var unreachable *backup.UnreachableError
	switch {
	case err == nil:
		s.libraryBackedUp(ctx, res, scheduled)
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("back up the library: %w", ctx.Err())
	case errors.As(err, &unreachable):
		s.backupUnreachable(ctx, unreachable, scheduled)
		return nil
	}
	log.Printf("back up the library: %v", err)
	s.record(ctx, activity.Error, 0, locales.ActivityLibraryBackupFailed(err.Error()))
	if !scheduled {
		s.toast(components.ToastError, locales.ToastLibraryBackupFailed(err.Error()))
	}
	return fmt.Errorf("back up the library: %w", err)
}

// libraryBackedUp records a backup that was made.
func (s *screen) libraryBackedUp(ctx context.Context, res backup.LibraryResult, scheduled bool) {
	s.backupNotified.Store(false)
	s.prefs.Update(func(p *preferences) { p.LastLibraryBackup = res.Manifest.Created.Unix() })
	s.record(ctx, activity.Success, 0, locales.ActivityLibraryBackedUp(res.Path, formatSize(res.Size)))
	if len(res.Manifest.Skipped) > 0 {
		s.record(ctx, activity.Warning, 0, locales.ActivityLibraryBackupSkipped(strings.Join(res.Manifest.Skipped, ", ")))
	}
	if !scheduled {
		s.toast(components.ToastSuccess, locales.ToastLibraryBackedUp(filepath.Base(res.Path)))
	}
}

// backupUnreachable handles a folder that cannot be reached. A backup the
// person asked for says so each time; a scheduled one tries again later and
// says so once, until a backup works.
func (s *screen) backupUnreachable(ctx context.Context, err *backup.UnreachableError, scheduled bool) {
	log.Printf("back up the library: %v", err)
	if !scheduled {
		s.toast(components.ToastError, locales.ToastBackupUnreachable(err.Folder))
		return
	}
	s.backupSched.RetryIn(backupRetryAfter)
	if !s.backupNotified.CompareAndSwap(false, true) {
		return
	}
	s.record(ctx, activity.Warning, 0, locales.ActivityBackupUnreachable(err.Folder))
	s.notify(ctx, background.Notice{ID: noticeBackup, Title: locales.NotifyBackupTitle(), Body: locales.NotifyBackupBody(err.Folder)})
}

// chooseBackupFile asks for a Library backup to restore. The dialog blocks
// until it closes, so it runs off the UI thread.
func (s *screen) chooseBackupFile() {
	s.browse(mygo.OpenDialogOptions{
		Title: locales.RestoreChoose(), DefaultPath: s.backupFolder(s.prefs.Get()),
		Filters: []mygo.FileFilter{{Name: locales.BackupFiles(), Extensions: []string{"zip"}}},
	}, s.startLibraryRestore)
}

// startLibraryRestore restores the Library from the backup file in the background.
func (s *screen) startLibraryRestore(path string) {
	err := s.jobs.Start(libraryRestoreJob, func(ctx context.Context, _ jobs.Report) error {
		return s.restoreLibrary(ctx, path)
	})
	if err != nil && !errors.Is(err, jobs.ErrRunning) && !errors.Is(err, jobs.ErrStopped) {
		log.Print(err)
	}
}

// restoreLibrary replaces the Library with the backup's. The database is open
// in the running application, so the backup's is staged and put in place, with
// a safety copy of the current one, as the application starts again. Portable
// apps' files in the backup are put back first.
func (s *screen) restoreLibrary(ctx context.Context, path string) error {
	m, err := backup.ReadManifest(path)
	if err != nil {
		s.toast(components.ToastError, restoreRefusal(err))
		return nil
	}
	if _, err := s.ask(ctx, restoreQuestion(m)); err != nil {
		if errors.Is(err, errDeclined) {
			return nil
		}
		return fmt.Errorf("restore the library: %w", err)
	}
	if _, err := backup.StageRestore(ctx, path, s.dirs.restore); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("restore the library: %w", ctx.Err())
		}
		s.toast(components.ToastError, restoreRefusal(err))
		return nil
	}
	if err := s.restoreBackedUpApps(ctx, path, m); err != nil {
		if rmErr := removeStaged(s.dirs.restore); rmErr != nil {
			log.Print(rmErr)
		}
		log.Printf("restore the apps' files: %v", err)
		s.toast(components.ToastError, locales.ToastRestoreAppsFailed(err.Error()))
		return nil
	}
	log.Printf("restoring the library from %s", filepath.Base(path))
	s.relaunch()
	return nil
}

// restoreBackedUpApps closes the programs running from the folders of the
// backup's apps, which the question said would happen, and puts their files back.
func (s *screen) restoreBackedUpApps(ctx context.Context, path string, m backup.Manifest) error {
	if len(m.Apps) == 0 {
		return nil
	}
	folders, err := s.platform.Folders()
	if err != nil {
		log.Printf("locate the user's folders: %v", err)
	}
	for _, app := range m.Apps {
		if err := install.ValidateFolder(app.Folder, folders.Protected); err != nil {
			return fmt.Errorf("%s: %w", app.Name, err)
		}
	}
	for _, app := range m.Apps {
		if procs := s.runningIn(ctx, app.Folder); len(procs) > 0 {
			if err := s.platform.CloseProcesses(ctx, procs); err != nil {
				return fmt.Errorf("close %s: %w", app.Name, err)
			}
		}
	}
	return backup.RestoreApps(ctx, path, m, folders.Protected, s.platform) //nolint:wrapcheck // The backup package names the app.
}

// removeStaged deletes a restore that is not going to happen.
func removeStaged(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear the staged restore: %w", err)
	}
	return nil
}

// restoreQuestion asks to replace the Library with the backup's.
func restoreQuestion(m backup.Manifest) question {
	q := question{
		title: locales.QuestionRestoreLibraryTitle(), confirm: locales.QuestionRestoreLibraryConfirm(),
		lines:    []string{locales.QuestionRestoreLibraryBody(m.Created, m.Machine, m.AppVersion)},
		warnings: []string{locales.QuestionRestoreLibraryWarning(appName)},
	}
	if len(m.Apps) > 0 {
		q.lines = append(q.lines, locales.QuestionRestoreLibraryApps(len(m.Apps)))
	}
	return q
}

// restoreRefusal says why a file cannot be restored from.
func restoreRefusal(err error) string {
	switch {
	case errors.Is(err, backup.ErrNewerBackup):
		return locales.RestoreRefusedNewer(appName)
	case errors.Is(err, backup.ErrNotABackup):
		return locales.RestoreRefusedNotBackup(appName)
	}
	return locales.RestoreRefusedOther(err.Error())
}

// libraryBackupOptions are the choices of how often to back the Library up.
func libraryBackupOptions() []option {
	labels := map[string]string{
		"6h": locales.BackupEvery6h(), "12h": locales.BackupEvery12h(), "1d": locales.BackupEveryDay(), "3d": locales.BackupEvery3d(),
		"7d": locales.BackupEveryWeek(), "14d": locales.BackupEvery2w(), "30d": locales.BackupEveryMonth(), background.BackupOff: locales.BackupOff(),
	}
	options := make([]option, 0, len(background.BackupIntervals))
	for _, o := range background.BackupIntervals {
		options = append(options, option{o.Value, labels[o.Value]})
	}
	// Off comes first, then the shortest.
	return append(options[len(options)-1:], options[:len(options)-1]...)
}

// libraryKeepOptions are the choices of how many backups to keep.
func libraryKeepOptions() []option {
	options := make([]option, 0, len(backupsKept))
	for _, n := range backupsKept {
		if n == 0 {
			options = append(options, option{"0", locales.BackupKeepAll()})
			continue
		}
		options = append(options, option{strconv.Itoa(n), locales.BackupKeepNewest(n)})
	}
	return options
}

// dataSettingsGroups holds the Data section: the data folder and support
// bundle, the backups, and sharing with the user's other devices.
func (s *screen) dataSettingsGroups() []components.SettingsGroup {
	p := s.prefs.Get()
	appBackups := p.BackupBeforeChanges
	return []components.SettingsGroup{{ID: "data", Items: []components.SettingsItem{
		{
			ID: "open-data-folder", Title: locales.DataOpenFolder(), Description: locales.DataOpenFolderHint(),
			Build: func(c *ui.Context) {
				if components.Button(c, locales.DataOpenFolder(), components.ButtonSecondary).Clicked() {
					s.openDataFolder()
				}
			},
		},
		{
			ID: "support-bundle", Title: locales.SaveSupportBundle(), Description: locales.SupportBundleHint(),
			Build: func(c *ui.Context) {
				if components.Button(c, locales.SaveSupportBundle(), components.ButtonSecondary).Clicked() {
					s.saveSupportBundle()
				}
			},
		},
	}}, {ID: "backups", Title: locales.BackupsSettingsTitle(), Items: []components.SettingsItem{
		{
			ID: "backup-apps", Title: locales.BackupApps(), Description: locales.BackupAppsHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &appBackups, locales.BackupAppsLabel(), components.SwitchOptions{HideLabel: true})
				if appBackups != p.BackupBeforeChanges {
					s.prefs.Update(func(p *preferences) { p.BackupBeforeChanges = appBackups })
				}
			},
		},
		{
			ID: "backup-every", Title: locales.BackupEvery(), Description: locales.BackupEveryHint(),
			Build: func(c *ui.Context) {
				choice, changed := dropdown(c, locales.BackupEveryLabel(), p.LibraryBackupEvery, libraryBackupOptions())
				if changed {
					s.prefs.Update(func(p *preferences) { p.LibraryBackupEvery = choice })
				}
			},
		},
		{
			ID: "backup-folder", Title: locales.BackupFolder(), Description: locales.BackupFolderHint(), Stacked: true,
			Build: func(c *ui.Context) {
				folder := s.backupFolder(p)
				s.folderControl(c, folder, func() {
					s.browseFolder(locales.BackupFolderChoose(), folder, func(path string) {
						s.prefs.Update(func(p *preferences) { p.LibraryBackupFolder = path })
					})
				}, func() { s.openFolder(folder) })
			},
		},
		{
			ID: "backup-include", Title: locales.BackupInclude(), Description: locales.BackupIncludeHint(),
			Build: func(c *ui.Context) {
				choice, changed := dropdown(c, locales.BackupIncludeLabel(), p.LibraryBackupInclude, []option{
					{string(backup.LibraryOnly), locales.BackupIncludeLibrary()},
					{string(backup.WithApps), locales.BackupIncludeApps()},
				})
				if changed {
					s.prefs.Update(func(p *preferences) { p.LibraryBackupInclude = choice })
				}
			},
		},
		{
			ID: "backup-keep", Title: locales.BackupKeep(), Description: locales.BackupKeepHint(),
			Build: func(c *ui.Context) {
				choice, changed := dropdown(c, locales.BackupKeepLabel(), strconv.Itoa(p.LibraryBackupKeep), libraryKeepOptions())
				if changed {
					var n int
					if _, err := fmt.Sscan(choice, &n); err == nil {
						s.prefs.Update(func(p *preferences) { p.LibraryBackupKeep = n })
					}
				}
			},
		},
		{
			ID: "backup-now", Title: locales.BackupNow(), Description: s.lastBackupText(p), Stacked: true,
			Keywords: []string{"restore", "backup"},
			Build: func(c *ui.Context) {
				t := c.Theme()
				status, started := s.jobs.Status(libraryBackupJob)
				running := started && status.State == jobs.Running
				ui.Row(c).FillWidth().Wrap().Gap(t.Space(2)).Children(func() {
					if components.Button(c, locales.BackupNow(), components.ButtonPrimary).Key("backup-now").Disabled(running).Clicked() {
						s.startLibraryBackup(false)
					}
					if components.Button(c, locales.RestoreFromFile(), components.ButtonSecondary).Key("restore-file").Clicked() {
						s.chooseBackupFile()
					}
				})
			},
		},
	}}, s.syncSettingsGroup()}
}

// lastBackupText says when the Library was last backed up.
func (s *screen) lastBackupText(p preferences) string {
	if last, ok := p.lastLibraryBackup(); ok {
		return locales.BackupLast(last)
	}
	return locales.BackupNever()
}
