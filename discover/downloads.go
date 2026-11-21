package discover

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

const (
	// answerFor is how long a remembered answer is used.
	answerFor = 3 * 24 * time.Hour
	// CheckBatch is how many projects are looked up at once.
	CheckBatch = 14
	// preparedChecks is how many projects of each prepared list are looked up.
	preparedChecks = 48
	// pollEvery and polls bound how long background work waits for searches
	// to be free: 90 seconds in all.
	pollEvery = 1500 * time.Millisecond
	polls     = 60
)

// Answer is whether a project has a download for this machine.
type Answer int

// The answers. Unknown is a project that could not be looked up: it is not
// remembered, and a list keeps it.
const (
	Unknown Answer = iota
	Has
	Lacks
)

// Downloads looks up which of the repositories have a download for this
// machine, in the order given. An answer remembered for under three days is
// used; the rest cost one request each, CheckBatch at a time, and only an answer
// the site gave is remembered. The answers are valid whatever the error, which
// is the first reason any stayed Unknown.
func (s *Service) Downloads(ctx context.Context, repos []source.Repository) ([]Answer, error) {
	policy := s.policy()
	machine := policy.Target.OS + "/" + policy.Target.Arch
	now := s.now()
	remembered, err := s.store.answers(ctx, machine, repos, now.Add(-answerFor))
	if err != nil {
		return make([]Answer, len(repos)), err
	}
	answers := make([]Answer, len(repos))
	fresh := map[string]bool{}
	failures := make([]error, len(repos))
	var mu sync.Mutex
	slots := make(chan struct{}, CheckBatch)
	var wg sync.WaitGroup
	for i, r := range repos {
		if has, ok := remembered[checkKey(r)]; ok {
			answers[i] = answerOf(has)
			continue
		}
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			has, err := s.lookUp(ctx, r, policy)
			if err != nil {
				failures[i] = err
				return
			}
			answers[i] = answerOf(has)
			mu.Lock()
			fresh[checkKey(r)] = has
			mu.Unlock()
		})
	}
	wg.Wait()
	failure := errors.Join(failures...)
	if len(fresh) > 0 {
		if err := s.store.saveAnswers(ctx, machine, fresh, repos, now); err != nil {
			failure = errors.Join(failure, err)
		}
	}
	return answers, failure
}

func answerOf(has bool) Answer {
	if has {
		return Has
	}
	return Lacks
}

// lookUp asks the site for a repository's newest releases. Anything but a
// successful answer is an error, so it is never taken for "no download".
func (s *Service) lookUp(ctx context.Context, r source.Repository, policy selection.Policy) (bool, error) {
	page, err := s.client.Releases(ctx, r.Ref(), "")
	if err != nil {
		return false, fmt.Errorf("check downloads of %s: %w", r.Ref(), err)
	}
	releases := make([][]selection.Asset, len(page.Releases))
	for i, rel := range page.Releases {
		for _, a := range rel.Assets {
			releases[i] = append(releases[i], selection.Asset{Name: a.Name, Size: a.Size})
		}
	}
	return selection.HasDownload(releases, r.Name, policy), nil
}

// PrepareRequest says what to get ready ahead of the user asking for it.
type PrepareRequest struct {
	Category string
	// Skip is the tab on show, which is ready already.
	Skip Tab
	// Window picks the ranking the lists are ordered against.
	Window Window
	// Downloads also looks up which of each list's top projects have a
	// download for this machine.
	Downloads bool
}

// Prepare downloads the other browsing lists of a category, and optionally
// their downloads, while the user looks at one. It stops when the allowance
// falls below what update checks need, and lets the user's own searches go
// first. A list that fails does not stop the others; the failures are joined.
func (s *Service) Prepare(ctx context.Context, p PrepareRequest) error {
	var failures []error
	for _, tab := range []Tab{Trending, Popular, New, Updated} {
		if tab == p.Skip {
			continue
		}
		if !s.CanPrepare() || !s.waitForSearches(ctx) {
			break
		}
		req := Request{Tab: tab, Category: p.Category}
		list, err := s.List(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("prepare the %s list: %w", tab, ctx.Err())
			}
			failures = append(failures, fmt.Errorf("prepare the %s list: %w", tab, err))
			continue
		}
		if !p.Downloads {
			continue
		}
		base, err := s.Baseline(ctx, req, p.Window)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		ordered := Order(list.Repositories, tab, base, s.now())
		if _, err := s.Downloads(ctx, ordered[:min(len(ordered), preparedChecks)]); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// waitForSearches waits until enough searches are free for background work to
// take one, and reports whether they are.
func (s *Service) waitForSearches(ctx context.Context) bool {
	for range polls {
		if s.bucket.Available() >= prepareSearches {
			return true
		}
		if s.bucket.sleep(ctx, pollEvery) != nil {
			return false
		}
	}
	return s.bucket.Available() >= prepareSearches
}
