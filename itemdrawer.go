package main

import (
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

// openItem shows an Item in the detail drawer.
func (s *screen) openItem(id int64) {
	e, ok := s.catalog.Get(id)
	if !ok {
		return
	}
	s.openDrawer(e.Title(), func(c *ui.Context) { s.itemDrawer(c, id) })
	s.refreshBackups(id)
}

// openProjectLabel is the label of the button that opens the Item's page on its site.
func openProjectLabel(e catalog.Entry) string {
	if source.IsGitHub(e.Item.Source) {
		return locales.DrawerOpenOnGitHub()
	}
	return locales.DrawerOpenProjectPage()
}

// openRepository opens the Item's page on the site in the browser.
func (s *screen) openRepository(e catalog.Entry) {
	if err := s.openURL(source.RepoURL(e.Ref())); err != nil {
		log.Printf("open %s: %v", e.FullName(), err)
	}
}

// itemDrawer is the drawer's content for one Item: header, actions,
// description, error, versions, release notes, every version and options.
func (s *screen) itemDrawer(c *ui.Context, id int64) {
	e, ok := s.catalog.Get(id)
	if !ok {
		// Stopped tracking while the drawer was open.
		s.closeTopOverlay()
		return
	}
	t := c.Theme()
	progress, isBusy := s.busy(id)
	row := libraryRow{entry: e, status: e.Status(isBusy), busy: progress}
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(t.Space(4)).Gap(t.Space(4)).Children(func() {
			s.drawerHeader(c, row)
			s.drawerActions(c, row)
			s.drawerGuessedLink(c, e)
			if e.Item.Description != "" {
				components.Text(c, e.Item.Description).Wrap()
			}
			s.drawerError(c, e)
			s.drawerInstallError(c, e)
			s.drawerVersionCard(c, e)
			s.drawerGroup(c, e)
			s.drawerDownload(c, e)
			s.drawerNotes(c, e)
			s.drawerVersions(c, e)
			s.drawerBackups(c, e)
			s.drawerOptions(c, e)
			s.drawerYourNotes(c, e)
		})
	})
}

func (s *screen) drawerHeader(c *ui.Context, r libraryRow) {
	t := c.Theme()
	e := r.entry
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
		s.avatar(c, e, 56)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(t.Space(1)).Children(func() {
			components.Text(c, e.Title()).Bold().FontSize(t.Rem(1.2)).Wrap()
			if components.Button(c, e.FullName(), components.ButtonLink).Clicked() {
				s.openRepository(e)
			}
			ui.Row(c).AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
				statusPill(c, r)
				siteBadge(c, e)
				if e.Item.Stars > 0 {
					ui.Row(c).AlignItems(ui.Center).Gap(t.Space(1)).Children(func() {
						components.Icon(c, lucide.Star()).Size(t.Rem(.9), t.Rem(.9)).TextColor(t.TextMuted)
						components.Text(c, locales.AddStars(e.Item.Stars)).FontSize(t.Rem(.85)).TextColor(t.TextMuted)
					})
				}
			})
		})
	})
}

func (s *screen) drawerActions(c *ui.Context, r libraryRow) {
	t := c.Theme()
	e := r.entry
	_, hasLatest := e.Latest()
	busy := s.checking(e.Item.ID)
	op, working := s.opOf(e.Item.ID)
	ui.Row(c).FillWidth().Wrap().Gap(t.Space(2)).Children(func() {
		if !working {
			s.installButtons(c, e)
		}
		if components.Button(c, locales.DrawerCheckNow(), components.ButtonSecondary).Disabled(busy).Clicked() {
			s.checkItem(e.Item.ID, true)
		}
		if components.Button(c, locales.DrawerMarkInstalled(), components.ButtonSecondary).Disabled(!hasLatest || working).Clicked() {
			s.markInstalled(e)
		}
		switch {
		case e.Skipped():
			if components.Button(c, locales.DrawerStopSkipping(), components.ButtonSecondary).Clicked() {
				s.stopSkipping(e)
			}
		case e.UpdateAvailable():
			if components.Button(c, locales.DrawerSkipVersion(), components.ButtonSecondary).Clicked() {
				s.skipVersion(e)
			}
		}
		if components.Button(c, openProjectLabel(e), components.ButtonOutline).Clicked() {
			s.openRepository(e)
		}
		stop := locales.DrawerStopTracking()
		if _, managed := e.Installed(); managed {
			stop = locales.DrawerStopTrackingInstalled()
		}
		if components.Button(c, stop, components.ButtonOutline).Disabled(working).Clicked() {
			s.stopTracking(e)
		}
	})
	if working {
		s.operationPanel(c, e, op)
	}
	s.installedNote(c, e)
}

