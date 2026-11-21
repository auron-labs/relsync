package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/auron-labs/mygo-framework/errorreport"
	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/selection"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// onUI runs fn on the UI thread. Without a window, as in tests and the MCP
// harness, it runs at the start of the next frame built by view.
func (s *screen) onUI(fn func()) {
	if s.win != nil {
		s.win.Update(fn)
		return
	}
	s.queueMu.Lock()
	s.queued = append(s.queued, fn)
	s.queueMu.Unlock()
}

// runQueuedUI runs what onUI queued for a screen without a window.
func (s *screen) runQueuedUI() {
	s.queueMu.Lock()
	queued := s.queued
	s.queued = nil
	s.queueMu.Unlock()
	for _, fn := range queued {
		fn()
	}
}

// toast shows a message from any goroutine.
func (s *screen) toast(kind, title string) {
	s.onUI(func() { s.queueToast(ui.Toast{Title: title, Type: kind}) })
}

// record appends to the activity log. It writes to the database, so it belongs
// in a job, never in a view.
func (s *screen) record(ctx context.Context, level activity.Level, itemID int64, message string) {
	if err := s.activity.Add(ctx, level, itemID, message); err != nil && ctx.Err() == nil {
		log.Print(err)
		errorreport.Capture(err, "activity.add", "jobs")
	}
}

// mutate changes an Item in the background and records message in the
// activity log, unless it is empty. A failed change is recorded as an error and
// shown.
func (s *screen) mutate(e catalog.Entry, change func(ctx context.Context) error, message string) {
	s.background(func(ctx context.Context) error {
		if err := change(ctx); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("update %s: %w", e.FullName(), ctx.Err())
			}
			s.record(ctx, activity.Error, e.Item.ID, locales.ActivityActionFailed(e.FullName(), err.Error()))
			s.toast(components.ToastError, locales.ActivityActionFailed(e.FullName(), err.Error()))
			return fmt.Errorf("update %s: %w", e.FullName(), err)
		}
		if message != "" {
			s.record(ctx, activity.Info, e.Item.ID, message)
		}
		return nil
	})
}

// background runs fn as a job of its own, off the UI thread and tracked by the
// job manager so shutdown waits for it.
func (s *screen) background(fn func(ctx context.Context) error) {
	name := fmt.Sprintf("action:%d", s.actions.Add(1))
	err := s.jobs.Start(name, func(ctx context.Context, _ jobs.Report) error { return fn(ctx) })
	if err != nil && !errors.Is(err, jobs.ErrStopped) {
		log.Print(err)
	}
}

// policyChanged chooses downloads again when the settings that decide them
// change. Subscribers must not block, and the catalog may be mid-write.
func (s *screen) policyChanged(p preferences) {
	policy := machinePolicy(p)
	if policy == s.catalog.Policy() {
		return
	}
	s.background(func(context.Context) error {
		s.catalog.SetPolicy(policy)
		return nil
	})
}

func (s *screen) markInstalled(e catalog.Entry) {
	latest, ok := e.Latest()
	if !ok {
		return
	}
	s.mutate(e, func(ctx context.Context) error { return s.catalog.MarkInstalled(ctx, e.Item.ID, "") },
		locales.ActivityMarkedInstalled(e.FullName(), latest.Tag))
}

func (s *screen) skipVersion(e catalog.Entry) {
	latest, ok := e.LatestInstallable()
	if !ok {
		return
	}
	tag := latest.Release.Tag
	s.mutate(e, func(ctx context.Context) error { return s.catalog.SetSkippedVersion(ctx, e.Item.ID, tag) },
		locales.ActivitySkipped(e.FullName(), tag))
}

func (s *screen) stopSkipping(e catalog.Entry) {
	s.mutate(e, func(ctx context.Context) error { return s.catalog.SetSkippedVersion(ctx, e.Item.ID, "") },
		locales.ActivityStoppedSkipping(e.FullName()))
}

func (s *screen) setHoldUpdates(e catalog.Entry, hold bool) {
	s.background(func(ctx context.Context) error {
		if err := s.catalog.SetHoldUpdates(ctx, e.Item.ID, hold); err != nil {
			return fmt.Errorf("hold updates of %s: %w", e.FullName(), err)
		}
		return nil
	})
}

// setIncludePrereleases changes what the Item tracks, then checks it again at
// once. The check follows the change so it judges the Item by the new choice.
func (s *screen) setIncludePrereleases(e catalog.Entry, include bool) {
	s.background(func(ctx context.Context) error {
		if err := s.catalog.SetIncludePrereleases(ctx, e.Item.ID, include); err != nil {
			return fmt.Errorf("set pre-releases of %s: %w", e.FullName(), err)
		}
		s.checkItem(e.Item.ID, false)
		return nil
	})
}

