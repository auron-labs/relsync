package discover_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/database"
	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/database/migrations"
	"github.com/aaronflorey/release-sync/discover"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

// fakeClient answers searches from a table keyed by the query text.
type fakeClient struct {
	mu sync.Mutex
	// results maps a search's text to what it finds; texts without an entry find nothing.
	results map[string][]source.Repository
	// failing maps a search's text to its failure.
	failing  map[string]error
	searches []source.SearchQuery
	// gate, when set, holds every search until it is closed.
	gate chan struct{}

	releases     map[string]source.ReleasePage
	releaseFails map[string]error
	releaseCalls map[string]int

	stars      [][]source.Repository
	starPages  int
	allowance  source.Allowance
	hasToken   bool
	readmeText string
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		results: map[string][]source.Repository{}, failing: map[string]error{},
		releases: map[string]source.ReleasePage{}, releaseFails: map[string]error{}, releaseCalls: map[string]int{},
		hasToken: true,
	}
}

func (f *fakeClient) HasToken() bool { return f.hasToken }

func (f *fakeClient) Allowance() source.Allowance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allowance
}

func (f *fakeClient) setAllowance(a source.Allowance) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowance = a
}

func (f *fakeClient) Search(ctx context.Context, q source.SearchQuery) (source.SearchPage, error) {
	f.mu.Lock()
	f.searches = append(f.searches, q)
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return source.SearchPage{}, fmt.Errorf("fake search: %w", ctx.Err())
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failing[q.Text]; err != nil {
		return source.SearchPage{}, err
	}
	found := f.results[q.Text]
	return source.SearchPage{Repositories: found, Total: len(found)}, nil
}

func (f *fakeClient) searchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.searches)
}

func (f *fakeClient) Starred(_ context.Context, page int) (source.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starPages++
	if page > len(f.stars) {
		return source.Page{}, nil
	}
	return source.Page{Repositories: f.stars[page-1], More: page < len(f.stars)}, nil
}

func (f *fakeClient) OwnerRepositories(context.Context, string, int) (source.Page, error) {
	return source.Page{}, nil
}

func (f *fakeClient) Readme(context.Context, source.RepoRef) (string, error) {
	return f.readmeText, nil
}

func (f *fakeClient) Repository(_ context.Context, ref source.RepoRef) (source.Repository, error) {
	return source.Repository{Owner: ref.Owner, Name: ref.Repo}, nil
}

func (f *fakeClient) Releases(_ context.Context, ref source.RepoRef, _ string) (source.ReleasePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.ToLower(ref.String())
	f.releaseCalls[key]++
	if err := f.releaseFails[key]; err != nil {
		return source.ReleasePage{}, err
	}
	return f.releases[key], nil
}

func (f *fakeClient) releaseRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, n := range f.releaseCalls {
		total += n
	}
	return total
}

// clock is a time the test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// sleep moves the clock instead of waiting.
func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sleep: %w", err)
	}
	c.advance(d)
	return nil
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	*discover.Service
	client *fakeClient
	clock  *clock
	db     *sql.DB
	jobs   *jobs.Manager
	// running counts the jobs not yet finished.
	running atomic.Int32
}

var start = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "app.db"), migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manager := jobs.New(t.Context())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	f := &fixture{client: newFakeClient(), clock: &clock{t: start}, db: db.SQL, jobs: manager}
	manager.Subscribe(func(st jobs.Status) {
		if st.State == jobs.Running && st.Done == 0 && st.Total == 0 && st.Message == "" {
			f.running.Add(1)
		} else if st.State != jobs.Running {
			f.running.Add(-1)
		}
	})
	f.Service = f.serviceFor(selection.ArchX64)
	return f
}

// serviceFor is a service over the fixture's database for a machine of arch.
func (f *fixture) serviceFor(arch string) *discover.Service {
	return discover.NewService(discover.Options{
		Client: f.client, DB: f.db, Jobs: f.jobs, Now: f.clock.now, Sleep: f.clock.sleep,
		Policy: func() selection.Policy {
			return selection.Policy{Target: selection.Target{OS: selection.OSWindows, Arch: arch}, PreferInstallers: true}
		},
	})
}

