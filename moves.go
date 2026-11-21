package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/platform"

	"github.com/oneo-me/onevra/source/components"
)

// chooseMoveFolder asks where to move an Item's Portable app to. The dialog
// blocks until it closes, so it runs off the UI thread.
func (s *screen) chooseMoveFolder(e catalog.Entry) {
	in, ok := e.Installed()
	if !ok {
		return
	}
	s.browseFolder(locales.MoveChoose(e.Title()), in.Folder, func(dest string) { s.startMove(e, dest) })
}

// startMove moves the Item's Portable app to dest. A folder it cannot go to is
// refused, with the reason, before anything is queued.
func (s *screen) startMove(e catalog.Entry, dest string) {
	id := e.Item.ID
	s.background(func(ctx context.Context) error {
		rec, ok, err := s.previousInstall(ctx, e)
		switch {
		case err != nil:
			return fmt.Errorf("read the install of %s: %w", e.FullName(), err)
		case !ok:
			return nil
		}
		if err := install.CheckMove(s.moveRequest(rec, dest)); err != nil {
			s.toast(components.ToastError, moveRefusal(err, e))
			return nil
		}
		if _, working := s.opOf(id); working {
			return nil
		}
		rq := installRequest{itemID: id, tag: e.InstalledVersion(), kind: opMove}
		s.putOp(operation{itemID: id, kind: opMove, tag: rq.tag})
		err = s.lanes.Start(laneApply, applyJob(id), func(ctx context.Context, _ jobs.Report) error {
			return s.runMove(ctx, rq, dest)
		})
		if err != nil {
			s.takeOp(id)
			if !errors.Is(err, jobs.ErrStopped) {
				return fmt.Errorf("queue the move of %s: %w", e.FullName(), err)
			}
		}
		return nil
	})
}

// moveRequest is what moving the install to dest asks of the install package.
func (s *screen) moveRequest(rec install.Record, dest string) install.MoveRequest {
	folders, err := s.platform.Folders()
	if err != nil {
		log.Printf("locate the user's folders: %v", err)
	}
	return install.MoveRequest{Record: rec, Destination: dest, Protected: folders.Protected, Platform: s.platform}
}

// moveRefusal says why an app cannot be moved to the folder.
func moveRefusal(err error, e catalog.Entry) string {
	switch {
	case errors.Is(err, install.ErrNotMovable):
		return locales.MoveRefusedInstaller(e.Title())
	case errors.Is(err, install.ErrMoveMissing):
		return locales.MoveRefusedMissing()
	case errors.Is(err, install.ErrMoveSameFolder):
		return locales.MoveRefusedSame()
	case errors.Is(err, install.ErrMoveInsideSource):
		return locales.MoveRefusedInside()
	case errors.Is(err, install.ErrMoveNotEmpty):
		return locales.MoveRefusedNotEmpty()
	case errors.Is(err, install.ErrMoveNoSpace):
		return locales.MoveRefusedNoSpace()
	case errors.Is(err, install.ErrTooBroad):
		return locales.InstallFolderTooBroad()
	}
	return locales.MoveRefusedOther(err.Error())
}

func (s *screen) runMove(ctx context.Context, rq installRequest, dest string) error {
	e, ok := s.catalog.Get(rq.itemID)
	if !ok {
		s.takeOp(rq.itemID)
		return nil
	}
	s.changeOp(rq.itemID, func(o *operation) { o.stage = stageApplying })
	res, from, err := s.moveInstall(ctx, e, rq, dest)
	if err != nil {
		return s.operationFailed(ctx, e, rq, err)
	}
	bg := context.WithoutCancel(ctx)
	s.takeOp(rq.itemID)
	s.record(bg, activity.Success, rq.itemID, locales.ActivityMoved(e.FullName(), from, res.Record.Folder))
	s.toast(components.ToastSuccess, locales.ToastMoved(e.Title(), res.Record.Folder))
	if res.Warning != nil {
		log.Printf("repoint the shortcuts of %s: %v", e.FullName(), res.Warning)
		s.record(bg, activity.Warning, rq.itemID, locales.ActivityInstallWarning(e.FullName(), locales.WarningShortcutsRepoint(res.Warning.Error())))
	}
	if res.Left > 0 {
		s.toast(components.ToastWarning, locales.ToastMovedLeft(e.Title(), res.Left))
	}
	return nil
}

// moveInstall closes the program when it runs, moves its files and records
// where the app is now. It returns the folder the app was in.
func (s *screen) moveInstall(ctx context.Context, e catalog.Entry, rq installRequest, dest string) (install.MoveResult, string, error) {
	id := rq.itemID
	rec, ok, err := s.previousInstall(ctx, e)
	if err != nil {
		return install.MoveResult{}, "", err
	}
	if !ok || rec.Method != install.MethodPortable {
		return install.MoveResult{}, "", errNotPortable
	}
	reopen, err := s.closeRunning(ctx, e, id, rec.Folder, locales.QuestionRunningMoveBody(), locales.QuestionRunningMove(), true)
	if err != nil {
		return install.MoveResult{}, "", err
	}
	s.changeOp(id, func(o *operation) { o.committing = true })
	res, err := install.MovePortable(ctx, s.moveRequest(rec, dest))
	if err != nil {
		return install.MoveResult{}, "", err //nolint:wrapcheck // The install package's errors are matched by type.
	}
	if err := s.catalog.MoveInstall(context.WithoutCancel(ctx), id, res.Record.Folder, res.Record.Dedicated); err != nil {
		// The files are in the new folder but the Library does not know.
		return install.MoveResult{}, "", fmt.Errorf("record the move of %s: %w", e.FullName(), err)
	}
	if reopen && res.Record.MainExe != "" {
		path := filepath.Join(res.Record.Folder, filepath.FromSlash(res.Record.MainExe))
		if err := s.platform.Launch(path); err != nil && !errors.Is(err, platform.ErrNotSupported) {
			log.Printf("open %s again: %v", e.FullName(), err)
		}
	}
	return res, rec.Folder, nil
}
