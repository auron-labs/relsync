package catalog

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/auron-labs/mygo-framework/jobs"
)

// CheckJob is the name of the job that checks one Item. Its Running state is
// the Item's busy state.
func CheckJob(id int64) string { return "check:" + strconv.FormatInt(id, 10) }

// Checker runs checks as jobs: one job per Item, a few at a time.
type Checker struct {
	catalog *Catalog
	jobs    *jobs.Manager
}

// NewChecker returns a Checker that runs its jobs on m.
func NewChecker(c *Catalog, m *jobs.Manager) *Checker { return &Checker{catalog: c, jobs: m} }

// checkOutcome is how one Item's check job ended.
type checkOutcome struct {
	result CheckResult
	err    error
}

// start begins the job checking one Item. The job also stops when caller is
// canceled, so canceling a full check stops the checks it started. It returns
// jobs.ErrRunning when that Item is already being checked. The channel receives
// the outcome once.
func (k *Checker) start(caller context.Context, id int64, force bool) (<-chan checkOutcome, error) {
	out := make(chan checkOutcome, 1)
	err := k.jobs.Start(CheckJob(id), func(ctx context.Context, _ jobs.Report) (err error) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(caller, cancel)()
		var o checkOutcome
		// Sent even when the check panics, so a waiting caller is never stuck.
		defer func() { o.err = err; out <- o }()
		o.result, err = k.catalog.Check(ctx, id, force)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("check item %d: %w", id, err)
	}
	return out, nil
}

// CheckItem checks one Item in the background. after runs inside the job, with
// the job's context and the outcome, so the Item stays busy until it returns.
// It returns jobs.ErrRunning when the Item is already being checked.
func (k *Checker) CheckItem(id int64, force bool, after func(context.Context, CheckResult, error)) error {
	err := k.jobs.Start(CheckJob(id), func(ctx context.Context, _ jobs.Report) error {
		result, err := k.catalog.Check(ctx, id, force)
		after(ctx, result, err)
		return err
	})
	if err != nil {
		return fmt.Errorf("check item %d: %w", id, err)
	}
	return nil
}

// Summary is the outcome of a full check.
type Summary struct {
	// Total is how many Items were in the Library.
	Total int
	// Checked is how many were asked about, successfully or not.
	Checked int
	// Failed is how many of those the site could not answer.
	Failed int
	// Skipped is how many were not checked: hidden, busy, or after a rate limit.
	Skipped int
	// Limit is the failure that stopped the check for lack of request
	// allowance, nil when none did.
	Limit error
}

// RateLimited reports that the request allowance stopped the check.
func (s Summary) RateLimited() bool { return s.Limit != nil }

// CheckAll checks every Item with at most parallel checks at once, skipping
// hidden Items and Items that are already being checked. When any check is rate
// limited the rest are skipped. It blocks until the checks end and returns the
// context's error if it was canceled.
func (k *Checker) CheckAll(ctx context.Context, force bool, parallel int, report jobs.Report) (Summary, error) {
	all := k.catalog.Entries().Get()
	sum := Summary{Total: len(all)}
	entries := slices.DeleteFunc(slices.Clone(all), func(e Entry) bool { return e.Item.Hidden })
	sum.Skipped = len(all) - len(entries)
	return k.checkEntries(ctx, force, parallel, report, sum, entries)
}

// CheckItems is CheckAll for the Items with the given ids only: the way an Item
// that arrived from another device is checked straight away. An id that is no
// longer in the Library is left out.
func (k *Checker) CheckItems(ctx context.Context, ids []int64, parallel int, report jobs.Report) (Summary, error) {
	var entries []Entry
	for _, id := range ids {
		if e, ok := k.catalog.Get(id); ok && !e.Item.Hidden {
			entries = append(entries, e)
		}
	}
	return k.checkEntries(ctx, false, parallel, report, Summary{Total: len(ids), Skipped: len(ids) - len(entries)}, entries)
}

// checkEntries checks the entries, with the counts of sum so far.
func (k *Checker) checkEntries(ctx context.Context, force bool, parallel int, report jobs.Report, sum Summary, entries []Entry) (Summary, error) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
		limited  atomic.Bool
		finished int
	)
	slots := make(chan struct{}, max(1, parallel))
	record := func(o checkOutcome) {
		mu.Lock()
		defer mu.Unlock()
		finished++
		switch {
		case o.err != nil:
			firstErr = o.err
		case o.result.Failure != nil:
			sum.Checked++
			sum.Failed++
			if o.result.RateLimited() {
				limited.Store(true)
				sum.Limit = o.result.Failure
			}
		default:
			sum.Checked++
		}
		report(finished, len(entries), "")
	}
	skip := func() {
		mu.Lock()
		sum.Skipped++
		mu.Unlock()
	}
	report(0, len(entries), "")
loop:
	for _, e := range entries {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		if limited.Load() {
			<-slots
			skip()
			continue
		}
		out, err := k.start(ctx, e.Item.ID, force)
		if err != nil {
			<-slots
			skip()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := <-out
			// Recorded before the slot is freed, so the loop sees a rate limit
			// before it starts another Item.
			record(o)
			<-slots
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if err := ctx.Err(); err != nil {
		return sum, fmt.Errorf("check all: %w", err)
	}
	if firstErr != nil {
		return sum, fmt.Errorf("check all: %w", firstErr)
	}
	return sum, nil
}
