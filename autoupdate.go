package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/auron-labs/mygo-framework/errorreport"
	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
)

const (
	autoUpdateJob = "auto-update"
	// maxNotifiedNames is how many Items a notification names before saying how many more.
	maxNotifiedNames = 4
	// The IDs of notifications; showing one replaces an earlier one with its ID.
	noticeAvailable = "updates-available"
	noticeInstalled = "updates-installed"
	noticeTray      = "tray"
)

// afterCheck follows a full check: it installs the updates that may be
// installed unattended and tells the user about the rest. It is a job of its
// own so that installs do not keep the check busy. A pass still running is
// not interrupted: what it missed is handled after the next check.
func (s *screen) afterCheck(manual bool) {
	err := s.jobs.Start(autoUpdateJob, func(ctx context.Context, _ jobs.Report) error {
		return s.handleFoundUpdates(ctx, manual)
	})
	if err != nil && !errors.Is(err, jobs.ErrRunning) && !errors.Is(err, jobs.ErrStopped) {
		log.Print(err)
	}
}

// handleFoundUpdates deals with the updates the user has not been told about.
// Those of Items that update automatically are installed when Judge allows it,
// and the person hears afterwards what was updated. Every other update is
// announced once. A program that is running holds its update back until the
// next check, unannounced. A manual check is announced by its own toast, so it
// shows no notification.
func (s *screen) handleFoundUpdates(ctx context.Context, manual bool) error {
	pending := background.Unannounced(s.catalog.Entries().Get())
	if len(pending) == 0 {
		return nil
	}
	p := s.prefs.Get()
	var announce []catalog.Entry
	var queue []catalog.Entry
	for _, e := range pending {
		if !p.AutoInstall && !e.Item.AutoUpdate {
			announce = append(announce, e)
			continue
		}
		switch reason := s.judge(ctx, e); {
		case reason == background.Eligible:
			queue = append(queue, e)
		case reason.Announce():
			s.record(ctx, activity.Warning, e.Item.ID, notUpdatedText(e, reason, ""))
			announce = append(announce, e)
		}
	}
	updated, left, declined := s.installUnattended(ctx, queue)
	announce = append(announce, left...)

	told := map[int64]string{}
	for _, e := range slices.Concat(announce, declined) {
		offer, _ := e.LatestInstallable()
		told[e.Item.ID] = offer.Release.Tag
	}
	if len(told) > 0 {
		if err := s.catalog.SetNotifiedVersions(ctx, told); err != nil {
			return fmt.Errorf("record the updates announced: %w", err)
		}
	}
	if p.NotifyUpdates && !manual {
		s.notifyAbout(ctx, announce, updated)
	}
	return nil
}

// judge decides whether an Item's update may be installed with nobody watching.
func (s *screen) judge(ctx context.Context, e catalog.Entry) background.Reason {
	return background.Judge(background.Facts{Entry: e, Running: s.programRunning(ctx, e)})
}

// programRunning reports whether a program of the installed copy is running.
// A Tool is never held back by one: replacing its commands does not close them.
func (s *screen) programRunning(ctx context.Context, e catalog.Entry) bool {
	in, ok := e.Installed()
	if !ok || in.Method == install.MethodTool || in.Folder == "" {
		return false
	}
	procs, err := s.platform.ProcessesIn(ctx, in.Folder)
	if err != nil {
		// Not knowing is not a reason to hold back: the install looks again.
		log.Printf("look for programs running from %s: %v", in.Folder, err)
		return false
	}
	return len(procs) > 0
}

// installUnattended updates the Items through the normal pipeline, unattended,
// and waits for each to end. It returns those that were updated, those left for
// the person, and those the person canceled. An Item held back by a running
// program, or that could not be queued, is in none: it is tried again at the
// next check.
func (s *screen) installUnattended(ctx context.Context, queue []catalog.Entry) (updated, left, declined []catalog.Entry) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := map[int64]outcome{}
	for _, e := range queue {
		offer, _ := e.LatestInstallable()
		wg.Add(1)
		queued := s.queueInstall(e.Item.ID, offer.Release.Tag, true, func(o outcome) {
			mu.Lock()
			results[e.Item.ID] = o
			mu.Unlock()
			wg.Done()
		})
		if !queued {
			wg.Done()
		}
	}
	ended := make(chan struct{})
	go func() { wg.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-ctx.Done():
		return nil, nil, nil
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range queue {
		o, ok := results[e.Item.ID]
		switch {
		case !ok || o == outcomeRetry:
		case o == outcomeDone:
			updated = append(updated, e)
		case o == outcomeCanceled:
			declined = append(declined, e)
		default:
			left = append(left, e)
		}
	}
	return updated, left, declined
}

// notifyAbout shows the notification for the updates found and the one for the
// updates installed. A notification the system will not show is not an error.
func (s *screen) notifyAbout(ctx context.Context, found, installed []catalog.Entry) {
	if len(found) > 0 {
		s.notify(ctx, availableNotice(found))
	}
	if len(installed) > 0 {
		s.notify(ctx, installedNotice(installed))
	}
}

func (s *screen) notify(ctx context.Context, n background.Notice) {
	err := s.desktop.Notify(n)
	switch {
	case err == nil:
	case errors.Is(err, background.ErrNotifications):
		log.Printf("show notification: %v", err)
	case ctx.Err() == nil:
		log.Printf("show notification: %v", err)
		errorreport.Capture(err, "notify", "jobs")
	}
}

// availableNotice announces updates the user can install.
func availableNotice(entries []catalog.Entry) background.Notice {
	if len(entries) == 1 {
		return background.Notice{ID: noticeAvailable, Title: locales.NotifyUpdateTitle(), Body: entries[0].Title()}
	}
	return background.Notice{ID: noticeAvailable, Title: locales.NotifyUpdatesTitle(len(entries)), Body: namesText(entries)}
}

// installedNotice lists what was updated unattended.
func installedNotice(entries []catalog.Entry) background.Notice {
	return background.Notice{ID: noticeInstalled, Title: locales.NotifyUpdatedTitle(len(entries)), Body: namesText(entries)}
}

// namesText names the Items, in alphabetical order: up to maxNotifiedNames, then
// how many more.
func namesText(entries []catalog.Entry) string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Title()
	}
	slices.SortFunc(names, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	if len(names) <= maxNotifiedNames {
		return strings.Join(names, ", ")
	}
	return locales.NotifyNamesMore(strings.Join(names[:maxNotifiedNames], ", "), len(names)-maxNotifiedNames)
}

// setAutoInstall turns installing updates automatically on or off. Turning it
// on makes the updates already found new again, so the next check handles them
// rather than leaving them as announced.
func (s *screen) setAutoInstall(on bool) {
	s.prefs.Update(func(p *preferences) { p.AutoInstall = on })
	if !on {
		return
	}
	s.background(func(ctx context.Context) error {
		if err := s.catalog.ForgetNotifiedVersions(ctx); err != nil {
			return fmt.Errorf("turn on automatic updates: %w", err)
		}
		return nil
	})
}

// setAutoUpdate makes one Item install its updates automatically, or not.
func (s *screen) setAutoUpdate(e catalog.Entry, auto bool) {
	s.background(func(ctx context.Context) error {
		if err := s.catalog.SetAutoUpdate(ctx, e.Item.ID, auto); err != nil {
			return fmt.Errorf("set automatic updates of %s: %w", e.FullName(), err)
		}
		return nil
	})
}
