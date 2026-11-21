package main

import (
	"slices"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
)

// groupsFixture tracks a game, a media app and one that fits no category.
func groupsFixture(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, defaultPreferences())
	for name, description := range map[string]string{
		"dolphin": "A console emulator", "mpv": "A video player", "quokka": "Something else",
	} {
		e.source.publish(name, description, release("v1.0.0", 1, ""))
		e.track(name, catalog.AddOptions{})
	}
	return e
}

func (e *env) entryOf(name string) catalog.Entry {
	e.t.Helper()
	entry, ok := e.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: name})
	if !ok {
		e.t.Fatalf("%s is not tracked", name)
	}
	return entry
}

// makeGroup makes the group "Work tools" and puts the named repositories in it.
func (e *env) makeGroup(repos ...string) catalog.Group {
	e.t.Helper()
	g, err := e.screen.catalog.CreateGroup(e.t.Context(), "Work tools")
	if err != nil {
		e.t.Fatal(err)
	}
	for _, repo := range repos {
		if err := e.screen.catalog.SetGroup(e.t.Context(), e.entryOf(repo).Item.ID, g.ID); err != nil {
			e.t.Fatal(err)
		}
	}
	return g
}

func TestTheLibraryIsArrangedIntoSectionsByCategory(t *testing.T) {
	e := groupsFixture(t)
	tt := e.open()
	requireText(t, tt, "Games & Emulation", "Media & Entertainment", "Other", "acme/dolphin", "acme/mpv", "acme/quokka",
		"3 apps · everything is up to date")
}

func TestSectionsFoldAndOpenOneAtATimeOrAllAtOnce(t *testing.T) {
	e := groupsFixture(t)
	tt := e.open()
	click(t, tt, "Show or hide the apps in Games & Emulation")
	requireNoText(t, tt, "acme/dolphin")
	requireText(t, tt, "acme/mpv", "acme/quokka", "Games & Emulation")

	click(t, tt, "Fold all sections")
	requireNoText(t, tt, "acme/mpv", "acme/quokka")
	requireText(t, tt, "Media & Entertainment", "Other")

	click(t, tt, "Open all sections")
	requireText(t, tt, "acme/dolphin", "acme/mpv", "acme/quokka")
}

func TestSectionsAlsoLayOutTheListView(t *testing.T) {
	prefs := defaultPreferences()
	prefs.LibraryView = viewList
	e := newEnv(t, prefs)
	e.source.publish("dolphin", "A console emulator", release("v1.0.0", 1, ""))
	e.track("dolphin", catalog.AddOptions{})
	tt := e.open()
	requireText(t, tt, "Games & Emulation", "acme/dolphin", "v1.0.0 available")
}

func TestSectionsAndAutomaticGroupsCanBeTurnedOff(t *testing.T) {
	e := groupsFixture(t)
	tt := e.open()
	click(t, tt, "Group apps into sections")
	if e.screen.prefs.Get().GroupSections {
		t.Error("sections are still on")
	}
	requireText(t, tt, "acme/dolphin", "acme/mpv", "acme/quokka")
	requireNoText(t, tt, "Fold all sections", "Open all sections")

	click(t, tt, "Auto")
	if e.screen.prefs.Get().AutoGroups {
		t.Error("automatic groups are still on")
	}
	requireText(t, tt, "Ungrouped")
	requireNoText(t, tt, "Games & Emulation", "Media & Entertainment")
}

func TestMakingAGroupAsksForAUniqueName(t *testing.T) {
	e := groupsFixture(t)
	e.makeGroup()
	tt := e.open()
	click(t, tt, "New group")
	requireText(t, tt, "Groups let you organize your library your own way.")
	click(t, tt, "Create group")
	requireText(t, tt, "Give the group a name.")

	// A name another group has, in any case, or one a category has, is taken.
	for _, taken := range []string{"work TOOLS", "Games & Emulation"} {
		click(t, tt, "Cancel")
		click(t, tt, "New group")
		typeInto(t, tt, "Group name", taken)
		click(t, tt, "Create group")
		requireText(t, tt, "A group with that name already exists.")
	}

	click(t, tt, "Cancel")
	click(t, tt, "New group")
	typeInto(t, tt, "Group name", "  Play ")
	click(t, tt, "Create group")
	e.settle()
	groups := e.screen.catalog.Groups().Get()
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	if !slices.Equal(names, []string{"Play", "Work tools"}) {
		t.Errorf("groups = %v", names)
	}
	// An empty group stays in the list, to be dropped on.
	requireText(t, tt, "Play, 0 apps", "Created group “Play”")
}

// openMenu opens the menu of an Item's card.
func openMenu(t *testing.T, tt *ui.Tester, repo string) {
	t.Helper()
	click(t, tt, "More actions for "+repo)
	tt.Frame()
}

