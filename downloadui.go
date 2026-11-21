package main

import (
	"strconv"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// filesKey identifies a release's expanded list of files.
func filesKey(e catalog.Entry, tag string) string {
	return strconv.FormatInt(e.Item.ID, 10) + "/" + tag
}

// assetNamed finds one of a release's files by name.
func assetNamed(r source.Release, name string) (source.Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return source.Asset{}, false
}

// downloadActions are the buttons under the chosen file: download it as it
// is, and list every file of the release.
func (s *screen) downloadActions(c *ui.Context, e catalog.Entry, v downloadView) {
	t := c.Theme()
	open := s.filesOpen[filesKey(e, v.release.Tag)]
	ui.Row(c).FillWidth().Wrap().Gap(t.Space(2)).Children(func() {
		var pick source.Asset
		found := false
		if v.hasPick {
			pick, found = assetNamed(v.release, v.pick.Asset.Name)
		}
		if components.Button(c, locales.DownloadFileOnly(), components.ButtonSecondary).Disabled(!found).Clicked() {
			s.downloadFile(e, v.release, pick)
		}
		s.filesToggle(c, e, v.release.Tag, open)
	})
	if open {
		s.releaseFiles(c, e, v.release)
	}
}

// filesToggle shows or hides the list of every file of a release.
func (s *screen) filesToggle(c *ui.Context, e catalog.Entry, tag string, open bool) {
	label := locales.DownloadShowAllFiles()
	if open {
		label = locales.DownloadHideFiles()
	}
	if components.Button(c, label, components.ButtonGhost).Key("files-" + tag).Clicked() {
		s.filesOpen[filesKey(e, tag)] = !open
	}
}

// releaseFiles lists every file of a release, each with a button to download it.
func (s *screen) releaseFiles(c *ui.Context, e catalog.Entry, r source.Release) {
	t := c.Theme()
	if len(r.Assets) == 0 {
		components.Text(c, locales.DownloadNoFiles()).TextColor(t.TextMuted)
		return
	}
	ui.Column(c).FillWidth().Gap(t.Space(1)).Children(func() {
		for _, a := range r.Assets {
			ui.Row(c).Key(r.Tag + "/" + a.Name).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
				components.Text(c, locales.DownloadFileOption(a.Name, formatSize(a.Size))).SingleLine().Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
				if components.Button(c, locales.DownloadFileOnly(), components.ButtonGhost).Key(a.Name).Clicked() {
					s.downloadFile(e, r, a)
				}
			})
		}
	})
}

// transferList shows the Item's downloads: progress while they run, the
// outcome after, until dismissed.
func (s *screen) transferList(c *ui.Context, e catalog.Entry) {
	transfers := s.transfersOf(e.Item.ID)
	if len(transfers) == 0 {
		return
	}
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		for _, tr := range transfers {
			s.transferRow(c, e, tr)
		}
	})
}

func (s *screen) transferRow(c *ui.Context, e catalog.Entry, tr transfer) {
	t := c.Theme()
	queued := tr.state == transferRunning && s.lanes.Queued(tr.key)
	ui.Column(c).Key(tr.key).FillWidth().Padding(t.Space(3)).Gap(t.Space(2)).Radius(t.Radius).Border(1, t.Border).Children(func() {
		components.Text(c, tr.file).Bold().Wrap()
		color := t.TextMuted
		switch tr.state {
		case transferFailed:
			color = t.Danger
		case transferNeedsCheck:
			color = t.Warning
		case transferDone:
			color = t.Success
		case transferRunning, transferVerifying, transferCanceled:
		}
		components.Text(c, transferStatus(tr, queued)).TextColor(color).FontSize(t.Rem(.85)).Wrap()
		if tr.state == transferRunning && !queued {
			components.Progress(c, transferFraction(tr)).Label(locales.DownloadTitle())
		}
		for _, f := range tr.findings {
			if tr.state == transferNeedsCheck {
				components.Text(c, findingText(f, tr.verified)).TextColor(t.Warning).FontSize(t.Rem(.85)).Wrap()
			}
		}
		ui.Row(c).FillWidth().Wrap().Gap(t.Space(2)).Children(func() {
			s.transferButtons(c, e, tr)
		})
	})
}

// transferButtons are the actions that fit a download's state.
func (s *screen) transferButtons(c *ui.Context, e catalog.Entry, tr transfer) {
	dismiss := func() {
		if components.Button(c, locales.DownloadDismiss(), components.ButtonGhost).Key("dismiss").Clicked() {
			s.dropTransfer(tr.key)
		}
	}
	switch tr.state {
	case transferRunning:
		if components.Button(c, locales.Cancel(), components.ButtonOutline).Key("cancel").Clicked() {
			s.cancelDownload(tr.key)
		}
	case transferDone, transferNeedsCheck:
		if components.Button(c, locales.DownloadShowInFolder(), components.ButtonSecondary).Key("show").Clicked() {
			s.showFile(tr.path)
		}
		dismiss()
	case transferFailed, transferCanceled:
		label := locales.DownloadRetry()
		if tr.state == transferCanceled {
			label = locales.DownloadResume()
		}
		if components.Button(c, label, components.ButtonSecondary).Key("retry").Clicked() {
			s.retryDownload(e, tr)
		}
		dismiss()
	case transferVerifying:
	}
}

// transferStatus is the line under a download's name.
func transferStatus(tr transfer, queued bool) string {
	switch tr.state {
	case transferRunning:
		switch {
		case queued:
			return locales.DownloadQueued()
		case tr.done == 0 && tr.speed == 0:
			return locales.DownloadStarting()
		case tr.total > 0:
			return locales.DownloadProgress(formatSize(tr.done), formatSize(tr.total), formatSize(int64(tr.speed)))
		}
		return locales.DownloadProgressUnknown(formatSize(tr.done), formatSize(int64(tr.speed)))
	case transferVerifying:
		return locales.DownloadVerifying()
	case transferDone, transferNeedsCheck:
		return tr.summary
	case transferFailed:
		return tr.problem
	case transferCanceled:
		return locales.DownloadCanceled()
	}
	return ""
}

// transferFraction is how much of the file has arrived, or -1 while its size
// is unknown, which the progress bar shows as indeterminate.
func transferFraction(tr transfer) float64 {
	if tr.total <= 0 {
		return -1
	}
	return min(float64(tr.done)/float64(tr.total), 1)
}

// downloadPill is the text of a Library card's pill while a file downloads.
func (s *screen) downloadPill(tr transfer) string {
	switch {
	case tr.state == transferVerifying:
		return locales.DownloadVerifying()
	case s.lanes.Queued(tr.key):
		return locales.DownloadQueued()
	case tr.total > 0 && tr.done > 0:
		return locales.StatusDownloading(int(transferFraction(tr) * 100))
	}
	return locales.StatusDownloadingUnknown()
}
