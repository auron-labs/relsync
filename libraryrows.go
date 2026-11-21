package main

import (
	"cmp"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/category"
	"github.com/aaronflorey/release-sync/selection"
)

// libraryFilter narrows the Library by status.
type libraryFilter int

// The Library's status filters, in the order their tabs appear.
const (
	filterAll libraryFilter = iota
	filterFavorites
	filterUpdates
	filterInstalled
	filterNotInstalled
	filterHidden
	filterCount
)

// matches reports whether the Item passes the status filter. Hidden Items are
// kept out of every filter but their own by the query, not here.
func (f libraryFilter) matches(e catalog.Entry) bool {
	switch f {
	case filterFavorites:
		return e.Item.Favorite
	case filterUpdates:
		return e.HasUpdate()
	case filterInstalled:
		return e.InstalledVersion() != ""
	case filterNotInstalled:
		return e.InstalledVersion() == ""
	case filterHidden:
		return e.Item.Hidden
	case filterAll, filterCount:
	}
	return true
}

// kindFilter is the kinds the Library is narrowed to: none selected shows both.
type kindFilter struct{ apps, tools bool }

func (k kindFilter) matches(kind selection.ItemKind) bool {
	switch {
	case k.apps == k.tools:
		return true
	case k.apps:
		return kind == selection.ItemApp
	}
	return kind == selection.ItemTool
}

// librarySort orders the Library.
type librarySort int

// The Library's sort orders.
const (
	sortName librarySort = iota
	sortRecentlyUpdated
	sortRecentlyAdded
	sortStatus
)

// ungrouped is the place of an Item with no group of its own while automatic
// groups are off. It is not a category, but it is shown like one.
const ungrouped category.Key = "ungrouped"

// groupRef names a place the Library shows Items: one of the user's groups or,
// for an Item that has none, its category. The zero value is every group.
type groupRef struct {
	custom   int64
	category category.Key
}

// libraryQuery is everything that decides what the Library shows.
type libraryQuery struct {
	status libraryFilter
	kinds  kindFilter
	group  groupRef
	text   string
	sort   librarySort
	// sections arranges the result by group.
	sections bool
	// auto shows an Item with no group of its own in its category.
	auto bool
}

// libraryRow is one Item as the Library lists it.
type libraryRow struct {
	entry  catalog.Entry
	status catalog.Status
	// busy is the progress text of the operation running on the Item, if any.
	busy string
	// group is where the Item is shown.
	group groupRef
}

// librarySection is a run of Items shown under one group's name.
type librarySection struct {
	ref   groupRef
	title string
	rows  []libraryRow
}

// groupChip is a group offered as a filter, with how many Items it holds.
type groupChip struct {
	ref   groupRef
	title string
	count int
}

// libraryResult is what a query makes of the Library: the Items to list, in
// order, with the counts that go with them.
type libraryResult struct {
	// status and group are the filters in effect: the query's own, unless it
	// names something that is no longer there.
	status libraryFilter
	group  groupRef
	// rows are the Items to list, in display order; with sections, the
	// sections' rows one after another.
	rows     []libraryRow
	sections []librarySection
	// counts are how many Items each status filter would show, whatever else
	// narrows the Library. Hidden Items count only for their own filter.
	counts [filterCount]int
	// groups are the chips: every group of the user's, then one per category
	// that holds Items. allGroups is how many Items they hold together.
	groups    []groupChip
	allGroups int
}

// apps is how many Items the Library has, not counting hidden ones.
func (r libraryResult) apps() int { return r.counts[filterAll] }

// busyFunc reports whether an operation is running on an Item, with its
// progress text.
type busyFunc func(id int64) (progress string, busy bool)

// titleFunc is the name shown for a category, and for ungrouped.
type titleFunc func(category.Key) string

// queryLibrary turns the Library into what its page lists.
func queryLibrary(entries []catalog.Entry, groups []catalog.Group, busy busyFunc, title titleFunc, q libraryQuery) libraryResult {
	names := make(map[int64]string, len(groups))
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	refOf := func(e catalog.Entry) groupRef {
		switch {
		case names[e.Item.GroupID] != "":
			return groupRef{custom: e.Item.GroupID}
		case q.auto && e.Item.Category != "":
			return groupRef{category: e.Item.Category}
		case q.auto:
			return groupRef{category: category.Other}
		}
		return groupRef{category: ungrouped}
	}
	titleOf := func(ref groupRef) string {
		if ref.custom != 0 {
			return names[ref.custom]
		}
		return title(ref.category)
	}

	res := libraryResult{status: q.status}
	for _, e := range entries {
		for f := range filterCount {
			if f.matches(e) && e.Item.Hidden == (f == filterHidden) {
				res.counts[f]++
			}
		}
	}
	if res.status == filterHidden && res.counts[filterHidden] == 0 {
		res.status = filterAll
	}

	// Chips count what the page would list in a view of its own: hidden Items
	// while looking at them, the rest otherwise.
	inScope := func(e catalog.Entry) bool { return e.Item.Hidden == (res.status == filterHidden) }
	held := map[groupRef]int{}
	for _, e := range entries {
		if inScope(e) {
			held[refOf(e)]++
			res.allGroups++
		}
	}
	res.groups = groupChips(groups, held, titleOf)
	res.group = q.group
	if q.group != (groupRef{}) && !slices.ContainsFunc(res.groups, func(c groupChip) bool { return c.ref == q.group }) {
		res.group = groupRef{}
	}

	text := strings.ToLower(strings.TrimSpace(q.text))
	for _, e := range entries {
		ref := refOf(e)
		if !inScope(e) || !res.status.matches(e) || (res.group != groupRef{} && res.group != ref) ||
			!matchesText(e, titleOf(ref), text) || (q.kinds != kindFilter{} && !q.kinds.matches(e.DisplayKind())) {
			continue
		}
		progress, isBusy := busy(e.Item.ID)
		res.rows = append(res.rows, libraryRow{entry: e, status: e.Status(isBusy), busy: progress, group: ref})
	}
	slices.SortStableFunc(res.rows, func(a, b libraryRow) int { return compareRows(a, b, q.sort) })
	if q.sections {
		plain := res.status == filterAll && q.kinds == (kindFilter{}) && text == ""
		res.sections = sectionsOf(res.rows, groups, res.group, plain, titleOf)
		res.rows = res.rows[:0]
		for _, s := range res.sections {
			res.rows = append(res.rows, s.rows...)
		}
	}
	return res
}

