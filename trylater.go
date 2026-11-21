package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

const tryLaterRowHeight = 88

// tryLaterState is what the Try later page remembers between frames. It is
// only touched on the UI thread.
type tryLaterState struct {
	query string
	sort  tryLaterSort
	group bool
	list  components.ListState
	// visible is what the last frame listed, for the list's callbacks.
	visible []tryLaterItem
	// note is the note being edited.
	note string
}

// tryLaterView is the Try later page: the saved repositories, searchable,
// sortable and optionally grouped by type.
func (s *screen) tryLaterView(c *ui.Context) {
	entries := s.catalog.TryLaterEntries().Get()
	if len(entries) == 0 {
		s.emptyState(c, locales.EmptyTryLaterTitle(), locales.EmptyTryLater())
		return
	}
	st := &s.tryLaterUI
	items := tryLaterItems(entries, st.query, st.sort, st.group)
	st.visible = items
	st.list.Key = st.itemKey
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		s.tryLaterToolbar(c, len(entries), countSaved(items))
		if len(items) == 0 {
			s.emptyState(c, "", locales.TryLaterNoMatches())
			return
		}
		ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, pagePadding-4, 0, pagePadding-4).Children(func() {
			components.List(c, &st.list, len(items), func(i int) {
				s.tryLaterRow(c, items[i])
			}).Fill()
		})
	})
}

func countSaved(items []tryLaterItem) int {
	n := 0
	for _, it := range items {
		if it.heading == "" {
			n++
		}
	}
	return n
}

// itemKey identifies a line by its saved repository or its group, so lines
// keep their identity while the order changes.
func (st *tryLaterState) itemKey(i int) any {
	if i < 0 || i >= len(st.visible) {
		return i
	}
	if it := st.visible[i]; it.heading != "" {
		return "group:" + it.heading
	}
	return st.visible[i].saved.ID
}

func tryLaterSortLabel(sortBy tryLaterSort) string {
	switch sortBy {
	case tryOldest:
		return locales.TryLaterSortOldest()
	case tryName:
		return locales.TryLaterSortName()
	case tryStars:
		return locales.TryLaterSortStars()
	case tryType:
		return locales.TryLaterSortType()
	case tryRecent:
	}
	return locales.TryLaterSortRecent()
}

// tryLaterToolbar holds the search box, the sort order, the grouping switch and
// how many repositories are saved.
func (s *screen) tryLaterToolbar(c *ui.Context, total, shown int) {
	t := c.Theme()
	st := &s.tryLaterUI
	ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Gap(t.Space(3)).Wrap().
		Padding(0, pagePadding, t.Space(3), pagePadding).Children(func() {
		components.TextBox(c, &st.query, components.TextBoxOptions{LeadingIcon: lucide.Search()}).
			Label(locales.TryLaterSearchLabel()).Placeholder(locales.TryLaterSearchPlaceholder()).
			Grow(1).Shrink(1).MinWidth(160).MaxWidth(360)

		choice := st.sort
		sel := components.Select(c, &choice)
		sel.Trigger.Label(locales.TryLaterSortLabel()).Children(func() {
			components.Text(c, tryLaterSortLabel(choice)).SingleLine()
		})
		sel.Popup(func(ui.Element) {
			for _, option := range []tryLaterSort{tryRecent, tryOldest, tryName, tryStars, tryType} {
				sel.Item(option).Children(func() { components.Text(c, tryLaterSortLabel(option)).SingleLine() })
			}
		})
		st.sort = choice

		components.Switch(c, &st.group, locales.TryLaterGroup())

		count := locales.TryLaterCount(total)
		if shown != total {
			count = locales.TryLaterShown(shown, total)
		}
		components.Text(c, count).FontSize(t.Rem(.85)).TextColor(t.TextMuted)
	})
}

