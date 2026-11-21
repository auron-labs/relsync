package main

import (
	"errors"
	"strconv"
	"strings"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
)

// installErrorText says why an operation failed, for the person, and whether
// the failure is ours to report: those of the network, the publisher and the
// person's own choices are not.
func installErrorText(err error, e catalog.Entry, rq installRequest) (text string, report bool) {
	var (
		rejected *rejectedError
		failed   *downloadError
		disk     *fetch.LocalError
		replace  *install.ReplaceError
		taken    *install.CommandTakenError
		setup    *install.SetupError
		msi      *install.MSIError
	)
	switch {
	case errors.As(err, &rejected):
		return rejected.text, rejected.cleanup != nil
	case errors.As(err, &failed):
		return downloadErrorText(failed.err), errors.As(failed.err, &disk)
	case errors.Is(err, errIntentBroken):
		return locales.DownloadChanged(e.Title(), rq.tag), false
	case errors.Is(err, errNoDownload):
		return locales.InstallErrorNoDownload(rq.tag), false
	case errors.Is(err, install.ErrStillListed):
		return locales.InstallErrorStillListed(), false
	case errors.Is(err, install.ErrNotListed):
		return locales.InstallErrorNoUninstaller(), false
	case errors.As(err, &setup):
		return locales.InstallErrorSetup(strconv.Itoa(setup.Code)), false
	case errors.As(err, &msi):
		return msiErrorText(e, msi), false
	case errors.As(err, &replace):
		if replace.Restored {
			return locales.InstallErrorReplace(), false
		}
		return locales.InstallErrorReplaceFailed(), true
	case errors.Is(err, install.ErrTooBroad):
		return locales.InstallFolderTooBroad(), false
	case isMoveRefusal(err):
		return moveRefusal(err, e), false
	case errors.Is(err, errNotPortable):
		return locales.InstallErrorNotPortable(), false
	case errors.As(err, &taken):
		return locales.InstallErrorTaken(taken.Command, taken.Owner), false
	case errors.Is(err, install.ErrNoProgram):
		return locales.InstallErrorNoProgram(), false
	case errors.Is(err, install.ErrUnsupportedFormat):
		return locales.InstallErrorFormat(), false
	case errors.Is(err, install.ErrUnsafeEntry):
		return locales.InstallErrorUnsafe(), false
	case errors.Is(err, install.ErrCorruptArchive):
		return locales.InstallErrorUnpack(err.Error()), false
	}
	return installErrorOther(err), true
}