// drawerError explains why the Item has no usable release, or why its last
// check failed.
func (s *screen) drawerError(c *ui.Context, e catalog.Entry) {
	var text string
	switch e.Item.LastFailure {
	case catalog.FailNone:
		return
	case catalog.FailNotFound:
		text = locales.DrawerNotFound(e.FullName(), source.SiteLabel(e.Item.Source))
	case catalog.FailNoReleases:
		if _, found := e.Latest(); found {
			return // Turning on pre-releases found one; the next check clears this.
		}
		text = locales.DrawerNoReleases()
		if len(e.Releases) > 0 {
			text = locales.DrawerNoStableReleases()
		}
	case catalog.FailOther:
		text = e.Item.LastFailureText
	}
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(t.Space(3)).Radius(t.Radius).Border(1, t.Danger).Children(func() {
		components.Text(c, text).TextColor(t.Danger).Wrap()
	})
}

// drawerInstallError shows why the last install, update or removal failed.
func (s *screen) drawerInstallError(c *ui.Context, e catalog.Entry) {
	if e.Item.InstallError == "" {
		return
	}
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(t.Space(3)).Radius(t.Radius).Border(1, t.Danger).Children(func() {
		components.Text(c, e.Item.InstallError).TextColor(t.Danger).Wrap()
	})
}

func (s *screen) drawerVersionCard(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	installed := locales.StatusNotInstalled()
	if v := e.InstalledVersion(); v != "" {
		installed = v
	}
	latest, released := "—", "—"
	if r, ok := e.Offered(); ok {
		latest = r.Tag
		if !r.Published.IsZero() {
			released = locales.DateMedium(r.Published)
		}
	}
	checked := locales.DrawerNever()
	if !e.Item.LastChecked.IsZero() {
		checked = locales.DateTimeMedium(e.Item.LastChecked)
	}
	ui.Column(c).FillWidth().Padding(t.Space(3)).Gap(t.Space(2)).Radius(t.Radius).Border(1, t.Border).Children(func() {
		lines := []struct{ label, value string }{
			{locales.DrawerVersionInstalled(), installed},
			{locales.DrawerVersionLatest(), latest},
			{locales.DrawerVersionReleased(), released},
			{locales.DrawerVersionChecked(), checked},
		}
		for _, line := range append(lines, installedLines(e)...) {
			ui.Row(c).FillWidth().Gap(t.Space(3)).Children(func() {
				components.Text(c, line.label).TextColor(t.TextMuted).Width(110).Shrink(0)
				components.Text(c, line.value).Wrap().Shrink(1).MinWidth(0)
			})
		}
	})
}

// drawerNotes shows the release notes: every release newer than the installed
// one, in order, when the Item is behind; otherwise the latest release's.
func (s *screen) drawerNotes(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	releases := e.NewerReleases()
	title := locales.DrawerNotesSince()
	if len(releases) == 0 || !e.UpdateAvailable() {
		title = locales.DrawerNotesTitle()
		releases = nil
		if latest, ok := e.Offered(); ok {
			releases = []source.Release{latest}
		}
	}
	if len(releases) == 0 {
		return
	}
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.Text(c, title).Bold()
		markdown.View(c, cumulativeNotes(releases), &s.markdown).Key(e.Item.ID)
	})
}

// cumulativeNotes joins the notes of several releases, each under its tag.
func cumulativeNotes(releases []source.Release) string {
	var b strings.Builder
	for i, r := range releases {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		if len(releases) > 1 {
			b.WriteString("### " + r.Tag)
			if !r.Published.IsZero() {
				b.WriteString(" · " + r.Published.Format(time.DateOnly))
			}
			b.WriteString("\n\n")
		}
		if strings.TrimSpace(r.Notes) == "" {
			b.WriteString(locales.DrawerNoNotes())
		} else {
			b.WriteString(r.Notes)
		}
	}
	return b.String()
}

