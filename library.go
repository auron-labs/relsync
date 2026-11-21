package main

import (
	"strconv"
	"strings"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

const (
	cardMinWidth  = 300
	cardHeight    = 150
	listRowHeight = 64
	avatarSize    = 40
	// pagePadding is the horizontal space around page content, matching the top bar.
	pagePadding = 24
	// cardActionsWidth is the room a card keeps free for its star and menu.
	cardActionsWidth = 76
)

// libraryState is what the Library page remembers between frames. It is only
// touched on the UI thread.
type libraryState struct {
	status libraryFilter
	kinds  kindFilter
	group  groupRef
	query  string
	sort   librarySort
	// folded are the sections the person closed; it lasts for the session.
	folded    map[groupRef]bool
	gridState components.GridState
	listState components.ListState
	// visible is what the last frame listed, for the grid and list callbacks.
	visible []libraryRow
}

// checking reports that the Item is being checked for new releases.
func (s *screen) checking(id int64) bool {
	status, ok := s.jobs.Status(catalog.CheckJob(id))
	return ok && status.State == jobs.Running
}

// busy reports an operation running on an Item, with its progress text: an
// install, update or removal, a check, or a download.
func (s *screen) busy(id int64) (string, bool) {
	if op, ok := s.opOf(id); ok {
		return s.operationPill(op), true
	}
	if s.checking(id) {
		status, _ := s.jobs.Status(catalog.CheckJob(id))
		return status.Message, true
	}
	for _, tr := range s.transfersOf(id) {
		if tr.active() {
			return s.downloadPill(tr), true
		}
	}
	return "", false
}

// libraryQuery is the Library's query as its filters, sort and the settings
// currently have it.
func (s *screen) libraryQuery() libraryQuery {
	lib := &s.library
	p := s.prefs.Get()
	return libraryQuery{
		status: lib.status, kinds: lib.kinds, group: lib.group, text: lib.query, sort: lib.sort,
		sections: p.GroupSections, auto: p.AutoGroups,
	}
}

func (s *screen) libraryView(c *ui.Context) {
	entries := s.catalog.Entries().Get()
	if len(entries) == 0 {
		s.emptyState(c, locales.EmptyLibraryTitle(), locales.EmptyLibrary())
		return
	}
	lib := &s.library
	q := s.libraryQuery()
	res := queryLibrary(entries, s.catalog.Groups().Get(), s.busy, groupTitle, q)
	// A filter naming something that is gone, such as the Hidden filter with
	// nothing hidden, is dropped.
	lib.status, lib.group = res.status, res.group
	lib.visible = res.rows
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		s.libraryHeader(c, res)
		s.libraryToolbar(c, res)
		s.groupBar(c, res)
		switch {
		case q.sections && len(res.sections) > 0:
			s.sectionsView(c, res)
		case len(res.rows) == 0:
			s.emptyState(c, "", s.noMatchesText(res))
		case s.prefs.Get().LibraryView == viewList:
			s.libraryList(c)
		default:
			s.libraryGrid(c)
		}
	})
}

// noMatchesText says why the Library lists nothing: a group of the person's
// own with nothing in it yet, or filters that match nothing.
func (s *screen) noMatchesText(res libraryResult) string {
	lib := &s.library
	if res.group.custom != 0 && res.status == filterAll && lib.kinds == (kindFilter{}) && strings.TrimSpace(lib.query) == "" {
		return locales.LibraryGroupEmpty()
	}
	return locales.LibraryNoMatches()
}

// libraryHeader is the line above the filters: how many apps, how many have
// updates, how many are hidden.
func (s *screen) libraryHeader(c *ui.Context, res libraryResult) {
	t := c.Theme()
	line := locales.LibraryApps(res.apps()) + " · " + locales.LibraryUpdateSummary(res.counts[filterUpdates])
	if hidden := res.counts[filterHidden]; hidden > 0 {
		line += " · " + locales.LibraryHiddenCount(hidden)
	}
	ui.Row(c).FillWidth().Shrink(0).Padding(0, pagePadding, t.Space(2), pagePadding).Children(func() {
		components.Text(c, line).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
	})
}