// isMoveRefusal reports a reason an app cannot be moved to a folder.
func isMoveRefusal(err error) bool {
	for _, refusal := range []error{
		install.ErrNotMovable, install.ErrMoveMissing, install.ErrMoveSameFolder,
		install.ErrMoveInsideSource, install.ErrMoveNotEmpty, install.ErrMoveNoSpace,
	} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// msiErrorText explains a failed MSI install: the installer's own message when
// the log has one, otherwise the cause the log shows, otherwise the code. The
// log is named when there is one, to read.
func msiErrorText(e catalog.Entry, failure *install.MSIError) string {
	reason := failure.Diagnosis.Message
	if reason == "" {
		reason = msiCauseText(failure.Diagnosis.Cause, failure.Code)
	}
	text := locales.InstallErrorMSI(e.Title(), reason)
	if failure.Log != "" {
		text = locales.InstallErrorLog(text, failure.Log)
	}
	return text
}

func msiCauseText(cause install.MSICause, code int) string {
	switch cause {
	case install.CauseBlocked:
		return locales.InstallErrorMSIBlocked()
	case install.CauseAdministrator:
		return locales.InstallErrorMSIAdministrator()
	case install.CauseOldVersion:
		return locales.InstallErrorMSIOldVersion()
	case install.CauseOtherVersion:
		return locales.InstallErrorMSIOtherVersion()
	case install.CauseBusy:
		return locales.InstallErrorMSIBusy()
	case install.CauseFileInUse:
		return locales.InstallErrorMSIFileInUse()
	case install.CauseDiskSpace:
		return locales.InstallErrorMSIDiskSpace()
	case install.CauseCustomAction:
		return locales.InstallErrorMSICustomAction()
	case install.CauseUnknown:
	}
	return locales.InstallErrorMSICode(strconv.Itoa(code))
}

// cancelledText is the toast of an install or removal that was cancelled at
// the system's prompt or in the installer.
func cancelledText(rq installRequest) string {
	if rq.kind == opUninstall {
		return locales.ToastUninstallCancelled()
	}
	return locales.ToastInstallCancelled()
}

func installErrorOther(err error) string { return locales.InstallErrorOther(err.Error()) }

// failedActivity is the Activity line of an operation that failed.
func failedActivity(rq installRequest, e catalog.Entry, reason string) string {
	name := e.FullName()
	switch rq.kind {
	case opUpdate:
		return locales.ActivityUpdateFailed(name, rq.tag, reason)
	case opRollback:
		return locales.ActivityRollbackFailed(name, rq.tag, reason)
	case opUninstall:
		return locales.ActivityUninstallFailed(name, reason)
	case opRestore:
		return locales.ActivityRestoreFailed(name, rq.tag, reason)
	case opMove:
		return locales.ActivityMoveFailed(name, reason)
	case opInstall, opReinstall:
	}
	return locales.ActivityInstallFailed(name, rq.tag, reason)
}

// canceledActivity is the Activity line of an operation that was canceled.
func canceledActivity(op operation, e catalog.Entry) string {
	name := e.FullName()
	switch op.kind {
	case opUpdate:
		return locales.ActivityUpdateCanceled(name, op.tag)
	case opRollback:
		return locales.ActivityRollbackCanceled(name, op.tag)
	case opUninstall:
		return locales.ActivityUninstallCanceled(name)
	case opRestore:
		return locales.ActivityRestoreCanceled(name, op.tag)
	case opMove:
		return locales.ActivityMoveCanceled(name)
	case opInstall, opReinstall:
	}
	return locales.ActivityInstallCanceled(name, op.tag)
}

// succeededActivity is the Activity line of an install that finished.
func succeededActivity(rq installRequest, e catalog.Entry) string {
	name := e.FullName()
	switch rq.kind {
	case opUpdate:
		return locales.ActivityUpdated(name, rq.tag)
	case opRollback:
		return locales.ActivityRolledBack(name, rq.tag)
	case opReinstall:
		return locales.ActivityReinstalled(name, rq.tag)
	case opInstall, opUninstall, opRestore, opMove:
	}
	return locales.ActivityInstalled(name, rq.tag)
}

// succeededToast is the toast of an install that finished.
func succeededToast(rq installRequest, e catalog.Entry) string {
	title := e.Title()
	switch rq.kind {
	case opUpdate:
		return locales.ToastUpdated(title, rq.tag)
	case opRollback:
		return locales.ToastRolledBack(title, rq.tag)
	case opReinstall:
		return locales.ToastReinstalled(title, rq.tag)
	case opInstall, opUninstall, opRestore, opMove:
	}
	return locales.ToastInstalled(title, rq.tag)
}

// commandList names the commands a Tool install provides.
func commandList(rec install.Record) string {
	var names []string
	for _, f := range rec.Files {
		if f.Command != "" {
			names = append(names, f.Command)
		}
	}
	return strings.Join(names, ", ")
}

// stageText is the line that says where an operation is.
func (s *screen) stageText(op operation) string {
	switch op.stage {
	case stageQueued:
		return locales.DownloadQueued()
	case stageDownloading:
		switch {
		case op.done == 0 && op.speed == 0:
			return locales.DownloadStarting()
		case op.total > 0:
			return locales.DownloadProgress(formatSize(op.done), formatSize(op.total), formatSize(int64(op.speed)))
		}
		return locales.DownloadProgressUnknown(formatSize(op.done), formatSize(int64(op.speed)))
	case stageVerifying:
		return locales.DownloadVerifying()
	case stageWaiting:
		return locales.StageWaiting()
	case stageClosing:
		return locales.StageClosing()
	case stageApplying:
	}
	switch op.kind {
	case opUpdate:
		return locales.StageUpdating()
	case opRollback:
		return locales.StageRollingBack()
	case opUninstall:
		return locales.StageRemoving()
	case opRestore:
		return locales.StageRestoring()
	case opMove:
		return locales.StageMoving()
	case opInstall, opReinstall:
	}
	return locales.StageInstalling()
}

// operationFraction is how much of the download has arrived, or -1 while its
// size is unknown.
func operationFraction(op operation) float64 {
	return transferFraction(transfer{done: op.done, total: op.total})
}

// operationPill is the text of a Library card's pill while an operation runs.
func (s *screen) operationPill(op operation) string {
	if op.stage == stageDownloading && op.total > 0 && op.done > 0 {
		return locales.StatusDownloading(int(operationFraction(op) * 100))
	}
	if op.stage == stageDownloading {
		return locales.StatusDownloadingUnknown()
	}
	return s.stageText(op)
}
