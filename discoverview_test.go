package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/discover"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
)

// discoverRepo is a repository Discover can list.
func discoverRepo(name string, stars int) source.Repository {
	return source.Repository{
		Owner: "acme", Name: name, Description: name + " description", Language: "Go", Stars: stars,
		Created: releaseDay.AddDate(-1, 0, 0), Pushed: releaseDay, Topics: []string{"emulator"},
		URL: "https://github.com/acme/" + name,
	}
}

// searchRule answers the searches whose text holds key.
type searchRule struct {
	key   string
	repos []source.Repository
}

// answerSearches makes the fake site answer a list's searches, by the first
// rule whose key the search text holds, so each tab can have its own.
func (e *env) answerSearches(rules ...searchRule) {
	e.discovery.mu.Lock()
	defer e.discovery.mu.Unlock()
	e.discovery.find = func(q source.SearchQuery) source.SearchPage {
		for _, r := range rules {
			if strings.Contains(q.Text, r.key) {
				return source.SearchPage{Repositories: r.repos, Total: len(r.repos)}
			}
		}
		return source.SearchPage{}
	}
}

// openDiscover opens the Discover page and waits for its first list.
func (e *env) openDiscover(wait string) *ui.Tester {
	e.t.Helper()
	tt := e.open()
	click(e.t, tt, "Discover")
	waitText(e.t, tt, wait)
	return tt
}

func TestDiscoverShowsTheSavedTabAndSwitchesTabsAndCategories(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	e.answerSearches(
		searchRule{"stars:>150", []source.Repository{discoverRepo("popular-tool", 9000)}},
		searchRule{"created:>", []source.Repository{discoverRepo("trending-tool", 700)}},
	)
	tt := e.openDiscover("trending-tool")
	requireText(t, tt, "acme/trending-tool", "trending-tool description", "★ 700", "Go", "Trending", "Popular", "Your stars", "Everything")
	// A first visit says movement comes next time.
	requireText(t, tt, "Movement arrows appear on your next visit — today's ranking is saved.")
	requireNoText(t, tt, "popular-tool")

	click(t, tt, "Popular")
	waitText(t, tt, "popular-tool")
	requireNoText(t, tt, "trending-tool")
	if got := e.screen.prefs.Get().DiscoverTab; got != "popular" {
		t.Errorf("saved tab = %q", got)
	}
}

// publishedTool publishes a repository whose release has a Windows setup program.
func (e *env) publishedTool(name string) {
	e.source.publish(name, name+" description", release("v1.0.0", 1, "First"))
}

func TestSwitchingTheCategorySearchesThatCategorysTopics(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	e.answerSearches(
		searchRule{"topic:emulation", []source.Repository{discoverRepo("emu-only", 100)}},
		searchRule{"topic:windows", []source.Repository{discoverRepo("everything-tool", 700)}},
	)
	tt := e.openDiscover("everything-tool")

	click(t, tt, "Category")
	click(t, tt, "Emulators")
	waitText(t, tt, "emu-only")
	requireNoText(t, tt, "everything-tool")
	if got := e.screen.prefs.Get().DiscoverCategory; got != "emulators" {
		t.Errorf("saved category = %q", got)
	}
}

// chooseMenuItem opens the card menu named label and chooses its nth item
// (from 1) with the keyboard, as items share names with other controls.
func chooseMenuItem(t *testing.T, tt *ui.Tester, label string, nth int) {
	t.Helper()
	click(t, tt, label)
	tt.Frame()
	pickMenuItem(tt, nth)
}

