package main

import (
	"cmp"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/discover"
	"github.com/aaronflorey/release-sync/locales"
)

// tryLaterSort orders the saved repositories.
type tryLaterSort int

// The orders of the Try later page.
const (
	tryRecent tryLaterSort = iota
	tryOldest
	tryName
	tryStars
	tryType
)

// tryLaterItem is one line of the Try later list: a saved repository, or the
// heading of a group of them.
type tryLaterItem struct {
	saved catalog.TryLater
	// heading is set instead of saved on the line that opens a group.
	heading string
	// count is how many repositories the group holds.
	count int
}

// typeOf is the type a saved repository is listed under: the Discover category
// it belongs to, or "Other".
func typeOf(t catalog.TryLater) string {
	if key, ok := discover.CategoryOf(t.Owner, t.Repo, t.Topics); ok {
		return categoryTitle(key)
	}
	return locales.TryLaterOther()
}

// matchesTryLater reports whether text appears in a saved repository's name,
// description, note, language, type or topics, ignoring case.
func matchesTryLater(t catalog.TryLater, kind, text string) bool {
	fields := append([]string{t.FullName(), t.Description, t.Note, t.Language, kind}, t.Topics...)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), text) {
			return true
		}
	}
	return false
}

// compareTypes orders types by name with "Other" last.
func compareTypes(a, b string) int {
	other := locales.TryLaterOther()
	switch {
	case a == b:
		return 0
	case a == other:
		return 1
	case b == other:
		return -1
	}
	return strings.Compare(a, b)
}

func compareTryLater(a, b catalog.TryLater, kindA, kindB string, sortBy tryLaterSort) int {
	byName := func() int {
		return cmp.Or(
			strings.Compare(strings.ToLower(a.Repo), strings.ToLower(b.Repo)),
			strings.Compare(strings.ToLower(a.Owner), strings.ToLower(b.Owner)),
			cmp.Compare(a.ID, b.ID),
		)
	}
	switch sortBy {
	case tryOldest:
		return cmp.Or(a.SavedAt.Compare(b.SavedAt), cmp.Compare(a.ID, b.ID))
	case tryName:
		return byName()
	case tryStars:
		return cmp.Or(cmp.Compare(b.Stars, a.Stars), byName())
	case tryType:
		return cmp.Or(compareTypes(kindA, kindB), byName())
	case tryRecent:
	}
	return cmp.Or(b.SavedAt.Compare(a.SavedAt), cmp.Compare(b.ID, a.ID))
}

// tryLaterItems filters the saved repositories by the search text, sorts them,
// and with group set puts a heading before each type, "Other" last.
func tryLaterItems(entries []catalog.TryLater, query string, sortBy tryLaterSort, group bool) []tryLaterItem {
	query = strings.ToLower(strings.TrimSpace(query))
	type typed struct {
		saved catalog.TryLater
		kind  string
	}
	var found []typed
	for _, t := range entries {
		kind := typeOf(t)
		if query == "" || matchesTryLater(t, kind, query) {
			found = append(found, typed{t, kind})
		}
	}
	slices.SortStableFunc(found, func(a, b typed) int {
		if group {
			if c := compareTypes(a.kind, b.kind); c != 0 {
				return c
			}
		}
		return compareTryLater(a.saved, b.saved, a.kind, b.kind, sortBy)
	})
	items := make([]tryLaterItem, 0, len(found)+len(found)/4)
	for i, f := range found {
		if group && (i == 0 || found[i-1].kind != f.kind) {
			count := 0
			for _, g := range found[i:] {
				if g.kind != f.kind {
					break
				}
				count++
			}
			items = append(items, tryLaterItem{heading: f.kind, count: count})
		}
		items = append(items, tryLaterItem{saved: f.saved})
	}
	return items
}
