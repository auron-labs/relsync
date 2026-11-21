package main

import (
	"slices"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/scan"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

// scanActive is the job of the Scan page that is running, with its status.
func (s *screen) scanActive() (jobs.Status, bool) {
	for _, name := range []string{scanJob, scanWingetJob, scanFindAllJob, scanAddJob} {
		if status, ok := s.jobs.Status(name); ok && status.State == jobs.Running {
			return status, true
		}
	}
	return jobs.Status{}, false
}

// scanView is the Scan PC page: the card that starts a scan, the programs
// linked to GitHub, the others, and the bar that adds the ticked ones.
func (s *screen) scanView(c *ui.Context) {
	results := s.scanPage.results.Get()
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		s.scanCard(c, results)
		if results.scanned {
			s.scanList(c, results)
		} else {
			ui.Spacer(c).Grow(1)
		}
		s.scanFooter(c, results)
	})
}

// scanCard starts a scan, shows how it is going and what it found, and keeps
// the folders to look in.
func (s *screen) scanCard(c *ui.Context, results scanResults) {
	t := c.Theme()
	status, running := s.scanActive()
	folders := s.prefs.Get().ScanFolders
	ui.Column(c).FillWidth().Shrink(0).Padding(0, pagePadding, t.Space(3), pagePadding).Children(func() {
		ui.Column(c).FillWidth().Padding(t.Space(4)).Gap(t.Space(3)).Radius(t.Radius).Border(1, t.Border).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
				components.Icon(c, lucide.ScanSearch()).Size(t.Rem(1.75), t.Rem(1.75)).TextColor(t.Accent).Shrink(0)
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(t.Space(1)).Children(func() {
					components.Text(c, locales.ScanTitle()).Bold()
					components.Text(c, locales.ScanIntro()).TextColor(t.TextMuted).Wrap()
				})
				if running {
					if components.Button(c, locales.Cancel(), components.ButtonOutline).Key("scan-cancel").Clicked() {
						s.cancelScan()
					}
				}
				if components.Button(c, locales.ScanNow(), components.ButtonPrimary).Key("scan-now").Disabled(running).Clicked() {
					s.startScan()
				}
			})
			switch {
			case running:
				components.Progress(c, status.Fraction()).Label(locales.ScanTitle())
				components.Text(c, status.Message).TextColor(t.TextMuted).Wrap()
			case results.result != "":
				components.Text(c, results.result).TextColor(t.TextMuted).Wrap()
			}
			ui.Row(c).FillWidth().Wrap().Gap(t.Space(2)).Children(func() {
				if components.Button(c, locales.ScanWinget(), components.ButtonOutline).Key("scan-winget").Disabled(running).Clicked() {
					s.startWinget()
				}
				if components.Button(c, locales.ScanAddFolder(), components.ButtonOutline).Key("scan-add-folder").Clicked() {
					s.browseScanFolder()
				}
			})
			if len(folders) > 0 {
				ui.Row(c).FillWidth().Wrap().Gap(t.Space(2)).Children(func() {
					for _, folder := range folders {
						s.folderChip(c, folder)
					}
				})
			}
		})
	})
}

// folderChip is a folder the scan looks in, with a button to stop.
func (s *screen) folderChip(c *ui.Context, folder string) {
	t := c.Theme()
	ui.Row(c).Key(folder).AlignItems(ui.Center).Gap(t.Space(1)).Padding(0, 0, 0, t.Space(2.5)).Radius(t.Radius).Border(1, t.Border).Children(func() {
		components.Text(c, folder).FontSize(t.Rem(.85)).TextColor(t.TextMuted).SingleLine()
		if components.IconButton(c, lucide.X(), locales.ScanRemoveFolder(folder), components.IconButtonCompact).Key(folder).Clicked() {
			s.removeScanFolder(folder)
		}
	})
}

// browseScanFolder asks for a folder of portable programs to look in.
func (s *screen) browseScanFolder() {
	s.browseFolder(locales.ScanAddFolderChoose(), "", func(path string) {
		s.prefs.Update(func(p *preferences) {
			if !slices.Contains(p.ScanFolders, path) {
				p.ScanFolders = append(slices.Clone(p.ScanFolders), path)
			}
		})
	})
}

func (s *screen) removeScanFolder(folder string) {
	s.prefs.Update(func(p *preferences) {
		p.ScanFolders = slices.DeleteFunc(slices.Clone(p.ScanFolders), func(f string) bool { return f == folder })
	})
}

