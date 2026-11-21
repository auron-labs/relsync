package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/category"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

const (
	workGroup  int64 = 7
	emptyGroup int64 = 9
)

var libraryGroups = []catalog.Group{{ID: emptyGroup, Name: "Empty"}, {ID: workGroup, Name: "Work"}}

// seeded is a small Library: alpha is a favorite command-line tool with an
// update, bravo an up to date game, charlie a media app in Work that is not
// installed, delta a hidden game with an update, and echo a favorite in Work.
func seeded() []catalog.Entry {
	releases := []source.Release{release("v2.0.0", 5, ""), release("v1.0.0", 1, "")}
	item := func(id int64, repo string, cat category.Key, change func(*catalog.Entry)) catalog.Entry {
		e := catalog.Entry{Item: catalog.Item{ID: id, Owner: "acme", Repo: repo, Category: cat}, Releases: releases}
		change(&e)
		return e
	}
	installedAt := func(tag string) func(*catalog.Entry) {
		return func(e *catalog.Entry) { e.Install = &catalog.Install{Tag: tag} }
	}
	return []catalog.Entry{
		item(1, "alpha", category.Developer, func(e *catalog.Entry) {
			installedAt("v1.0.0")(e)
			e.Item.Favorite, e.Item.Kind = true, selection.ItemTool
		}),
		item(2, "bravo", category.Games, installedAt("v2.0.0")),
		item(3, "charlie", category.Media, func(e *catalog.Entry) {
			e.Item.GroupID, e.Item.Note, e.Item.Tags = workGroup, "for the build box", []string{"ci", "Nightly"}
		}),
		item(4, "delta", category.Games, func(e *catalog.Entry) {
			installedAt("v1.0.0")(e)
			e.Item.Hidden = true
		}),
		item(5, "echo", category.Other, func(e *catalog.Entry) { e.Item.GroupID, e.Item.Favorite = workGroup, true }),
	}
}

func query(q libraryQuery) libraryResult {
	notBusy := func(int64) (string, bool) { return "", false }
	return queryLibrary(seeded(), libraryGroups, notBusy, groupTitle, q)
}

func repos(rows []libraryRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.entry.Item.Repo)
	}
	return out
}