// pickMenuItem chooses the nth item (from 1) of the menu showing.
func pickMenuItem(tt *ui.Tester, nth int) {
	for range nth {
		tt.Key(0, ui.KeyDown)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
}

func TestAddingFromDiscoverTurnsTheButtonIntoInLibrary(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	prefs.InstallAfterAdding = false
	e := newEnv(t, prefs)
	e.publishedTool("tool")
	e.answerSearches(searchRule{"created:>", []source.Repository{discoverRepo("tool", 700)}})
	tt := e.openDiscover("acme/tool")
	if countText(tt, "Add") == 0 || countText(tt, "In library") != 0 {
		t.Fatalf("before adding: %q", tt.Texts())
	}

	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Add to library", "Latest version: v1.0.0")
	// The dialog's own button shares its name with the card's, so it is pressed through the screen.
	e.screen.dialogOpen = false
	e.screen.confirmAdd(e.screen.add, downloadView{}, false)
	e.settle()
	tt.Frame()
	click(t, tt, "Discover")
	requireText(t, tt, "acme/tool", "In library")
	if countText(tt, "Add") != 0 {
		t.Errorf("an added project still offers Add: %q", tt.Texts())
	}
}

func TestAddingASavedRepositoryRemovesItFromTryLater(t *testing.T) {
	prefs := defaultPreferences()
	prefs.InstallAfterAdding = false
	e := newEnv(t, prefs)
	e.publishedTool("tool")
	e.publishedTool("other")
	for _, name := range []string{"tool", "other"} {
		if _, err := e.screen.catalog.SaveForLater(t.Context(), discoverRepo(name, 700), ""); err != nil {
			t.Fatal(err)
		}
	}
	tt := e.open()
	if n := e.screen.tryLaterCount.Get(); n != 2 {
		t.Fatalf("badge count = %d", n)
	}
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	click(t, tt, "Add")
	e.settle()

	saved := e.screen.catalog.TryLaterEntries().Get()
	if len(saved) != 1 || saved[0].Repo != "other" {
		t.Fatalf("saved after adding = %+v", saved)
	}
	if n := e.screen.tryLaterCount.Get(); n != 1 {
		t.Errorf("badge count = %d, want 1", n)
	}
	click(t, tt, "Try later")
	requireText(t, tt, "acme/other")
	requireNoText(t, tt, "acme/tool")
}

func TestSavingToTryLaterMarksTheCardCountsItAndRecordsIt(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	e.answerSearches(searchRule{"created:>", []source.Repository{discoverRepo("tool", 700)}})
	tt := e.openDiscover("acme/tool")

	chooseMenuItem(t, tt, "More actions for acme/tool", 2)
	e.settle()
	requireText(t, tt, "Saved acme/tool for later")
	saved := e.screen.catalog.TryLaterEntries().Get()
	if len(saved) != 1 || saved[0].FullName() != "acme/tool" || saved[0].Stars != 700 || saved[0].Description != "tool description" {
		t.Fatalf("saved = %+v", saved)
	}
	if n := e.screen.tryLaterCount.Get(); n != 1 {
		t.Errorf("badge count = %d", n)
	}
	entries := e.screen.activity.Entries().Get()
	if len(entries) != 1 || entries[0].Message != "Saved acme/tool for later" {
		t.Errorf("activity = %+v", entries)
	}

	// A saved card offers to take it off the list.
	click(t, tt, "More actions for acme/tool")
	requireText(t, tt, "Remove from Try later")
	pickMenuItem(tt, 2)
	e.settle()
	if n := len(e.screen.catalog.TryLaterEntries().Get()); n != 0 {
		t.Errorf("%d saved after removing", n)
	}
}

// manyRepos is n repositories named prefix-0, prefix-1, … with falling stars.
func manyRepos(prefix string, n int) []source.Repository {
	repos := make([]source.Repository, n)
	for i := range repos {
		repos[i] = discoverRepo(prefix+"-"+itoa(i), 10000-i)
	}
	return repos
}

func itoa(n int) string { return fmt.Sprintf("%03d", n) }

func TestATextInTheTopBoxThatIsNotARepositorySearchesDiscoverAPageAtATimeUpToTwoHundredForty(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	all := manyRepos("hit", 300)
	e.discovery.find = func(q source.SearchQuery) source.SearchPage {
		from := (q.Page - 1) * q.PerPage
		if from >= len(all) {
			return source.SearchPage{Total: 300}
		}
		return source.SearchPage{Repositories: all[from:min(len(all), from+q.PerPage)], Total: 300}
	}
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "language:go editor")
	click(t, tt, "Search")
	waitText(t, tt, "Results for “language:go editor”")
	e.settle()
	requireText(t, tt, "Showing 24 of 240 results", "Load more")
	first := e.discovery.searched()[0]
	if first.Text != "language:go editor" || first.PerPage != 24 || first.Page != 1 || !first.ByStars {
		t.Errorf("first search = %+v", first)
	}

	for page := 2; page <= 10; page++ {
		click(t, tt, "Load more")
		e.settle()
		tt.Frame()
	}
	if got := len(e.screen.discoverUI.side.rows); got != 240 {
		t.Fatalf("%d results after ten pages, want 240", got)
	}
	requireText(t, tt, "Showing 240 of 240 results")
	requireNoText(t, tt, "Load more")

	click(t, tt, "Back to Discover")
	waitText(t, tt, "Projects gaining stars fastest right now. Movement arrows show how the ranking changed.")
	requireNoText(t, tt, "Results for")
}

