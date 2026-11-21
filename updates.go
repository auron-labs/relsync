package main

import (
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// notePreviewLength is how much of a release's notes an Updates row previews.
const notePreviewLength = 220

// previewKey identifies the notes a preview was made from.
type previewKey struct {
	item int64
	tag  string
}

// updateEntries are the Items with an update to act on, most recently
// released first.
func updateEntries(entries []catalog.Entry) []catalog.Entry {
	var updates []catalog.Entry
	for _, e := range entries {
		if e.HasUpdate() {
			updates = append(updates, e)
		}
	}
	slices.SortStableFunc(updates, func(a, b catalog.Entry) int {
		if c := b.LatestPublished().Compare(a.LatestPublished()); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.Title()), strings.ToLower(b.Title()))
	})
	return updates
}

// updatesView lists what has a newer release, with a button to update each
// Item and one to update them all.
func (s *screen) updatesView(c *ui.Context) {
	updates := updateEntries(s.catalog.Entries().Get())
	if len(updates) == 0 {
		sentence := locales.UpdatesNeverChecked()
		if when, ok := s.prefs.Get().lastFullCheck(); ok {
			sentence = locales.UpdatesLastChecked(when)
		}
		s.emptyState(c, locales.EmptyUpdatesTitle(), sentence)
		return
	}
	t := c.Theme()
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		ui.Row(c).FillWidth().Shrink(0).Justify(ui.End).Padding(0, pagePadding, t.Space(3), pagePadding).Children(func() {
			if components.Button(c, locales.ActionUpdateEverything(), components.ButtonPrimary).Clicked() {
				s.updateAll()
			}
		})
		s.updateRows(c, updates)
	})
}

func (s *screen) updateRows(c *ui.Context, updates []catalog.Entry) {
	t := c.Theme()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(0, pagePadding, pagePadding, pagePadding).Gap(t.Space(3)).Children(func() {
			for _, e := range updates {
				s.updateRow(c, e)
			}
		})
	})
}

func (s *screen) updateRow(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	latest, _ := e.Offered()
	ui.Row(c).Key(e.Item.ID).FillWidth().Padding(t.Space(3)).Gap(t.Space(3)).AlignItems(ui.Start).
		Radius(t.Radius).Border(1, t.Border).Children(func() {
		s.avatar(c, e, avatarSize)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(t.Space(1)).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Wrap().Children(func() {
				components.Text(c, e.Title()).Bold()
				components.Text(c, versionText(e)).TextColor(t.Accent)
			})
			ui.Row(c).FillWidth().Gap(t.Space(3)).Wrap().Children(func() {
				components.Text(c, e.FullName()).FontSize(t.Rem(.85)).TextColor(t.TextMuted)
				siteBadge(c, e)
				if !latest.Published.IsZero() {
					components.Text(c, locales.UpdatesReleased(latest.Published)).FontSize(t.Rem(.85)).TextColor(t.TextMuted)
				}
			})
			if preview := s.notePreview(e.Item.ID, latest.Tag, latest.Notes); preview != "" {
				components.Text(c, preview).FontSize(t.Rem(.85)).TextColor(t.TextMuted).MaxLines(3).Ellipsis("…")
			}
			if op, working := s.opOf(e.Item.ID); working {
				components.Text(c, s.stageText(op)).FontSize(t.Rem(.85)).TextColor(t.Accent).Wrap()
			}
		})
		ui.Row(c).Shrink(0).Gap(t.Space(2)).Children(func() {
			s.updateButtons(c, e)
		})
	})
}

// notePreview is the plain-text preview of a release's notes, made once per release.
func (s *screen) notePreview(item int64, tag, notes string) string {
	key := previewKey{item: item, tag: tag}
	if preview, ok := s.previews[key]; ok {
		return preview
	}
	if s.previews == nil {
		s.previews = map[previewKey]string{}
	}
	preview := notesPreview(notes, notePreviewLength)
	s.previews[key] = preview
	return preview
}

// updateButtons are the actions on an Updates row: update it, or cancel the
// update while the machine is untouched, then skip it or open its details.
func (s *screen) updateButtons(c *ui.Context, e catalog.Entry) {
	if op, working := s.opOf(e.Item.ID); working {
		if op.offersCancel() {
			if components.Button(c, locales.Cancel(), components.ButtonOutline).Key("cancel").Clicked() {
				s.cancelOperation(e.Item.ID)
			}
		}
	} else if offer, ok := e.LatestInstallable(); ok {
		if components.Button(c, locales.ActionUpdate(), components.ButtonPrimary).Key("update").Clicked() {
			s.startInstall(e.Item.ID, offer.Release.Tag, nil)
		}
	}
	if components.Button(c, locales.UpdatesSkip(), components.ButtonOutline).Key("skip").Clicked() {
		s.skipVersion(e)
	}
	if components.Button(c, locales.UpdatesDetails(), components.ButtonSecondary).Key("details").Clicked() {
		s.openItem(e.Item.ID)
	}
}
