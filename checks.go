package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/auron-labs/mygo-framework/errorreport"
	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/oneo-me/onevra/source/components"
)

const (
	checkAllJob = "check-all"
	// startupCheckDelay lets the window settle before the first checks start.
	startupCheckDelay = 5 * time.Second
)

// countUpdates is how many Items have an update the user should act on.
func countUpdates(entries []catalog.Entry) int {
	n := 0
	for _, e := range entries {
		if e.HasUpdate() {
			n++
		}
	}
	return n
}

// checkLibrary checks every Item for new releases in the background. manual
// says the user asked, which earns a toast with the result.
func (s *screen) checkLibrary(manual bool) {
	if len(s.catalog.Entries().Get()) == 0 {
		if manual {
			s.toast(components.ToastInfo, locales.CheckAddFirst())
		}
		return
	}
	parallel := s.prefs.Get().ParallelChecks
	err := s.jobs.Start(checkAllJob, func(ctx context.Context, report jobs.Report) error {
		return s.runLibraryCheck(ctx, report, parallel, manual)
	})
	if err != nil && !errors.Is(err, jobs.ErrRunning) && !errors.Is(err, jobs.ErrStopped) {
		log.Print(err)
	}
}

// runLibraryCheck is the check-all job: it reports progress through the job's
// status, then records the outcome.
func (s *screen) runLibraryCheck(ctx context.Context, report jobs.Report, parallel int, manual bool) error {
	sum, err := s.checker.CheckAll(ctx, false, parallel, report)
	if err != nil {
		if ctx.Err() == nil {
			s.record(ctx, activity.Error, 0, locales.ActivityActionFailed(appName, err.Error()))
			s.toast(components.ToastError, locales.ActivityActionFailed(appName, err.Error()))
		}
		return fmt.Errorf("check the library: %w", err)
	}
	s.prefs.Update(func(p *preferences) { p.LastFullCheck = time.Now().Unix() })
	s.checkOutsideUpdates(ctx)
	updates := countUpdates(s.catalog.Entries().Get())
	switch {
	case sum.RateLimited():
		text := errorText(sum.Limit)
		s.record(ctx, activity.Warning, 0, locales.ActivityRateLimited())
		s.toast(components.ToastWarning, text)
	case sum.Failed > 0:
		s.record(ctx, activity.Warning, 0, locales.ActivityChecked(sum.Checked, updates)+" "+locales.ActivityCheckedFailures(sum.Failed))
	default:
		s.record(ctx, activity.Info, 0, locales.ActivityChecked(sum.Checked, updates))
	}
	if !sum.RateLimited() {
		s.afterCheck(manual)
	}
	if manual && !sum.RateLimited() {
		if updates == 0 {
			s.toast(components.ToastSuccess, locales.ToastUpToDate())
		} else {
			s.toast(components.ToastInfo, locales.ToastUpdatesAvailable(updates))
		}
	}
	return nil
}

// checkItem checks one Item now, ignoring its stored ETag. Its pill shows the
// progress while the job runs.
func (s *screen) checkItem(id int64, force bool) {
	e, ok := s.catalog.Get(id)
	if !ok {
		return
	}
	err := s.checker.CheckItem(id, force, func(ctx context.Context, res catalog.CheckResult, err error) {
		switch {
		case err != nil && !errors.Is(err, context.Canceled):
			s.record(ctx, activity.Error, id, locales.ActivityActionFailed(e.FullName(), err.Error()))
		case res.Failure != nil:
			s.record(ctx, activity.Error, id, locales.ActivityCheckFailed(e.FullName(), errorText(res.Failure)))
		default:
			s.checkOutsideUpdates(ctx)
		}
	})
	if err != nil && !errors.Is(err, jobs.ErrRunning) && !errors.Is(err, jobs.ErrStopped) {
		log.Print(err)
		errorreport.Capture(err, "check.item", "ui")
	}
}

// scheduleStartupCheck starts the update schedule after delay, which lets the
// window settle. With the setting on it also checks then, due or not. The
// returned function stops the schedule.
func (s *screen) scheduleStartupCheck(delay time.Duration) (stop func()) {
	s.sched.Start(delay, s.prefs.Get().CheckOnStartup)
	return s.sched.Stop
}
