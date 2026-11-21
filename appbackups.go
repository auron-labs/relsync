package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/backup"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/platform"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// errNotPortable is an operation that only a Portable app can have.
var errNotPortable = errors.New("the app is not a portable install")

// appBackupDir is the folder of an Item's backups.
func (s *screen) appBackupDir(e catalog.Entry) string {
	return backup.AppDir(s.dirs.appBackups, e.Item.Owner, e.Item.Repo)
}

// backUpBeforeChange zips a Portable app's folder before it is updated or
// removed, when the person wants that. A backup that cannot be made, as one of
// a folder too large for it, is logged and never the reason the change fails.
func (s *screen) backUpBeforeChange(ctx context.Context, e catalog.Entry, rec install.Record, prefs preferences, reason backup.Reason) {
	if !prefs.BackupBeforeChanges || rec.Method != install.MethodPortable {
		return
	}
	_, err := backup.CreateApp(ctx, backup.AppRequest{
		Dir: s.appBackupDir(e), Record: rec, Tag: e.InstalledVersion(), Reason: reason, Now: time.Now(),
	})
	switch {
	case err == nil, ctx.Err() != nil:
	case errors.Is(err, backup.ErrTooLarge), errors.Is(err, backup.ErrNothingToBackUp):
		log.Printf("skip the backup of %s: %v", e.FullName(), err)
	default:
		log.Printf("back up %s: %v", e.FullName(), err)
	}
}

// refreshBackups reads an Item's backups from disk in the background and shows
// them in its drawer.
func (s *screen) refreshBackups(id int64) {
	e, ok := s.catalog.Get(id)
	if !ok || s.dirs.appBackups == "" {
		return
	}
	dir := s.appBackupDir(e)
	s.background(func(context.Context) error {
		list := backup.ListApp(dir)
		s.onUI(func() {
			if s.backups == nil {
				s.backups = map[int64][]backup.AppInfo{}
			}
			s.backups[id] = list
		})
		return nil
	})
}

// drawerBackups lists the Item's backups, newest first, with a button to
// restore each while the app is installed as a Portable app.
func (s *screen) drawerBackups(c *ui.Context, e catalog.Entry) {
	list := s.backups[e.Item.ID]
	if len(list) == 0 {
		return
	}
	t := c.Theme()
	in, managed := e.Installed()
	canRestore := managed && in.Method == install.MethodPortable
	_, working := s.opOf(e.Item.ID)
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.Text(c, locales.BackupsTitle()).Bold()
		for i, b := range list {
			ui.Row(c).Key(strconv.Itoa(i)).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					components.Text(c, backupTitle(b)).Wrap()
					components.Text(c, locales.BackupDetail(b.Created, formatSize(b.Size))).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
				})
				if canRestore {
					if components.Button(c, locales.BackupRestore(), components.ButtonSecondary).Key("restore").Disabled(working).Clicked() {
						s.startRestore(e, b)
					}
				}
			})
		}
	})
}

// backupTitle says which version a backup holds and why it was taken.
func backupTitle(b backup.AppInfo) string {
	if b.Reason == backup.BeforeRemoval {
		return locales.BackupBeforeRemoval(b.Tag)
	}
	return locales.BackupBeforeUpdate(b.Tag)
}

// startRestore queues putting an Item back from one of its backups, with the
// installs: nothing else changes its files while it runs.
func (s *screen) startRestore(e catalog.Entry, b backup.AppInfo) {
	id := e.Item.ID
	if _, working := s.opOf(id); working {
		return
	}
	rq := installRequest{itemID: id, tag: b.Tag, kind: opRestore}
	s.putOp(operation{itemID: id, kind: opRestore, tag: b.Tag})
	err := s.lanes.Start(laneApply, applyJob(id), func(ctx context.Context, _ jobs.Report) error {
		return s.runRestore(ctx, rq, b)
	})
	if err != nil {
		s.takeOp(id)
		if !errors.Is(err, jobs.ErrStopped) {
			log.Print(err)
		}
	}
}

func (s *screen) runRestore(ctx context.Context, rq installRequest, b backup.AppInfo) error {
	e, ok := s.catalog.Get(rq.itemID)
	if !ok {
		s.takeOp(rq.itemID)
		return nil
	}
	if err := s.restoreFrom(ctx, e, rq, b); err != nil {
		return s.operationFailed(ctx, e, rq, err)
	}
	bg := context.WithoutCancel(ctx)
	s.takeOp(rq.itemID)
	s.record(bg, activity.Success, rq.itemID, locales.ActivityRestored(e.FullName(), b.Tag))
	s.toast(components.ToastSuccess, locales.ToastRestored(e.Title(), b.Tag))
	return nil
}

// restoreFrom asks, closes the program when it runs, puts the backup's files
// back and records the version that is installed now.
func (s *screen) restoreFrom(ctx context.Context, e catalog.Entry, rq installRequest, b backup.AppInfo) error {
	id := rq.itemID
	if _, err := s.askOp(ctx, id, question{
		title: locales.QuestionRestoreTitle(e.Title()), lines: []string{locales.QuestionRestoreBody(b.Tag, appName)},
		confirm: locales.QuestionRestoreConfirm(),
	}); err != nil {
		return err
	}
	rec, ok, err := s.previousInstall(ctx, e)
	if err != nil {
		return err
	}
	if !ok || rec.Method != install.MethodPortable {
		return errNotPortable
	}
	folders, err := s.platform.Folders()
	if err != nil {
		log.Printf("locate the user's folders: %v", err)
	}
	if err := s.closeProgramsIn(ctx, id, rec.Folder); err != nil {
		return fmt.Errorf("close %s: %w", e.Title(), err)
	}
	s.changeOp(id, func(o *operation) { o.committing = true })
	restored, err := backup.RestoreApp(ctx, backup.RestoreRequest{Backup: b, Current: rec, Protected: folders.Protected, Platform: s.platform})
	if err != nil {
		return err //nolint:wrapcheck // The install package's errors are matched by type.
	}
	if err := s.catalog.RecordInstall(context.WithoutCancel(ctx), id, catalog.Installed{Tag: b.Tag, Record: restored}); err != nil {
		return fmt.Errorf("record the restore of %s: %w", e.FullName(), err)
	}
	return nil
}

// runningIn lists the programs running from a folder. Not knowing is not a
// reason to stop: a program in the way makes the replacement fail, and that
// puts the old files back.
func (s *screen) runningIn(ctx context.Context, folder string) []platform.Process {
	procs, err := s.platform.ProcessesIn(ctx, folder)
	if err != nil {
		if !errors.Is(err, platform.ErrNotSupported) {
			log.Printf("look for programs running from %s: %v", folder, err)
		}
		return nil
	}
	return procs
}

// closeProgramsIn closes what runs from a folder, the person having agreed to
// that in the question that came before.
func (s *screen) closeProgramsIn(ctx context.Context, id int64, folder string) error {
	procs := s.runningIn(ctx, folder)
	if len(procs) == 0 {
		return nil
	}
	s.changeOp(id, func(o *operation) { o.stage = stageClosing })
	if err := s.platform.CloseProcesses(ctx, procs); err != nil {
		return err //nolint:wrapcheck // The caller names the program.
	}
	s.changeOp(id, func(o *operation) { o.stage = stageApplying })
	return nil
}
