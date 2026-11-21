package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
	"sync"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"

	"github.com/oneo-me/onevra/source/components"
)

// opKind is what an operation does to an Item.
type opKind int

const (
	opInstall opKind = iota
	opUpdate
	opReinstall
	opRollback
	opUninstall
	// opRestore puts a Portable app back from one of its backups, and opMove
	// moves it to another folder.
	opRestore
	opMove
)

// opStage is where an operation is: queued, downloading, verifying, waiting for
// an answer, closing the program, applying.
type opStage int

const (
	stageQueued opStage = iota
	stageDownloading
	stageVerifying
	stageWaiting
	stageClosing
	stageApplying
)

// outcome is how an operation ended.
type outcome int

const (
	outcomeDone outcome = iota
	outcomeFailed
	outcomeCanceled
	// outcomeLeft is an unattended update that stopped for something only the
	// person can decide, and was left for them.
	outcomeLeft
	// outcomeRetry is an unattended update held back by a program that is
	// running, to be tried again at the next check.
	outcomeRetry
)

// operation is an install, update, rollback or removal under way on one Item.
// It lives in a state cell, shared with the goroutines that run it, until it
// ends.
type operation struct {
	itemID int64
	kind   opKind
	tag    string
	stage  opStage
	// committing says the machine is being changed, so the operation can no
	// longer be canceled.
	committing  bool
	done, total int64
	speed       float64
	// finished tells whoever started the operation how it ended; nil for none.
	finished func(outcome)
	// unattended says nobody is watching: it asks no questions, and stops at
	// the first thing that needs an answer.
	unattended bool
}

// canCancel reports an operation that can still be canceled with the machine untouched.
func (o operation) canCancel() bool { return !o.committing }

// offersCancel reports an operation whose Cancel button is shown. While it
// waits for an answer the question has the Cancel button.
func (o operation) offersCancel() bool { return o.canCancel() && o.stage != stageWaiting }

// installRequest is one install, update, reinstall or rollback.
type installRequest struct {
	itemID     int64
	tag        string
	kind       opKind
	unattended bool
}

// fetchJob names the job that downloads and verifies the release an operation
// installs; applyJob names the one that puts it on the machine.
func fetchJob(id int64) string { return "fetch:" + strconv.FormatInt(id, 10) }
func applyJob(id int64) string { return "apply:" + strconv.FormatInt(id, 10) }

func (s *screen) opOf(id int64) (operation, bool) {
	for _, op := range s.operations.Get() {
		if op.itemID == id {
			return op, true
		}
	}
	return operation{}, false
}

func (s *screen) putOp(op operation) {
	s.operations.Update(func(all *[]operation) { *all = append(slices.Clone(*all), op) })
}

func (s *screen) changeOp(id int64, change func(*operation)) {
	s.operations.Update(func(all *[]operation) {
		i := slices.IndexFunc(*all, func(o operation) bool { return o.itemID == id })
		if i < 0 {
			return
		}
		next := slices.Clone(*all)
		change(&next[i])
		*all = next
	})
}

// endOp ends an operation that has nothing to report and tells whoever started
// it that it failed.
func (s *screen) endOp(id int64) {
	if op, ok := s.takeOp(id); ok && op.finished != nil {
		op.finished(outcomeFailed)
	}
}

// takeOp removes the operation of an Item and returns it. Only one of the
// parties that may end an operation gets it.
func (s *screen) takeOp(id int64) (op operation, ok bool) {
	s.operations.Update(func(all *[]operation) {
		i := slices.IndexFunc(*all, func(o operation) bool { return o.itemID == id })
		if i < 0 {
			return
		}
		op, ok = (*all)[i], true
		*all = slices.Delete(slices.Clone(*all), i, i+1)
	})
	if ok {
		// The operation may have made a backup, or been the one that removed it.
		s.refreshBackups(id)
	}
	return op, ok
}

