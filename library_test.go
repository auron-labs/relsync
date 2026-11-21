package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"
)

// libraryFixture tracks a repository in each state a card can be in.
func libraryFixture(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, defaultPreferences())
	two := []source.Release{release("v2.0.0", 5, "Big changes"), release("v1.0.0", 1, "First")}
	e.source.publish("behind", "Has a newer version", two...)
	e.source.publish("current", "Is up to date", two...)
	e.source.publish("fresh", "Not installed yet", two...)
	e.source.publish("skipped", "Skipped the latest", two...)
	e.source.publish("held", "Ignores updates", two...)
	e.source.publish("empty", "No releases", []source.Release{}...)
	e.source.publish("gone", "Disappears", []source.Release{}...)
	e.source.publish("flaky", "Check fails", []source.Release{}...)

	e.track("behind", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	e.track("current", catalog.AddOptions{AlreadyHave: true})
	e.track("fresh", catalog.AddOptions{})
	skipped := e.track("skipped", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	held := e.track("held", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	e.track("empty", catalog.AddOptions{})
	gone := e.track("gone", catalog.AddOptions{})
	flaky := e.track("flaky", catalog.AddOptions{})

	ctx := t.Context()
	cat := e.screen.catalog
	if err := cat.SetSkippedVersion(ctx, skipped.Item.ID, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := cat.SetHoldUpdates(ctx, held.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	e.source.fail("gone", &source.Error{Kind: source.KindNotFound, Status: 404, Resource: "acme/gone"})
	e.source.fail("flaky", &source.Error{Kind: source.KindUnreachable})
	for _, id := range []int64{gone.Item.ID, flaky.Item.ID} {
		if _, err := cat.Check(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestEveryCardShowsItsStatusAndVersion(t *testing.T) {
	e := libraryFixture(t)
	tt := e.open()
	requireText(t, tt,
		// Update: installed → latest.
		"behind", "acme/behind", "Has a newer version", "v1.0.0 → v2.0.0", "Update available",
		// Up to date shows the installed version.
		"current", "Up to date",
		// Not installed shows what is available.
		"fresh", "v2.0.0 available", "Not installed",
		// Ignored, by skipping and by holding.
		"Skipped v2.0.0", "Update ignored",
		// Errors with no release known.
		"No releases", "Project not found", "Check failed",
	)
	if got := countText(tt, "v1.0.0"); got < 2 {
		t.Errorf("the up-to-date... the skipped and held items should show their installed version, found %d", got)
	}
}

func TestABusyItemShowsItsOperationInsteadOfItsStatus(t *testing.T) {
	e := libraryFixture(t)
	behind, _ := e.screen.catalog.Find(source.RepoRef{Owner: "acme", Repo: "behind"})
	release := make(chan struct{})
	started := make(chan struct{})
	err := e.screen.jobs.Start(catalog.CheckJob(behind.Item.ID), func(ctx context.Context, report jobs.Report) error {
		report(1, 3, "Working on it")
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	tt := e.open()
	requireText(t, tt, "Working on it")
	// Its status is replaced, not duplicated; the rest keep theirs.
	if countText(tt, "Update available") != 0 || countText(tt, "Up to date") == 0 {
		t.Errorf("texts = %q", tt.Texts())
	}
	close(release)
	e.settle()
	requireText(t, tt, "Update available")
	requireNoText(t, tt, "Working on it")
}

func TestStatusTextForEveryStatus(t *testing.T) {
	two := []source.Release{release("v2.0.0", 5, ""), release("v1.0.0", 0, "")}
	entry := func(mutate func(*catalog.Entry)) catalog.Entry {
		e := catalog.Entry{Item: catalog.Item{ID: 1, Owner: "a", Repo: "b"}, Releases: two}
		mutate(&e)
		return e
	}
	installed := func(tag string) func(*catalog.Entry) {
		return func(e *catalog.Entry) { e.Install = &catalog.Install{Tag: tag} }
	}
	for _, c := range []struct {
		name   string
		entry  catalog.Entry
		busy   bool
		status catalog.Status
		pill   string
		busyAs string
		text   string
	}{
		{"busy with progress", entry(func(*catalog.Entry) {}), true, catalog.StatusBusy, "Checking 2/9", "Checking 2/9", "v2.0.0 available"},
		{"busy without progress", entry(func(*catalog.Entry) {}), true, catalog.StatusBusy, "Checking…", "", "v2.0.0 available"},
		{"update", entry(installed("v1.0.0")), false, catalog.StatusUpdate, "Update available", "", "v1.0.0 → v2.0.0"},
		{"skipped", entry(func(e *catalog.Entry) { installed("v1.0.0")(e); e.Item.SkippedVersion = "v2.0.0" }), false, catalog.StatusIgnored, "Skipped v2.0.0", "", "v1.0.0"},
		{"held", entry(func(e *catalog.Entry) { installed("v1.0.0")(e); e.Item.HoldUpdates = true }), false, catalog.StatusIgnored, "Update ignored", "", "v1.0.0"},
		{"up to date", entry(installed("v2.0.0")), false, catalog.StatusUpToDate, "Up to date", "", "v2.0.0"},
		{"not installed", entry(func(*catalog.Entry) {}), false, catalog.StatusNotInstalled, "Not installed", "", "v2.0.0 available"},
		{"unchecked", entry(func(e *catalog.Entry) { e.Releases = nil }), false, catalog.StatusUnchecked, "Not checked yet", "", "—"},
		{"not found", entry(func(e *catalog.Entry) { e.Releases = nil; e.Item.LastFailure = catalog.FailNotFound }), false, catalog.StatusError, "Project not found", "", "—"},
		{"no releases", entry(func(e *catalog.Entry) { e.Releases = nil; e.Item.LastFailure = catalog.FailNoReleases }), false, catalog.StatusError, "No releases", "", "—"},
		{"failed", entry(func(e *catalog.Entry) { e.Releases = nil; e.Item.LastFailure = catalog.FailOther }), false, catalog.StatusError, "Check failed", "", "—"},
		// An error only wins while no release is known.
		{"failed with a known release", entry(func(e *catalog.Entry) { e.Item.LastFailure = catalog.FailOther }), false, catalog.StatusNotInstalled, "Not installed", "", "v2.0.0 available"},
	} {
		status := c.entry.Status(c.busy)
		if status != c.status {
			t.Errorf("%s: status = %v, want %v", c.name, status, c.status)
		}
		if got := statusText(c.entry, status, c.busyAs); got != c.pill {
			t.Errorf("%s: pill = %q, want %q", c.name, got, c.pill)
		}
		if got := versionText(c.entry); got != c.text {
			t.Errorf("%s: version text = %q, want %q", c.name, got, c.text)
		}
	}
}

// queryNames runs a Library query over entries and returns the repositories
// it lists, in order.
func queryNames(entries []catalog.Entry, groups []catalog.Group, q libraryQuery) []string {
	notBusy := func(int64) (string, bool) { return "", false }
	rows := queryLibrary(entries, groups, notBusy, groupTitle, q).rows
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.entry.Item.Repo)
	}
	return names
}

func TestFilterCountsAndFilters(t *testing.T) {
	e := libraryFixture(t)
	entries := e.screen.catalog.Entries().Get()
	notBusy := func(int64) (string, bool) { return "", false }
	// 8 items; only "behind" has an update to act on; 4 have a version recorded.
	counts := queryLibrary(entries, nil, notBusy, groupTitle, libraryQuery{}).counts
	if counts != [filterCount]int{filterAll: 8, filterUpdates: 1, filterInstalled: 4, filterNotInstalled: 4} {
		t.Fatalf("counts = %v", counts)
	}
	for filter, want := range map[libraryFilter][]string{
		filterUpdates:      {"behind"},
		filterInstalled:    {"behind", "current", "held", "skipped"},
		filterNotInstalled: {"empty", "flaky", "fresh", "gone"},
	} {
		if got := queryNames(entries, nil, libraryQuery{status: filter}); !slices.Equal(got, want) {
			t.Errorf("filter %d = %v, want %v", filter, got, want)
		}
	}
}

func TestTextFilterSearchesNameOwnerAndDescription(t *testing.T) {
	e := libraryFixture(t)
	entries := e.screen.catalog.Entries().Get()
	for query, want := range map[string][]string{
		"BEHIND":            {"behind"},
		"acme/cur":          {"current"},
		"newer version":     {"behind"},
		"  ignores ":        {"held"},
		"nothing like this": {},
	} {
		if got := queryNames(entries, nil, libraryQuery{text: query}); !slices.Equal(got, want) {
			t.Errorf("query %q = %v, want %v", query, got, want)
		}
	}
	if n := len(queryNames(entries, nil, libraryQuery{text: "acme"})); n != 8 {
		t.Errorf("the owner matches every item, got %d", n)
	}
}

func TestSortOrders(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	item := func(id int64, name string, added int, status catalog.Status, published int) libraryRow {
		e := catalog.Entry{Item: catalog.Item{ID: id, Owner: "o", Repo: name, AddedAt: now.AddDate(0, 0, added)}}
		if published != 0 {
			e.Releases = []source.Release{{Tag: "v1", Published: now.AddDate(0, 0, published)}}
		}
		return libraryRow{entry: e, status: status}
	}
	rows := []libraryRow{
		item(1, "Charlie", 3, catalog.StatusUpToDate, 2),
		item(2, "alpha", 1, catalog.StatusUpdate, 0),
		item(3, "Bravo", 2, catalog.StatusError, 9),
		item(4, "delta", 2, catalog.StatusUpdate, 5),
	}
	order := func(sortBy librarySort) string {
		sorted := slices.Clone(rows)
		slices.SortStableFunc(sorted, func(a, b libraryRow) int { return compareRows(a, b, sortBy) })
		names := make([]string, 0, len(sorted))
		for _, r := range sorted {
			names = append(names, r.entry.Item.Repo)
		}
		return strings.Join(names, ",")
	}
	for sortBy, want := range map[librarySort]string{
		sortName:            "alpha,Bravo,Charlie,delta",
		sortRecentlyUpdated: "Bravo,delta,Charlie,alpha", // an Item with no release goes last
		sortRecentlyAdded:   "Charlie,delta,Bravo,alpha", // ties fall back to the newest id
		sortStatus:          "alpha,delta,Bravo,Charlie", // updates first, then by name
	} {
		if got := order(sortBy); got != want {
			t.Errorf("sort %d = %s, want %s", sortBy, got, want)
		}
	}
}

func TestLibraryViewFiltersAndSwitchesLayout(t *testing.T) {
	e := libraryFixture(t)
	tt := e.open()
	// The filter tabs carry their counts.
	requireText(t, tt, "All", "Installed", "Not installed", "8", "4")

	e.screen.library.status = filterUpdates
	requireText(t, tt, "behind")
	requireNoText(t, tt, "acme/current", "acme/fresh")

	e.screen.library.status = filterAll
	typeInto(t, tt, "Filter your library", "fresh")
	requireText(t, tt, "acme/fresh")
	requireNoText(t, tt, "acme/behind")
	typeInto(t, tt, "Filter your library", "zzz")
	requireText(t, tt, "Nothing in your library matches.")

	e.screen.library.query = ""
	click(t, tt, "List view")
	requireText(t, tt, "acme/behind", "acme/gone", "v1.0.0 → v2.0.0")
	if got := e.screen.prefs.Get().LibraryView; got != viewList {
		t.Errorf("view after choosing the list = %q", got)
	}
	click(t, tt, "Grid view")
	if got := e.screen.prefs.Get().LibraryView; got != viewGrid {
		t.Errorf("view after choosing the grid = %q", got)
	}
}

func TestClickingACardOpensTheDrawer(t *testing.T) {
	e := libraryFixture(t)
	tt := e.open()
	click(t, tt, "Open details for behind")
	requireText(t, tt, "What's new since your version", "Big changes", "Check now", "Open on GitHub", "Stop tracking")
	if !e.screen.drawer.open {
		t.Fatal("the drawer did not open")
	}
}

func TestCardsShowInitialsUntilTheAvatarArrives(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	e.track("tool", catalog.AddOptions{})
	tt := e.open()
	if countText(tt, "T") != 1 {
		t.Fatalf("no initial before the avatar loads: %q", tt.Texts())
	}
	close(e.avatarGate)
	select {
	case <-e.avatarArrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the avatar never arrived")
	}
	tt.Frame()
	if countText(tt, "T") != 0 {
		t.Errorf("the initial is still shown: %q", tt.Texts())
	}
	// Frames after that reuse the cached image.
	tt.Frame()
	tt.Frame()
	if got := e.avatarRequests.Load(); got != 1 {
		t.Errorf("%d avatar downloads", got)
	}
}