func TestMovingAnItemToAGroupFromItsMenu(t *testing.T) {
	e := groupsFixture(t)
	e.makeGroup()
	tt := e.open()
	openMenu(t, tt, "dolphin")
	requireText(t, tt, "Move to group", "Automatic (by type)", "New group…")
	// The names of groups also label chips and sections, so the item is chosen
	// by its place: Install, Check, Open details, Add to favorites, Automatic, Work tools.
	pickMenuItem(tt, 6)
	e.settle()
	if got := e.entryOf("dolphin").Item.GroupID; got == 0 {
		t.Fatal("the item did not move")
	}
	requireText(t, tt, "Moved dolphin to Work tools", "Work tools, 1 app")
	// It is shown under its group now, not its category.
	requireNoText(t, tt, "Games & Emulation")

	openMenu(t, tt, "dolphin")
	click(t, tt, "Automatic (by type)")
	e.settle()
	if got := e.entryOf("dolphin").Item.GroupID; got != 0 {
		t.Errorf("group after choosing automatic = %d", got)
	}
	requireText(t, tt, "Games & Emulation", "dolphin is back in its automatic group")
}

func TestMakingAGroupFromAnItemsMenuMovesTheItemIn(t *testing.T) {
	e := groupsFixture(t)
	tt := e.open()
	openMenu(t, tt, "mpv")
	click(t, tt, "New group…")
	requireText(t, tt, "Create a group and move mpv into it.")
	typeInto(t, tt, "Group name", "Watching")
	click(t, tt, "Create group")
	e.settle()
	groups := e.screen.catalog.Groups().Get()
	if len(groups) != 1 || groups[0].Name != "Watching" || e.entryOf("mpv").Item.GroupID != groups[0].ID {
		t.Errorf("groups = %v, item in group %d", groups, e.entryOf("mpv").Item.GroupID)
	}
}

// drag presses on the element labelled from and releases over the one labelled to.
func drag(t *testing.T, tt *ui.Tester, from, to string) {
	t.Helper()
	src, ok := tt.Find(from)
	if !ok {
		t.Fatalf("no %q to drag", from)
	}
	dst, ok := tt.Find(to)
	if !ok {
		t.Fatalf("no %q to drop on", to)
	}
	sx, sy := src.X+src.W/2, src.Y+src.H/2
	dx, dy := dst.X+dst.W/2, dst.Y+dst.H/2
	tt.Press(sx, sy)
	for step := 1; step <= 4; step++ {
		f := float32(step) / 4
		tt.Move(sx+(dx-sx)*f, sy+(dy-sy)*f)
	}
	tt.Release(dx, dy)
	tt.Frame()
}

func TestDraggingACardOntoAGroupChipMovesIt(t *testing.T) {
	e := groupsFixture(t)
	g := e.makeGroup()
	tt := e.open()
	drag(t, tt, "Open details for dolphin", "Work tools, 0 apps")
	e.settle()
	if got := e.entryOf("dolphin").Item.GroupID; got != g.ID {
		t.Fatalf("group after the drop = %d, want %d", got, g.ID)
	}
	if got := e.entryOf("mpv").Item.GroupID; got != 0 {
		t.Errorf("another item moved to group %d", got)
	}
	// A press that hardly moves is still a click, not a drag.
	click(t, tt, "Open details for mpv")
	if !e.screen.drawer.open {
		t.Error("clicking a card no longer opens its details")
	}
}

func TestDraggingACardOntoASectionMovesIt(t *testing.T) {
	e := groupsFixture(t)
	g := e.makeGroup()
	tt := e.open()
	requireText(t, tt, "Nothing here yet. Drag an app onto this group to add it.")
	drag(t, tt, "Open details for mpv", "Nothing here yet. Drag an app onto this group to add it.")
	e.settle()
	if got := e.entryOf("mpv").Item.GroupID; got != g.ID {
		t.Fatalf("group after the drop = %d, want %d", got, g.ID)
	}
}

func TestOnlyTheUsersGroupsTakeDroppedCards(t *testing.T) {
	e := groupsFixture(t)
	tt := e.open()
	drag(t, tt, "Open details for dolphin", "Media & Entertainment, 1 app")
	e.settle()
	if got := e.entryOf("dolphin").Item.GroupID; got != 0 {
		t.Errorf("a card dropped on a category moved to group %d", got)
	}
}