// askOp asks a question as part of an operation, which waits for you while it
// is shown.
func (s *screen) askOp(ctx context.Context, id int64, q question) (answer, error) {
	if s.isUnattended(id) {
		return answer{}, q.refusal()
	}
	var before opStage
	s.changeOp(id, func(o *operation) { before, o.stage = o.stage, stageWaiting })
	a, err := s.ask(ctx, q)
	if err == nil {
		s.changeOp(id, func(o *operation) { o.stage = before })
	}
	return a, err
}

// plan is what an install will do: the release and the file chosen from it.
type plan struct {
	release source.Release
	pick    selection.Candidate
	asset   source.Asset
}

// A failed plan, as errors the person can read.
var (
	errNoDownload     = errors.New("the release has no download for this PC")
	errChosenFileMiss = errors.New("the chosen file is not in the release")
)

// planInstall chooses the file to install from a release, applying the
// Item's stored choice as an update does. A stored choice that no longer
// describes any file is not guessed at: the person checks the file first.
func planInstall(e catalog.Entry, tag string) (plan, error) {
	release, ok := e.Release(tag)
	if !ok {
		return plan{}, fmt.Errorf("%w: %s", errNoDownload, tag)
	}
	result := e.Resolve(release)
	if result.IntentBroken {
		return plan{}, fmt.Errorf("%w: %s", errIntentBroken, tag)
	}
	pick, ok := result.Pick()
	if !ok {
		return plan{}, fmt.Errorf("%w: %s", errNoDownload, tag)
	}
	asset, ok := assetNamed(release, pick.Asset.Name)
	if !ok {
		return plan{}, errChosenFileMiss
	}
	return plan{release: release, pick: pick, asset: asset}, nil
}

// errIntentBroken is a stored choice of file that describes nothing in the release.
var errIntentBroken = errors.New("the chosen file is not in the release")

func opKindOf(r catalog.Relation) opKind {
	switch r {
	case catalog.RelationNewer:
		return opUpdate
	case catalog.RelationSame:
		return opReinstall
	case catalog.RelationOlder:
		return opRollback
	case catalog.RelationFresh:
	}
	return opInstall
}

// startInstall queues installing the release with tag: a first install, an
// update, a reinstall or a rollback, by how it compares with what is
// installed. finished, if set, hears how it ended. It reports whether the
// operation was queued.
func (s *screen) startInstall(id int64, tag string, finished func(outcome)) bool {
	return s.queueInstall(id, tag, false, finished)
}

// queueInstall is startInstall for an install that may run unattended.
func (s *screen) queueInstall(id int64, tag string, unattended bool, finished func(outcome)) bool {
	e, ok := s.catalog.Get(id)
	if !ok {
		return false
	}
	if _, working := s.opOf(id); working {
		return false
	}
	rq := installRequest{itemID: id, tag: tag, kind: opKindOf(e.Relation(tag)), unattended: unattended}
	s.putOp(operation{itemID: id, kind: rq.kind, tag: tag, finished: finished, unattended: unattended})
	err := s.lanes.Start(laneDownload, fetchJob(id), func(ctx context.Context, report jobs.Report) error {
		return s.runFetch(ctx, report, rq)
	})
	if err != nil {
		s.takeOp(id)
		if !errors.Is(err, jobs.ErrStopped) {
			log.Print(err)
		}
		return false
	}
	return true
}

// cancelOperation stops an operation that has not begun to change the machine.
func (s *screen) cancelOperation(id int64) {
	op, ok := s.opOf(id)
	if !ok || !op.canCancel() {
		return
	}
	for _, name := range []string{fetchJob(id), applyJob(id)} {
		queued := s.lanes.Queued(name)
		// A job that never left the queue never runs, so nothing else would say it ended.
		if s.jobs.Cancel(name) && queued {
			s.operationCanceled(context.Background(), id)
		}
	}
}

