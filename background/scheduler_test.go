package background

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeClock is a clock that moves only when told to.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() {
		c.mu.Lock()
		t.stopped = true
		c.mu.Unlock()
	}
}

// Advance moves time forward, calling the timers that come due on the way in
// the order they do.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.stopped = true
		c.now = laterOf(c.now, next.at)
		c.mu.Unlock()
		next.f()
	}
}

// Jump moves time forward without calling any timer, as a computer asleep does:
// its timers did not run.
func (c *fakeClock) Jump(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// task is a scheduled task that, like a check, records when it ended.
type task struct {
	clock *fakeClock
	mu    sync.Mutex
	runs  []time.Time
	// last is when the task last completed; unset until a run completes.
	last time.Time
	// finishes says whether a run records its end; a check that fails does not.
	finishes bool
}

func (k *task) run() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.runs = append(k.runs, k.clock.Now())
	if k.finishes {
		k.last = k.clock.Now()
	}
}

func (k *task) lastRun() (time.Time, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.last, !k.last.IsZero()
}

func (k *task) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.runs)
}

func newTask(finishes bool) (*fakeClock, *task, *Scheduler) {
	clock := newFakeClock()
	k := &task{clock: clock, finishes: finishes}
	return clock, k, NewScheduler(clock, k.lastRun, k.run)
}

func TestRunsEveryInterval(t *testing.T) {
	clock, k, s := newTask(true)
	s.SetInterval(time.Hour)
	s.Start(5*time.Second, true)
	clock.Advance(5*time.Second + 3*time.Hour + 30*time.Minute)
	want := []time.Time{
		clock.now.Add(-3*time.Hour - 30*time.Minute),
		clock.now.Add(-2*time.Hour - 30*time.Minute),
		clock.now.Add(-time.Hour - 30*time.Minute),
		clock.now.Add(-30 * time.Minute),
	}
	if !slices.Equal(k.runs, want) {
		t.Fatalf("runs = %v, want %v", k.runs, want)
	}
}

