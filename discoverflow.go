package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/discover"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"

	"github.com/oneo-me/onevra/source/components"
)

const (
	// fullPageSize and lightPageSize are how many projects a list shows at a
	// time in Full and Light mode.
	fullPageSize  = 36
	lightPageSize = 15
	// fullChecks and lightChecks are how many projects one load looks up
	// downloads for.
	fullChecks  = 100
	lightChecks = 40
	// starsPageSize is how many stars the Your stars tab shows at a time.
	starsPageSize = 60
)

// discoverMode says what the Discover page lists.
type discoverMode int

const (
	// modeBrowse lists a tab and category.
	modeBrowse discoverMode = iota
	// modeSearch lists the results of a search typed in the top box.
	modeSearch
	// modeOwner lists one developer's repositories.
	modeOwner
)

// discoverRow is one project in a Discover list.
type discoverRow struct {
	repo source.Repository
	// rank is its place in the list shown, 0 where the list is not ranked.
	rank int
	move discover.Movement
	// answer is whether it has a download for this PC, when that is known.
	answer discover.Answer
}

// discoverNotes are the facts about a browsing list the status line reports.
type discoverNotes struct {
	fetched time.Time
	// base is the ranking movement is measured against; nil on a first visit.
	base *discover.Baseline
	// ranked says the list has movement at all, which Your stars does not.
	ranked  bool
	stale   bool
	partial bool
	// paused says the look-up of downloads stopped for lack of allowance.
	paused bool
}

// discoverFeed is a list of projects on the page and what is going on with it.
type discoverFeed struct {
	rows []discoverRow
	// busy says a load is under way, and loaded that one has finished.
	busy, loaded bool
	// failure is why the last load failed, for the user.
	failure string
	// needsToken says the list cannot be shown without a GitHub token.
	needsToken bool
	// more says another page may follow, and total how many results a search found.
	more  bool
	total int
	notes discoverNotes
	// generation numbers loads, so a superseded one is ignored when it ends.
	generation int
	cancel     context.CancelFunc
}

// discoverOptions are the choices a browsing list was loaded with.
type discoverOptions struct {
	tab       discover.Tab
	category  string
	window    discover.Window
	downloads bool
	light     bool
	// user is whose stars the Your stars tab lists.
	user string
}

func (o discoverOptions) pageSize() int {
	if o.light {
		return lightPageSize
	}
	return fullPageSize
}

func (o discoverOptions) checkLimit() int {
	if o.light {
		return lightChecks
	}
	return fullChecks
}

// request is the list the options describe.
func (o discoverOptions) request(refresh bool) discover.Request {
	return discover.Request{Tab: o.tab, Category: o.category, User: o.user, Refresh: refresh}
}

// discoverState is what the Discover page remembers between frames. It is only
// touched on the UI thread.
type discoverState struct {
	mode discoverMode
	// browse is the tab and category; side is a search or an owner's repositories.
	browse, side discoverFeed
	opts         discoverOptions

	// ordered is the browsing list in order, next how far into it rows have been
	// taken, and base the ranking it is compared with.
	ordered []source.Repository
	next    int
	base    *discover.Baseline

	// starsText filters the Your stars tab. matching is the stars that pass the
	// text matchingFor, and starsShown how many of them the page shows.
	starsText, matchingFor string
	matching               []source.Repository
	matched                bool
	starsShown             int

	query     string
	owner     string
	nextPage  int
	grid      components.GridState
	preparing context.CancelFunc
}

// active is the feed the page shows.
func (d *discoverState) active() *discoverFeed {
	if d.mode == modeBrowse {
		return &d.browse
	}
	return &d.side
}

// discoverOptions reads the choices that decide what a browsing list holds.
func (s *screen) discoverOptions() discoverOptions {
	p := s.prefs.Get()
	return discoverOptions{
		tab: discover.Tab(p.DiscoverTab), category: p.DiscoverCategory, window: discover.Window(p.DiscoverCompare),
		downloads: p.DiscoverDownloadsOnly, light: p.DiscoverLight, user: s.account.Get().Login,
	}
}

// cancellable runs fn as a job with a context of its own, which the returned
// function cancels, before or after the job starts. Quitting cancels it too.
func (s *screen) cancellable(fn func(ctx context.Context) error) context.CancelFunc {
	var mu sync.Mutex
	var stop context.CancelFunc
	stopped := false
	s.background(func(jobCtx context.Context) error {
		ctx, cancel := context.WithCancel(jobCtx)
		defer cancel()
		mu.Lock()
		stop = cancel
		if stopped {
			cancel()
		}
		mu.Unlock()
		return fn(ctx)
	})
	return func() {
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		if stop != nil {
			stop()
		}
	}
}