// runFetch is the first job of an install: download and verify the release's
// file, then hand it to the apply lane. Only failures that are not the
// network's or the publisher's fault are returned, so the job manager reports
// those and nothing routine.
func (s *screen) runFetch(ctx context.Context, report jobs.Report, rq installRequest) error {
	e, ok := s.catalog.Get(rq.itemID)
	if !ok {
		s.endOp(rq.itemID) // Stopped tracking while it waited.
		return nil
	}
	s.changeOp(rq.itemID, func(o *operation) { o.stage = stageDownloading })
	if err := s.catalog.SetInstallError(ctx, rq.itemID, ""); err != nil && ctx.Err() == nil {
		log.Print(err)
	}
	p, err := planInstall(e, rq.tag)
	if err != nil {
		return s.operationFailed(ctx, e, rq, err)
	}
	if rq.kind == opUpdate && e.Item.LinkGuessed {
		if err := s.confirmGuessedLink(ctx, e); err != nil {
			return s.operationFailed(ctx, e, rq, err)
		}
	}
	if rq.kind == opRollback {
		if _, err := s.askOp(ctx, rq.itemID, question{
			title:   locales.QuestionDowngradeTitle(rq.tag),
			lines:   []string{locales.QuestionDowngradeBody(e.Title(), e.InstalledVersion(), rq.tag)},
			confirm: locales.QuestionDowngradeConfirm(),
		}); err != nil {
			return s.operationFailed(ctx, e, rq, err)
		}
	}
	prefs := s.prefs.Get()
	d := download{
		key: fetchJob(rq.itemID), entry: e, release: p.release, asset: p.asset, root: s.downloadRoot(prefs),
		checks: verifyChecks(prefs),
	}
	file, result, err := s.fetchVerified(ctx, d,
		func(pr fetch.Progress) {
			s.changeOp(rq.itemID, func(o *operation) { o.done, o.total, o.speed = pr.Done, pr.Total, pr.BytesPerSecond })
			report(int(pr.Done>>10), int(pr.Total>>10), "")
		},
		func() { s.changeOp(rq.itemID, func(o *operation) { o.stage = stageVerifying }) })
	if err != nil {
		return s.operationFailed(ctx, e, rq, &downloadError{err: err})
	}
	if result.Verdict == fetch.VerdictStop {
		err := &rejectedError{text: rejectedText(result)}
		if removeErr := os.Remove(file.Path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err.cleanup = removeErr
		}
		return s.operationFailed(ctx, e, rq, err)
	}
	s.changeOp(rq.itemID, func(o *operation) { o.stage, o.done, o.total = stageQueued, 0, 0 })
	err = s.lanes.Start(laneApply, applyJob(rq.itemID), func(ctx context.Context, _ jobs.Report) error {
		return s.runApply(ctx, rq, p, file, result)
	})
	if err != nil {
		return s.operationFailed(ctx, e, rq, fmt.Errorf("queue the install: %w", err))
	}
	return nil
}

// confirmGuessedLink asks whether a repository matched by name is the right
// project before an update from it replaces the program, and on yes keeps the
// answer. Unattended updates never ask: they refuse.
func (s *screen) confirmGuessedLink(ctx context.Context, e catalog.Entry) error {
	if _, err := s.askOp(ctx, e.Item.ID, question{
		title:   locales.QuestionLinkTitle(e.FullName()),
		lines:   []string{locales.QuestionLinkBody(appName, e.Title(), source.RepoURL(e.Ref()))},
		confirm: locales.QuestionLinkConfirm(), unattended: background.LinkUnconfirmed,
	}); err != nil {
		return err
	}
	if err := s.catalog.SetLinkVerified(context.WithoutCancel(ctx), e.Item.ID); err != nil && !errors.Is(err, catalog.ErrNotFound) {
		return fmt.Errorf("keep the confirmed link of %s: %w", e.FullName(), err)
	}
	return nil
}