// groupChips lists the groups to filter by: the user's own, empty or not, by
// name, then each category that holds Items, then Items without a group.
func groupChips(groups []catalog.Group, held map[groupRef]int, titleOf func(groupRef) string) []groupChip {
	var chips []groupChip
	for _, g := range groups {
		ref := groupRef{custom: g.ID}
		chips = append(chips, groupChip{ref: ref, title: g.Name, count: held[ref]})
	}
	var automatic []groupChip
	for ref, n := range held {
		if ref.custom == 0 && n > 0 {
			automatic = append(automatic, groupChip{ref: ref, title: titleOf(ref), count: n})
		}
	}
	slices.SortFunc(automatic, func(a, b groupChip) int { return compareAutomatic(a.ref, a.title, b.ref, b.title) })
	return append(chips, automatic...)
}

// compareAutomatic orders automatic groups by name, with Other and then
// Ungrouped last.
func compareAutomatic(a groupRef, titleA string, b groupRef, titleB string) int {
	last := func(ref groupRef) int {
		if ref.category == ungrouped {
			return 2
		}
		if ref.category == category.Other {
			return 1
		}
		return 0
	}
	return cmp.Or(cmp.Compare(last(a), last(b)), strings.Compare(strings.ToLower(titleA), strings.ToLower(titleB)))
}

// sectionsOf splits sorted rows into one section per group: the user's groups
// by name, then the automatic ones. plain says nothing but the group narrows
// the Library, so the user's groups with no Items are shown too, to be dropped
// on.
func sectionsOf(rows []libraryRow, groups []catalog.Group, only groupRef, plain bool, titleOf func(groupRef) string) []librarySection {
	var sections []librarySection
	index := map[groupRef]int{}
	add := func(ref groupRef) {
		index[ref] = len(sections)
		sections = append(sections, librarySection{ref: ref, title: titleOf(ref)})
	}
	for _, g := range groups {
		ref := groupRef{custom: g.ID}
		if plain && (only == groupRef{} || only == ref) {
			add(ref)
		}
	}
	for _, r := range rows {
		if _, ok := index[r.group]; !ok {
			add(r.group)
		}
		i := index[r.group]
		sections[i].rows = append(sections[i].rows, r)
	}
	slices.SortStableFunc(sections, func(a, b librarySection) int {
		switch {
		case a.ref.custom != 0 && b.ref.custom != 0:
			return strings.Compare(strings.ToLower(a.title), strings.ToLower(b.title))
		case a.ref.custom != 0:
			return -1
		case b.ref.custom != 0:
			return 1
		}
		return compareAutomatic(a.ref, a.title, b.ref, b.title)
	})
	return sections
}

// matchesText reports whether text, lower-cased, appears in the Item's name,
// owner/repo, description, group, note or tags.
func matchesText(e catalog.Entry, group, text string) bool {
	if text == "" {
		return true
	}
	fields := append([]string{e.Title(), e.FullName(), e.Item.Description, group, e.Item.Note}, e.Item.Tags...)
	return slices.ContainsFunc(fields, func(field string) bool { return strings.Contains(strings.ToLower(field), text) })
}

func compareRows(a, b libraryRow, sortBy librarySort) int {
	if a.entry.Item.Favorite != b.entry.Item.Favorite {
		if a.entry.Item.Favorite {
			return -1
		}
		return 1
	}
	byName := func() int {
		return cmp.Or(
			strings.Compare(strings.ToLower(a.entry.Title()), strings.ToLower(b.entry.Title())),
			cmp.Compare(a.entry.Item.ID, b.entry.Item.ID),
		)
	}
	switch sortBy {
	case sortRecentlyUpdated:
		// Newest first; Items with no known release come last.
		x, y := a.entry.LatestPublished(), b.entry.LatestPublished()
		switch {
		case x.IsZero() != y.IsZero():
			if x.IsZero() {
				return 1
			}
			return -1
		case !x.Equal(y):
			return y.Compare(x)
		}
	case sortRecentlyAdded:
		if c := b.entry.Item.AddedAt.Compare(a.entry.Item.AddedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.entry.Item.ID, a.entry.Item.ID)
	case sortStatus:
		if c := cmp.Compare(a.status, b.status); c != 0 {
			return c
		}
	case sortName:
	}
	return byName()
}