func TestASearchWithNoMatchesSaysSo(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.discovery.find = func(source.SearchQuery) source.SearchPage { return source.SearchPage{} }
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "nothing here")
	click(t, tt, "Search")
	waitText(t, tt, "No repositories matched “nothing here”.")
}

func TestMoreFromThisDeveloperListsTheirRepositoriesThirtyAtATime(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	e.answerSearches(searchRule{"created:>", []source.Repository{discoverRepo("tool", 700)}})
	e.discovery.owned["acme"] = manyRepos("own", 45)
	tt := e.openDiscover("acme/tool")

	chooseMenuItem(t, tt, "More actions for acme/tool", 3)
	waitText(t, tt, "More from acme")
	e.settle()
	if got := len(e.screen.discoverUI.side.rows); got != 30 {
		t.Fatalf("%d repositories on the first page, want 30", got)
	}
	click(t, tt, "Load more")
	e.settle()
	tt.Frame()
	if got := len(e.screen.discoverUI.side.rows); got != 45 {
		t.Fatalf("%d repositories after loading more, want 45", got)
	}
	requireNoText(t, tt, "Load more")
}

func TestOpeningAProjectShowsItsDetailsAndReadmeWithTheDialogsButtons(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	repo := discoverRepo("tool", 12300)
	repo.Forks, repo.OpenIssues, repo.License, repo.Homepage = 12, 34, "MIT", "https://tool.example"
	e.answerSearches(searchRule{"created:>", []source.Repository{repo}})
	e.discovery.readmes["acme/tool"] = "# Tool\n\nDoes things."
	tt := e.openDiscover("acme/tool")

	click(t, tt, "Open details for acme/tool")
	waitText(t, tt, "Does things.")
	requireText(t, tt, "Stars", "12k", "Forks", "Open issues", "34", "License", "MIT", "Last updated", "Created",
		"emulator", "More from acme", "Open on GitHub", "Website", "README", "Try later", "Close", "Add")

	click(t, tt, "Close")
	requireNoText(t, tt, "README")
}

func TestARepositoryWithoutAReadmeSaysSo(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	e.answerSearches(searchRule{"created:>", []source.Repository{discoverRepo("tool", 700)}})
	tt := e.openDiscover("acme/tool")
	click(t, tt, "Open details for acme/tool")
	waitText(t, tt, "This repository has no README.")
}

func TestTheDownloadFilterDropsProjectsKnownToHaveNoneKeepsThoseItCouldNotCheckAndRemembersOnlyDefiniteAnswers(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	prefs.DiscoverDownloadsOnly = true
	prefs.DiscoverLight = true
	e := newEnv(t, prefs)
	e.source.publish("has", "has a setup", release("v1.0.0", 1, ""))
	e.source.publish("lacks", "linux only", source.Release{Tag: "v1.0.0", Published: releaseDay, Assets: []source.Asset{{Name: "lacks-linux-amd64.tar.gz", Size: 1 << 20}}})
	e.source.publish("flaky", "unreachable", release("v1.0.0", 1, ""))
	e.source.fail("flaky", &source.Error{Kind: source.KindUnreachable})
	e.answerSearches(searchRule{"created:>", []source.Repository{
		discoverRepo("has", 900), discoverRepo("lacks", 800), discoverRepo("flaky", 700),
	}})
	tt := e.openDiscover("acme/has")
	requireText(t, tt, "acme/flaky", "Download for this PC")
	requireNoText(t, tt, "acme/lacks")
	if got := len(e.screen.discoverUI.browse.rows); got != 2 {
		t.Fatalf("%d projects listed, want 2", got)
	}
	if n := e.source.releaseRequests(); n != 3 {
		t.Fatalf("%d release requests, want one per project", n)
	}

	// Loading the list again asks again only about the project that could not be checked.
	click(t, tt, "Refresh")
	e.settle()
	tt.Frame()
	if n := e.source.releaseRequests(); n != 4 {
		t.Errorf("%d release requests after refreshing, want 4: only the unchecked project is asked about again", n)
	}
}

