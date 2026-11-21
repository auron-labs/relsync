package main

import (
	"strconv"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

// groupBar is the row of group chips with the buttons that make a group and
// change how the Library is arranged by group.
func (s *screen) groupBar(c *ui.Context, res libraryResult) {
	t := c.Theme()
	p := s.prefs.Get()
	ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Gap(t.Space(2)).Wrap().
		Padding(0, pagePadding, t.Space(3), pagePadding).Children(func() {
		s.groupChip(c, groupChip{title: locales.GroupAll(), count: res.allGroups}, res.group, nil)
		for _, chip := range res.groups {
			var custom *catalog.Group
			if g, ok := s.groupOf(chip.ref.custom); ok {
				custom = &g
			}
			s.groupChip(c, chip, res.group, custom)
		}
		if components.Button(c, locales.GroupNew(), components.ButtonGhost).Clicked() {
			s.newGroupDialog(nil)
		}
		ui.Spacer(c).Grow(1)
		auto, sections := p.AutoGroups, p.GroupSections
		if components.ToggleButton(c, &auto, locales.LibraryAuto()).Description(locales.LibraryAutoLabel()).Changed() {
			s.prefs.Update(func(p *preferences) { p.AutoGroups = auto })
		}
		if components.ToggleButton(c, &sections, locales.LibrarySections()).Changed() {
			s.prefs.Update(func(p *preferences) { p.GroupSections = sections })
		}
		if p.GroupSections {
			s.foldButtons(c, res)
		}
	})
}

// foldButtons close or open every section at once.
func (s *screen) foldButtons(c *ui.Context, res libraryResult) {
	lib := &s.library
	if components.Button(c, locales.LibraryFoldAll(), components.ButtonGhost).Clicked() {
		lib.folded = make(map[groupRef]bool, len(res.sections))
		for _, sec := range res.sections {
			lib.folded[sec.ref] = true
		}
	}
	if components.Button(c, locales.LibraryOpenAll(), components.ButtonGhost).Clicked() {
		lib.folded = nil
	}
}

// groupChip is one group as a filter. Chips of the user's groups (custom is
// set) take a card dropped on them and offer to rename or delete the group.
func (s *screen) groupChip(c *ui.Context, chip groupChip, current groupRef, custom *catalog.Group) {
	lib := &s.library
	t := c.Theme()
	// The key goes on a container: a toggle takes none of its own.
	ui.Row(c).Key(chip.ref).Children(func() {
		on := chip.ref == current
		b := components.ToggleButton(c, &on, "").Label(locales.LibraryGroupLabel(chip.title, chip.count))
		b.Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(t.Space(1.5)).Children(func() {
				components.Text(c, chip.title).SingleLine()
				components.Text(c, strconv.Itoa(chip.count)).FontSize(t.Rem(.8)).SingleLine()
			})
		})
		if b.Changed() {
			lib.group = groupRef{}
			if on {
				lib.group = chip.ref
			}
		}
		if custom != nil {
			s.groupTarget(c, b, *custom)
		}
	})
}

// groupTarget makes an element a place to drop cards on, and gives it the
// menu to rename or delete the group.
func (s *screen) groupTarget(c *ui.Context, target ui.Element, g catalog.Group) {
	if dropped, ok := ui.Drop[itemDrag](target); ok {
		if e, found := s.catalog.Get(dropped.id); found {
			s.moveToGroup(e, g.ID)
		}
	}
	if _, over := ui.DragOver[itemDrag](target); over {
		target.Border(2, c.Theme().Accent)
	}
	components.ContextMenu(c, target, func(m *components.Menu) {
		if m.Item(locales.GroupMenuRename()).Chosen() {
			s.renameGroupDialog(g)
		}
		if m.Item(locales.GroupMenuDelete()).Chosen() {
			s.deleteGroupDialog(g)
		}
	})
}

// sectionsView lists the Library as one collapsible section per group.
func (s *screen) sectionsView(c *ui.Context, res libraryResult) {
	t := c.Theme()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(0, pagePadding, pagePadding, pagePadding).Gap(t.Space(3)).Children(func() {
			for _, sec := range res.sections {
				s.section(c, sec)
			}
		})
	})
}

// section is one group's heading, which folds it, and its Items.
func (s *screen) section(c *ui.Context, sec librarySection) {
	t := c.Theme()
	lib := &s.library
	folded := lib.folded[sec.ref]
	column := ui.Column(c).Key(sec.ref).FillWidth().Gap(t.Space(2))
	column.Children(func() {
		chevron := lucide.ChevronDown
		if folded {
			chevron = lucide.ChevronRight
		}
		head := ui.ButtonBase(c).Key("heading").Label(locales.LibrarySectionLabel(sec.title)).FillWidth().
			Padding(t.Space(1.5), t.Space(2)).Radius(t.Radius).Justify(ui.Start).AlignItems(ui.Center).
			TextColor(t.Text).Background(ui.Color{})
		if head.Hovered() {
			head.Background(t.SurfaceHover)
		}
		head.Children(func() {
			components.Icon(c, chevron()).Size(t.Rem(1), t.Rem(1)).TextColor(t.TextMuted)
			components.Text(c, sec.title).Bold().SingleLine().Ellipsis("…")
			components.Text(c, strconv.Itoa(len(sec.rows))).FontSize(t.Rem(.85)).TextColor(t.TextMuted)
		})
		if head.Clicked() {
			if lib.folded == nil {
				lib.folded = map[groupRef]bool{}
			}
			lib.folded[sec.ref] = !folded
		}
		if folded {
			return
		}
		switch {
		case len(sec.rows) == 0:
			components.Text(c, locales.LibraryGroupEmpty()).TextColor(t.TextMuted).Padding(0, t.Space(2))
		case s.prefs.Get().LibraryView == viewList:
			for _, r := range sec.rows {
				ui.Box(c).Key(r.entry.Item.ID).FillWidth().Height(listRowHeight).Children(func() { s.listRow(c, r) })
			}
		default:
			ui.Row(c).FillWidth().Wrap().Gap(t.Space(3)).Children(func() {
				for _, r := range sec.rows {
					ui.Box(c).Key(r.entry.Item.ID).Width(cardMinWidth).Height(cardHeight).Children(func() { s.card(c, r) })
				}
			})
		}
	})
	if sec.ref.custom != 0 {
		if g, ok := s.groupOf(sec.ref.custom); ok {
			s.groupTarget(c, column, g)
		}
	}
}

func (s *screen) groupOf(id int64) (catalog.Group, bool) {
	for _, g := range s.catalog.Groups().Get() {
		if g.ID == id {
			return g, true
		}
	}
	return catalog.Group{}, false
}
