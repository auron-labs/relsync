package discover

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

const (
	// freshFor is how long a downloaded list is used as it is.
	freshFor = 6 * time.Hour
	// staleFor is how long an older list is still shown at once while a new one
	// is downloaded.
	staleFor = 14 * 24 * time.Hour
	// maxStarPages bounds the stars read: 4,000 at 100 a page.
	maxStarPages = 40
	// SearchPageSize is how many results a search shows at a time.
	SearchPageSize = 24
	// MaxSearchResults is the most results a search shows in all.
	MaxSearchResults = 240
)

// Client is the site Discover reads from. Only GitHub is one.
type Client interface {
	source.Source
	source.Discovery
	// HasToken reports whether requests carry a token, which raises the
	// allowance and the pace of searches.
	HasToken() bool
}

// Options are what a Service is built from.
type Options struct {
	Client Client
	DB     *sql.DB
	// Jobs runs the downloads of lists, so quitting waits for them.
	Jobs *jobs.Manager
	// Policy is what this machine can run, asked for each look-up of downloads.
	Policy func() selection.Policy
	// Now defaults to time.Now.
	Now func() time.Time
	// Sleep waits for d, or until ctx ends, and defaults to a timer. Tests
	// give a clock of their own one that moves it instead.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Service builds the Discover lists. It is safe for concurrent use.
type Service struct {
	client Client
	store  store
	jobs   *jobs.Manager
	policy func() selection.Policy
	now    func() time.Time
	bucket *Bucket

	seq     atomic.Int64
	mu      sync.Mutex
	flights map[string]*flight
}

// flight is one download of a list that every caller wanting it waits for.
type flight struct {
	done chan struct{}
	list List
	err  error
}

// NewService returns a Service over the options.
func NewService(o Options) *Service {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	bucket := NewBucket(o.Client.HasToken, now)
	if o.Sleep != nil {
		bucket.sleep = o.Sleep
	}
	return &Service{
		client: o.Client, store: store{db: o.DB}, jobs: o.Jobs, policy: o.Policy, now: now,
		bucket: bucket, flights: map[string]*flight{},
	}
}

// Request describes a list: a tab, and for the browsing tabs a category.
type Request struct {
	Tab      Tab
	Category string
	// User is whose stars the Stars tab lists; its cached copy is per user.
	User string
	// Refresh downloads the list again whatever is saved.
	Refresh bool
}

func (r Request) key() string {
	if r.Tab == Stars {
		return listKey(Stars, strings.ToLower(r.User))
	}
	return listKey(r.Tab, Find(r.Category).Key)
}

// List is a list of repositories, in no particular order.
type List struct {
	Repositories []source.Repository
	// Fetched is when it was downloaded.
	Fetched time.Time
	// Stale says the saved copy was past its time, so a newer one is being
	// downloaded for the next visit.
	Stale bool
	// Partial is why some of the searches failed. Such a list is shown but
	// not saved.
	Partial error
}

// List returns the list a request describes. A saved copy under six hours old
// is used as it is; one up to 14 days old is returned at once and downloaded
// again in the background. Callers wanting the same list wait for one download.
func (s *Service) List(ctx context.Context, req Request) (List, error) {
	if !req.Refresh {
		cached, ok, err := s.store.list(ctx, req.key())
		if err != nil {
			return List{}, err
		}
		if age := s.now().Sub(cached.fetched); ok && age < staleFor {
			list := List{Repositories: cached.repositories, Fetched: cached.fetched, Stale: age >= freshFor}
			if list.Stale {
				if _, err := s.download(req); err != nil && !errors.Is(err, jobs.ErrStopped) {
					log.Printf("discover: refresh %s: %v", req.key(), err)
				}
			}
			return list, nil
		}
	}
	f, err := s.download(req)
	if err != nil {
		return List{}, err
	}
	select {
	case <-f.done:
		return f.list, f.err
	case <-ctx.Done():
		return List{}, fmt.Errorf("wait for list %s: %w", req.key(), ctx.Err())
	}
}

// download joins the download of a list already under way, or starts one.
func (s *Service) download(req Request) (*flight, error) {
	key := req.key()
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.flights[key]; ok {
		return f, nil
	}
	f := &flight{done: make(chan struct{})}
	// Every download is a job of its own, named apart from the one before it.
	name := "discover:" + key + ":" + strconv.FormatInt(s.seq.Add(1), 10)
	err := s.jobs.Start(name, func(ctx context.Context, _ jobs.Report) error {
		defer func() {
			s.mu.Lock()
			delete(s.flights, key)
			s.mu.Unlock()
			close(f.done)
		}()
		f.list, f.err = s.fetch(ctx, req)
		return unexpected(f.err)
	})
	if err != nil {
		return nil, fmt.Errorf("download list %s: %w", key, err)
	}
	s.flights[key] = f
	return f, nil
}

// unexpected keeps what is worth reporting: a failure of the request itself,
// such as an offline machine or a spent allowance, is not.
func unexpected(err error) error {
	var se *source.Error
	if err == nil || errors.As(err, &se) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// fetch downloads a list and saves it when nothing failed.
func (s *Service) fetch(ctx context.Context, req Request) (List, error) {
	var repos []source.Repository
	var failure error
	if req.Tab == Stars {
		repos, failure = s.fetchStars(ctx)
	} else {
		repos, failure = s.fetchSearches(ctx, Queries(req.Tab, Find(req.Category), s.now()))
	}
	if err := ctx.Err(); err != nil {
		return List{}, fmt.Errorf("download list %s: %w", req.key(), err)
	}
	switch {
	case len(repos) == 0 && failure != nil:
		return List{}, failure
	case len(repos) == 0:
		return List{Fetched: s.now()}, nil
	}
	list := List{Repositories: repos, Fetched: s.now(), Partial: failure}
	if failure == nil {
		if err := s.store.saveList(ctx, req.key(), cachedList{fetched: list.Fetched, repositories: repos}, list.Fetched); err != nil {
			return List{}, err
		}
	}
	return list, nil
}

// fetchSearches runs every query at once, paced by the bucket, and merges the
// results in the order of the queries without repeats or noise. It returns the
// first failure alongside whatever the other queries found.
func (s *Service) fetchSearches(ctx context.Context, queries []Query) ([]source.Repository, error) {
	results := make([][]source.Repository, len(queries))
	failures := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Go(func() {
			if err := s.bucket.Wait(ctx); err != nil {
				failures[i] = err
				return
			}
			page, err := s.client.Search(ctx, source.SearchQuery{Text: q.Text, ByStars: true, PerPage: q.PerPage, Page: 1})
			results[i], failures[i] = page.Repositories, err
		})
	}
	wg.Wait()
	var merged []source.Repository
	seen := map[string]bool{}
	for _, found := range results {
		for _, r := range found {
			if key := repoKey(r); !seen[key] && !IsNoise(r) {
				seen[key] = true
				merged = append(merged, r)
			}
		}
	}
	return merged, errors.Join(failures...)
}

// fetchStars reads the signed-in user's stars, newest first.
func (s *Service) fetchStars(ctx context.Context) ([]source.Repository, error) {
	var all []source.Repository
	var failure error
	for page := 1; page <= maxStarPages; page++ {
		got, err := s.client.Starred(ctx, page)
		if err != nil {
			failure = err
			break
		}
		all = append(all, got.Repositories...)
		if !got.More {
			break
		}
	}
	kept := all[:0]
	for _, r := range all {
		if !IsNoise(r) {
			kept = append(kept, r)
		}
	}
	return kept, failure
}

// Baseline returns the earlier ranking of a list to compare with, if there is one.
func (s *Service) Baseline(ctx context.Context, req Request, w Window) (*Baseline, error) {
	history, err := s.store.snapshots(ctx, req.key())
	if err != nil {
		return nil, err
	}
	base, ok := BaselineFor(history, w, s.now())
	if !ok {
		return nil, nil //nolint:nilnil // No earlier ranking is an ordinary answer.
	}
	return &base, nil
}

// Record saves the top of a list as it was shown, unless a ranking was saved
// in the last 20 hours.
func (s *Service) Record(ctx context.Context, req Request, shown []source.Repository) error {
	history, err := s.store.snapshots(ctx, req.key())
	if err != nil {
		return err
	}
	if len(shown) == 0 || !SnapshotDue(history, s.now()) {
		return nil
	}
	return s.store.addSnapshot(ctx, req.key(), NewSnapshot(s.now(), shown))
}

// Search runs a search typed by the user, a page of 24 at a time starting at 1.
// A query with a qualifier is sorted by stars.
func (s *Service) Search(ctx context.Context, text string, page int) (source.SearchPage, error) {
	if err := s.bucket.Wait(ctx); err != nil {
		return source.SearchPage{}, err
	}
	found, err := s.client.Search(ctx, source.SearchQuery{
		Text: text, ByStars: strings.Contains(text, ":"), PerPage: SearchPageSize, Page: page,
	})
	if err != nil {
		return source.SearchPage{}, fmt.Errorf("search for %q: %w", text, err)
	}
	return found, nil
}

// OwnerRepositories lists an owner's repositories, 30 a page starting at 1.
func (s *Service) OwnerRepositories(ctx context.Context, owner string, page int) (source.Page, error) {
	found, err := s.client.OwnerRepositories(ctx, owner, page)
	if err != nil {
		return source.Page{}, fmt.Errorf("list repositories of %s: %w", owner, err)
	}
	return found, nil
}

// Repository looks a repository up.
func (s *Service) Repository(ctx context.Context, ref source.RepoRef) (source.Repository, error) {
	repo, err := s.client.Repository(ctx, ref)
	if err != nil {
		return source.Repository{}, fmt.Errorf("look up %s: %w", ref, err)
	}
	return repo, nil
}

// Readme returns a repository's README as Markdown.
func (s *Service) Readme(ctx context.Context, ref source.RepoRef) (string, error) {
	text, err := s.client.Readme(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("read the README of %s: %w", ref, err)
	}
	return text, nil
}

// HasToken reports whether requests carry a token.
func (s *Service) HasToken() bool { return s.client.HasToken() }

// CanPrepare reports whether the allowance allows preparing lists ahead.
func (s *Service) CanPrepare() bool {
	return CanPrepare(s.client.Allowance(), s.client.HasToken())
}

// CanCheckDownloads reports whether the allowance allows looking up downloads.
func (s *Service) CanCheckDownloads() bool {
	return CanCheckDownloads(s.client.Allowance(), s.client.HasToken())
}