// libraryToolbar holds the status filters with their counts, the kind
// filters, the text filter, the sort order and the grid or list switch.
func (s *screen) libraryToolbar(c *ui.Context, res libraryResult) {
	t := c.Theme()
	lib := &s.library
	ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Gap(t.Space(3)).Wrap().
		Padding(0, pagePadding, t.Space(3), pagePadding).Children(func() {
		labels := []string{locales.FilterAll(), locales.FilterFavorites(), locales.FilterUpdates(), locales.FilterInstalled(), locales.FilterNotInstalled()}
		if res.counts[filterHidden] > 0 {
			labels = append(labels, locales.FilterHidden())
		}
		selected := int(lib.status)
		components.TabsWith(c, &selected, components.TabsOptions{
			Variant: components.TabsSegmented,
			Trailing: func(c *ui.Context, i int, _ float32) {
				components.Text(c, strconv.Itoa(res.counts[i])).FontSize(t.Rem(.8)).TextColor(t.TextMuted)
			},
		}, labels...).List.Label(locales.FilterLabel())
		lib.status = libraryFilter(selected)

		ui.Row(c).AlignItems(ui.Center).Gap(t.Space(1.5)).Children(func() {
			components.ToggleButton(c, &lib.kinds.apps, locales.KindApps()).Description(locales.KindFilterLabel())
			components.ToggleButton(c, &lib.kinds.tools, locales.KindTools()).Description(locales.KindFilterLabel())
		})

		components.TextBox(c, &lib.query, components.TextBoxOptions{LeadingIcon: lucide.Search()}).
			Label(locales.LibraryFilterLabel()).Placeholder(locales.LibraryFilterPlaceholder()).
			Grow(1).Shrink(1).MinWidth(160).MaxWidth(320)

		s.sortSelect(c)

		for _, v := range []struct {
			view  string
			icon  func() *ui.SVG
			label string
		}{{viewGrid, lucide.LayoutGrid, locales.ViewGrid()}, {viewList, lucide.List, locales.ViewList()}} {
			style := components.IconButtonPlain
			if s.prefs.Get().LibraryView == v.view {
				style = components.IconButtonToolbar
			}
			if components.IconButton(c, v.icon(), v.label, style).Key(v.view).Clicked() {
				s.prefs.Update(func(p *preferences) { p.LibraryView = v.view })
			}
		}
	})
}

func sortLabel(sortBy librarySort) string {
	switch sortBy {
	case sortRecentlyUpdated:
		return locales.SortRecentlyUpdated()
	case sortRecentlyAdded:
		return locales.SortRecentlyAdded()
	case sortStatus:
		return locales.SortStatus()
	case sortName:
	}
	return locales.SortName()
}

func (s *screen) sortSelect(c *ui.Context) {
	choice := s.library.sort
	sel := components.Select(c, &choice)
	sel.Trigger.Label(locales.SortLabel()).Children(func() {
		components.Text(c, sortLabel(choice)).SingleLine()
	})
	sel.Popup(func(ui.Element) {
		for _, option := range []librarySort{sortName, sortRecentlyUpdated, sortRecentlyAdded, sortStatus} {
			sel.Item(option).Children(func() { components.Text(c, sortLabel(option)).SingleLine() })
		}
	})
	s.library.sort = choice
}

func (s *screen) libraryGrid(c *ui.Context) {
	lib := &s.library
	lib.gridState.Key = lib.rowKey
	ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, pagePadding-2, 0, pagePadding-2).Children(func() {
		components.GridView(c, &lib.gridState, len(lib.visible), cardMinWidth, cardHeight, func(i int) {
			s.card(c, lib.visible[i])
		}).Fill()
	})
}

func (s *screen) libraryList(c *ui.Context) {
	lib := &s.library
	lib.listState.Key = lib.rowKey
	lib.listState.KeepOffset = true
	ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, pagePadding-4, 0, pagePadding-4).Children(func() {
		components.List(c, &lib.listState, len(lib.visible), func(i int) {
			s.listRow(c, lib.visible[i])
		}).Fill()
	})
}

// rowKey identifies a visible row by its Item, so rows keep their identity
// while the order changes.
func (l *libraryState) rowKey(i int) any {
	if i < 0 || i >= len(l.visible) {
		return i
	}
	return l.visible[i].entry.Item.ID
}

// avatar draws an Item's avatar, or its initials until the image has loaded.
func (s *screen) avatar(c *ui.Context, e catalog.Entry, size float32) {
	components.Avatar(c, e.Title(), components.AvatarOptions{Image: s.avatars.Get(e.AvatarURL()), Size: size})
}

// repoLine is the owner/repo under an Item's name, with the badge of the site
// when it is not GitHub.
func repoLine(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(t.Space(1.5)).Children(func() {
		components.Text(c, e.FullName()).FontSize(t.Rem(.8)).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		siteBadge(c, e)
	})
}

// siteBadge names the site of an Item that is not on GitHub.
func siteBadge(c *ui.Context, e catalog.Entry) {
	if !source.IsGitHub(e.Item.Source) {
		components.Badge(c, source.SiteLabel(e.Item.Source)).Shrink(0)
	}
}

