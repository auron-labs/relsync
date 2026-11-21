package catalog_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/category"
	"github.com/aaronflorey/release-sync/source"
)

func groupNames(h *harness) []string {
	groups := h.catalog.Groups().Get()
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	return names
}

func TestGroupNamesAreRequiredAndUniqueIgnoringCase(t *testing.T) {
	h := openHarness(t)
	ctx := t.Context()
	work, err := h.catalog.CreateGroup(ctx, "  Work tools ")
	if err != nil || work.Name != "Work tools" {
		t.Fatalf("created %+v, %v", work, err)
	}
	if _, err := h.catalog.CreateGroup(ctx, "   "); !errors.Is(err, catalog.ErrGroupName) {
		t.Errorf("a blank name: %v", err)
	}
	if _, err := h.catalog.CreateGroup(ctx, "WORK TOOLS"); !errors.Is(err, catalog.ErrGroupExists) {
		t.Errorf("a name differing in case: %v", err)
	}
	play, err := h.catalog.CreateGroup(ctx, "Play")
	if err != nil {
		t.Fatal(err)
	}
	// Groups are listed by name, whatever order they were made in.
	if got := groupNames(h); !slices.Equal(got, []string{"Play", "Work tools"}) {
		t.Errorf("groups = %v", got)
	}
	if err := h.catalog.RenameGroup(ctx, play.ID, "work tools"); !errors.Is(err, catalog.ErrGroupExists) {
		t.Errorf("renaming onto another group: %v", err)
	}
	if err := h.catalog.RenameGroup(ctx, play.ID, ""); !errors.Is(err, catalog.ErrGroupName) {
		t.Errorf("renaming to nothing: %v", err)
	}
	// Changing only the case of a group's own name is not a clash.
	if err := h.catalog.RenameGroup(ctx, work.ID, "WORK TOOLS"); err != nil {
		t.Errorf("renaming a group to its own name in capitals: %v", err)
	}
	if err := h.catalog.RenameGroup(ctx, 999, "Anything"); !errors.Is(err, catalog.ErrGroupNotFound) {
		t.Errorf("renaming a group that is not there: %v", err)
	}
}

