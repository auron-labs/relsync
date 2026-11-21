package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/auron-labs/mygo-framework/errorreport"
	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
	"github.com/oneo-me/onevra/source/shells/sidebar"
)

const (
	updateCheckJob   = "update-check"
	updateInstallJob = "update-install"
)

// checkForUpdates looks for a newer signed version in the background. It
// contacts no server unless the build was made with an update feed and public
// key (the updates section of mygo.json); otherwise Updater.Enabled is false.
func (s *screen) checkForUpdates() {
	if !mygo.Updater.Enabled() {
		return
	}
	err := s.jobs.Start(updateCheckJob, func(ctx context.Context, _ jobs.Report) error {
		found, err := mygo.Updater.Check(ctx)
		if err != nil {
			// An unreachable feed is routine for a desktop app; the next launch asks again.
			log.Printf("check for updates: %v", err)
			return nil
		}
		if found != nil {
			if s.win != nil {
				s.win.Update(func() { s.update = found })
			} else {
				s.update = found
			}
		}
		return nil
	})
	if err != nil {
		log.Print(err)
	}
}

func (s *screen) installUpdate() {
	found := s.update
	err := s.jobs.Start(updateInstallJob, func(ctx context.Context, report jobs.Report) error {
		if err := found.Install(ctx, func(downloaded, total int64) { report(int(downloaded>>10), int(total>>10), "") }); err != nil {
			err = fmt.Errorf("install update: %w", err)
			log.Print(err)
			errorreport.Capture(err, "updater.install", "jobs")
			return err
		}
		return nil
	})
	if err != nil && !errors.Is(err, jobs.ErrRunning) {
		log.Print(err)
	}
}

// updateButton renders the footer's self-update control: an "Update available"
// button, progress while it installs, then "Restart to finish updating". It
// renders nothing until an update has been found.
func (s *screen) updateButton(c *ui.Context) {
	if s.update == nil {
		return
	}
	t := c.Theme()
	status, started := s.jobs.Status(updateInstallJob)
	label := locales.UpdateAvailable()
	icon := lucide.Download()
	if started && status.State == jobs.Done {
		label = locales.UpdateRestart()
		icon = lucide.RefreshCw()
	}
	if s.compact {
		if sidebar.IconButton(c, icon, label).Clicked() {
			s.installUpdate()
		}
		return
	}
	switch {
	case started && status.State == jobs.Running:
		ui.Column(c).FillWidth().Gap(t.Space(1)).Children(func() {
			components.Text(c, locales.UpdateInstalling(s.update.Version)).FontSize(t.Rem(.85)).SingleLine()
			components.Progress(c, status.Fraction()).Label(locales.UpdateProgressLabel())
		})
	case started && status.State == jobs.Done:
		if components.Button(c, locales.UpdateRestart(), components.ButtonPrimary).FillWidth().Clicked() {
			mygo.App.Relaunch()
		}
	default:
		if started && status.State == jobs.Failed {
			components.Text(c, locales.UpdateFailed()).FontSize(t.Rem(.85)).TextColor(t.Danger).Wrap()
		}
		if components.Button(c, label, components.ButtonPrimary).FillWidth().Clicked() {
			s.installUpdate()
		}
	}
}
