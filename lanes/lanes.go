// Package lanes limits how many jobs of a kind run at once and queues the
// rest. Jobs are ordinary jobs of the framework's manager, so cancellation,
// panic reporting and shutdown behave as they do for any job; a lane only
// decides when a started job begins its work.
package lanes

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/auron-labs/mygo-framework/jobs"
)

// Lanes runs jobs on a jobs.Manager under per-lane concurrency limits. It is
// safe for concurrent use.
type Lanes struct {
	jobs *jobs.Manager

	mu    sync.Mutex
	lanes map[string]*lane
	// tickets holds a ticket for every job that has been started and has not
	// finished, by job name.
	tickets map[string]*ticket
}

// lane admits at most limit jobs at once, in the order they were started.
type lane struct {
	limit   int
	running int
	waiting []*ticket
}

// ticket is a job's place in its lane. It is taken when the job is started,
// not when its goroutine happens to run, so the queue keeps start order.
type ticket struct {
	lane    *lane
	granted bool
	ready   chan struct{}
}

// New returns Lanes over m. limits maps each lane's name to how many of its
// jobs may run at once; a lane absent from it runs one at a time.
func New(m *jobs.Manager, limits map[string]int) *Lanes {
	l := &Lanes{jobs: m, lanes: map[string]*lane{}, tickets: map[string]*ticket{}}
	for name, limit := range limits {
		l.lanes[name] = &lane{limit: max(1, limit)}
	}
	return l
}

// Start starts the job name in the named lane. The job is Running from the
// start, but fn is not called until the lane has room; until then Queued
// reports true. When the job begins its work it reports progress once with an
// empty message, so subscribers of the manager hear of the change. It returns
// jobs.ErrRunning when a job of that name has not finished.
func (l *Lanes) Start(laneName, name string, fn jobs.Func) error {
	t, err := l.take(laneName, name)
	if err != nil {
		return err
	}
	err = l.jobs.Start(name, func(ctx context.Context, report jobs.Report) error {
		defer l.finish(name, t)
		select {
		case <-t.ready:
		case <-ctx.Done():
			return fmt.Errorf("wait in lane %s: %w", laneName, ctx.Err())
		}
		report(0, 0, "")
		return fn(ctx, report)
	})
	if err != nil {
		l.finish(name, t)
		return fmt.Errorf("start %s in lane %s: %w", name, laneName, err)
	}
	return nil
}

// Queued reports whether the job is waiting for room in its lane.
func (l *Lanes) Queued(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.tickets[name]
	return ok && !t.granted
}

// take puts a new job at the back of the lane.
func (l *Lanes) take(laneName, name string) (*ticket, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.tickets[name]; ok {
		return nil, fmt.Errorf("start %s in lane %s: %w", name, laneName, jobs.ErrRunning)
	}
	ln, ok := l.lanes[laneName]
	if !ok {
		ln = &lane{limit: 1}
		l.lanes[laneName] = ln
	}
	t := &ticket{lane: ln, ready: make(chan struct{})}
	if ln.running < ln.limit && len(ln.waiting) == 0 {
		ln.running++
		t.granted = true
		close(t.ready)
	} else {
		ln.waiting = append(ln.waiting, t)
	}
	l.tickets[name] = t
	return t, nil
}

// finish gives up t's place, whether it is waiting or holds a slot, and hands
// a slot it held to the longest waiting job. Calling it again does nothing.
func (l *Lanes) finish(name string, t *ticket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tickets[name] != t {
		return
	}
	delete(l.tickets, name)
	ln := t.lane
	if !t.granted {
		ln.waiting = slices.DeleteFunc(ln.waiting, func(w *ticket) bool { return w == t })
		return
	}
	if len(ln.waiting) == 0 {
		ln.running--
		return
	}
	next := ln.waiting[0]
	ln.waiting = ln.waiting[1:]
	next.granted = true
	close(next.ready)
}