// drawerDownload shows which file would be installed and why, lets the user
// pick another, and says what the Item is and how an App may be installed.
func (s *screen) drawerDownload(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	offered, hasOffer := e.LatestInstallable()
	release, hasRelease := e.Offered()
	if !hasRelease {
		return
	}
	view := newDownloadView(e, release, "")
	if hasOffer {
		view = newDownloadView(e, release, storedChoice(e, offered.Result))
	}
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.Text(c, locales.DownloadTitle()).Bold()
		if newest, offeredRelease, skipped := e.SkippedOver(); skipped {
			components.Text(c, locales.DownloadNewestMissing(newest.Tag, offeredRelease.Tag)).TextColor(t.Warning).Wrap()
		}
		downloadNotes(c, view, locales.DownloadNoneTracked(), func() { s.setKind(e, selection.ItemApp) })
		s.downloadActions(c, e, view)
		s.transferList(c, e)
		if hasOffer {
			if name, changed := fileChooser(c, view); changed {
				s.chooseFile(e, view, name)
			}
		}
		if kind, changed := kindChooser(c, e.Item.Kind); changed {
			s.setKind(e, kind)
		}
		if e.KindFor(release) == selection.ItemApp {
			if method, changed := methodChooser(c, e.Item.Method); changed {
				s.setMethod(e, method)
			}
		}
	})
}

// chooseFile keeps the file picked in the drawer, or goes back to the best
// match for an empty name.
func (s *screen) chooseFile(e catalog.Entry, v downloadView, name string) {
	i := slices.IndexFunc(v.result.Candidates, func(cand selection.Candidate) bool { return cand.Asset.Name == name })
	if name == "" || i < 0 {
		s.setFile(e, nil)
		return
	}
	s.setFile(e, &v.result.Candidates[i])
}

func (s *screen) drawerVersions(c *ui.Context, e catalog.Entry) {
	if len(e.Releases) == 0 {
		return
	}
	t := c.Theme()
	latest, _ := e.Offered()
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.Text(c, locales.DrawerAllVersions()).Bold()
		for i, r := range e.Releases {
			ui.Column(c).Key(strconv.Itoa(i)).FillWidth().Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
					components.Text(c, r.Tag).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
					if r.Tag == latest.Tag {
						components.Badge(c, locales.DrawerLatestMarker(), components.BadgeOptions{Tone: components.BadgeAccent})
					}
					if r.Prerelease {
						components.Badge(c, locales.DrawerPrerelease())
					}
					ui.Spacer(c).Grow(1)
					if !r.Published.IsZero() {
						components.Text(c, locales.DateMedium(r.Published)).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Shrink(0)
					}
				})
				components.Text(c, versionFileText(e, r)).FontSize(t.Rem(.85)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				if _, working := s.opOf(e.Item.ID); !working {
					s.versionAction(c, e, r.Tag)
				}
				// The latest release's files are listed under Download.
				if r.Tag != latest.Tag {
					open := s.filesOpen[filesKey(e, r.Tag)]
					s.filesToggle(c, e, r.Tag, open)
					if open {
						s.releaseFiles(c, e, r)
					}
				}
			})
		}
	})
}

// versionFileText says which file a release would install.
func versionFileText(e catalog.Entry, r source.Release) string {
	pick, ok := e.Resolve(r).Pick()
	if !ok {
		return locales.DrawerVersionNoDownload()
	}
	return locales.DownloadFileOption(pick.Asset.Name, formatSize(pick.Asset.Size))
}

// drawerOptions are the Item's two switches. Controls bind to local copies and
// commit through the catalog when changed.
func (s *screen) drawerOptions(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	includePre, hold, auto := e.Item.IncludePrereleases, e.Item.HoldUpdates, e.Item.AutoUpdate
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.Text(c, locales.DrawerOptions()).Bold()
		components.Checkbox(c, &includePre, locales.AddIncludePrereleases()).OnChange(func() {
			s.setIncludePrereleases(e, includePre)
		})
		components.Checkbox(c, &hold, locales.DrawerIgnoreUpdates()).OnChange(func() {
			s.setHoldUpdates(e, hold)
		})
		components.Checkbox(c, &auto, locales.DrawerAutoUpdate()).OnChange(func() {
			s.setAutoUpdate(e, auto)
		})
		if release, ok := e.Offered(); ok && e.KindFor(release) == selection.ItemApp {
			s.installerOptions(c, e)
		}
	})
}