// scanList is the programs found: those linked to GitHub, then the others.
func (s *screen) scanList(c *ui.Context, results scanResults) {
	page := &s.scanPage
	page.shown = page.items(results)
	page.list.Key = page.itemKey
	page.list.KeepOffset = true
	ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, pagePadding-4, 0, pagePadding-4).Children(func() {
		components.List(c, &page.list, len(page.shown), func(i int) {
			it := page.shown[i]
			switch it.kind {
			case itemNoLinks:
				s.scanNoLinks(c)
			case itemLinkedHeader:
				s.scanLinkedHeader(c, results)
			case itemLinked:
				s.scanLinkedRow(c, it.row)
			case itemOtherHeader:
				s.scanOtherHeader(c, results)
			case itemOther:
				s.scanOtherRow(c, it.row)
			}
		}).Fill()
	})
}

func (s *screen) scanNoLinks(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).FillWidth().Height(scanNoLinksHeight).Padding(0, t.Space(3)).AlignItems(ui.Center).Children(func() {
		components.Text(c, locales.ScanNoLinks()).TextColor(t.TextMuted).Wrap()
	})
}

func (s *screen) scanLinkedHeader(c *ui.Context, results scanResults) {
	t := c.Theme()
	ui.Row(c).FillWidth().Height(scanHeaderHeight).Padding(0, t.Space(3)).AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
		components.Text(c, locales.ScanLinkedHeader(len(results.linked()))).Bold().Grow(1).Shrink(1).SingleLine()
		if components.Button(c.Key("select-all"), locales.ScanSelectAll(), components.ButtonLink).Clicked() {
			s.selectAllScanned(true)
		}
		if components.Button(c.Key("select-none"), locales.ScanSelectNone(), components.ButtonLink).Clicked() {
			s.selectAllScanned(false)
		}
	})
}

// scanLinkedRow is a program linked to a repository: whether it is ticked, its
// name and version, how it was linked, the repository and where its releases stand.
func (s *screen) scanLinkedRow(c *ui.Context, r scanRow) {
	t := c.Theme()
	ui.Row(c).Key(r.Key).FillWidth().Height(scanLinkedHeight).Padding(0, t.Space(3)).AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
		on := r.ticked()
		// The name is shown beside it, so the box itself carries only its accessible name.
		components.Checkbox(c.Key("tick-"+r.Key), &on, "").Label(locales.ScanSelectRow(r.Name)).Disabled(!r.canSelect()).
			OnChange(func() { s.setScanSelected(r.Key, on) })
		components.Avatar(c, r.Name, components.AvatarOptions{Image: s.avatars.Get(r.avatarURL()), Size: avatarSize})
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(t.Space(.5)).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
				components.Text(c, r.Name).Bold().SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				components.Badge(c, r.linkLabel(), components.BadgeOptions{Tone: scanTone(r)})
			})
			components.Text(c, scanRepoText(r)).FontSize(t.Rem(.8)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			if r.preview != nil && r.preview.Repository.Description != "" {
				components.Text(c, r.preview.Repository.Description).FontSize(t.Rem(.85)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			}
		})
		ui.Column(c).Width(220).Shrink(0).AlignItems(ui.End).Gap(t.Space(.5)).Children(func() {
			components.Text(c, r.status()).Bold().Wrap().TextColor(scanStatusColor(c, r))
			components.Text(c, scanVersionText(r)).FontSize(t.Rem(.8)).TextColor(t.TextMuted).SingleLine()
		})
	})
}

func scanStatusColor(c *ui.Context, r scanRow) ui.Color {
	t := c.Theme()
	if r.hasUpdate() && !r.tracked {
		return t.Accent
	}
	return t.TextMuted
}

func (s *screen) scanOtherHeader(c *ui.Context, results scanResults) {
	t := c.Theme()
	others := results.others(!s.scanPage.showSystem)
	_, running := s.scanActive()
	ui.Column(c).FillWidth().Height(scanHeaderHeight+40).Padding(t.Space(3), t.Space(3), 0, t.Space(3)).Gap(t.Space(2)).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
			components.Text(c, locales.ScanOtherHeader(len(others))).Bold().Grow(1).Shrink(1).SingleLine()
			label := findAllLabel(len(others), s.searchGap())
			if components.Button(c, label, components.ButtonOutline).Key("find-all").Disabled(running || len(others) == 0).Clicked() {
				s.startFindAll()
			}
		})
		components.Text(c, locales.ScanOtherHint()).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
		hide := !s.scanPage.showSystem
		components.Switch(c, &hide, locales.ScanHideSystem(), components.SwitchOptions{})
		s.scanPage.showSystem = !hide
	})
}

