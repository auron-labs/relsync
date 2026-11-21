package lanes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/lanes"
)

// gate is a job body that reports when it starts and finishes when released.
type gate struct {
	started chan string
	release map[string]chan struct{}
}

func newGate(names ...string) *gate {
	g := &gate{started: make(chan string, len(names)), release: map[string]chan struct{}{}}
	for _, n := range names {
		g.release[n] = make(chan struct{})
	}
	return g
}

func (g *gate) job(name string) jobs.Func {
	return func(ctx context.Context, _ jobs.Report) error {
		g.started <- name
		select {
		case <-g.release[name]:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func receive(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a job to start")
		return ""
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestALaneRunsJobsInOrderUpToItsLimit(t *testing.T) {
	m := jobs.New(t.Context())
	l := lanes.New(m, map[string]int{"apply": 1})
	g := newGate("a", "b", "c")
	for _, name := range []string{"a", "b", "c"} {
		if err := l.Start("apply", name, g.job(name)); err != nil {
			t.Fatal(err)
		}
	}
	if got := receive(t, g.started); got != "a" {
		t.Fatalf("first to run = %s", got)
	}
	if l.Queued("a") || !l.Queued("b") || !l.Queued("c") {
		t.Fatalf("queued: a=%v b=%v c=%v", l.Queued("a"), l.Queued("b"), l.Queued("c"))
	}
	close(g.release["a"])
	if got := receive(t, g.started); got != "b" {
		t.Fatalf("second to run = %s", got)
	}
	close(g.release["b"])
	if got := receive(t, g.started); got != "c" {
		t.Fatalf("third to run = %s", got)
	}
	close(g.release["c"])
	waitFor(t, "c to finish", func() bool { s, _ := m.Status("c"); return s.State == jobs.Done })
}

func TestALaneAdmitsItsLimitAtOnce(t *testing.T) {
	m := jobs.New(t.Context())
	l := lanes.New(m, map[string]int{"download": 3})
	g := newGate("a", "b", "c", "d")
	for _, name := range []string{"a", "b", "c", "d"} {
		if err := l.Start("download", name, g.job(name)); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		receive(t, g.started)
	}
	waitFor(t, "d to queue", func() bool { return l.Queued("d") })
	select {
	case name := <-g.started:
		t.Fatalf("%s ran past the limit", name)
	case <-time.After(30 * time.Millisecond):
	}
	close(g.release["a"])
	if got := receive(t, g.started); got != "d" {
		t.Fatalf("started = %s", got)
	}
}

func TestLanesDoNotBlockEachOther(t *testing.T) {
	m := jobs.New(t.Context())
	l := lanes.New(m, map[string]int{"apply": 1, "download": 1})
	g := newGate("install", "fetch")
	if err := l.Start("apply", "install", g.job("install")); err != nil {
		t.Fatal(err)
	}
	if err := l.Start("download", "fetch", g.job("fetch")); err != nil {
		t.Fatal(err)
	}
	receive(t, g.started)
	receive(t, g.started)
}

func TestACanceledQueuedJobNeverRunsAndFreesTheQueue(t *testing.T) {
	m := jobs.New(t.Context())
	l := lanes.New(m, map[string]int{"apply": 1})
	g := newGate("a", "b", "c")
	for _, name := range []string{"a", "b", "c"} {
		if err := l.Start("apply", name, g.job(name)); err != nil {
			t.Fatal(err)
		}
	}
	receive(t, g.started)
	waitFor(t, "b and c to queue", func() bool { return l.Queued("b") && l.Queued("c") })
	if !m.Cancel("b") {
		t.Fatal("queued job was not found")
	}
	waitFor(t, "b to be canceled", func() bool { s, _ := m.Status("b"); return s.State == jobs.Canceled })
	if l.Queued("b") {
		t.Error("a canceled job is still queued")
	}
	close(g.release["a"])
	if got := receive(t, g.started); got != "c" {
		t.Fatalf("after cancel the next to run = %s", got)
	}
}

func TestStartingARunningNameIsRefusedWithoutDisturbingTheQueue(t *testing.T) {
	m := jobs.New(t.Context())
	l := lanes.New(m, map[string]int{"apply": 1})
	g := newGate("a", "b")
	for _, name := range []string{"a", "b"} {
		if err := l.Start("apply", name, g.job(name)); err != nil {
			t.Fatal(err)
		}
	}
	receive(t, g.started)
	waitFor(t, "b to queue", func() bool { return l.Queued("b") })
	err := l.Start("apply", "b", g.job("b"))
	if !errors.Is(err, jobs.ErrRunning) {
		t.Fatalf("second start = %v", err)
	}
	if !l.Queued("b") {
		t.Error("the refused start cleared the waiting job's queued state")
	}
}

func TestShutdownStopsQueuedJobs(t *testing.T) {
	m := jobs.New(t.Context())
	l := lanes.New(m, map[string]int{"apply": 1})
	g := newGate("a", "b")
	if err := l.Start("apply", "a", g.job("a")); err != nil {
		t.Fatal(err)
	}
	if err := l.Start("apply", "b", g.job("b")); err != nil {
		t.Fatal(err)
	}
	receive(t, g.started)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if l.Queued("b") {
		t.Error("b is still queued after shutdown")
	}
}
