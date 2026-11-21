package scan

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// The time between searches that keeps within GitHub's allowance of searches:
// 30 a minute with a token, 10 without.
const (
	gapWithToken    = 2300 * time.Millisecond
	gapWithoutToken = 6600 * time.Millisecond
)

// SearchGap is how long to leave between searches.
func SearchGap(hasToken bool) time.Duration {
	if hasToken {
		return gapWithToken
	}
	return gapWithoutToken
}

// Pacer spaces requests at least gap apart, however many goroutines ask.
type Pacer struct {
	gap time.Duration

	mu   sync.Mutex
	next time.Time
}

// NewPacer returns a Pacer that lets one request through every gap.
func NewPacer(gap time.Duration) *Pacer { return &Pacer{gap: gap} }

// Wait blocks until the caller's turn, or fails when ctx ends first.
func (p *Pacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	now := time.Now()
	at := now
	if p.next.After(now) {
		at = p.next
	}
	p.next = at.Add(p.gap)
	p.mu.Unlock()
	if wait := at.Sub(now); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return fmt.Errorf("wait for a turn: %w", ctx.Err())
		}
	}
	return nil
}
