package main

import (
	"context"
	"fmt"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

const activityRowHeight = 44

// activityView lists the persisted log, newest first.
func (s *screen) activityView(c *ui.Context) {
	entries := s.activity.Entries().Get()
	if len(entries) == 0 {
		s.emptyState(c, locales.EmptyActivityTitle(), locales.EmptyActivity())
		return
	}
	t := c.Theme()
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		ui.Row(c).FillWidth().Shrink(0).Justify(ui.End).Padding(0, pagePadding, t.Space(3), pagePadding).Children(func() {
			if components.Button(c, locales.ActivityClear(), components.ButtonOutline).Clicked() {
				s.clearActivity()
			}
		})
		ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, pagePadding-4, 0, pagePadding-4).Children(func() {
			components.List(c, &s.activityList, len(entries), func(i int) {
				s.activityRow(c, entries[i])
			}).Fill()
		})
	})
}

func (s *screen) activityRow(c *ui.Context, e activity.Entry) {
	t := c.Theme()
	icon, color := lucide.Info(), t.TextMuted
	switch e.Level {
	case activity.Success:
		icon, color = lucide.CircleCheck(), t.Success
	case activity.Warning:
		icon, color = lucide.TriangleAlert(), t.Warning
	case activity.Error:
		icon, color = lucide.CircleX(), t.Danger
	case activity.Info:
	}
	_, tracked := s.catalog.Get(e.ItemID)
	ui.Row(c).Key(e.ID).FillWidth().Height(activityRowHeight).Padding(0, t.Space(3)).AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
		components.Icon(c, icon).Size(t.Rem(1.1), t.Rem(1.1)).TextColor(color).Shrink(0)
		components.Text(c, e.Message).SingleLine().Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
		components.Text(c, locales.DateTimeMedium(e.At)).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Shrink(0)
		if tracked {
			if components.Button(c, locales.ActivityDetails(), components.ButtonGhost).Key(e.ID).Clicked() {
				s.openItem(e.ItemID)
			}
		}
	})
}

// clearActivity empties the log in the background.
func (s *screen) clearActivity() {
	s.background(func(ctx context.Context) error {
		if err := s.activity.Clear(ctx); err != nil && ctx.Err() == nil {
			return fmt.Errorf("clear activity: %w", err)
		}
		return nil
	})
}