func TestTheDownloadFilterPausesWhenTheAllowanceRunsLow(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	prefs.DiscoverDownloadsOnly = true
	e := newEnv(t, prefs)
	e.source.allowance = source.Allowance{Known: true, Limit: 5000, Remaining: 149, Authenticated: true}
	e.answerSearches(searchRule{"created:>", []source.Repository{discoverRepo("tool", 700)}})
	tt := e.openDiscover("acme/tool")
	requireText(t, tt, "Download check paused to save your requests")
	if n := e.source.releaseRequests(); n != 0 {
		t.Errorf("%d release requests below 150 left", n)
	}
}

func TestLightModeShowsFifteenAProjectFullModeThirtySixAndLoadMoreAddsAPage(t *testing.T) {
	for _, c := range []struct {
		light bool
		first int
	}{{false, 36}, {true, 15}} {
		prefs := defaultPreferences()
		prefs.DiscoverIntroSeen = true
		prefs.DiscoverLight = c.light
		e := newEnv(t, prefs)
		repos := manyRepos("p", 100)
		e.answerSearches(searchRule{"created:>", repos})
		tt := e.openDiscover("acme/p-000")
		if got := len(e.screen.discoverUI.browse.rows); got != c.first {
			t.Fatalf("light %v: %d projects, want %d", c.light, got, c.first)
		}
		click(t, tt, "Load more")
		e.settle()
		tt.Frame()
		if got := len(e.screen.discoverUI.browse.rows); got != 2*c.first {
			t.Errorf("light %v: %d projects after loading more, want %d", c.light, got, 2*c.first)
		}
	}
}

// testClock is a time the test moves by sleeping.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sleep: %w", err)
	}
	return nil
}

func TestFullModePreparesTheOtherTabsAndLightModeDoesNot(t *testing.T) {
	for _, light := range []bool{false, true} {
		prefs := defaultPreferences()
		prefs.DiscoverIntroSeen = true
		prefs.DiscoverLight = light
		e := newEnv(t, prefs)
		clock := &testClock{t: time.Now()}
		e.screen.discover = discover.NewService(discover.Options{
			Client: e.discovery, DB: e.db.SQL, Jobs: e.screen.jobs, Policy: e.screen.catalog.Policy, Now: clock.now, Sleep: clock.sleep,
		})
		e.answerSearches(searchRule{"topic:windows", []source.Repository{discoverRepo("tool", 700)}})
		e.source.allowance = source.Allowance{Known: true, Limit: 5000, Remaining: 4000, Authenticated: true}
		e.openDiscover("acme/tool")
		e.settle()
		var popular bool
		for _, q := range e.discovery.searched() {
			popular = popular || strings.Contains(q.Text, "stars:>150")
		}
		if popular == light {
			t.Errorf("light %v: the Popular list was prepared = %v", light, popular)
		}
	}
}

func TestYourStarsNeedATokenThenListNewestFirstWithATextFilter(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	prefs.DiscoverTab = "stars"
	e := newEnv(t, prefs)
	e.discovery.hasToken = false
	tt := e.openDiscover("Add a GitHub token in Settings to see the projects you have starred.")
	requireText(t, tt, "Open GitHub settings")

	e = newEnv(t, prefs)
	rust := discoverRepo("newest-star", 5)
	rust.Language = "Rust"
	e.discovery.stars = []source.Repository{rust, discoverRepo("older-star", 50), discoverRepo("awesome-things", 99)}
	tt = e.openDiscover("acme/newest-star")
	rows := e.screen.rowsOf()
	if len(rows) != 2 || rows[0].repo.Name != "newest-star" || rows[1].repo.Name != "older-star" {
		t.Fatalf("stars = %+v", rows)
	}
	requireText(t, tt, "2 of 2 starred projects")
	requireNoText(t, tt, "#1")

	typeInto(t, tt, "Filter your stars", "rust")
	requireText(t, tt, "acme/newest-star", "1 of 2 starred projects")
	requireNoText(t, tt, "acme/older-star")
	typeInto(t, tt, "Filter your stars", "zzz")
	requireText(t, tt, "None of your stars match that filter.")
}