func TestRenamingAGroupFromItsMenuKeepsItsItems(t *testing.T) {
	e := groupsFixture(t)
	g := e.makeGroup("dolphin")
	tt := e.open()
	if err := tt.RightClick("Work tools, 1 app"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	click(t, tt, "Rename group…")
	typeInto(t, tt, "Group name", " 2")
	click(t, tt, "Rename")
	e.settle()
	groups := e.screen.catalog.Groups().Get()
	if len(groups) != 1 || groups[0].ID != g.ID || groups[0].Name != "Work tools 2" {
		t.Fatalf("groups = %v", groups)
	}
	requireText(t, tt, "Work tools 2, 1 app")
	if e.entryOf("dolphin").Item.GroupID != g.ID {
		t.Error("the item left its group")
	}
}

func TestDeletingAGroupSendsItsItemsBackToTheirCategories(t *testing.T) {
	e := groupsFixture(t)
	e.makeGroup("dolphin")
	tt := e.open()
	requireNoText(t, tt, "Games & Emulation")
	if err := tt.RightClick("Work tools, 1 app"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	click(t, tt, "Delete group…")
	requireText(t, tt, "Delete “Work tools”?", "The 1 app inside go back to their automatic groups. Nothing is uninstalled.")
	click(t, tt, "Delete group")
	e.settle()
	if got := e.screen.catalog.Groups().Get(); len(got) != 0 {
		t.Errorf("groups = %v", got)
	}
	requireText(t, tt, "Games & Emulation", "acme/dolphin")
	if e.entryOf("dolphin").Item.GroupID != 0 {
		t.Error("the item is still in the deleted group")
	}
}

func TestHidingAnItemTakesItOutOfTheLibraryUntilItIsUnhidden(t *testing.T) {
	e := groupsFixture(t)
	tt := e.open()
	requireNoText(t, tt, "1 hidden")
	openMenu(t, tt, "dolphin")
	click(t, tt, "Hide from library")
	e.settle()
	requireText(t, tt, "dolphin is hidden. Find it under Hidden.", "2 apps · everything is up to date · 1 hidden", "Hidden")
	requireNoText(t, tt, "acme/dolphin")

	click(t, tt, "Hidden")
	requireText(t, tt, "acme/dolphin")
	requireNoText(t, tt, "acme/mpv", "acme/quokka")
	openMenu(t, tt, "dolphin")
	click(t, tt, "Unhide")
	e.settle()
	// With nothing hidden any more the Hidden filter is gone and everything shows.
	requireText(t, tt, "acme/dolphin", "acme/mpv", "acme/quokka", "3 apps")
	requireNoText(t, tt, "1 hidden")
	if e.screen.library.status != filterAll {
		t.Errorf("filter = %d", e.screen.library.status)
	}
}

func TestStarringAnItemMakesItAFavorite(t *testing.T) {
	e := groupsFixture(t)
	tt := e.open()
	click(t, tt, "Add quokka to favorites")
	e.settle()
	if !e.entryOf("quokka").Item.Favorite {
		t.Fatal("the item is not a favorite")
	}
	requireText(t, tt, "Remove quokka from favorites")
	click(t, tt, "★ Favorites")
	requireText(t, tt, "acme/quokka")
	requireNoText(t, tt, "acme/dolphin", "acme/mpv")
	click(t, tt, "Remove quokka from favorites")
	e.settle()
	if e.entryOf("quokka").Item.Favorite {
		t.Error("the item is still a favorite")
	}
}

func TestNotesAndTagsAreEditedInTheDrawerAndSearchable(t *testing.T) {
	e := groupsFixture(t)
	tt := e.open()
	tt.SetSize(windowWidth, 2400)
	e.screen.openItem(e.entryOf("dolphin").Item.ID)
	tt.Frame()
	requireText(t, tt, "Your notes", "Group")
	typeInto(t, tt, "Note", "for the build box")
	typeInto(t, tt, "Tags", "ci, Nightly , ci")
	click(t, tt, "Save notes")
	e.settle()
	got := e.entryOf("dolphin").Item
	if got.Note != "for the build box" || !slices.Equal(got.Tags, []string{"ci", "Nightly"}) {
		t.Fatalf("note %q, tags %q", got.Note, got.Tags)
	}
	requireText(t, tt, "ci", "Nightly")

	typeInto(t, tt, "Filter your library", "nightly")
	requireText(t, tt, "acme/dolphin")
	requireNoText(t, tt, "acme/mpv", "acme/quokka")
}

func TestTheDrawerMovesAnItemToAGroup(t *testing.T) {
	e := groupsFixture(t)
	g := e.makeGroup()
	tt := e.open()
	tt.SetSize(windowWidth, 2400)
	e.screen.openItem(e.entryOf("mpv").Item.ID)
	tt.Frame()
	click(t, tt, "Choose a group")
	tt.Frame()
	pickMenuItem(tt, 2) // Automatic, then Work tools.
	e.settle()
	if got := e.entryOf("mpv").Item.GroupID; got != g.ID {
		t.Errorf("group = %d, want %d", got, g.ID)
	}
}

func TestTheAddDialogPutsTheNewItemInTheChosenGroup(t *testing.T) {
	prefs := defaultPreferences()
	prefs.InstallAfterAdding = false
	e := newEnv(t, prefs)
	e.source.publish("newcomer", "Something new", release("v1.0.0", 1, ""))
	g := e.makeGroup()
	tt := e.open()
	e.screen.startAdd(source.RepoRef{Owner: testOwner, Repo: "newcomer"})
	waitText(t, tt, "Add to library")
	click(t, tt, "Choose a group")
	tt.Frame()
	pickMenuItem(tt, 2) // Automatic, then Work tools.
	click(t, tt, "Add")
	e.settle()
	if got := e.entryOf("newcomer").Item.GroupID; got != g.ID {
		t.Errorf("group of the new item = %d, want %d", got, g.ID)
	}
}

// menuEntries sorts labels into those an open menu shows and those it lacks.
func menuEntries(tt *ui.Tester, labels ...string) (present, missing []string) {
	for _, l := range labels {
		if hasLabel(tt, l) {
			present = append(present, l)
		} else {
			missing = append(missing, l)
		}
	}
	return present, missing
}

func TestTheItemMenuOffersWhatAppliesToAnItemThatIsNotInstalled(t *testing.T) {
	e := groupsFixture(t)
	tt := e.open()
	openMenu(t, tt, "dolphin")
	_, missing := menuEntries(tt, "Install", "Check for updates", "Open details", "Add to favorites", "Move to group", "Manage",
		"Mark as installed (current version)", "Change repository…", "Project", "Repository details",
		"More from this developer", "Open on GitHub", "Hide from library", "Stop tracking")
	if len(missing) > 0 {
		t.Errorf("the menu lacks %v", missing)
	}
	present, _ := menuEntries(tt, "Update", "Launch", "See file location", "Open folder", "Skip this version", "Reinstall",
		"Uninstall from this PC", "Stop tracking (keeps it installed)", "Unhide", "Remove from favorites")
	if len(present) > 0 {
		t.Errorf("the menu offers %v for an item that is not installed", present)
	}
}

func TestTheItemMenuOffersWhatAppliesToAnInstalledItemThatIsBehind(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("app", "An app", release("v2.0.0", 5, ""), release("v1.0.0", 1, ""))
	rec := install.AdoptPortable(t.TempDir(), "app.exe")
	entry := e.track("app", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0", Install: &rec})
	if err := e.screen.catalog.SetFavorite(t.Context(), entry.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	tt := e.open()
	openMenu(t, tt, "app")
	_, missing := menuEntries(tt, "Update", "Launch", "See file location", "Skip this version", "Reinstall",
		"Uninstall from this PC", "Stop tracking (keeps it installed)", "Remove from favorites")
	if len(missing) > 0 {
		t.Errorf("the menu lacks %v", missing)
	}
	present, _ := menuEntries(tt, "Install", "Add to favorites", "Stop tracking", "Unhide")
	if len(present) > 0 {
		t.Errorf("the menu offers %v", present)
	}
}

func TestTheMenuOfAnItemThatIsUpToDateOffersNoUpdateAndTheMenuOfAHiddenOneUnhides(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("app", "An app", release("v1.0.0", 1, ""))
	entry := e.track("app", catalog.AddOptions{AlreadyHave: true})
	if err := e.screen.catalog.SetHidden(t.Context(), entry.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	tt := e.open()
	click(t, tt, "Hidden")
	openMenu(t, tt, "app")
	_, missing := menuEntries(tt, "Unhide", "Check for updates")
	present, _ := menuEntries(tt, "Update", "Install", "Skip this version", "Hide from library")
	if len(missing) > 0 || len(present) > 0 {
		t.Errorf("menu lacks %v and offers %v", missing, present)
	}
}

func TestAHiddenItemIsOutOfTheUpdatesPageTheBadgeAndTheCheck(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("app", "An app", release("v2.0.0", 5, ""), release("v1.0.0", 1, ""))
	entry := e.track("app", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	if got := e.screen.updateCount.Get(); got != 1 {
		t.Fatalf("updates before hiding = %d", got)
	}
	if err := e.screen.catalog.SetHidden(t.Context(), entry.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := e.screen.updateCount.Get(); got != 0 {
		t.Errorf("the badge counts %d updates with the only one hidden", got)
	}
	tt := e.open()
	click(t, tt, "Updates")
	requireText(t, tt, "You're all caught up")

	before := e.source.releaseRequests()
	e.screen.checkLibrary(false)
	e.settle()
	if got := e.source.releaseRequests() - before; got != 0 {
		t.Errorf("a check of the library asked about a hidden item %d times", got)
	}
}