// tryLaterRow is one line of the list: a group heading or a saved repository.
func (s *screen) tryLaterRow(c *ui.Context, it tryLaterItem) {
	t := c.Theme()
	if it.heading != "" {
		ui.Row(c).Key("group:"+it.heading).FillWidth().Padding(t.Space(3), t.Space(3), t.Space(1), t.Space(3)).Gap(t.Space(2)).Children(func() {
			components.Text(c, it.heading).Bold()
			components.Text(c, strconv.Itoa(it.count)).TextColor(t.TextMuted)
		})
		return
	}
	saved := it.saved
	repo := saved.Repository()
	_, tracked := s.catalog.Find(saved.Ref())
	ui.Row(c).Key(saved.ID).FillWidth().MinHeight(tryLaterRowHeight).Padding(t.Space(2), t.Space(3)).AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
		open := ui.ButtonBase(c).Key("open").Label(locales.OpenDetails(saved.FullName())).Grow(1).Shrink(1).MinWidth(0).
			Justify(ui.Start).TextColor(t.Text).Background(ui.Color{})
		if open.Hovered() {
			open.Background(t.SurfaceHover)
		}
		open.Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
				components.Avatar(c, saved.Repo, components.AvatarOptions{Image: s.avatars.Get(saved.AvatarURL), Size: avatarSize})
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(t.Space(0.5)).Children(func() {
					components.Text(c, saved.FullName()).Bold().SingleLine().Ellipsis("…")
					if saved.Description != "" {
						components.Text(c, saved.Description).FontSize(t.Rem(.85)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
					}
					if saved.Note != "" {
						components.Text(c, saved.Note).FontSize(t.Rem(.85)).MaxLines(2).Ellipsis("…")
					}
					components.Text(c, tryLaterFacts(saved)).FontSize(t.Rem(.8)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				})
			})
		})
		if open.Clicked() {
			s.showRepository(repo)
		}
		if tracked {
			components.Button(c, locales.DiscoverCardInLibrary(), components.ButtonSecondary).Key("add").Disabled(true)
		} else if components.Button(c, locales.TryLaterAddToLibrary(), components.ButtonPrimary).Key("add").Clicked() {
			s.startAdd(saved.Ref())
		}
		more := components.IconButton(c, lucide.Ellipsis(), locales.TryLaterMenu(saved.FullName()), components.IconButtonPlain).Key("menu")
		components.MenuButton(c, more, func(m *components.Menu) { s.tryLaterMenu(m, saved, tracked) })
	})
}

// tryLaterFacts is the line under a saved repository: its type, stars, language
// and when it was saved.
func tryLaterFacts(saved catalog.TryLater) string {
	parts := []string{typeOf(saved), locales.DiscoverStars(compactCount(saved.Stars))}
	if saved.Language != "" {
		parts = append(parts, saved.Language)
	}
	parts = append(parts, locales.TryLaterSavedOn(saved.SavedAt))
	return strings.Join(parts, "  ·  ")
}

func (s *screen) tryLaterMenu(m *components.Menu, saved catalog.TryLater, tracked bool) {
	if m.Item(locales.DiscoverCardDetails()).Chosen() {
		s.showRepository(saved.Repository())
	}
	if m.Item(locales.TryLaterAddToLibrary()).Disabled(tracked).Chosen() {
		s.startAdd(saved.Ref())
	}
	if m.Item(locales.TryLaterEditNote()).Chosen() {
		s.editNote(saved)
	}
	if m.Item(locales.TryLaterRemove()).Chosen() {
		s.removeFromTryLater(saved.ID, saved.FullName())
	}
	if m.Item(locales.DiscoverCardMore()).Chosen() {
		s.openOwner(saved.Owner)
	}
	if m.Item(locales.TryLaterOpenProject()).Chosen() {
		s.openOnGitHub(saved.Ref())
	}
}

// editNote opens the dialog for a saved repository's note.
func (s *screen) editNote(saved catalog.TryLater) {
	st := &s.tryLaterUI
	st.note = saved.Note
	s.showDialog(locales.TryLaterNoteTitle(saved.FullName()), func(c *ui.Context) {
		t := c.Theme()
		ui.Column(c).Gap(t.Space(3)).MinWidth(400).MaxWidth(520).Children(func() {
			components.Text(c, locales.TryLaterNoteTitle(saved.FullName())).Bold().FontSize(t.Rem(1.1)).Wrap()
			components.TextBox(c, &st.note, components.TextBoxOptions{Multiline: true}).
				Label(locales.TryLaterNoteLabel()).Placeholder(locales.TryLaterNotePlaceholder()).FillWidth().MinHeight(96)
			ui.Row(c).FillWidth().Gap(t.Space(2)).Justify(ui.End).Children(func() {
				if components.Button(c, locales.Cancel(), components.ButtonOutline).Clicked() {
					s.dialogOpen = false
				}
				if components.Button(c, locales.TryLaterNoteSave(), components.ButtonPrimary).Clicked() {
					s.dialogOpen = false
					s.saveNote(saved, strings.TrimSpace(st.note))
				}
			})
		})
	})
}

// saveNote keeps a saved repository's note, in the background.
func (s *screen) saveNote(saved catalog.TryLater, note string) {
	s.background(func(ctx context.Context) error {
		err := s.catalog.SetTryLaterNote(ctx, saved.ID, note)
		switch {
		case ctx.Err() != nil:
			return fmt.Errorf("save note of %s: %w", saved.FullName(), ctx.Err())
		case err != nil:
			s.record(ctx, activity.Error, 0, locales.ActivityTryLaterFailed(saved.FullName(), err.Error()))
			s.toast(components.ToastError, locales.ActivityTryLaterFailed(saved.FullName(), err.Error()))
			return err //nolint:wrapcheck // The catalog says what it was saving.
		}
		return nil
	})
}