// scanOtherRow is a program with no repository: its name, version and
// publisher, why it has none, and the ways to give it one.
func (s *screen) scanOtherRow(c *ui.Context, r scanRow) {
	t := c.Theme()
	detail := scanVersionText(r)
	if r.Publisher != "" {
		detail += " · " + r.Publisher
	}
	if r.Origin == scan.OriginFolder && r.Folder != "" {
		detail += " · " + r.Folder
	}
	ui.Row(c).Key(r.Key).FillWidth().Height(scanOtherHeight).Padding(0, t.Space(3)).AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
		components.Avatar(c, r.Name, components.AvatarOptions{Size: avatarSize - 6})
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			components.Text(c, r.Name).Bold().SingleLine().Ellipsis("…")
			components.Text(c, detail).FontSize(t.Rem(.8)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		})
		if note := r.status(); note != "" {
			components.Text(c, note).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap().MaxWidth(240).Shrink(1)
		}
		if r.busy {
			components.Spinner(c, components.SpinnerRing).Size(t.Rem(1.25), t.Rem(1.25))
			return
		}
		if components.Button(c, locales.ScanLink(), components.ButtonOutline).Key("link-" + r.Key).Clicked() {
			s.openLinkDialog(r)
		}
		if components.Button(c, locales.ScanFind(), components.ButtonOutline).Key("find-" + r.Key).Clicked() {
			s.startFind(r.Key)
		}
	})
}

// scanFooter says what adding does, with the button that does it.
func (s *screen) scanFooter(c *ui.Context, results scanResults) {
	if len(results.linked()) == 0 {
		return
	}
	t := c.Theme()
	count := 0
	for _, row := range results.rows {
		if row.ticked() {
			count++
		}
	}
	_, running := s.scanActive()
	ui.Row(c).FillWidth().Shrink(0).Padding(t.Space(3), pagePadding).AlignItems(ui.Center).Gap(t.Space(4)).Border(1, t.Border).Children(func() {
		components.Text(c, locales.ScanFooter(appName)).TextColor(t.TextMuted).Wrap().Grow(1).Shrink(1).MinWidth(0)
		if components.Button(c, locales.ScanAdd(count), components.ButtonPrimary).Key("scan-add").Disabled(running || count == 0).Clicked() {
			s.addScanned()
		}
	})
}

// scanLinkState is the dialog for linking a program to a repository by hand.
type scanLinkState struct {
	key, name string
	text      string
	problem   string
}

// openLinkDialog asks for the address of the repository a program belongs to.
func (s *screen) openLinkDialog(r scanRow) {
	st := &scanLinkState{key: r.Key, name: r.Name}
	s.scanPage.link = st
	s.showDialog(locales.ScanLinkTitle(r.Name), func(c *ui.Context) { s.linkDialog(c, st) })
}

func (s *screen) linkDialog(c *ui.Context, st *scanLinkState) {
	t := c.Theme()
	ui.Column(c).Gap(t.Space(3)).MinWidth(400).MaxWidth(520).Children(func() {
		components.Text(c, locales.ScanLinkTitle(st.name)).Bold().FontSize(t.Rem(1.1)).Wrap()
		components.Text(c, locales.ScanLinkBody()).TextColor(t.TextMuted).Wrap()
		box := components.TextBox(c, &st.text, components.TextBoxOptions{LeadingIcon: lucide.Link()}).
			Label(locales.ScanLinkLabel()).Placeholder(locales.ScanLinkPlaceholder()).FillWidth()
		submitted := box.Submitted()
		if st.problem != "" {
			components.Text(c, st.problem).TextColor(t.Danger).Wrap()
		}
		ui.Row(c).FillWidth().Gap(t.Space(2)).Justify(ui.End).Children(func() {
			if components.Button(c, locales.Cancel(), components.ButtonOutline).Clicked() {
				s.dialogOpen = false
			}
			if components.Button(c, locales.ScanLinkConfirm(), components.ButtonPrimary).Clicked() || submitted {
				ref, ok := s.sources.ParseLink(st.text)
				if !ok {
					st.problem = locales.ScanLinkInvalid()
					return
				}
				s.dialogOpen = false
				s.startLink(st.key, ref)
			}
		})
	})
}