// downloadError is a download that did not finish.
type downloadError struct{ err error }

func (e *downloadError) Error() string { return e.err.Error() }
func (e *downloadError) Unwrap() error { return e.err }

// rejectedError is a download verification stopped and deleted.
type rejectedError struct {
	text string
	// cleanup is why the file could not be deleted, if it could not.
	cleanup error
}

func (e *rejectedError) Error() string {
	if e.cleanup != nil {
		return "download rejected: " + e.text + "; " + e.cleanup.Error()
	}
	return "download rejected: " + e.text
}

// runApply is the second job: the questions, then putting the release on the
// machine.
func (s *screen) runApply(ctx context.Context, rq installRequest, p plan, file fetch.File, result fetch.Result) error {
	e, ok := s.catalog.Get(rq.itemID)
	if !ok {
		s.endOp(rq.itemID)
		return nil
	}
	s.changeOp(rq.itemID, func(o *operation) { o.stage = stageApplying })
	done, err := s.applyInstall(ctx, s.installContext(e, rq, p), file, result)
	if err != nil {
		return s.operationFailed(ctx, e, rq, err)
	}
	return s.operationSucceeded(ctx, e, rq, done)
}

// operationFailed ends an operation that did not finish. A canceled or
// declined one leaves a note in Activity; a failed one is stored on the Item
// and shown. The returned error is for the job manager, which reports it.
func (s *screen) operationFailed(ctx context.Context, e catalog.Entry, rq installRequest, err error) error {
	var needs *needsUserError
	if errors.As(err, &needs) {
		s.operationLeft(ctx, e, rq, needs)
		return nil
	}
	cancelled := errors.Is(err, install.ErrCancelled)
	if errors.Is(err, errDeclined) || cancelled || ctx.Err() != nil {
		if cancelled {
			s.toast(components.ToastInfo, cancelledText(rq))
		}
		s.operationCanceled(ctx, rq.itemID)
		if ctx.Err() != nil {
			return fmt.Errorf("%s %s: %w", e.FullName(), rq.tag, context.Cause(ctx))
		}
		return nil
	}
	op, ok := s.takeOp(rq.itemID)
	if !ok {
		return nil
	}
	bg := context.WithoutCancel(ctx)
	text, report := installErrorText(err, e, rq)
	log.Printf("%s %s: %v", e.FullName(), rq.tag, err)
	if err := s.catalog.SetInstallError(bg, rq.itemID, text); err != nil && !errors.Is(err, catalog.ErrNotFound) {
		log.Print(err)
	}
	s.record(bg, activity.Error, rq.itemID, failedActivity(rq, e, text))
	if !rq.unattended {
		s.toast(components.ToastError, text)
	}
	if op.finished != nil {
		op.finished(outcomeFailed)
	}
	if report {
		return fmt.Errorf("%s %s: %w", e.FullName(), rq.tag, err)
	}
	return nil
}

// operationCanceled ends an operation the person canceled or declined to go on
// with.
func (s *screen) operationCanceled(ctx context.Context, id int64) {
	op, ok := s.takeOp(id)
	if !ok {
		return
	}
	bg := context.WithoutCancel(ctx)
	if e, found := s.catalog.Get(id); found {
		s.record(bg, activity.Info, id, canceledActivity(op, e))
	}
	if op.finished != nil {
		op.finished(outcomeCanceled)
	}
}

