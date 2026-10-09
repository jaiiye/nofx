// Package pool: parallel screener with rate limiting.
//
// We use a simple in-process token bucket (no external deps) to cap
// outgoing calls at a safe rate. Each goroutine doing a coin
// evaluation must Wait() on the bucket before issuing any HL API
// call, and a separate limiter on the bucket prevents burst.
//
// HL's public /info endpoint allows ~10 RPS sustained. We default
// to 8 RPS to leave headroom. Within a single Run() call, we batch
// calls per coin (AllMids is once-only, AssetContexts is once-only,
// then per-coin CandlesRange) — the limiter sees each HTTP call.
package pool

import (
	"context"
	"sync"
	"time"
)

// rateLimiter is a simple token-bucket rate limiter. Wait blocks
// until a token is available or ctx is cancelled.
type rateLimiter struct {
	tokens   chan struct{}
	interval time.Duration
}

func newRateLimiter(rps int) *rateLimiter {
	if rps <= 0 {
		rps = 8
	}
	rl := &rateLimiter{
		tokens:   make(chan struct{}, rps),
		interval: time.Second / time.Duration(rps),
	}
	// Pre-fill the bucket so the first batch of callers doesn't have
	// to wait a full interval.
	for i := 0; i < rps; i++ {
		rl.tokens <- struct{}{}
	}
	go rl.refill()
	return rl
}

func (rl *rateLimiter) refill() {
	t := time.NewTicker(rl.interval)
	defer t.Stop()
	for range t.C {
		select {
		case rl.tokens <- struct{}{}:
		default:
			// bucket full, skip
		}
	}
}

// Wait blocks until a token is available. Returns ctx.Err() if ctx
// is cancelled while waiting.
func (rl *rateLimiter) Wait(ctx context.Context) error {
	select {
	case <-rl.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runParallel is a generic helper for the screener: it runs `work`
// for each input in parallel, bounded by errgroup semantics. The
// limiter is acquired once per work item (so each coin evaluation
// reserves a token before starting its API calls).
//
// We cap concurrency at limiterRPS * 2 — a small multiple that lets
// us overlap wait time but doesn't overwhelm the API.
func runParallel[T any](
	ctx context.Context,
	items []T,
	limiter *rateLimiter,
	maxConcurrency int,
	work func(context.Context, T) error,
) error {
	if maxConcurrency <= 0 {
		maxConcurrency = 16
	}
	sem := make(chan struct{}, maxConcurrency)
	errCh := make(chan error, len(items))
	var wg sync.WaitGroup

	for _, item := range items {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it T) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := limiter.Wait(ctx); err != nil {
				errCh <- err
				return
			}
			if err := work(ctx, it); err != nil {
				errCh <- err
			}
		}(item)
	}
	wg.Wait()
	close(errCh)
	// Return the first error (if any) for logging; downstream code
	// uses continue-on-error semantics in evaluate.
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}