func TestTheExplainerShowsOnceAndLightModeFromItTakesEffect(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.answerSearches(searchRule{"created:>", manyRepos("p", 40)})
	tt := e.openDiscover("acme/p-000")
	requireText(t, tt, "Discover uses your GitHub requests", "With your token you have 5,000 requests an hour.")
	if got := len(e.screen.discoverUI.browse.rows); got != 36 {
		t.Fatalf("%d projects before choosing Light mode", got)
	}

	click(t, tt, "Use Light mode")
	e.settle()
	tt.Frame()
	requireNoText(t, tt, "Discover uses your GitHub requests")
	p := e.screen.prefs.Get()
	if !p.DiscoverLight || !p.DiscoverIntroSeen {
		t.Errorf("preferences = %+v", p)
	}
	if got := len(e.screen.discoverUI.browse.rows); got != 15 {
		t.Errorf("%d projects after choosing Light mode, want 15", got)
	}
}

func TestTheExplainerSaysHowFewRequestsThereAreWithoutAToken(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.discovery.hasToken = false
	tt := e.openDiscover("Discover uses your GitHub requests")
	requireText(t, tt, "Without a token you only have 60 requests an hour", "Open GitHub settings")
}

func TestSettingsCanShowTheExplainerAgainAndSwitchTheDiscoverOptions(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	tt := e.open()
	click(t, tt, "Settings")
	click(t, tt, "GitHub")
	requireText(t, tt, "Discover and your request allowance", "Light mode", "Only show projects with downloads for this PC", "Show the explainer again")
	click(t, tt, "Toggle Light mode")
	click(t, tt, "Toggle showing only projects with downloads for this PC")
	if p := e.screen.prefs.Get(); !p.DiscoverLight || !p.DiscoverDownloadsOnly {
		t.Errorf("preferences = %+v", p)
	}
	click(t, tt, "Show")
	waitText(t, tt, "Discover uses your GitHub requests")
	if e.screen.prefs.Get().DiscoverIntroSeen {
		t.Error("the explainer is still marked as seen")
	}
}

func TestTheAllowanceLineSaysHowManyRequestsAreLeft(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	if err := e.api.RefreshAllowance(t.Context()); err != nil {
		t.Fatal(err)
	}
	tt := e.openDiscover("Projects gaining stars fastest right now")
	if !tt.HasText("55 of 60 requests left this hour (resets at ") {
		t.Errorf("texts = %q", tt.Texts())
	}
}

func TestMovementShowsAgainstTheRankingSavedADayAgo(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DiscoverIntroSeen = true
	e := newEnv(t, prefs)
	e.answerSearches(searchRule{"created:>", []source.Repository{
		discoverRepo("rising", 900), discoverRepo("falling", 800), discoverRepo("steady", 700), discoverRepo("fresh", 600),
	}})
	// Yesterday's ranking: falling led, steady was second, rising was third, and fresh was not on the list.
	// Trending puts the project that gained the most stars a day first.
	taken := time.Now().Add(-25 * time.Hour).Unix()
	if _, err := e.db.SQL.ExecContext(t.Context(), `INSERT INTO rank_snapshots (list_key, taken_at, ranks) VALUES (?, ?, ?)`,
		"trending|all", taken, `{"acme/falling":{"position":1,"stars":790},"acme/steady":{"position":2,"stars":700},"acme/rising":{"position":3,"stars":850}}`); err != nil {
		t.Fatal(err)
	}
	tt := e.openDiscover("acme/rising")
	e.settle()
	tt.Frame()
	if rows := e.screen.discoverUI.browse.rows; len(rows) != 4 {
		t.Fatalf("%d rows", len(rows))
	}
	requireText(t, tt, "Movement since yesterday", "▲ 2", "▼ 1", "▼ 2", "New", "+50 ★", "+10 ★")
	wantOrder := []string{"rising", "falling", "fresh", "steady"}
	for i, r := range e.screen.discoverUI.browse.rows {
		if r.repo.Name != wantOrder[i] {
			t.Errorf("trending order = %v at %d, want %v", r.repo.Name, i, wantOrder)
		}
	}
	requireNoText(t, tt, "Movement arrows appear on your next visit")
}
