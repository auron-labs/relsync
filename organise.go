package main

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/category"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// itemDrag is the value a card carries while it is dragged onto a group.
type itemDrag struct{ id int64 }

// groupTitle is the name shown for a category, and for the place of Items
// that have no group while automatic groups are off.
func groupTitle(key category.Key) string {
	switch key {
	case category.Games:
		return locales.CategoryGames()
	case category.Media:
		return locales.CategoryMedia()
	case category.Graphics:
		return locales.CategoryGraphics()
	case category.Productivity:
		return locales.CategoryProductivity()
	case category.Security:
		return locales.CategorySecurity()
	case category.Internet:
		return locales.CategoryInternet()
	case category.Communication:
		return locales.CategoryCommunication()
	case category.AI:
		return locales.CategoryAi()
	case category.System:
		return locales.CategorySystem()
	case category.Developer:
		return locales.CategoryDeveloper()
	case category.Other:
		return locales.CategoryOther()
	}
	return locales.GroupUngrouped()
}

func (s *screen) setFavorite(e catalog.Entry, favorite bool) {
	s.mutate(e, func(ctx context.Context) error { return s.catalog.SetFavorite(ctx, e.Item.ID, favorite) }, "")
}

func (s *screen) setHidden(e catalog.Entry, hidden bool) {
	s.mutate(e, func(ctx context.Context) error {
		if err := s.catalog.SetHidden(ctx, e.Item.ID, hidden); err != nil {
			return err //nolint:wrapcheck // mutate says which Item it was changing.
		}
		if hidden {
			s.toast(components.ToastInfo, locales.ToastHidden(e.Title()))
		}
		return nil
	}, "")
}

// moveToGroup puts an Item in one of the user's groups, or back in the group
// its category gives it when groupID is 0.
func (s *screen) moveToGroup(e catalog.Entry, groupID int64) {
	s.mutate(e, func(ctx context.Context) error {
		if err := s.catalog.SetGroup(ctx, e.Item.ID, groupID); err != nil {
			return err //nolint:wrapcheck // mutate says which Item it was changing.
		}
		s.toast(components.ToastSuccess, s.movedText(e, groupID))
		return nil
	}, "")
}

func (s *screen) movedText(e catalog.Entry, groupID int64) string {
	for _, g := range s.catalog.Groups().Get() {
		if g.ID == groupID {
			return locales.ToastGroupMoved(e.Title(), g.Name)
		}
	}
	return locales.ToastGroupAutomatic(e.Title())
}

// saveNotes keeps the user's note and tags for an Item.
func (s *screen) saveNotes(e catalog.Entry, note string, tags []string) {
	s.mutate(e, func(ctx context.Context) error {
		if err := s.catalog.SetNote(ctx, e.Item.ID, note); err != nil {
			return err //nolint:wrapcheck // mutate says which Item it was changing.
		}
		return s.catalog.SetTags(ctx, e.Item.ID, tags)
	}, "")
}

// groupNameProblem says, for the person, why name cannot be a group's, empty
// when it can. self is the group being renamed, or 0. A category's name is
// refused too: two sections of the same name would be confusing.
func (s *screen) groupNameProblem(name string, self int64) string {
	switch err := s.catalog.ValidateGroupName(name, self); {
	case err == nil:
	case strings.TrimSpace(name) == "":
		return locales.GroupNameRequired()
	default:
		return locales.GroupNameExists()
	}
	name = strings.TrimSpace(name)
	if slices.ContainsFunc(category.All(), func(k category.Key) bool { return strings.EqualFold(groupTitle(k), name) }) {
		return locales.GroupNameExists()
	}
	return ""
}

