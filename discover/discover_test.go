package discover

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/database"

	"github.com/aaronflorey/release-sync/database/migrations"
	"github.com/aaronflorey/release-sync/source"
)

var noon = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func TestQueriesPerTab(t *testing.T) {
	dev := Find("dev")
	topics := dev.Topics
	for _, c := range []struct {
		name string
		tab  Tab
		want []string
	}{
		{"popular", Popular, []string{" archived:false stars:>150"}},
		{"new", New, []string{" archived:false stars:>40 created:>2026-09-21"}},
		{"updated", Updated, []string{" archived:false stars:>200 pushed:>2026-11-13"}},
		{"trending", Trending, []string{" archived:false stars:>40 created:>2025-11-20 pushed:>2026-11-06"}},
	} {
		queries := Queries(c.tab, dev, noon)
		extra := 0
		if c.tab == Trending {
			extra = 2
		}
		if len(queries) != len(topics)+extra {
			t.Fatalf("%s: %d queries, want %d", c.name, len(queries), len(topics)+extra)
		}
		for i, topic := range topics {
			if want := topic + c.want[0]; queries[i].Text != want || queries[i].PerPage != 40 {
				t.Errorf("%s query %d = %+v, want %q at 40", c.name, i, queries[i], want)
			}
		}
		for i := range extra {
			q := queries[len(topics)+i]
			if want := topics[i] + " archived:false stars:>1000 pushed:>2026-11-13"; q.Text != want || q.PerPage != 20 {
				t.Errorf("%s established query %d = %+v, want %q at 20", c.name, i, q, want)
			}
		}
	}
}

func TestEverythingAsksForFewerResultsPerTopicAndStarsAreNotSearched(t *testing.T) {
	for _, q := range Queries(Popular, Find(AllKey), noon) {
		if q.PerPage != 30 {
			t.Errorf("%q asks for %d, want 30", q.Text, q.PerPage)
		}
	}
	if q := Queries(Stars, Find("dev"), noon); q != nil {
		t.Errorf("stars are searched for: %v", q)
	}
}

func TestFeaturedNamesCuratedRepositoriesSixToASearch(t *testing.T) {
	games := Find("games")
	queries := Queries(Featured, games, noon)
	wantSearches := (len(games.Seeds) + 5) / 6
	if len(queries) != wantSearches {
		t.Fatalf("%d searches for %d seeds, want %d", len(queries), len(games.Seeds), wantSearches)
	}
	if got := len(strings.Fields(queries[0].Text)); got != 6 {
		t.Errorf("first search names %d repositories, want 6", got)
	}
	if !strings.HasPrefix(queries[0].Text, "repo:"+games.Seeds[0]+" ") {
		t.Errorf("first search = %q", queries[0].Text)
	}

	// Everything shows the first five of each category's, once.
	named := make([]string, 0, 64)
	for _, q := range Queries(Featured, Find(AllKey), noon) {
		named = append(named, strings.Fields(q.Text)...)
	}
	slices.Sort(named)
	if len(slices.Compact(slices.Clone(named))) != len(named) {
		t.Error("Everything names a curated repository twice")
	}
	if len(named) < len(Categories())*3 {
		t.Errorf("Everything names only %d repositories", len(named))
	}
}

func TestTheCategoriesAreTheDocumentedOnesWithQueriesAndTheCommandLineAddition(t *testing.T) {
	cats := Categories()
	if len(cats) != 14 || cats[0].Key != AllKey {
		t.Fatalf("%d categories, first %q", len(cats), cats[0].Key)
	}
	for _, c := range cats {
		if len(c.Topics) == 0 {
			t.Errorf("%s has no topic queries", c.Key)
		}
		if c.Key != AllKey && len(c.Seeds) == 0 {
			t.Errorf("%s has no curated repositories", c.Key)
		}
		for _, s := range c.Seeds {
			if strings.Count(s, "/") != 1 || strings.Contains(s, ":") {
				t.Errorf("%s: seed %q is not owner/repo", c.Key, s)
			}
		}
	}
	cli := Find("cli")
	for _, topic := range []string{"cli", "command-line-tool", "tui", "terminal"} {
		if !slices.Contains(cli.Topics, "topic:"+topic) {
			t.Errorf("command-line tools lack topic %s: %v", topic, cli.Topics)
		}
	}
	if Find("nonsense").Key != AllKey {
		t.Error("an unknown key does not fall back to Everything")
	}
}