// operationSucceeded records a finished install and tells the person.
func (s *screen) operationSucceeded(ctx context.Context, e catalog.Entry, rq installRequest, done applied) error {
	// The machine has changed whether or not the person canceled as it finished.
	bg := context.WithoutCancel(ctx)
	err := s.catalog.RecordInstall(bg, rq.itemID, catalog.Installed{Tag: rq.tag, Record: done.record, Kind: done.kind, Intent: done.intent})
	if err != nil {
		// The files are in place but the Library does not know: report it.
		s.endOp(rq.itemID)
		text := installErrorOther(err)
		if !rq.unattended {
			s.toast(components.ToastError, text)
		}
		return fmt.Errorf("record install of %s %s: %w", e.FullName(), rq.tag, err)
	}
	op, _ := s.takeOp(rq.itemID)
	s.record(bg, activity.Success, rq.itemID, succeededActivity(rq, e))
	if !rq.unattended {
		s.toast(components.ToastSuccess, succeededToast(rq, e))
	}
	for _, w := range done.warnings {
		s.record(bg, activity.Warning, rq.itemID, locales.ActivityInstallWarning(e.FullName(), w))
	}
	for _, shadow := range done.shadowed {
		text := locales.WarningShadowed(shadow.Command, shadow.Folder)
		s.record(bg, activity.Warning, rq.itemID, locales.ActivityInstallWarning(e.FullName(), text))
		if !rq.unattended {
			s.toast(components.ToastWarning, text)
		}
	}
	if done.restart {
		s.toast(components.ToastWarning, locales.ToastRestartNeeded(e.Title()))
		s.record(bg, activity.Warning, rq.itemID, locales.ActivityRestartNeeded(e.FullName()))
	}
	if !done.record.Removable() {
		s.record(bg, activity.Warning, rq.itemID, locales.ActivityUnlisted(e.FullName()))
	}
	if done.pathAdded {
		s.toast(components.ToastInfo, locales.ToastNewTerminal(commandList(done.record)))
	}
	if done.reopen != "" {
		if err := s.platform.Launch(done.reopen); err != nil {
			log.Printf("open %s again: %v", e.FullName(), err)
			s.record(bg, activity.Warning, rq.itemID, locales.ActivityInstallWarning(e.FullName(), locales.WarningReopen(e.Title(), err.Error())))
		}
	}
	if op.finished != nil {
		op.finished(outcomeDone)
	}
	return nil
}

// updateAll updates every Item that has an update, one at a time, and says how
// many were updated and how many need attention.
func (s *screen) updateAll() {
	var queue []catalog.Entry
	attention := 0
	for _, e := range s.catalog.Entries().Get() {
		switch {
		case !e.HasUpdate():
		case e.NeedsAttention():
			attention++
		default:
			queue = append(queue, e)
		}
	}
	if len(queue) == 0 && attention == 0 {
		s.toast(components.ToastSuccess, locales.ToastUpToDate())
		return
	}
	total := &updateTally{pending: len(queue), attention: attention}
	for _, e := range queue {
		latest, _ := e.LatestInstallable()
		started := s.startInstall(e.Item.ID, latest.Release.Tag, total.finish(s))
		if !started {
			total.finish(s)(outcomeFailed)
		}
	}
	if len(queue) == 0 {
		total.report(s)
	}
}

// updateTally counts how an update of everything is going.
type updateTally struct {
	mu                          sync.Mutex
	pending, updated, attention int
}

// finish returns the callback that hears how one operation of the update ended.
func (t *updateTally) finish(s *screen) func(outcome) {
	return func(o outcome) {
		t.mu.Lock()
		t.pending--
		if o == outcomeDone {
			t.updated++
		} else {
			t.attention++
		}
		last := t.pending == 0
		t.mu.Unlock()
		if last {
			t.report(s)
		}
	}
}

// report says how the update went, in a toast and in Activity.
func (t *updateTally) report(s *screen) {
	t.mu.Lock()
	updated, attention := t.updated, t.attention
	t.mu.Unlock()
	text, level := locales.ToastUpdateAll(updated), activity.Success
	kind := components.ToastSuccess
	if attention > 0 {
		text, level, kind = locales.ToastUpdateAllAttention(updated, attention), activity.Warning, components.ToastWarning
	}
	s.toast(kind, text)
	s.background(func(ctx context.Context) error {
		s.record(ctx, level, 0, text)
		return nil
	})
}