// groupAction runs a change to the user's groups in the background; a failure
// is recorded and shown.
func (s *screen) groupAction(name string, change func(ctx context.Context) error) {
	s.background(func(ctx context.Context) error {
		err := change(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() == nil {
			s.record(ctx, activity.Error, 0, locales.ActivityActionFailed(name, err.Error()))
			s.toast(components.ToastError, locales.ActivityActionFailed(name, err.Error()))
		}
		return err
	})
}

// groupNameState is the name being typed in the dialog that makes or renames a
// group.
type groupNameState struct{ text, problem string }

// newGroupDialog asks for the name of a group to make. With assign set, the Item
// is moved into it.
func (s *screen) newGroupDialog(assign *catalog.Entry) {
	st := &groupNameState{}
	body := locales.GroupNewBody()
	if assign != nil {
		body = locales.GroupNewBodyFor(assign.Title())
	}
	s.groupNameDialog(locales.GroupNewTitle(), body, locales.GroupCreate(), st, func(name string) {
		s.groupAction(name, func(ctx context.Context) error {
			g, err := s.catalog.CreateGroup(ctx, name)
			if err != nil {
				return err //nolint:wrapcheck // groupAction says which group it was making.
			}
			if assign == nil {
				s.toast(components.ToastSuccess, locales.ToastGroupCreated(g.Name))
				return nil
			}
			if err := s.catalog.SetGroup(ctx, assign.Item.ID, g.ID); err != nil {
				return err //nolint:wrapcheck // groupAction says which group it was making.
			}
			s.toast(components.ToastSuccess, locales.ToastGroupMoved(assign.Title(), g.Name))
			return nil
		})
	}, 0)
}

// renameGroupDialog asks for a new name for one of the user's groups.
func (s *screen) renameGroupDialog(g catalog.Group) {
	st := &groupNameState{text: g.Name}
	s.groupNameDialog(locales.GroupRenameTitle(), "", locales.GroupRename(), st, func(name string) {
		s.groupAction(name, func(ctx context.Context) error { return s.catalog.RenameGroup(ctx, g.ID, name) })
	}, g.ID)
}

// groupNameDialog is the dialog that reads a group's name. confirm gets the
// name once it is acceptable.
func (s *screen) groupNameDialog(title, body, confirmLabel string, st *groupNameState, confirm func(name string), self int64) {
	s.showDialog(title, func(c *ui.Context) {
		t := c.Theme()
		ui.Column(c).Gap(t.Space(3)).MinWidth(360).MaxWidth(480).Children(func() {
			components.Text(c, title).Bold().FontSize(t.Rem(1.1)).Wrap()
			if body != "" {
				components.Text(c, body).TextColor(t.TextMuted).Wrap()
			}
			box := components.TextBox(c, &st.text, components.TextBoxOptions{}).
				Label(locales.GroupNameLabel()).Placeholder(locales.GroupNamePlaceholder()).FillWidth()
			submitted := box.Submitted()
			if st.problem != "" {
				components.Text(c, st.problem).TextColor(t.Danger).Wrap()
			}
			ui.Row(c).FillWidth().Gap(t.Space(2)).Justify(ui.End).Children(func() {
				if components.Button(c, locales.Cancel(), components.ButtonOutline).Clicked() {
					s.dialogOpen = false
				}
				if components.Button(c, confirmLabel, components.ButtonPrimary).Clicked() || submitted {
					if st.problem = s.groupNameProblem(st.text, self); st.problem != "" {
						return
					}
					s.dialogOpen = false
					confirm(strings.TrimSpace(st.text))
				}
			})
		})
	})
}

// deleteGroupDialog asks before deleting one of the user's groups.
func (s *screen) deleteGroupDialog(g catalog.Group) {
	inside := 0
	for _, e := range s.catalog.Entries().Get() {
		if e.Item.GroupID == g.ID {
			inside++
		}
	}
	body := locales.GroupDeleteEmpty()
	if inside > 0 {
		body = locales.GroupDeleteBody(inside)
	}
	s.showDialog(locales.GroupDeleteTitle(g.Name), func(c *ui.Context) {
		t := c.Theme()
		ui.Column(c).Gap(t.Space(3)).MinWidth(360).MaxWidth(480).Children(func() {
			components.Text(c, locales.GroupDeleteTitle(g.Name)).Bold().FontSize(t.Rem(1.1)).Wrap()
			components.Text(c, body).TextColor(t.TextMuted).Wrap()
			ui.Row(c).FillWidth().Gap(t.Space(2)).Justify(ui.End).Children(func() {
				if components.Button(c, locales.Cancel(), components.ButtonOutline).Clicked() {
					s.dialogOpen = false
				}
				if components.Button(c, locales.GroupDelete(), components.ButtonPrimary).Clicked() {
					s.dialogOpen = false
					s.groupAction(g.Name, func(ctx context.Context) error { return s.catalog.DeleteGroup(ctx, g.ID) })
				}
			})
		})
	})
}

// groupOptions are the choices of a group for an Item: the automatic one, then
// the user's groups. A group's value is its id.
func (s *screen) groupOptions() []option {
	automatic := locales.MenuGroupAutomatic()
	if !s.prefs.Get().AutoGroups {
		automatic = locales.MenuGroupNone()
	}
	groups := s.catalog.Groups().Get()
	options := make([]option, 1, 1+len(groups))
	options[0] = option{"", automatic}
	for _, g := range groups {
		options = append(options, option{strconv.FormatInt(g.ID, 10), g.Name})
	}
	return options
}

// groupValue is the option value of an Item's own group.
func groupValue(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// parseGroupValue is the group id an option value stands for.
func parseGroupValue(value string) int64 {
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}

// notesEditor is the note and tags being edited in the drawer. saved is the
// Item's values they started from, so a change made elsewhere replaces what is
// shown.
type notesEditor struct {
	id                   int64
	savedNote, savedTags string
	note, tags           string
}

// drawerGroup is the Item's group selector.
func (s *screen) drawerGroup(c *ui.Context, e catalog.Entry) {
	options := s.groupOptions()
	labelled(c, locales.DrawerGroup(), func() {
		choice, changed := dropdown(c, locales.DrawerGroupLabel(), groupValue(e.Item.GroupID), options)
		if changed {
			s.moveToGroup(e, parseGroupValue(choice))
		}
		if components.Button(c, locales.MenuNewGroup(), components.ButtonGhost).Clicked() {
			s.newGroupDialog(&e)
		}
	})
}

// drawerYourNotes is the Item's note and tags, searchable from the Library.
// Tags are typed as text and shown as chips.
func (s *screen) drawerYourNotes(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	saved := catalog.FormatTags(e.Item.Tags)
	ed := &s.notesEdit
	if ed.id != e.Item.ID || ed.savedNote != e.Item.Note || ed.savedTags != saved {
		*ed = notesEditor{id: e.Item.ID, savedNote: e.Item.Note, savedTags: saved, note: e.Item.Note, tags: saved}
	}
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.Text(c, locales.DrawerYourNotes()).Bold()
		components.TextBox(c, &ed.note, components.TextBoxOptions{Multiline: true}).
			Label(locales.DrawerNoteLabel()).Placeholder(locales.DrawerNotePlaceholder()).FillWidth().Lines(3, 6)
		components.TextBox(c, &ed.tags, components.TextBoxOptions{}).
			Label(locales.DrawerTagsLabel()).Placeholder(locales.DrawerTagsPlaceholder()).FillWidth()
		if len(e.Item.Tags) > 0 {
			ui.Row(c).FillWidth().Wrap().Gap(t.Space(1.5)).Children(func() {
				for _, tag := range e.Item.Tags {
					components.Badge(c, tag).Description(locales.DrawerTagChip(tag))
				}
			})
		}
		unchanged := strings.TrimSpace(ed.note) == e.Item.Note && slices.Equal(catalog.ParseTags(ed.tags), e.Item.Tags)
		if components.Button(c, locales.DrawerNotesSave(), components.ButtonSecondary).Disabled(unchanged).Clicked() {
			s.saveNotes(e, ed.note, catalog.ParseTags(ed.tags))
		}
	})
}