// settled reports that no download job is running.
func (f *fixture) settled() bool { return f.running.Load() == 0 }

func repo(owner, name string, stars int) source.Repository {
	return source.Repository{Owner: owner, Name: name, Stars: stars, Description: name + " does things"}
}

func names(repos []source.Repository) []string {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = r.Owner + "/" + r.Name
	}
	return out
}

// queriesOf lists the search texts a request makes at the fixture's time.
func queriesOf(tab discover.Tab, category string, at time.Time) []string {
	queries := discover.Queries(tab, discover.Find(category), at)
	texts := make([]string, 0, len(queries))
	for _, q := range queries {
		texts = append(texts, q.Text)
	}
	return texts
}

func TestAListMergesItsSearchesInOrderWithoutRepeatsOrNoise(t *testing.T) {
	f := newFixture(t)
	queries := queriesOf(discover.Popular, "emulators", start)
	archived := repo("o", "old-emu", 900)
	archived.Archived = true
	f.client.results[queries[0]] = []source.Repository{repo("a", "first", 500), repo("a", "awesome-emulators", 800), archived}
	f.client.results[queries[1]] = []source.Repository{repo("b", "second", 400), repo("A", "First", 500), repo("c", "unstarred", 0)}
	f.client.results[queries[2]] = []source.Repository{{Owner: "d", Name: "list", Stars: 50, Description: "A list of emulators"}}

	list, err := f.List(t.Context(), discover.Request{Tab: discover.Popular, Category: "emulators"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/first", "b/second"}; !slices.Equal(names(list.Repositories), want) {
		t.Errorf("list = %v, want %v", names(list.Repositories), want)
	}
	if list.Partial != nil || list.Stale {
		t.Errorf("partial %v, stale %v", list.Partial, list.Stale)
	}
	if got := f.client.searchCount(); got != len(queries) {
		t.Errorf("%d searches, want one per topic (%d)", got, len(queries))
	}
}

func TestAListIsFreshForSixHoursStaleUpToFourteenDaysAndThenDownloadedAgain(t *testing.T) {
	f := newFixture(t)
	req := discover.Request{Tab: discover.Popular, Category: "dev"}
	queries := queriesOf(discover.Popular, "dev", start)
	f.client.results[queries[0]] = []source.Repository{repo("a", "one", 500)}
	perDownload := len(queries)

	if _, err := f.List(t.Context(), req); err != nil {
		t.Fatal(err)
	}

	f.clock.advance(5 * time.Hour)
	list, err := f.List(t.Context(), req)
	if err != nil || list.Stale || f.client.searchCount() != perDownload {
		t.Fatalf("after 5h: stale %v, %d searches, %v", list.Stale, f.client.searchCount(), err)
	}

	// Past six hours the saved copy is shown at once and a newer one is downloaded behind it.
	f.client.results[queries[0]] = []source.Repository{repo("a", "one", 500), repo("a", "two", 400)}
	f.clock.advance(2 * time.Hour)
	list, err = f.List(t.Context(), req)
	if err != nil || !list.Stale || len(list.Repositories) != 1 {
		t.Fatalf("after 7h: stale %v, %v, %v", list.Stale, names(list.Repositories), err)
	}
	waitUntil(t, "the refresh", func() bool { return f.client.searchCount() == 2*perDownload && f.settled() })
	list, err = f.List(t.Context(), req)
	if err != nil || list.Stale || len(list.Repositories) != 2 {
		t.Fatalf("after the refresh: stale %v, %v, %v", list.Stale, names(list.Repositories), err)
	}

	// Past 14 days the saved copy is not used: the caller waits for a download.
	f.client.results[queries[0]] = []source.Repository{repo("a", "three", 300)}
	f.clock.advance(15 * 24 * time.Hour)
	list, err = f.List(t.Context(), req)
	if err != nil || list.Stale || !slices.Equal(names(list.Repositories), []string{"a/three"}) {
		t.Fatalf("after 15 days: stale %v, %v, %v", list.Stale, names(list.Repositories), err)
	}
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAListWithAFailedSearchIsShownButNotSaved(t *testing.T) {
	f := newFixture(t)
	req := discover.Request{Tab: discover.New, Category: "media"}
	queries := queriesOf(discover.New, "media", start)
	f.client.results[queries[0]] = []source.Repository{repo("a", "one", 500)}
	f.client.failing[queries[1]] = &source.Error{Kind: source.KindUnreachable}

	list, err := f.List(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	var se *source.Error
	if !errors.As(list.Partial, &se) || len(list.Repositories) != 1 {
		t.Fatalf("partial = %v, list = %v", list.Partial, names(list.Repositories))
	}
	before := f.client.searchCount()
	delete(f.client.failing, queries[1])
	list, err = f.List(t.Context(), req)
	if err != nil || list.Partial != nil || f.client.searchCount() != before+len(queries) {
		t.Errorf("a partial list was reused: %d searches after %d, %v, %v", f.client.searchCount(), before, list.Partial, err)
	}
}

func TestEveryFailedSearchIsAnErrorAndSavesNothing(t *testing.T) {
	f := newFixture(t)
	req := discover.Request{Tab: discover.Updated, Category: "ai"}
	for _, q := range queriesOf(discover.Updated, "ai", start) {
		f.client.failing[q] = &source.Error{Kind: source.KindRateLimited, Status: 403}
	}
	_, err := f.List(t.Context(), req)
	var se *source.Error
	if !errors.As(err, &se) || se.Kind != source.KindRateLimited {
		t.Fatalf("err = %v", err)
	}
}

func TestTwoVisitorsToTheSameListWaitForOneDownload(t *testing.T) {
	f := newFixture(t)
	req := discover.Request{Tab: discover.Popular, Category: "privacy"}
	queries := queriesOf(discover.Popular, "privacy", start)
	f.client.results[queries[0]] = []source.Repository{repo("a", "one", 500)}
	f.client.gate = make(chan struct{})

	results := make(chan int, 2)
	for range 2 {
		go func() {
			list, err := f.List(t.Context(), req)
			if err != nil {
				results <- -1
				return
			}
			results <- len(list.Repositories)
		}()
	}
	waitUntil(t, "the searches to start", func() bool { return f.client.searchCount() >= len(queries) })
	time.Sleep(20 * time.Millisecond)
	close(f.client.gate)
	for range 2 {
		if got := <-results; got != 1 {
			t.Fatalf("a visitor got %d repositories", got)
		}
	}
	if got := f.client.searchCount(); got != len(queries) {
		t.Errorf("%d searches for two visitors, want %d", got, len(queries))
	}
}

func TestAVisitorWhoLeavesDoesNotStopTheDownloadOthersWaitFor(t *testing.T) {
	f := newFixture(t)
	req := discover.Request{Tab: discover.Popular, Category: "graphics"}
	queries := queriesOf(discover.Popular, "graphics", start)
	f.client.results[queries[0]] = []source.Repository{repo("a", "one", 500)}
	f.client.gate = make(chan struct{})

	ctx, leave := context.WithCancel(t.Context())
	left := make(chan error, 1)
	go func() { _, err := f.List(ctx, req); left <- err }()
	waitUntil(t, "the searches to start", func() bool { return f.client.searchCount() >= len(queries) })
	leave()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatalf("leaving visitor: %v", err)
	}
	close(f.client.gate)
	list, err := f.List(t.Context(), req)
	if err != nil || len(list.Repositories) != 1 {
		t.Fatalf("next visitor: %v, %v", names(list.Repositories), err)
	}
}

func TestYourStarsAreReadPageByPageNewestFirstFilteredAndKeptPerUser(t *testing.T) {
	f := newFixture(t)
	f.client.stars = [][]source.Repository{
		{repo("a", "newest", 10), repo("a", "awesome-stuff", 10)},
		{repo("b", "older", 5), repo("c", "empty", 0)},
	}
	req := discover.Request{Tab: discover.Stars, User: "Octo"}
	list, err := f.List(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/newest", "b/older"}; !slices.Equal(names(list.Repositories), want) {
		t.Errorf("stars = %v, want %v", names(list.Repositories), want)
	}
	if f.client.starPages != 2 {
		t.Errorf("read %d pages, want 2", f.client.starPages)
	}
	if _, err := f.List(t.Context(), req); err != nil || f.client.starPages != 2 {
		t.Errorf("the saved stars were read again: %d pages, %v", f.client.starPages, err)
	}
	other := discover.Request{Tab: discover.Stars, User: "someone-else"}
	if _, err := f.List(t.Context(), other); err != nil || f.client.starPages != 4 {
		t.Errorf("another user shared the first user's stars: %d pages, %v", f.client.starPages, err)
	}
}

func TestSearchPacesWithTheBucketAndSortsByStarsOnlyForQualifiers(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		text    string
		byStars bool
	}{{"editor", false}, {"language:go stars:>100", true}} {
		if _, err := f.Search(t.Context(), c.text, 2); err != nil {
			t.Fatal(err)
		}
		got := f.client.searches[len(f.client.searches)-1]
		if got.ByStars != c.byStars || got.PerPage != discover.SearchPageSize || got.Page != 2 || got.Text != c.text {
			t.Errorf("search %q sent %+v", c.text, got)
		}
	}
}

func TestPrepareFetchesTheOtherTabsOnlyWhileTheAllowanceHasRoom(t *testing.T) {
	f := newFixture(t)
	f.client.setAllowance(source.Allowance{Known: true, Limit: 5000, Remaining: 1499, Authenticated: true})
	err := f.Prepare(t.Context(), discover.PrepareRequest{Category: "dev", Skip: discover.Popular})
	if err != nil || f.client.searchCount() != 0 {
		t.Fatalf("below 1,500 left: %d searches, %v", f.client.searchCount(), err)
	}

	f.client.setAllowance(source.Allowance{Known: true, Limit: 5000, Remaining: 1500, Authenticated: true})
	if err := f.Prepare(t.Context(), discover.PrepareRequest{Category: "dev", Skip: discover.Popular}); err != nil {
		t.Fatal(err)
	}
	var want int
	for _, tab := range []discover.Tab{discover.Trending, discover.New, discover.Updated} {
		want += len(queriesOf(tab, "dev", start))
	}
	if got := f.client.searchCount(); got != want {
		t.Errorf("%d searches, want %d for Trending, New and Updated", got, want)
	}
}

func TestPrepareDoesNothingWithoutAToken(t *testing.T) {
	f := newFixture(t)
	f.client.hasToken = false
	if err := f.Prepare(t.Context(), discover.PrepareRequest{Category: "dev", Skip: discover.Popular}); err != nil || f.client.searchCount() != 0 {
		t.Fatalf("%d searches, %v", f.client.searchCount(), err)
	}
}

func TestPrepareReportsAFailedListAndCarriesOn(t *testing.T) {
	f := newFixture(t)
	for _, q := range queriesOf(discover.Trending, "dev", start) {
		f.client.failing[q] = &source.Error{Kind: source.KindUnreachable}
	}
	err := f.Prepare(t.Context(), discover.PrepareRequest{Category: "dev", Skip: discover.Popular})
	if err == nil || !strings.Contains(err.Error(), "trending") {
		t.Fatalf("err = %v", err)
	}
	// The lists after the failed one were still searched for.
	if got := len(f.client.searches); got <= len(queriesOf(discover.Trending, "dev", start)) {
		t.Errorf("%d searches: the lists after the failure were skipped", got)
	}
}

func TestMovementBaselinesAndFirstVisitFromRecordedRankings(t *testing.T) {
	f := newFixture(t)
	req := discover.Request{Tab: discover.Popular, Category: "dev"}
	shown := []source.Repository{repo("a", "one", 500), repo("b", "two", 400)}

	base, err := f.Baseline(t.Context(), req, discover.Day)
	if err != nil || base != nil {
		t.Fatalf("first visit baseline = %v, %v", base, err)
	}
	if err := f.Record(t.Context(), req, shown); err != nil {
		t.Fatal(err)
	}
	// Recorded at most once every 20 hours.
	f.clock.advance(10 * time.Hour)
	if err := f.Record(t.Context(), req, []source.Repository{repo("z", "other", 1)}); err != nil {
		t.Fatal(err)
	}
	if base, _ = f.Baseline(t.Context(), req, discover.Day); base != nil {
		t.Fatalf("a ranking 10 hours old is a baseline: %+v", base)
	}
	f.clock.advance(15 * time.Hour)
	base, err = f.Baseline(t.Context(), req, discover.Day)
	if err != nil || base == nil || base.Age != 25*time.Hour {
		t.Fatalf("baseline = %+v, %v", base, err)
	}
	if _, in := base.Snapshot.Ranks["z/other"]; in {
		t.Error("the second ranking inside 20 hours was recorded")
	}
	if got := discover.MovementOf(base, repo("b", "two", 450), 1); got.Kind != discover.MoveUp || got.Places != 1 || got.Gained != 50 {
		t.Errorf("movement = %+v", got)
	}
}

func TestDownloadChecksRememberOnlyDefiniteAnswers(t *testing.T) {
	f := newFixture(t)
	win := source.Release{Tag: "v1", Assets: []source.Asset{{Name: "tool-x64-setup.exe", Size: 5 << 20}}}
	linux := source.Release{Tag: "v1", Assets: []source.Asset{{Name: "tool-linux-amd64.tar.gz", Size: 5 << 20}}}
	f.client.releases["a/has"] = source.ReleasePage{Releases: []source.Release{win}}
	f.client.releases["a/lacks"] = source.ReleasePage{Releases: []source.Release{linux}}
	f.client.releases["a/none"] = source.ReleasePage{}
	f.client.releaseFails["a/offline"] = &source.Error{Kind: source.KindUnreachable}
	repos := []source.Repository{repo("a", "has", 1), repo("a", "lacks", 1), repo("a", "none", 1), repo("a", "offline", 1)}

	answers, err := f.Downloads(t.Context(), repos)
	var se *source.Error
	if !errors.As(err, &se) || se.Kind != source.KindUnreachable {
		t.Fatalf("err = %v, want the unreachable failure", err)
	}
	if want := []discover.Answer{discover.Has, discover.Lacks, discover.Lacks, discover.Unknown}; !slices.Equal(answers, want) {
		t.Fatalf("answers = %v, want %v", answers, want)
	}
	if f.client.releaseRequests() != 4 {
		t.Fatalf("%d release requests, want 4", f.client.releaseRequests())
	}

	// The definite answers come from the saved copy; the one that failed is asked again.
	delete(f.client.releaseFails, "a/offline")
	f.client.releases["a/offline"] = source.ReleasePage{Releases: []source.Release{win}}
	answers, err = f.Downloads(t.Context(), repos)
	if err != nil || answers[3] != discover.Has || f.client.releaseRequests() != 5 {
		t.Fatalf("second look: %v, %d requests, %v", answers, f.client.releaseRequests(), err)
	}

	// Past three days even a definite answer is asked for again.
	f.clock.advance(3*24*time.Hour + time.Minute)
	if _, err := f.Downloads(t.Context(), repos); err != nil || f.client.releaseRequests() != 9 {
		t.Fatalf("after three days: %d requests, %v", f.client.releaseRequests(), err)
	}
}

func TestACachedAnswerIsForOneKindOfMachine(t *testing.T) {
	f := newFixture(t)
	f.client.releases["a/tool"] = source.ReleasePage{Releases: []source.Release{
		{Tag: "v1", Assets: []source.Asset{{Name: "tool-arm64-setup.exe", Size: 5 << 20}}},
	}}
	repos := []source.Repository{repo("a", "tool", 1)}
	if answers, err := f.Downloads(t.Context(), repos); err != nil || answers[0] != discover.Lacks {
		t.Fatalf("on x64: %v, %v", answers, err)
	}

	arm := f.serviceFor(selection.ArchARM64)
	answers, err := arm.Downloads(t.Context(), repos)
	if err != nil || answers[0] != discover.Has {
		t.Fatalf("on ARM: %v, %v", answers, err)
	}
}
