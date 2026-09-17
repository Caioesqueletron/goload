package loadtest

import (
	"context"
	"time"
)

// RateLimiter is a minimal token-bucket limiter implemented with the
// standard library only (no golang.org/x/time/rate dependency), so the
// whole project builds with zero external modules.
//
// A ticker refills tokens into a buffered channel at a fixed interval;
// workers call Wait to block until a token is available or the context
// is cancelled.
type RateLimiter struct {
	tokens chan struct{}
	ticker *time.Ticker
	stop   chan struct{}
}

// NewRateLimiter creates a limiter that allows approximately ratePerSec
// operations per second. A ratePerSec <= 0 means "unlimited" and NewRateLimiter
// returns nil, which callers must treat as "no limiting".
func NewRateLimiter(ratePerSec float64) *RateLimiter {
	if ratePerSec <= 0 {
		return nil
	}

	interval := time.Duration(float64(time.Second) / ratePerSec)
	if interval <= 0 {
		interval = time.Nanosecond
	}

	rl := &RateLimiter{
		tokens: make(chan struct{}, 1),
		ticker: time.NewTicker(interval),
		stop:   make(chan struct{}),
	}

	go rl.refill()
	return rl
}

func (rl *RateLimiter) refill() {
	for {
		select {
		case <-rl.stop:
			rl.ticker.Stop()
			return
		case <-rl.ticker.C:
			select {
			case rl.tokens <- struct{}{}:
			default:
				// bucket full, drop the tick
			}
		}
	}
}

// Wait blocks until a token is available or ctx is cancelled.
func (rl *RateLimiter) Wait(ctx context.Context) error {
	if rl == nil {
		return nil
	}
	select {
	case <-rl.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops the internal ticker goroutine. Safe to call on a nil limiter.
func (rl *RateLimiter) Close() {
	if rl == nil {
		return
	}
	close(rl.stop)
}