func TestCategoryOfPrefersACuratedListingThenSharedTopics(t *testing.T) {
	for _, c := range []struct {
		owner, repo string
		topics      []string
		want        string
		ok          bool
	}{
		{"dolphin-emu", "dolphin", nil, "emulators", true},
		{"unknown", "thing", []string{"windows", "password-manager"}, "privacy", true},
		{"unknown", "thing", []string{"windows"}, "", false},
		{"unknown", "thing", nil, "", false},
		{"unknown", "tool", []string{"TUI", "rust"}, "cli", true},
	} {
		got, ok := CategoryOf(c.owner, c.repo, c.topics)
		if got != c.want || ok != c.ok {
			t.Errorf("CategoryOf(%s/%s, %v) = %q, %v; want %q, %v", c.owner, c.repo, c.topics, got, ok, c.want, c.ok)
		}
	}
}

func TestNoise(t *testing.T) {
	r := func(name, description string, stars int) source.Repository {
		return source.Repository{Owner: "o", Name: name, Description: description, Stars: stars}
	}
	archived := r("fine", "", 5)
	archived.Archived = true
	for _, c := range []struct {
		name string
		repo source.Repository
		want bool
	}{
		{"software", r("tool", "A tool", 5), false},
		{"archived", archived, true},
		{"no stars", r("tool", "A tool", 0), true},
		{"awesome prefix", r("awesome-go", "", 5), true},
		{"awesome suffix", r("go-awesome", "", 5), true},
		{"awesome list name", r("my-awesome-list-of-things", "", 5), true},
		{"awesome as part of a word", r("awesomewm", "", 5), true},
		{"curated list", r("links", "A Curated List of things", 5), true},
		{"awesome list", r("links", "An awesome list", 5), true},
		{"a list of", r("links", "A list of tools", 5), true},
		{"mentions list later", r("tool", "Makes a list of files", 5), false},
	} {
		if got := IsNoise(c.repo); got != c.want {
			t.Errorf("%s: IsNoise = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMatchesTextLooksAtNameDescriptionLanguageAndTopics(t *testing.T) {
	repo := source.Repository{Owner: "Acme", Name: "Tool", Description: "Fast SYNC", Language: "Rust", Topics: []string{"backup"}}
	for text, want := range map[string]bool{"": true, "acme/to": true, "sync": true, "rust": true, "BACK": true, "python": false} {
		if got := MatchesText(repo, text); got != want {
			t.Errorf("MatchesText(%q) = %v, want %v", text, got, want)
		}
	}
}

func repoWith(name string, stars int, created time.Time) source.Repository {
	return source.Repository{Owner: "o", Name: name, Stars: stars, Created: created}
}

func TestEveryTabButTrendingRanksByStars(t *testing.T) {
	repos := []source.Repository{repoWith("b", 10, noon), repoWith("a", 30, noon), repoWith("c", 30, noon)}
	for _, tab := range []Tab{Popular, New, Updated, Featured} {
		got := names(Order(repos, tab, nil, noon))
		if want := []string{"a", "c", "b"}; !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v (ties by name)", tab, got, want)
		}
	}
	if got := names(Order(repos, Stars, nil, noon)); !slices.Equal(got, []string{"b", "a", "c"}) {
		t.Errorf("stars = %v, want the order given", got)
	}
}

func names(repos []source.Repository) []string {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = r.Name
	}
	return out
}

func TestTrendingWithoutABaselineUsesStarsOverAgeFlooredAtAWeek(t *testing.T) {
	day := 24 * time.Hour
	repos := []source.Repository{
		repoWith("old-giant", 3650, noon.Add(-3650*day)), // 1 star a day
		repoWith("young", 700, noon.Add(-1*day)),         // age floored at 7: 100 a day
		repoWith("steady", 800, noon.Add(-100*day)),      // 8 a day
		repoWith("undated", 365, time.Time{}),            // 365 days assumed: 1 a day
	}
	got := names(Order(repos, Trending, nil, noon))
	if want := []string{"young", "steady", "old-giant", "undated"}; !slices.Equal(got, want) {
		t.Errorf("trending = %v, want %v", got, want)
	}
}

func TestTrendingWithABaselineUsesTheStarsGainedAndNudgesTowardEstablishedProjects(t *testing.T) {
	base := &Baseline{Age: 2 * 24 * time.Hour, Snapshot: Snapshot{Ranks: map[string]Rank{
		"o/small": {Position: 1, Stars: 100}, "o/big": {Position: 2, Stars: 9000}, "o/flat": {Position: 3, Stars: 500},
	}}}
	repos := []source.Repository{
		repoWith("flat", 500, noon),         // gained nothing
		repoWith("small", 140, noon),        // 20 a day
		repoWith("big", 9040, noon),         // 20 a day, more stars
		repoWith("shrunk", 10, noon),        // not in the baseline: stars over unknown age
		repoWith("unstarred-lost", 1, noon), // dropped below its baseline stars
	}
	base.Snapshot.Ranks["o/unstarred-lost"] = Rank{Position: 4, Stars: 50}
	got := names(Order(repos, Trending, base, noon))
	if got[0] != "big" || got[1] != "small" {
		t.Errorf("trending = %v, want the bigger project first on equal gains", got)
	}
	score := starsPerDay(repoWith("unstarred-lost", 1, noon), base, noon)
	if score != 0 {
		t.Errorf("stars lost counted as a gain of %v", score)
	}
	if math.Abs(starsPerDay(repoWith("small", 140, noon), base, noon)-20) > 1e-9 {
		t.Errorf("gain per day = %v, want 20", starsPerDay(repoWith("small", 140, noon), base, noon))
	}
}

func snapshotAt(age time.Duration) Snapshot {
	return Snapshot{At: noon.Add(-age)}
}

func TestBaselineForPicksTheRankingTheWindowAsksFor(t *testing.T) {
	hour, day := time.Hour, 24*time.Hour
	history := []Snapshot{
		snapshotAt(2 * hour), snapshotAt(25 * hour), snapshotAt(3 * day), snapshotAt(8 * day), snapshotAt(20 * day),
	}
	for _, c := range []struct {
		name    string
		history []Snapshot
		window  Window
		want    time.Duration
		ok      bool
	}{
		{"day takes the newest at least 18 hours old", history, Day, 25 * hour, true},
		{"week takes the newest at least 6 days old", history, Week, 8 * day, true},
		{"week with only a day of history takes the oldest there is", history[:2], Week, 25 * hour, true},
		{"nothing old enough", history[:1], Day, 0, false},
		{"no history", nil, Day, 0, false},
	} {
		got, ok := BaselineFor(c.history, c.window, noon)
		if ok != c.ok || got.Age != c.want {
			t.Errorf("%s: age %v, ok %v; want %v, %v", c.name, got.Age, ok, c.want, c.ok)
		}
	}
}

func TestMovementAgainstADayOldAndAWeekOldRanking(t *testing.T) {
	one := repoWith("one", 100, noon)
	two := repoWith("two", 90, noon)
	fresh := repoWith("fresh", 80, noon)
	ranks := map[string]Rank{"o/one": {Position: 3, Stars: 70}, "o/two": {Position: 1, Stars: 90}, "o/third": {Position: 2, Stars: 1}}
	day := &Baseline{Age: 25 * time.Hour, Snapshot: Snapshot{Ranks: ranks}}
	week := &Baseline{Age: 8 * 24 * time.Hour, Snapshot: Snapshot{Ranks: map[string]Rank{"o/one": {Position: 1, Stars: 10}}}}

	for _, c := range []struct {
		name     string
		base     *Baseline
		repo     source.Repository
		position int
		want     Movement
	}{
		{"rose two places and gained stars", day, one, 1, Movement{Kind: MoveUp, Places: 2, Gained: 30}},
		{"fell one place without gaining", day, two, 2, Movement{Kind: MoveDown, Places: 1}},
		{"unchanged", day, two, 1, Movement{Kind: MoveSame}},
		{"new to the list", day, fresh, 3, Movement{Kind: MoveNew}},
		{"against a week-old ranking", week, one, 2, Movement{Kind: MoveDown, Places: 1, Gained: 90}},
		{"first visit has nothing to compare", nil, one, 1, Movement{}},
	} {
		if got := MovementOf(c.base, c.repo, c.position); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestSinceDescribesTheBaselineAge(t *testing.T) {
	for _, c := range []struct {
		age  time.Duration
		kind SinceKind
		days int
	}{
		{25 * time.Hour, SinceYesterday, 1},
		{29 * time.Hour, SinceYesterday, 1},
		{3 * 24 * time.Hour, SinceDays, 3},
		{7 * 24 * time.Hour, SinceLastWeek, 7},
	} {
		kind, days := Baseline{Age: c.age}.Since()
		if kind != c.kind || days != c.days {
			t.Errorf("%v: %v %d, want %v %d", c.age, kind, days, c.kind, c.days)
		}
	}
}

func TestSnapshotsKeepTheTopSixtyAndAreDueEveryTwentyHours(t *testing.T) {
	repos := make([]source.Repository, 0, 80)
	for i := range 80 {
		repos = append(repos, repoWith(fmt.Sprintf("r%d", i), 1000-i, noon))
	}
	snap := NewSnapshot(noon, repos)
	if len(snap.Ranks) != 60 || snap.Ranks["o/r0"] != (Rank{Position: 1, Stars: 1000}) || snap.Ranks["o/r60"].Position != 0 {
		t.Errorf("snapshot has %d ranks, first %+v", len(snap.Ranks), snap.Ranks["o/r0"])
	}
	for age, want := range map[time.Duration]bool{19 * time.Hour: false, 20 * time.Hour: true, 40 * time.Hour: true} {
		if got := SnapshotDue([]Snapshot{{At: noon.Add(-age)}}, noon); got != want {
			t.Errorf("due after %v = %v, want %v", age, got, want)
		}
	}
	if !SnapshotDue(nil, noon) {
		t.Error("a list never saved is not due")
	}
}

func TestBudgetThresholds(t *testing.T) {
	at := func(remaining int) source.Allowance {
		return source.Allowance{Known: true, Limit: 5000, Remaining: remaining}
	}
	for _, c := range []struct {
		name            string
		a               source.Allowance
		token           bool
		prepare, filter bool
	}{
		{"plenty", at(4000), true, true, true},
		{"1,500 left still prepares", at(1500), true, true, true},
		{"below 1,500 stops preparing", at(1499), true, false, true},
		{"150 left still filters", at(150), true, false, true},
		{"below 150 pauses the filter", at(149), true, false, false},
		{"unknown allowance does not stop anything", source.Allowance{}, true, true, true},
		{"no token", at(60), false, false, false},
	} {
		if got := CanPrepare(c.a, c.token); got != c.prepare {
			t.Errorf("%s: prepare = %v, want %v", c.name, got, c.prepare)
		}
		if got := CanCheckDownloads(c.a, c.token); got != c.filter {
			t.Errorf("%s: filter = %v, want %v", c.name, got, c.filter)
		}
	}
}

// bucketFixture is a bucket on a clock that only the bucket's own waiting moves.
type bucketFixture struct {
	*Bucket
	now    time.Time
	token  bool
	waited []time.Duration
}

func newBucketFixture(token bool) *bucketFixture {
	f := &bucketFixture{now: noon, token: token}
	f.Bucket = NewBucket(func() bool { return f.token }, func() time.Time { return f.now })
	f.sleep = func(_ context.Context, d time.Duration) error {
		f.waited = append(f.waited, d)
		f.now = f.now.Add(d)
		return nil
	}
	return f
}

func TestTheBucketLetsACapacityBurstThroughThenPacesByTheRefillRate(t *testing.T) {
	for _, c := range []struct {
		name     string
		token    bool
		capacity int
		perToken time.Duration
	}{
		{"with a token", true, 16, 2222222222 * time.Nanosecond},
		{"without a token", false, 6, 6666666667 * time.Nanosecond},
	} {
		f := newBucketFixture(c.token)
		for range c.capacity {
			if err := f.Wait(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.waited) != 0 {
			t.Fatalf("%s: waited %v inside the burst", c.name, f.waited)
		}
		if err := f.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(f.waited) == 0 {
			t.Fatalf("%s: the search after the burst did not wait", c.name)
		}
		total := time.Duration(0)
		for _, w := range f.waited {
			total += w
		}
		if diff := total - c.perToken; diff < -time.Millisecond || diff > time.Millisecond {
			t.Errorf("%s: waited %v for one token, want %v", c.name, total, c.perToken)
		}
	}
}

func TestTheBucketRefillsWithTimeAndNeverAboveItsCapacity(t *testing.T) {
	f := newBucketFixture(true)
	for range 16 {
		_ = f.Wait(t.Context())
	}
	f.now = f.now.Add(10 * time.Second) // 4.5 tokens
	if got := f.Available(); math.Abs(got-4.5) > 1e-9 {
		t.Errorf("available = %v, want 4.5", got)
	}
	f.now = f.now.Add(time.Hour)
	if got := f.Available(); got != 16 {
		t.Errorf("available after an hour = %v, want the capacity", got)
	}
	f.token = false
	if got := f.Available(); got != 6 {
		t.Errorf("available without a token = %v, want 6", got)
	}
}

func TestTheBucketStopsWaitingWhenTheContextEnds(t *testing.T) {
	b := NewBucket(func() bool { return false }, func() time.Time { return noon })
	for range 6 {
		if err := b.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.Wait(ctx); err == nil {
		t.Error("an exhausted bucket let a canceled search through")
	}
}

func TestTheStoreKeepsTwelveRankingsPerListAndDropsOldListsAndAnswers(t *testing.T) {
	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "app.db"), migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := store{db: db.SQL}
	ctx := t.Context()

	for i := range 15 {
		snap := Snapshot{At: noon.Add(time.Duration(i) * time.Hour), Ranks: map[string]Rank{"o/r": {Position: i + 1}}}
		if err := s.addSnapshot(ctx, "popular|dev", snap); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.addSnapshot(ctx, "popular|media", Snapshot{At: noon, Ranks: map[string]Rank{}}); err != nil {
		t.Fatal(err)
	}
	history, err := s.snapshots(ctx, "popular|dev")
	if err != nil || len(history) != 12 || history[0].Ranks["o/r"].Position != 4 {
		t.Fatalf("%d rankings, first %+v, %v", len(history), history[0].Ranks, err)
	}
	if other, _ := s.snapshots(ctx, "popular|media"); len(other) != 1 {
		t.Errorf("trimming one list touched another: %d", len(other))
	}

	old, fresh := noon.Add(-15*24*time.Hour), noon.Add(-time.Hour)
	if err := s.saveList(ctx, "old", cachedList{fetched: old, repositories: []source.Repository{{Owner: "a", Name: "b"}}}, old); err != nil {
		t.Fatal(err)
	}
	if err := s.saveList(ctx, "fresh", cachedList{fetched: fresh, repositories: []source.Repository{{Owner: "a", Name: "b", Stars: 3}}}, noon); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.list(ctx, "old"); ok {
		t.Error("a list past 14 days was kept")
	}
	got, ok, err := s.list(ctx, "fresh")
	if err != nil || !ok || got.repositories[0].Stars != 3 || !got.fetched.Equal(fresh.Truncate(time.Second)) {
		t.Fatalf("fresh list = %+v, %v, %v", got, ok, err)
	}
}
