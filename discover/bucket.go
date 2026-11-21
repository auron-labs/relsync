package discover

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// GitHub allows about 30 searches a minute with a token and 10 without. A
// small bucket lets a whole list, a handful of searches, go out at once and
// paces the rest so none is refused.
const (
	capacityWithToken    = 16.0
	refillWithToken      = 0.45 // tokens a second
	capacityWithoutToken = 6.0
	refillWithoutToken   = 0.15
	// longestSleep bounds one wait, so a change of token is noticed.
	longestSleep = 5 * time.Second
)

// Bucket paces searches. It is safe for concurrent use.
type Bucket struct {
	hasToken func() bool
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error

	mu     sync.Mutex
	tokens float64
	last   time.Time
	// started is false until the first use, when the bucket is full.
	started bool
}

// NewBucket returns a full bucket whose rate follows hasToken.
func NewBucket(hasToken func() bool, now func() time.Time) *Bucket {
	return &Bucket{hasToken: hasToken, now: now, sleep: sleepFor}
}

func sleepFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for a search: %w", ctx.Err())
	}
}

func (b *Bucket) rates() (capacity, refill float64) {
	if b.hasToken() {
		return capacityWithToken, refillWithToken
	}
	return capacityWithoutToken, refillWithoutToken
}

// refill adds the tokens earned since the last call. The caller holds mu.
func (b *Bucket) refill() (refillPerSecond float64) {
	capacity, perSecond := b.rates()
	now := b.now()
	if !b.started {
		b.tokens, b.started = capacity, true
	}
	b.tokens = min(capacity, b.tokens+now.Sub(b.last).Seconds()*perSecond)
	b.last = now
	return perSecond
}

// Available is how many searches could go out now without waiting.
func (b *Bucket) Available() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	return b.tokens
}

// Wait takes a token, blocking until one is available or ctx ends.
func (b *Bucket) Wait(ctx context.Context) error {
	for {
		b.mu.Lock()
		perSecond := b.refill()
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}
		// Rounded up, so the wait never ends a hair short of a whole token.
		wait := time.Duration(math.Ceil((1 - b.tokens) / perSecond * float64(time.Second)))
		b.mu.Unlock()
		if err := b.sleep(ctx, min(wait, longestSleep)); err != nil {
			return err
		}
	}
}