// setFile keeps the file the user chose for an Item; nil goes back to taking
// the best match.
func (s *screen) setFile(e catalog.Entry, file *selection.Candidate) {
	var intent *selection.Intent
	message := locales.ActivityFileAutomatic(e.FullName())
	if file != nil {
		chosen := selection.IntentOf(*file, true)
		intent, message = &chosen, locales.ActivityFileChosen(file.Asset.Name, e.FullName())
	}
	s.mutate(e, func(ctx context.Context) error {
		defer s.logSelection(e.Item.ID)
		return s.catalog.SetIntent(ctx, e.Item.ID, intent)
	}, message)
}

// setKind says what an Item is, "" to detect it again.
func (s *screen) setKind(e catalog.Entry, kind selection.ItemKind) {
	label := locales.InstallAsAutomatic()
	switch kind {
	case selection.ItemApp:
		label = locales.InstallAsApp()
	case selection.ItemTool:
		label = locales.InstallAsTool()
	}
	s.mutate(e, func(ctx context.Context) error {
		defer s.logSelection(e.Item.ID)
		return s.catalog.SetKind(ctx, e.Item.ID, kind)
	}, locales.ActivityKindChanged(e.FullName(), label))
}

// setMethod limits how an App may be installed, "" for any way.
func (s *screen) setMethod(e catalog.Entry, method selection.Method) {
	label := locales.MethodAutomatic()
	switch method {
	case selection.MethodInstaller:
		label = locales.MethodInstaller()
	case selection.MethodPortable:
		label = locales.MethodPortable()
	case selection.MethodAutomatic:
	}
	s.mutate(e, func(ctx context.Context) error {
		defer s.logSelection(e.Item.ID)
		return s.catalog.SetMethod(ctx, e.Item.ID, method)
	}, locales.ActivityMethodChanged(e.FullName(), label))
}

// setInstallerArgs keeps the user's own arguments for the Item's setup program.
func (s *screen) setInstallerArgs(e catalog.Entry, args string) {
	s.background(func(ctx context.Context) error {
		if err := s.catalog.SetInstallerArgs(ctx, e.Item.ID, args); err != nil {
			return fmt.Errorf("set installer arguments of %s: %w", e.FullName(), err)
		}
		return nil
	})
}

// setInstallForEveryone makes the Item install for every user of the PC, or not.
func (s *screen) setInstallForEveryone(e catalog.Entry, everyone bool) {
	s.background(func(ctx context.Context) error {
		if err := s.catalog.SetInstallForEveryone(ctx, e.Item.ID, everyone); err != nil {
			return fmt.Errorf("set install for everyone of %s: %w", e.FullName(), err)
		}
		return nil
	})
}

// setInstallFolder keeps the folder the Item is installed in next time; empty
// is the default place.
func (s *screen) setInstallFolder(e catalog.Entry, folder string) {
	s.background(func(ctx context.Context) error {
		if err := s.catalog.SetInstallFolder(ctx, e.Item.ID, folder); err != nil {
			return fmt.Errorf("set install folder of %s: %w", e.FullName(), err)
		}
		return nil
	})
}

// stopTracking removes an Item from the Library, after asking. What is
// installed stays unless the person ticks the box to delete it; the question is
// skipped when the person turned confirming off and there is nothing the box
// would offer to delete.
func (s *screen) stopTracking(e catalog.Entry) {
	in, installed := e.Installed()
	managed := installed && in.Removable()
	if !managed && !s.prefs.Get().ConfirmRemove {
		s.removeItem(e)
		return
	}
	deleteFiles := false
	s.showDialog(locales.DrawerStopTrackingTitle(e.Title()), func(c *ui.Context) {
		t := c.Theme()
		ui.Column(c).Gap(t.Space(3)).MinWidth(360).Children(func() {
			components.Text(c, locales.DrawerStopTrackingTitle(e.Title())).Bold().Wrap()
			components.Text(c, locales.DrawerStopTrackingBody()).TextColor(t.TextMuted).Wrap()
			if managed {
				components.Checkbox(c, &deleteFiles, locales.DrawerStopTrackingDelete(e.Title()))
			}
			ui.Row(c).Gap(t.Space(2)).Justify(ui.End).Children(func() {
				if components.Button(c, locales.Cancel(), components.ButtonOutline).Clicked() {
					s.dialogOpen = false
				}
				if components.Button(c, locales.DrawerRemove(), components.ButtonPrimary).Clicked() {
					s.dialogOpen = false
					s.closeTopOverlay()
					if deleteFiles {
						s.startUninstall(e, true)
					} else {
						s.removeItem(e)
					}
				}
			})
		})
	})
}

func (s *screen) removeItem(e catalog.Entry) {
	s.mutate(e, func(ctx context.Context) error { return s.catalog.Remove(ctx, e.Item.ID) },
		locales.ActivityRemoved(e.FullName()))
}
