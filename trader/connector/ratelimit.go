package connector

import (
	"context"
	"sync"
	"time"
)

// Limiter is a token-bucket rate limiter safe for concurrent use. Adapters use
// it to avoid tripping exchange rate limits when several goroutines (trading
// loop, order sync) share one account.
type Limiter struct {
	mu       sync.Mutex
	rate     float64 // tokens added per second
	capacity float64 // bucket size (max burst)
	tokens   float64
	last     time.Time
}

// NewLimiter builds a limiter allowing ratePerSec requests per second with a
// burst of burst. Non-positive arguments fall back to 1.
func NewLimiter(ratePerSec float64, burst int) *Limiter {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &Limiter{
		rate:     ratePerSec,
		capacity: float64(burst),
		tokens:   float64(burst),
		last:     time.Now(),
	}
}

// Wait blocks until a token is available or ctx is done.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		l.tokens = min(l.capacity, l.tokens+now.Sub(l.last).Seconds()*l.rate)
		l.last = now
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		need := (1 - l.tokens) / l.rate
		l.mu.Unlock()

		wait := time.Duration(need * float64(time.Second))
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