func assertRepos(t *testing.T, what string, rows []libraryRow, want ...string) {
	t.Helper()
	if got := repos(rows); !slices.Equal(got, append([]string{}, want...)) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestStatusFiltersAndTheirCounts(t *testing.T) {
	res := query(libraryQuery{auto: true})
	want := [filterCount]int{
		filterAll: 4, filterFavorites: 2, filterUpdates: 1, filterInstalled: 2, filterNotInstalled: 2, filterHidden: 1,
	}
	if res.counts != want {
		t.Errorf("counts = %v, want %v", res.counts, want)
	}
	for filter, repos := range map[libraryFilter][]string{
		filterAll:          {"alpha", "echo", "bravo", "charlie"},
		filterFavorites:    {"alpha", "echo"},
		filterUpdates:      {"alpha"},
		filterInstalled:    {"alpha", "bravo"},
		filterNotInstalled: {"echo", "charlie"},
		filterHidden:       {"delta"},
	} {
		assertRepos(t, "filter", query(libraryQuery{status: filter, auto: true}).rows, repos...)
	}
}

func TestFavoritesComeFirstWhateverTheSortOrder(t *testing.T) {
	for _, sortBy := range []librarySort{sortName, sortRecentlyAdded, sortStatus} {
		rows := query(libraryQuery{sort: sortBy, auto: true}).rows
		if !rows[0].entry.Item.Favorite || !rows[1].entry.Item.Favorite || rows[2].entry.Item.Favorite {
			t.Errorf("sort %d lists %v", sortBy, repos(rows))
		}
	}
}

func TestHiddenItemsAreExcludedEverywhereButTheHiddenFilter(t *testing.T) {
	res := query(libraryQuery{auto: true, text: "delta"})
	assertRepos(t, "searching for a hidden item", res.rows)
	// Not in the counts of the other filters, nor in a group's.
	if res.counts[filterAll] != 4 || res.counts[filterUpdates] != 1 {
		t.Errorf("counts = %v", res.counts)
	}
	for _, chip := range query(libraryQuery{auto: true}).groups {
		if chip.ref == (groupRef{category: category.Games}) && chip.count != 1 {
			t.Errorf("the Games chip counts %d, want the one game that is not hidden", chip.count)
		}
	}
	// Looking at them, the chips count only them.
	hidden := query(libraryQuery{status: filterHidden, auto: true})
	assertRepos(t, "hidden filter", hidden.rows, "delta")
	if hidden.allGroups != 1 {
		t.Errorf("groups hold %d items while looking at hidden ones", hidden.allGroups)
	}
	// The other filters still narrow what is hidden.
	assertRepos(t, "hidden command-line tools", query(libraryQuery{status: filterHidden, kinds: kindFilter{tools: true}}).rows)
}

func TestAHiddenItemWithAnUpdateIsNotCountedAsOne(t *testing.T) {
	if got := query(libraryQuery{auto: true}).counts[filterUpdates]; got != 1 {
		t.Errorf("updates = %d, want only alpha's", got)
	}
}

func TestFiltersForKindGroupAndTextCombine(t *testing.T) {
	assertRepos(t, "tools", query(libraryQuery{kinds: kindFilter{tools: true}, auto: true}).rows, "alpha")
	assertRepos(t, "apps", query(libraryQuery{kinds: kindFilter{apps: true}, auto: true}).rows, "echo", "bravo", "charlie")
	assertRepos(t, "both kinds", query(libraryQuery{kinds: kindFilter{apps: true, tools: true}, auto: true}).rows,
		"alpha", "echo", "bravo", "charlie")
	assertRepos(t, "a group of the user's", query(libraryQuery{group: groupRef{custom: workGroup}, auto: true}).rows, "echo", "charlie")
	assertRepos(t, "a category", query(libraryQuery{group: groupRef{category: category.Games}, auto: true}).rows, "bravo")
	assertRepos(t, "everything at once", query(libraryQuery{
		status: filterNotInstalled, kinds: kindFilter{apps: true}, group: groupRef{custom: workGroup}, text: "echo", auto: true,
	}).rows, "echo")
	assertRepos(t, "filters that exclude each other", query(libraryQuery{
		status: filterFavorites, group: groupRef{category: category.Games}, auto: true,
	}).rows)
}

func TestTextSearchReadsNameGroupNoteAndTags(t *testing.T) {
	for text, want := range map[string][]string{
		"BRAVO":           {"bravo"},
		"acme/charlie":    {"charlie"},
		"build box":       {"charlie"},
		"nightly":         {"charlie"},
		"work":            {"echo", "charlie"},
		"developer tools": {"alpha"},
		"  games ":        {"bravo"},
		"missing":         {},
	} {
		got := repos(query(libraryQuery{text: text, auto: true}).rows)
		if !slices.Equal(got, want) {
			t.Errorf("search %q = %v, want %v", text, got, want)
		}
	}
}

func TestGroupChipsListTheUsersGroupsThenCategoriesWithItems(t *testing.T) {
	res := query(libraryQuery{auto: true})
	titles := make([]string, 0, len(res.groups))
	for _, c := range res.groups {
		titles = append(titles, c.title)
	}
	// An empty group of the user's stays; an empty category does not.
	want := []string{"Empty", "Work", "Developer Tools", "Games & Emulation"}
	if !slices.Equal(titles, want) {
		t.Errorf("chips = %v, want %v", titles, want)
	}
	counts := map[string]int{}
	for _, c := range res.groups {
		counts[c.title] = c.count
	}
	if counts["Empty"] != 0 || counts["Work"] != 2 || res.allGroups != 4 {
		t.Errorf("counts = %v, all %d", counts, res.allGroups)
	}
}

func TestWithoutAutomaticGroupsItemsOutsideTheUsersGroupsAreUngrouped(t *testing.T) {
	res := query(libraryQuery{auto: false})
	titles := make([]string, 0, len(res.groups))
	for _, c := range res.groups {
		titles = append(titles, c.title)
	}
	if want := []string{"Empty", "Work", "Ungrouped"}; !slices.Equal(titles, want) {
		t.Errorf("chips = %v, want %v", titles, want)
	}
	assertRepos(t, "ungrouped", query(libraryQuery{group: groupRef{category: ungrouped}}).rows, "alpha", "bravo")
}

func TestSectionsFollowGroupOrderAndKeepEmptyGroupsOfTheUsersToDropOn(t *testing.T) {
	res := query(libraryQuery{auto: true, sections: true})
	got := make([]string, 0, len(res.sections))
	for _, s := range res.sections {
		got = append(got, s.title+":"+strings.Join(repos(s.rows), ","))
	}
	want := []string{"Empty:", "Work:echo,charlie", "Developer Tools:alpha", "Games & Emulation:bravo"}
	if !slices.Equal(got, want) {
		t.Errorf("sections = %v, want %v", got, want)
	}
	assertRepos(t, "flattened", res.rows, "echo", "charlie", "alpha", "bravo")

	// Empty groups are shown only while nothing but a group narrows the Library.
	for name, q := range map[string]libraryQuery{
		"status": {status: filterFavorites, auto: true, sections: true},
		"kind":   {kinds: kindFilter{apps: true}, auto: true, sections: true},
		"text":   {text: "a", auto: true, sections: true},
	} {
		for _, s := range query(q).sections {
			if s.title == "Empty" {
				t.Errorf("the empty group is shown with a %s filter", name)
			}
		}
	}
	only := query(libraryQuery{group: groupRef{custom: emptyGroup}, auto: true, sections: true})
	if len(only.sections) != 1 || only.sections[0].title != "Empty" {
		t.Errorf("sections of the empty group = %+v", only.sections)
	}
}

func TestOtherAndUngroupedCloseTheListOfSections(t *testing.T) {
	entries := seeded()
	entries[4].Item.GroupID = 0
	entries = append(entries, catalog.Entry{Item: catalog.Item{ID: 6, Owner: "acme", Repo: "zulu", Category: category.Security}})
	notBusy := func(int64) (string, bool) { return "", false }
	sections := queryLibrary(entries, libraryGroups, notBusy, groupTitle, libraryQuery{auto: true, sections: true}).sections
	titles := make([]string, 0, len(sections))
	for _, s := range sections {
		titles = append(titles, s.title)
	}
	want := []string{"Empty", "Work", "Developer Tools", "Games & Emulation", "Security & Privacy", "Other"}
	if !slices.Equal(titles, want) {
		t.Errorf("sections = %v, want %v", titles, want)
	}
}

func TestFiltersThatNameSomethingGoneAreDropped(t *testing.T) {
	res := query(libraryQuery{status: filterHidden, auto: true})
	if res.status != filterHidden {
		t.Fatalf("status = %d", res.status)
	}
	entries := seeded()[:3] // nothing hidden any more
	notBusy := func(int64) (string, bool) { return "", false }
	res = queryLibrary(entries, libraryGroups, notBusy, groupTitle, libraryQuery{status: filterHidden, auto: true})
	if res.status != filterAll || len(res.rows) != 3 {
		t.Errorf("the Hidden filter with nothing hidden: status %d, %d rows", res.status, len(res.rows))
	}
	res = query(libraryQuery{group: groupRef{custom: 99}, auto: true})
	if res.group != (groupRef{}) || len(res.rows) != 4 {
		t.Errorf("a group that is gone: group %+v, %d rows", res.group, len(res.rows))
	}
	// A category with nothing in it is not a chip any more either.
	res = query(libraryQuery{group: groupRef{category: category.Security}, auto: true})
	if res.group != (groupRef{}) {
		t.Errorf("a category with nothing in it: %+v", res.group)
	}
}