// unexpectedError keeps the failures worth reporting: a request that failed or
// was given up on is shown to the user instead.
func unexpectedError(err error) error {
	var se *source.Error
	if err == nil || errors.As(err, &se) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// discoverEntered loads the browsing list when the page is opened and holds
// none for the choices now made.
func (s *screen) discoverEntered() {
	d := &s.discoverUI
	if d.mode != modeBrowse {
		return
	}
	if (d.browse.loaded || d.browse.busy) && d.opts == s.discoverOptions() {
		return
	}
	s.loadBrowse(false)
}

// startFeed resets a feed for a new load and returns its generation.
func startFeed(f *discoverFeed) int {
	if f.cancel != nil {
		f.cancel()
	}
	generation := f.generation + 1
	*f = discoverFeed{busy: true, generation: generation}
	return generation
}

// loadBrowse loads the tab and category the choices name. refresh downloads the
// list again whatever is saved.
func (s *screen) loadBrowse(refresh bool) {
	d := &s.discoverUI
	d.mode = modeBrowse
	opts := s.discoverOptions()
	d.opts, d.ordered, d.next, d.base = opts, nil, 0, nil
	d.starsShown, d.matched = 0, false
	f := &d.browse
	generation := startFeed(f)
	f.cancel = s.cancellable(func(ctx context.Context) error { return s.browseJob(ctx, generation, opts, refresh) })
	s.invalidate()
}

func (s *screen) invalidate() {
	if s.win != nil {
		s.win.Invalidate()
	}
}

// browseJob downloads or reads the list, puts it in order and takes its first rows.
func (s *screen) browseJob(ctx context.Context, generation int, opts discoverOptions, refresh bool) error {
	f := &s.discoverUI.browse
	current := func() bool { return f.generation == generation }
	if opts.tab == discover.Stars && !s.discover.HasToken() {
		s.onUI(func() {
			if current() {
				f.busy, f.loaded, f.needsToken = false, true, true
			}
		})
		return nil
	}
	req := opts.request(refresh)
	list, err := s.discover.List(ctx, req)
	var base *discover.Baseline
	if err == nil && opts.tab != discover.Stars {
		base, err = s.discover.Baseline(ctx, req, opts.window)
	}
	if err != nil {
		return s.browseFailed(ctx, generation, opts, err)
	}
	ordered := discover.Order(list.Repositories, opts.tab, base, time.Now())
	notes := discoverNotes{fetched: list.Fetched, base: base, ranked: opts.tab != discover.Stars, stale: list.Stale, partial: list.Partial != nil}
	if list.Partial != nil {
		log.Printf("discover: %s list is incomplete: %v", opts.tab, list.Partial)
	}
	if opts.tab == discover.Stars {
		if refresh {
			s.record(ctx, activity.Info, 0, locales.ActivityStarsRead(len(ordered)))
		}
		s.onUI(func() {
			if !current() {
				return
			}
			d := &s.discoverUI
			d.ordered, d.starsShown, d.matched = ordered, starsPageSize, false
			f.busy, f.loaded, f.notes = false, true, notes
		})
		return nil
	}
	filled := s.fillRows(ctx, fillRequest{ordered: ordered, base: base, opts: opts})
	notes.paused = filled.paused
	s.onUI(func() {
		if !current() {
			return
		}
		d := &s.discoverUI
		d.ordered, d.next, d.base = ordered, filled.next, base
		f.rows, f.more, f.busy, f.loaded, f.notes = filled.rows, filled.next < len(ordered), false, true, notes
	})
	if ctx.Err() != nil {
		return nil //nolint:nilerr // The load was superseded or the application is quitting; there is nothing to report.
	}
	shown := make([]source.Repository, len(filled.rows))
	for i, r := range filled.rows {
		shown[i] = r.repo
	}
	if err := s.discover.Record(ctx, req, shown); err != nil && ctx.Err() == nil {
		return fmt.Errorf("record the ranking of %s: %w", opts.tab, err)
	}
	s.afterBrowse(ctx, generation, opts, filled.rows)
	return nil
}

// afterBrowse does the work that follows showing a list: marking which
// featured projects have downloads, and preparing the other lists.
func (s *screen) afterBrowse(ctx context.Context, generation int, opts discoverOptions, rows []discoverRow) {
	switch {
	case opts.tab == discover.Featured:
		if s.discover.HasToken() {
			s.markDownloads(ctx, generation, rows)
		}
	case !opts.light && s.discover.CanPrepare():
		s.onUI(func() { s.prepareAhead(opts) })
	}
}

// browseFailed reports a list that could not be loaded.
func (s *screen) browseFailed(ctx context.Context, generation int, opts discoverOptions, err error) error {
	if ctx.Err() != nil {
		return nil //nolint:nilerr // The load was superseded or the application is quitting; there is nothing to report.
	}
	text := errorText(err)
	log.Printf("discover: load %s: %v", opts.tab, err)
	s.record(ctx, activity.Error, 0, locales.ActivityDiscoverFailed(tabTitle(opts.tab), text))
	s.onUI(func() {
		if f := &s.discoverUI.browse; f.generation == generation {
			f.busy, f.loaded, f.failure = false, true, text
		}
	})
	return unexpectedError(err)
}

// fillRequest is where in a list the next rows come from.
type fillRequest struct {
	ordered []source.Repository
	next    int
	// shown is how many rows the list already has, so ranks carry on from it.
	shown int
	base  *discover.Baseline
	opts  discoverOptions
}

// fillResult is the rows taken and how far into the list that got.
type fillResult struct {
	rows []discoverRow
	next int
	// paused says the look-up of downloads stopped for lack of allowance.
	paused bool
}

// fillRows takes the next page of rows from a list. With the download filter
// on, it looks projects up CheckBatch at a time and drops those known to have
// no download, up to the limit of one load; one that could not be looked up
// stays. When the allowance runs low the look-up stops and the list is taken as
// it is.
func (s *screen) fillRows(ctx context.Context, req fillRequest) fillResult {
	want := req.opts.pageSize()
	filter := req.opts.downloads && req.opts.tab != discover.Featured && s.discover.HasToken()
	res := fillResult{next: req.next}
	row := func(r source.Repository, answer discover.Answer) discoverRow {
		rank := req.shown + len(res.rows) + 1
		return discoverRow{repo: r, rank: rank, move: discover.MovementOf(req.base, r, rank), answer: answer}
	}
	checked := 0
	for res.next < len(req.ordered) && len(res.rows) < want && ctx.Err() == nil {
		if filter && !s.discover.CanCheckDownloads() {
			filter, res.paused = false, true
		}
		if !filter {
			res.rows = append(res.rows, row(req.ordered[res.next], discover.Unknown))
			res.next++
			continue
		}
		if checked >= req.opts.checkLimit() {
			break
		}
		batch := req.ordered[res.next:min(len(req.ordered), res.next+discover.CheckBatch)]
		checked += len(batch)
		answers, err := s.discover.Downloads(ctx, batch)
		if err != nil {
			log.Printf("discover: look up downloads: %v", err)
		}
		for k, r := range batch {
			if len(res.rows) == want {
				break
			}
			res.next++
			if answers[k] != discover.Lacks {
				res.rows = append(res.rows, row(r, answers[k]))
			}
		}
	}
	return res
}

// markDownloads looks up which of the rows have a download and shows it on
// their cards. It stops quietly when the allowance runs low.
func (s *screen) markDownloads(ctx context.Context, generation int, rows []discoverRow) {
	for start := 0; start < len(rows) && ctx.Err() == nil && s.discover.CanCheckDownloads(); start += discover.CheckBatch {
		batch := rows[start:min(len(rows), start+discover.CheckBatch)]
		repos := make([]source.Repository, len(batch))
		for i, r := range batch {
			repos[i] = r.repo
		}
		answers, err := s.discover.Downloads(ctx, repos)
		if err != nil {
			log.Printf("discover: look up downloads: %v", err)
		}
		s.onUI(func() {
			f := &s.discoverUI.browse
			if f.generation != generation {
				return
			}
			for i, a := range answers {
				if idx := start + i; idx < len(f.rows) && f.rows[idx].repo.Ref() == repos[i].Ref() {
					f.rows[idx].answer = a
				}
			}
		})
		if err != nil {
			return
		}
	}
}

// prepareAhead downloads the other lists of the category in the background.
func (s *screen) prepareAhead(opts discoverOptions) {
	d := &s.discoverUI
	if d.preparing != nil {
		d.preparing()
	}
	d.preparing = s.cancellable(func(ctx context.Context) error {
		err := s.discover.Prepare(ctx, discover.PrepareRequest{
			Category: opts.category, Skip: opts.tab, Window: opts.window, Downloads: opts.downloads,
		})
		if err != nil && ctx.Err() == nil {
			log.Printf("discover: prepare lists: %v", err)
			s.record(ctx, activity.Warning, 0, locales.ActivityPrepareFailed(errorText(err)))
		}
		return unexpectedError(err)
	})
}

// loadMore shows the next page of whatever the page lists.
func (s *screen) loadMore() {
	d := &s.discoverUI
	switch f := d.active(); {
	case f.busy || !f.more && !s.moreStars():
		return
	case d.mode == modeSearch:
		s.loadSearchPage(d.nextPage)
	case d.mode == modeOwner:
		s.loadOwnerPage(d.nextPage)
	case d.opts.tab == discover.Stars:
		d.starsShown += starsPageSize
	default:
		s.loadMoreBrowse()
	}
}

// loadMoreBrowse takes the next rows of the browsing list.
func (s *screen) loadMoreBrowse() {
	d := &s.discoverUI
	f := &d.browse
	f.busy = true
	generation := f.generation
	req := fillRequest{ordered: d.ordered, next: d.next, shown: len(f.rows), base: d.base, opts: d.opts}
	f.cancel = s.cancellable(func(ctx context.Context) error {
		filled := s.fillRows(ctx, req)
		s.onUI(func() {
			if f.generation != generation {
				return
			}
			d.next = filled.next
			f.rows = append(f.rows, filled.rows...)
			f.more, f.busy = filled.next < len(d.ordered), false
			f.notes.paused = f.notes.paused || filled.paused
		})
		return nil
	})
}

// moreStars reports more stars than the Your stars tab shows so far.
func (s *screen) moreStars() bool {
	d := &s.discoverUI
	return d.mode == modeBrowse && d.opts.tab == discover.Stars && d.starsShown < len(d.matching)
}

// startSearch lists the results of a search typed in the top box.
func (s *screen) startSearch(text string) {
	d := &s.discoverUI
	d.mode, d.query, d.nextPage = modeSearch, text, 1
	startFeed(&d.side)
	s.navigate("discover")
	s.loadSearchPage(1)
}

// loadSearchPage adds a page of results to the search.
func (s *screen) loadSearchPage(page int) {
	d := &s.discoverUI
	f := &d.side
	f.busy = true
	generation, text := f.generation, d.query
	f.cancel = s.cancellable(func(ctx context.Context) error {
		found, err := s.discover.Search(ctx, text, page)
		return s.sideLoaded(ctx, generation, found.Repositories, len(found.Repositories) > 0, found.Total, err)
	})
	s.invalidate()
}

// openOwner lists one developer's repositories.
func (s *screen) openOwner(owner string) {
	d := &s.discoverUI
	d.mode, d.owner, d.nextPage = modeOwner, owner, 1
	startFeed(&d.side)
	s.dialogOpen = false
	s.navigate("discover")
	s.loadOwnerPage(1)
}

// loadOwnerPage adds a page of the developer's repositories.
func (s *screen) loadOwnerPage(page int) {
	d := &s.discoverUI
	f := &d.side
	f.busy = true
	generation, owner := f.generation, d.owner
	f.cancel = s.cancellable(func(ctx context.Context) error {
		found, err := s.discover.OwnerRepositories(ctx, owner, page)
		return s.sideLoaded(ctx, generation, found.Repositories, found.More, 0, err)
	})
	s.invalidate()
}

// sideLoaded adds a page to the search or owner list, or shows why it could not be loaded.
func (s *screen) sideLoaded(ctx context.Context, generation int, found []source.Repository, more bool, total int, err error) error {
	if ctx.Err() != nil {
		return nil //nolint:nilerr // The load was superseded or the application is quitting; there is nothing to report.
	}
	if err != nil {
		log.Printf("discover: %v", err)
		text := errorText(err)
		s.onUI(func() {
			if f := &s.discoverUI.side; f.generation == generation {
				f.busy, f.loaded, f.failure = false, true, text
			}
		})
		return unexpectedError(err)
	}
	s.onUI(func() {
		d := &s.discoverUI
		f := &d.side
		if f.generation != generation {
			return
		}
		for _, r := range found {
			f.rows = append(f.rows, discoverRow{repo: r})
		}
		if d.mode == modeSearch {
			f.total = total
			f.rows = f.rows[:min(len(f.rows), discover.MaxSearchResults)]
			more = more && len(f.rows) < min(total, discover.MaxSearchResults)
		}
		d.nextPage++
		f.more, f.busy, f.loaded = more, false, true
	})
	return nil
}

// leaveSide returns the page from a search or an owner to the browsing lists.
func (s *screen) leaveSide() {
	d := &s.discoverUI
	if d.side.cancel != nil {
		d.side.cancel()
	}
	d.side = discoverFeed{generation: d.side.generation + 1}
	d.mode = modeBrowse
	s.discoverEntered()
}

// setDiscoverChoice changes a choice that decides what the browsing list holds,
// and loads the list again.
func (s *screen) setDiscoverChoice(change func(p *preferences)) {
	s.prefs.Update(change)
	if d := &s.discoverUI; d.mode != modeBrowse {
		s.leaveSide()
		return
	}
	s.loadBrowse(false)
}

// saveForLater keeps a repository to try later, in the background.
func (s *screen) saveForLater(repo source.Repository) {
	name := repo.Ref().String()
	s.background(func(ctx context.Context) error {
		saved, err := s.catalog.SaveForLater(ctx, repo, "")
		switch {
		case errors.Is(err, catalog.ErrSavedAlready):
			s.toast(components.ToastInfo, locales.ToastAlreadySaved(saved.FullName()))
			return nil
		case ctx.Err() != nil:
			return fmt.Errorf("save %s for later: %w", name, ctx.Err())
		case err != nil:
			s.record(ctx, activity.Error, 0, locales.ActivityTryLaterFailed(name, err.Error()))
			s.toast(components.ToastError, locales.ActivityTryLaterFailed(name, err.Error()))
			return err //nolint:wrapcheck // The catalog says what it was saving.
		}
		s.record(ctx, activity.Info, 0, locales.ActivityTryLaterSaved(name))
		s.toast(components.ToastSuccess, locales.ToastSavedForLater(name))
		return nil
	})
}

// removeFromTryLater forgets a saved repository, in the background.
func (s *screen) removeFromTryLater(id int64, name string) {
	s.background(func(ctx context.Context) error {
		err := s.catalog.RemoveTryLater(ctx, id)
		switch {
		case ctx.Err() != nil:
			return fmt.Errorf("remove %s from Try later: %w", name, ctx.Err())
		case err != nil:
			s.record(ctx, activity.Error, 0, locales.ActivityTryLaterFailed(name, err.Error()))
			s.toast(components.ToastError, locales.ActivityTryLaterFailed(name, err.Error()))
			return err //nolint:wrapcheck // The catalog says what it was removing.
		}
		s.record(ctx, activity.Info, 0, locales.ActivityTryLaterRemoved(name))
		return nil
	})
}

// openOnGitHub opens a repository's page in the browser.
func (s *screen) openOnGitHub(ref source.RepoRef) {
	if err := s.openURL(source.RepoURL(ref)); err != nil {
		log.Printf("open %s: %v", ref, err)
	}
}

// heldSets are the repositories in the Library and in Try later, by lower-case
// owner/repo, for the cards to show.
type heldSets struct {
	library, saved map[string]bool
}

func (s *screen) held() heldSets {
	h := heldSets{library: map[string]bool{}, saved: map[string]bool{}}
	for _, e := range s.catalog.Entries().Get() {
		h.library[repoKey(e.Ref())] = true
	}
	for _, t := range s.catalog.TryLaterEntries().Get() {
		h.saved[repoKey(t.Ref())] = true
	}
	return h
}

func repoKey(ref source.RepoRef) string { return strings.ToLower(ref.String()) }

// rowsOf is the rows the page shows for the active feed.
func (s *screen) rowsOf() []discoverRow {
	d := &s.discoverUI
	if d.mode == modeBrowse && d.opts.tab == discover.Stars {
		return s.starRows()
	}
	return d.active().rows
}

// starRows are the stars passing the text filter, as far as the page has been
// extended. The matching is worked out again only when the text or the stars
// change.
func (s *screen) starRows() []discoverRow {
	d := &s.discoverUI
	if !d.matched || d.matchingFor != d.starsText {
		d.matching, d.matchingFor, d.matched, d.starsShown = nil, d.starsText, true, starsPageSize
		for _, r := range d.ordered {
			if discover.MatchesText(r, d.starsText) {
				d.matching = append(d.matching, r)
			}
		}
	}
	rows := make([]discoverRow, 0, min(len(d.matching), d.starsShown))
	for _, r := range d.matching[:min(len(d.matching), d.starsShown)] {
		rows = append(rows, discoverRow{repo: r})
	}
	return rows
}

// compactCount writes a count the way a card shows it: 950, 1.2k, 12k, 3.4M.
func compactCount(n int) string {
	switch {
	case n >= 1_000_000:
		return trimTenth(float64(n)/1_000_000) + "M"
	case n >= 1_000:
		return trimTenth(float64(n)/1_000) + "k"
	}
	return strconv.Itoa(n)
}

func trimTenth(v float64) string {
	if v >= 10 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}
