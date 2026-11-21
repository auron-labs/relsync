package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"
)

var savedAt = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// saved is a saved repository, saved days after a fixed day.
func saved(id int64, owner, repo, description, note string, stars int, topics []string, days int) catalog.TryLater {
	return catalog.TryLater{
		ID: id, Source: source.GitHubName, Owner: owner, Repo: repo, Description: description, Note: note, Stars: stars,
		Topics: topics, SavedAt: savedAt.AddDate(0, 0, days),
	}
}

func names(items []tryLaterItem) []string {
	var out []string
	for _, it := range items {
		if it.heading != "" {
			out = append(out, "["+it.heading+"]")
		} else {
			out = append(out, it.saved.Repo)
		}
	}
	return out
}

func TestTryLaterListsAreFilteredSortedAndGrouped(t *testing.T) {
	entries := []catalog.TryLater{
		saved(1, "dolphin-emu", "dolphin", "GameCube and Wii emulator", "", 14000, nil, 1),
		saved(2, "someone", "vault", "Keeps secrets", "try with the family", 800, []string{"password-manager"}, 3),
		saved(3, "someone", "zeta", "Something odd", "", 50, nil, 2),
		saved(4, "sharkdp", "bat", "A cat clone", "", 50000, []string{"cli", "terminal"}, 0),
	}
	for _, c := range []struct {
		name  string
		query string
		sort  tryLaterSort
		group bool
		want  []string
	}{
		{"recently saved", "", tryRecent, false, []string{"vault", "zeta", "dolphin", "bat"}},
		{"oldest first", "", tryOldest, false, []string{"bat", "dolphin", "zeta", "vault"}},
		{"name", "", tryName, false, []string{"bat", "dolphin", "vault", "zeta"}},
		{"most stars", "", tryStars, false, []string{"bat", "dolphin", "vault", "zeta"}},
		{"type then name, Other last", "", tryType, false, []string{"bat", "dolphin", "vault", "zeta"}},
		{
			"grouped by type, Other last", "", tryRecent, true,
			[]string{"[Developer tools]", "bat", "[Emulators]", "dolphin", "[Privacy & security]", "vault", "[Other]", "zeta"},
		},
		{"search the note", "family", tryRecent, false, []string{"vault"}},
		{"search the description", "KEEPS", tryRecent, false, []string{"vault"}},
		{"search the topic", "terminal", tryRecent, false, []string{"bat"}},
		{"search the type", "emulators", tryRecent, false, []string{"dolphin"}},
		{"search the name", "someone/", tryName, false, []string{"vault", "zeta"}},
		{"no match", "nothing like it", tryRecent, false, nil},
	} {
		got := names(tryLaterItems(entries, c.query, c.sort, c.group))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCompactCount(t *testing.T) {
	for n, want := range map[int]string{0: "0", 950: "950", 1000: "1.0k", 1234: "1.2k", 12300: "12k", 999999: "1000k", 1_500_000: "1.5M"} {
		if got := compactCount(n); got != want {
			t.Errorf("compactCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTheTryLaterPageListsSavedRepositoriesWithTheirNotesAndSearchesThem(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	for _, repo := range []source.Repository{
		discoverRepo("alpha", 5000), discoverRepo("beta", 40),
	} {
		if _, err := e.screen.catalog.SaveForLater(t.Context(), repo, "note for "+repo.Name); err != nil {
			t.Fatal(err)
		}
	}
	tt := e.open()
	click(t, tt, "Try later")
	requireText(t, tt, "acme/alpha", "acme/beta", "note for alpha", "alpha description", "2 saved", "Recently saved", "Group by type")

	typeInto(t, tt, "Search saved repositories", "beta")
	requireText(t, tt, "acme/beta", "Showing 1 of 2")
	requireNoText(t, tt, "acme/alpha")
	typeInto(t, tt, "Search saved repositories", "zzz")
	requireText(t, tt, "No saved repositories match that search.")
}

func TestTheTryLaterMenuEditsTheNoteRemovesAndAddsToTheLibrary(t *testing.T) {
	prefs := defaultPreferences()
	prefs.InstallAfterAdding = false
	e := newEnv(t, prefs)
	e.publishedTool("alpha")
	for _, name := range []string{"alpha", "beta"} {
		if _, err := e.screen.catalog.SaveForLater(t.Context(), discoverRepo(name, 100), ""); err != nil {
			t.Fatal(err)
		}
	}
	tt := e.open()
	click(t, tt, "Try later")

	// Items: Details, Add to library, Edit note, Remove from this list, More from this developer, Open project page.
	click(t, tt, "More actions for acme/beta")
	pickMenuItem(tt, 4)
	e.settle()
	tt.Frame()
	requireNoText(t, tt, "acme/beta")
	if n := e.screen.tryLaterCount.Get(); n != 1 {
		t.Errorf("badge count = %d", n)
	}
	entries := e.screen.activity.Entries().Get()
	if len(entries) != 1 || entries[0].Message != "Removed acme/beta from Try later" {
		t.Errorf("activity = %+v", entries)
	}

	click(t, tt, "More actions for acme/alpha")
	pickMenuItem(tt, 3)
	requireText(t, tt, "Note for acme/alpha")
	typeInto(t, tt, "Note", "check the portable build")
	click(t, tt, "Save")
	e.settle()
	tt.Frame()
	requireText(t, tt, "check the portable build")
	if got, _ := e.screen.catalog.FindTryLater(source.RepoRef{Owner: "acme", Repo: "alpha"}); got.Note != "check the portable build" {
		t.Errorf("note = %q", got.Note)
	}

	// "Add to library" opens the add dialog, and adding removes the entry.
	click(t, tt, "Add to library")
	e.settle()
	requireText(t, tt, "Latest version: v1.0.0")
	e.screen.dialogOpen = false
	e.screen.confirmAdd(e.screen.add, downloadView{}, false)
	e.settle()
	tt.Frame()
	if n := len(e.screen.catalog.TryLaterEntries().Get()); n != 0 {
		t.Errorf("%d saved after adding", n)
	}
	click(t, tt, "Try later")
	requireText(t, tt, "Nothing saved for later")
}

func TestAdoptingAScannedProgramRemovesItFromTryLater(t *testing.T) {
	f := newScanFixture(t)
	f.source.publishOwned("acme", "behind", "Has an update", twoReleases()...)
	f.source.publishOwned("acme", "kept", "Stays saved", twoReleases()...)
	f.program("behind", "Behind App", "", "1.0.0", "https://github.com/acme/behind")
	for _, name := range []string{"behind", "kept"} {
		if _, err := f.screen.catalog.SaveForLater(t.Context(), discoverRepo(name, 100), ""); err != nil {
			t.Fatal(err)
		}
	}
	f.scan()
	click(t, f.tt, "Add 1 app to library")
	f.settle()

	if _, ok := f.screen.catalog.Find(source.RepoRef{Owner: "acme", Repo: "behind"}); !ok {
		t.Fatal("the program was not added")
	}
	left := f.screen.catalog.TryLaterEntries().Get()
	if len(left) != 1 || !strings.EqualFold(left[0].Repo, "kept") {
		t.Errorf("saved after adopting = %+v", left)
	}
	if n := f.screen.tryLaterCount.Get(); n != 1 {
		t.Errorf("badge count = %d", n)
	}
}