// statusPill is the coloured status label.
func statusPill(c *ui.Context, r libraryRow) {
	components.Badge(c, statusText(r.entry, r.status, r.busy), components.BadgeOptions{Tone: statusTone(r.status)})
}

// itemActions are the star that makes an Item a favorite and the button that
// opens its menu.
func (s *screen) itemActions(c *ui.Context, r libraryRow) {
	t := c.Theme()
	e := r.entry
	label := locales.FavoriteAdd(e.Title())
	if e.Item.Favorite {
		label = locales.FavoriteRemove(e.Title())
	}
	star := components.IconButton(c, lucide.Star(), label, components.IconButtonPlain).Key("favorite")
	if e.Item.Favorite {
		star.TextColor(t.Warning)
	}
	if star.Clicked() {
		s.setFavorite(e, !e.Item.Favorite)
	}
	more := components.IconButton(c, lucide.Ellipsis(), locales.ItemMenuLabel(e.Title()), components.IconButtonPlain).Key("menu")
	components.MenuButton(c, more, func(m *components.Menu) { s.itemMenu(m, r) })
}

// openButton is the button that opens an Item's details. It carries the Item
// when dragged, and has the Item's menu as its context menu.
func (s *screen) openButton(c *ui.Context, r libraryRow) ui.Element {
	e := r.entry
	b := ui.ButtonBase(c).Key("open").Label(locales.OpenDetails(e.Title())).Drag(itemDrag{id: e.Item.ID})
	components.ContextMenu(c, b, func(m *components.Menu) { s.itemMenu(m, r) })
	return b
}

// card is one Item in the grid. Clicking it opens the detail drawer.
func (s *screen) card(c *ui.Context, r libraryRow) {
	t := c.Theme()
	e := r.entry
	ui.Box(c).Key(e.Item.ID).Fill().Children(func() {
		card := s.openButton(c, r).Fill().
			Padding(t.Space(3)).Radius(t.Radius).Border(1, t.Border).Justify(ui.Start).AlignItems(ui.Start).
			TextColor(t.Text).Background(t.Surface)
		if card.Hovered() {
			card.Background(t.SurfaceHover)
		}
		card.Children(func() {
			ui.Column(c).Fill().MinWidth(0).Gap(t.Space(2)).Children(func() {
				ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(t.Space(2.5)).Children(func() {
					s.avatar(c, e, avatarSize)
					ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						components.Text(c, e.Title()).Bold().SingleLine().Ellipsis("…")
						repoLine(c, e)
					})
					ui.Box(c).Width(cardActionsWidth).Shrink(0)
				})
				components.Text(c, e.Item.Description).FontSize(t.Rem(.85)).TextColor(t.TextMuted).MaxLines(2).Ellipsis("…")
				ui.Spacer(c).Grow(1)
				ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(t.Space(2)).Children(func() {
					components.Text(c, versionText(e)).FontSize(t.Rem(.85)).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
					statusPill(c, r)
				})
			})
		})
		if card.Clicked() {
			s.openItem(e.Item.ID)
		}
		ui.Row(c).Absolute().Top(t.Space(2)).Right(t.Space(2)).AlignItems(ui.Center).Children(func() { s.itemActions(c, r) })
	})
}

// listRow is one Item in the list view.
func (s *screen) listRow(c *ui.Context, r libraryRow) {
	t := c.Theme()
	e := r.entry
	ui.Row(c).Key(e.Item.ID).FillWidth().Height(listRowHeight).AlignItems(ui.Center).Gap(t.Space(1)).Children(func() {
		row := s.openButton(c, r).Grow(1).Shrink(1).MinWidth(0).FillHeight().
			Padding(0, t.Space(3)).Radius(t.Radius).Justify(ui.Start).TextColor(t.Text).Background(ui.Color{})
		if row.Hovered() {
			row.Background(t.SurfaceHover)
		}
		row.Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
				s.avatar(c, e, avatarSize)
				ui.Column(c).Width(220).Shrink(0).Children(func() {
					components.Text(c, e.Title()).Bold().SingleLine().Ellipsis("…")
					repoLine(c, e)
				})
				components.Text(c, e.Item.Description).FontSize(t.Rem(.85)).TextColor(t.TextMuted).SingleLine().Ellipsis("…").
					Grow(1).Shrink(1).MinWidth(0)
				components.Text(c, versionText(e)).FontSize(t.Rem(.85)).SingleLine().Ellipsis("…").Width(150).Shrink(0)
				ui.Box(c).Width(150).Shrink(0).Children(func() { statusPill(c, r) })
			})
		})
		if row.Clicked() {
			s.openItem(e.Item.ID)
		}
		s.itemActions(c, r)
	})
}