func TestRenamingAGroupKeepsItsItemsAndSurvivesARestart(t *testing.T) {
	h := openHarness(t)
	ctx := t.Context()
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	item := h.add("tool", catalog.AddOptions{})
	group, err := h.catalog.CreateGroup(ctx, "Work")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.SetGroup(ctx, item.Item.ID, group.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.RenameGroup(ctx, group.ID, "Office"); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if got := groupNames(h); !slices.Equal(got, []string{"Office"}) {
		t.Errorf("groups after a restart = %v", got)
	}
	if got := h.get(item.Item.ID).Item.GroupID; got != group.ID {
		t.Errorf("group of the item = %d, want %d", got, group.ID)
	}
}

func TestDeletingAGroupReturnsItsItemsToTheirAutomaticGroups(t *testing.T) {
	h := openHarness(t)
	ctx := t.Context()
	h.source.publish("acme", "inside", page("", rel("v1", 1)))
	h.source.publish("acme", "outside", page("", rel("v1", 1)))
	inside := h.add("inside", catalog.AddOptions{})
	outside := h.add("outside", catalog.AddOptions{})
	doomed, _ := h.catalog.CreateGroup(ctx, "Doomed")
	kept, _ := h.catalog.CreateGroup(ctx, "Kept")
	for _, move := range []struct{ item, group int64 }{{inside.Item.ID, doomed.ID}, {outside.Item.ID, kept.ID}} {
		if err := h.catalog.SetGroup(ctx, move.item, move.group); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.catalog.DeleteGroup(ctx, doomed.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.get(inside.Item.ID).Item.GroupID; got != 0 {
		t.Errorf("an item of the deleted group is in group %d", got)
	}
	if got := h.get(outside.Item.ID).Item.GroupID; got != kept.ID {
		t.Errorf("an item of another group moved to %d", got)
	}
	if err := h.catalog.DeleteGroup(ctx, doomed.ID); !errors.Is(err, catalog.ErrGroupNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	// What was stored agrees with what is shown.
	h.reopen()
	if got := h.get(inside.Item.ID).Item.GroupID; got != 0 {
		t.Errorf("after a restart the item is in group %d", got)
	}
	if got := groupNames(h); !slices.Equal(got, []string{"Kept"}) {
		t.Errorf("groups after a restart = %v", got)
	}
}

func TestAnItemCannotBePutInAGroupThatIsNotThere(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	item := h.add("tool", catalog.AddOptions{})
	if err := h.catalog.SetGroup(t.Context(), item.Item.ID, 42); !errors.Is(err, catalog.ErrGroupNotFound) {
		t.Errorf("SetGroup: %v", err)
	}
	h.source.publish("acme", "other", page("", rel("v1", 1)))
	preview, err := h.catalog.Preview(t.Context(), source.RepoRef{Owner: "acme", Repo: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.catalog.Add(t.Context(), preview, catalog.AddOptions{GroupID: 42}); !errors.Is(err, catalog.ErrGroupNotFound) {
		t.Errorf("Add into a missing group: %v", err)
	}
	if len(h.catalog.Entries().Get()) != 1 {
		t.Error("an item was added despite the error")
	}
}

func TestAddingIntoAGroupAndMovingBackToAutomatic(t *testing.T) {
	h := openHarness(t)
	ctx := t.Context()
	group, _ := h.catalog.CreateGroup(ctx, "Work")
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	item := h.add("tool", catalog.AddOptions{GroupID: group.ID})
	if item.Item.GroupID != group.ID {
		t.Fatalf("added into group %d", item.Item.GroupID)
	}
	if err := h.catalog.SetGroup(ctx, item.Item.ID, 0); err != nil {
		t.Fatal(err)
	}
	if got := h.get(item.Item.ID).Item.GroupID; got != 0 {
		t.Errorf("group after choosing automatic = %d", got)
	}
}

func TestAnItemIsCategorisedFromItsRepositoryWhenAdded(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	h.source.repos["acme/tool"] = source.Repository{
		Owner: "acme", Name: "tool", Description: "A fast video player", Topics: []string{"video", "player"},
	}
	item := h.add("tool", catalog.AddOptions{})
	if item.Item.Category != category.Media {
		t.Fatalf("category = %q", item.Item.Category)
	}
	h.reopen()
	if got := h.get(item.Item.ID).Item.Category; got != category.Media {
		t.Errorf("category after a restart = %q", got)
	}
}

func TestChangingTheRepositoryCategorisesItAgain(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	item := h.add("tool", catalog.AddOptions{})
	h.source.publish("acme", "emu", page("", rel("v1", 1)))
	h.source.repos["acme/emu"] = source.Repository{
		Owner: "acme", Name: "emu", Description: "A console emulator", Topics: []string{"emulator"},
	}
	preview, err := h.catalog.Preview(t.Context(), source.RepoRef{Owner: "acme", Repo: "emu"})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := h.catalog.ChangeRepository(t.Context(), item.Item.ID, preview)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Item.Category != category.Games {
		t.Errorf("category after changing repository = %q", moved.Item.Category)
	}
}

func TestFavoriteHiddenNoteAndTagsSurviveARestart(t *testing.T) {
	h := openHarness(t)
	ctx := t.Context()
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	item := h.add("tool", catalog.AddOptions{})
	id := item.Item.ID
	for _, err := range []error{
		h.catalog.SetFavorite(ctx, id, true),
		h.catalog.SetHidden(ctx, id, true),
		h.catalog.SetNote(ctx, id, "  for the build box \n"),
		h.catalog.SetTags(ctx, id, []string{"work", " Emulator ", "", "WORK"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	h.reopen()
	got := h.get(id).Item
	if !got.Favorite || !got.Hidden || got.Note != "for the build box" || !slices.Equal(got.Tags, []string{"work", "Emulator"}) {
		t.Errorf("item after a restart = %+v", got)
	}
	if err := h.catalog.SetFavorite(ctx, 999, true); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("favouriting an item that is not there: %v", err)
	}
}

func TestParseTagsReadsCommaSeparatedText(t *testing.T) {
	for text, want := range map[string][]string{
		"":                        {},
		" , ,":                    {},
		"work, emulator ,Work":    {"work", "emulator"},
		"one,two,three":           {"one", "two", "three"},
		"two words, another one ": {"two words", "another one"},
	} {
		if got := catalog.ParseTags(text); !slices.Equal(got, want) {
			t.Errorf("ParseTags(%q) = %q, want %q", text, got, want)
		}
	}
	if got := catalog.FormatTags([]string{"a", "b"}); got != "a, b" {
		t.Errorf("FormatTags = %q", got)
	}
}

func TestAHiddenItemHasNoUpdateToActOnButKeepsItsStatus(t *testing.T) {
	h := openHarness(t)
	ctx := t.Context()
	h.source.publish("acme", "tool", page("", rel("v2", 5), rel("v1", 1)))
	item := h.add("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1"})
	if !h.get(item.Item.ID).HasUpdate() {
		t.Fatal("the fixture has no update")
	}
	if err := h.catalog.SetHidden(ctx, item.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	hidden := h.get(item.Item.ID)
	if hidden.HasUpdate() {
		t.Error("a hidden item has an update to act on")
	}
	if got := hidden.Status(false); got != catalog.StatusUpdate {
		t.Errorf("status of a hidden item = %v, want it to still say an update is available", got)
	}
	if err := h.catalog.SetHidden(ctx, item.Item.ID, false); err != nil {
		t.Fatal(err)
	}
	if !h.get(item.Item.ID).HasUpdate() {
		t.Error("unhiding did not bring the update back")
	}
}

func TestCheckAllLeavesHiddenItemsAlone(t *testing.T) {
	h := openHarness(t)
	ctx := t.Context()
	for _, name := range []string{"shown", "hidden"} {
		h.source.publish("acme", name, page("", rel("v1", 1)))
		item := h.add(name, catalog.AddOptions{})
		if name == "hidden" {
			if err := h.catalog.SetHidden(ctx, item.Item.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, before := h.source.counts()
	var total int
	checker := catalog.NewChecker(h.catalog, newManager(t))
	sum, err := checker.CheckAll(ctx, true, 2, func(_, of int, _ string) { total = of })
	if err != nil {
		t.Fatal(err)
	}
	if _, after := h.source.counts(); after-before != 1 {
		t.Errorf("%d release requests, want one for the item that is not hidden", after-before)
	}
	if sum.Total != 2 || sum.Checked != 1 || sum.Skipped != 1 || total != 1 {
		t.Errorf("summary = %+v, progress total %d", sum, total)
	}
}