func TestStartingChecksAtOnceOnlyWhenAskedTo(t *testing.T) {
	for _, checkNow := range []bool{true, false} {
		clock, k, s := newTask(true)
		s.SetInterval(6 * time.Hour)
		// The last check was long ago, so the task is overdue as the application starts.
		k.last = clock.now.Add(-48 * time.Hour)
		s.Start(5*time.Second, checkNow)
		clock.Advance(10 * time.Second)
		if got := k.count(); got != btoi(checkNow) {
			t.Errorf("checkNow %v: runs after start = %d", checkNow, got)
		}
		if !checkNow {
			// Starting counted as the last run, so the first scheduled one is an interval later.
			clock.Advance(6 * time.Hour)
			if got := k.count(); got != 1 {
				t.Errorf("runs an interval after start = %d, want 1", got)
			}
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestManualOnlyNeverSchedules(t *testing.T) {
	clock, k, s := newTask(true)
	s.SetInterval(0)
	s.Start(time.Second, false)
	clock.Advance(30 * 24 * time.Hour)
	s.Wake()
	s.SetOnline(true)
	if got := k.count(); got != 0 {
		t.Fatalf("runs = %d, want none", got)
	}
	// A check at startup is the user's other setting, and still happens.
	s2clock, k2, s2 := newTask(true)
	s2.Start(time.Second, true)
	s2clock.Advance(time.Minute)
	if got := k2.count(); got != 1 {
		t.Errorf("startup check runs = %d, want 1", got)
	}
}

func TestACheckThatIsOverdueAfterSleepRunsOnWake(t *testing.T) {
	clock, k, s := newTask(true)
	s.SetInterval(time.Hour)
	s.Start(time.Second, true)
	clock.Advance(time.Minute)
	if k.count() != 1 {
		t.Fatalf("startup runs = %d", k.count())
	}
	// Eight hours pass while the computer sleeps; the timers did not run.
	clock.Jump(8 * time.Hour)
	s.Wake()
	if got := k.count(); got != 2 {
		t.Fatalf("runs after wake = %d, want 1 more", got)
	}
	clock.Advance(59 * time.Minute)
	if got := k.count(); got != 2 {
		t.Errorf("runs before the next interval = %d, want no more", got)
	}
}

func TestACheckThatIsOverdueAfterSleepRunsWithoutAWakeEventWithinAMinute(t *testing.T) {
	clock, k, s := newTask(true)
	s.SetInterval(time.Hour)
	s.Start(time.Second, true)
	clock.Advance(time.Minute)
	clock.Jump(8 * time.Hour)
	clock.Advance(maxWait)
	if got := k.count(); got != 2 {
		t.Errorf("runs = %d, want the overdue one", got)
	}
}

func TestACheckDueOfflineRunsWhenTheNetworkReturns(t *testing.T) {
	clock, k, s := newTask(true)
	s.SetInterval(time.Hour)
	s.SetOnline(false)
	s.Start(time.Second, true)
	clock.Advance(3 * time.Hour)
	if got := k.count(); got != 0 {
		t.Fatalf("runs while offline = %d", got)
	}
	s.SetOnline(true)
	if got := k.count(); got != 1 {
		t.Errorf("runs after the network returned = %d, want 1", got)
	}
}

func TestACheckThatDidNotFinishIsNotStartedAgainBeforeTheNextInterval(t *testing.T) {
	clock, k, s := newTask(false)
	s.SetInterval(time.Hour)
	s.Start(time.Second, true)
	clock.Advance(time.Minute)
	for range 5 {
		s.Wake()
		clock.Advance(10 * time.Minute)
	}
	if got := k.count(); got != 1 {
		t.Fatalf("runs = %d, want 1 until the interval passes", got)
	}
	clock.Advance(time.Hour)
	if got := k.count(); got != 2 {
		t.Errorf("runs after the interval = %d, want 2", got)
	}
}

func TestChangingTheIntervalTakesEffectAtOnce(t *testing.T) {
	clock, k, s := newTask(true)
	s.SetInterval(0)
	s.Start(time.Second, false)
	clock.Advance(2 * time.Hour)
	k.mu.Lock()
	k.last = clock.now.Add(-2 * time.Hour)
	k.mu.Unlock()
	// Two hours since the last check is overdue for an hourly schedule.
	s.SetInterval(time.Hour)
	if got := k.count(); got != 1 {
		t.Fatalf("runs after choosing hourly = %d, want 1", got)
	}
	s.SetInterval(0)
	clock.Advance(5 * time.Hour)
	if got := k.count(); got != 1 {
		t.Errorf("runs after choosing manual = %d, want no more", got)
	}
}

func TestAClockSetBackDoesNotPostponeTheCheckByTheDifference(t *testing.T) {
	clock, k, s := newTask(true)
	s.SetInterval(time.Hour)
	s.Start(time.Second, false)
	k.mu.Lock()
	k.last = clock.now.Add(24 * time.Hour) // Recorded while the clock was ahead.
	k.mu.Unlock()
	clock.Advance(time.Hour + time.Minute)
	if got := k.count(); got != 1 {
		t.Errorf("runs = %d, want one an interval after the clock was corrected", got)
	}
}

func TestStoppingEndsScheduling(t *testing.T) {
	clock, k, s := newTask(true)
	s.SetInterval(time.Hour)
	s.Start(time.Second, true)
	clock.Advance(time.Minute)
	s.Stop()
	clock.Advance(10 * time.Hour)
	s.Wake()
	if got := k.count(); got != 1 {
		t.Errorf("runs = %d, want none after Stop", got)
	}
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func TestAFailedRunIsRetriedSoonerThanAnInterval(t *testing.T) {
	clock, k, s := newTask(false) // The task never completes, as a backup to an unreachable folder.
	s.SetInterval(7 * 24 * time.Hour)
	s.Start(time.Second, true)
	clock.Advance(2 * time.Second)
	if k.count() != 1 {
		t.Fatalf("runs = %d, want the first attempt", k.count())
	}
	s.RetryIn(time.Hour)
	clock.Advance(59 * time.Minute)
	if k.count() != 1 {
		t.Fatalf("retried after %d runs, too soon", k.count())
	}
	clock.Advance(2 * time.Minute)
	if k.count() != 2 {
		t.Fatalf("runs = %d, want a retry an hour after the failure", k.count())
	}
}

func TestRetryingNeverWaitsLongerThanTheInterval(t *testing.T) {
	clock, k, s := newTask(false)
	s.SetInterval(6 * time.Hour)
	s.Start(time.Second, true)
	clock.Advance(2 * time.Second)
	s.RetryIn(24 * time.Hour)
	clock.Advance(5*time.Hour + 59*time.Minute)
	if k.count() != 1 {
		t.Fatalf("runs = %d before the interval ended", k.count())
	}
	clock.Advance(2 * time.Minute)
	if k.count() != 2 {
		t.Fatalf("runs = %d, want the next one at the interval", k.count())
	}
}

func TestBackupIntervals(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"6h": 6 * time.Hour, "12h": 12 * time.Hour, "1d": 24 * time.Hour, "3d": 72 * time.Hour,
		"7d": 7 * 24 * time.Hour, "14d": 14 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, BackupOff: 0, "bogus": 0,
	} {
		if got := BackupEvery(value); got != want {
			t.Errorf("BackupEvery(%q) = %v, want %v", value, got, want)
		}
	}
}
