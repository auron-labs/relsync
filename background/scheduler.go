// Package background holds what keeps the application useful while its window
// is closed: the update schedule, the decision about which updates may be
// installed with nobody watching, which versions the user has been told about,
// and the desktop features (tray icon, notifications, start at login) the
// application reaches through the Desktop interface.
package background

import (
	"sync"
	"time"
)

// maxWait bounds how long the scheduler sleeps between looking at the clock.
// A timer does not count the time a computer spends asleep, so the wall clock
// is looked at again at least this often.
const maxWait = time.Minute

// Clock is the time the Scheduler reads and waits on.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f once after d. The returned function cancels the call
	// if it has not happened.
	AfterFunc(d time.Duration, f func()) (stop func())
}

type systemClock struct{}

// SystemClock is the real time.
func SystemClock() Clock { return systemClock{} }

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) func() {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// Scheduler runs a task every interval. When the task last ran is not its own
// to keep: lastRun reports it from wherever the caller persists it, so a task
// that is overdue at start, or after the computer slept, runs at once.
type Scheduler struct {
	clock   Clock
	lastRun func() (time.Time, bool)
	run     func()

	mu       sync.Mutex
	interval time.Duration
	online   bool
	active   bool
	// started is when the scheduler last started the task itself. The task
	// records its own end, which can be later or never (a failed check), so
	// this keeps a task that did not finish from being started again at once.
	started time.Time
	// force makes the first look at the clock run the task.
	force bool
	stop  func()
}

// NewScheduler returns a stopped Scheduler that calls run, which must not
// block, whenever the task is due. lastRun reports when the task last
// completed, and whether it ever did.
func NewScheduler(clock Clock, lastRun func() (time.Time, bool), run func()) *Scheduler {
	return &Scheduler{clock: clock, lastRun: lastRun, run: run, online: true}
}

// Start begins scheduling after delay, which lets the application settle
// before the first check. Nothing runs before then. With checkNow the task runs
// then even if it is not due; without it, starting counts as the last run, so
// the first scheduled one is a whole interval away. A task that is overdue
// after the computer wakes or the network returns runs either way.
func (s *Scheduler) Start(delay time.Duration, checkNow bool) {
	s.mu.Lock()
	s.active = true
	s.force = checkNow
	if !checkNow {
		s.started = s.clock.Now()
	}
	s.arm(delay)
	s.mu.Unlock()
}

// Stop ends scheduling. A task that is running is left to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	s.active = false
	s.cancel()
	s.mu.Unlock()
}

// SetInterval changes how often the task runs; zero or less never schedules it.
func (s *Scheduler) SetInterval(d time.Duration) {
	s.mu.Lock()
	s.interval = d
	s.mu.Unlock()
	s.Wake()
}

// RetryIn makes the task due again after d, rather than a whole interval after
// it last started, for a run that failed and is worth trying again sooner. A
// d at or past the interval changes nothing.
func (s *Scheduler) RetryIn(d time.Duration) {
	s.mu.Lock()
	if d < s.interval {
		s.started = s.clock.Now().Add(d - s.interval)
		s.rearm()
	}
	s.mu.Unlock()
}

// SetOnline says whether the network is reachable. A task that comes due while
// offline waits for it to return.
func (s *Scheduler) SetOnline(online bool) {
	s.mu.Lock()
	s.online = online
	s.mu.Unlock()
	s.Wake()
}

// Wake looks at the clock now, as after the computer wakes from sleep or the
// network returns, and runs the task if it is overdue.
func (s *Scheduler) Wake() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	wait, scheduled := s.untilDue()
	fire := scheduled && wait <= 0
	if s.force && s.online {
		fire, s.force = true, false
	}
	if fire {
		s.started = s.clock.Now()
	}
	s.rearm()
	s.mu.Unlock()
	if fire {
		s.run()
	}
}

// untilDue is how long until the task is due, zero or less when it is overdue.
// It reports false when the task is not scheduled: manual only, or offline.
// The caller holds mu.
func (s *Scheduler) untilDue() (time.Duration, bool) {
	if s.interval <= 0 || !s.online {
		return 0, false
	}
	now := s.clock.Now()
	last := s.started
	// A last run in the future was recorded while the clock was set ahead of
	// where it is now; waiting out the difference could take days.
	if done, ok := s.lastRun(); ok && done.After(last) && !done.After(now) {
		last = done
	}
	if last.IsZero() {
		return 0, true
	}
	return last.Add(s.interval).Sub(now), true
}

// rearm schedules the next look at the clock: when the task is next due, but
// no later than maxWait. The caller holds mu.
func (s *Scheduler) rearm() {
	if wait, scheduled := s.untilDue(); scheduled {
		s.arm(max(wait, 0))
		return
	}
	s.cancel()
}

// arm schedules Wake after wait, at most maxWait away. The caller holds mu.
func (s *Scheduler) arm(wait time.Duration) {
	s.cancel()
	if s.active {
		s.stop = s.clock.AfterFunc(min(wait, maxWait), s.Wake)
	}
}

func (s *Scheduler) cancel() {
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
}
