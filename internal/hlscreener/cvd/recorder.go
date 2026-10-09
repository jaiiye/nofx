// Package cvd implements the CVD (cumulative volume delta) recorder.
//
// Mirrors the design from Superior Terminal's orderflow/CVD:
//   - 5-minute bucket aggregation from HL trade tape
//   - Each trade classified as aggressive buy/sell by side field
//   - Baseline anchored: pan the chart, the curve doesn't re-anchor
//   - Sub-bucket walking for OHLC of the cumulative path (intrabar absorption)
//
// The recorder is per-coin. Run one process per top-20 coin, or a single
// multi-coin process (see cmd/cvd-recorder).
//
// Two implementations:
//   - Recorder: poll HL `recentTrades` every 3s. Simple, robust.
//   - WSRecorder: persistent WebSocket subscription. Latency <200ms.
//     Recommended for production.
package cvd

import (
	"context"
	"log"
	"sync"
	"time"

	"nofx/internal/hlscreener/hyperliquid"
	"nofx/internal/hlscreener/store"
)

const (
	DefaultBucketMs = 5 * 60 * 1000
)

// bucketAcc accumulates buy/sell notional within a single time bucket.
// Exposed at package level so tests can construct and assert on it.
type bucketAcc struct {
	buy, sell float64
}

type Recorder struct {
	coin      string
	hl        *hyperliquid.Client
	store     *store.Store
	bucketMs  int64

	mu        sync.Mutex
	lastTid   int64
}

func New(coin string, hl *hyperliquid.Client, st *store.Store, bucketMs int64) *Recorder {
	if bucketMs == 0 {
		bucketMs = DefaultBucketMs
	}
	return &Recorder{
		coin:     coin,
		hl:       hl,
		store:    st,
		bucketMs: bucketMs,
	}
}

// Run loops forever, polling HL every `pollInterval`. Polling rather than WS
// keeps things simple; HL recentTrades returns up to ~2000 most-recent.
func (r *Recorder) Run(ctx context.Context, pollInterval time.Duration) error {
	t := time.NewTicker(pollInterval)
	defer t.Stop()

	if err := r.tick(ctx); err != nil {
		log.Printf("[cvd %s] initial tick err: %v", r.coin, err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := r.tick(ctx); err != nil {
				log.Printf("[cvd %s] tick err: %v", r.coin, err)
			}
		}
	}
}

func (r *Recorder) tick(ctx context.Context) error {
	trades, err := r.hl.RecentTrades(ctx, r.coin)
	if err != nil {
		return err
	}
	if len(trades) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// HL returns trades in descending time order. We want only the new ones
	// (tid > lastTid). If lastTid is 0, this is first run — accept all and
	// seed lastTid to the max.
	var maxTid int64
	for _, tr := range trades {
		if tr.Tid > maxTid {
			maxTid = tr.Tid
		}
	}

	// bucket the incoming deltas
	accs := map[int64]*bucketAcc{}
	for _, tr := range trades {
		if tr.Tid <= r.lastTid {
			continue
		}
		bucket := alignDown(tr.Time, r.bucketMs)
		a, ok := accs[bucket]
		if !ok {
			a = &bucketAcc{}
			accs[bucket] = a
		}
		notional := tr.Price() * tr.Size()
		if tr.Side == "B" { // aggressive buy
			a.buy += notional
		} else {
			a.sell += notional
		}
	}
	r.lastTid = maxTid

	// persist + recompute baseline once we cross retention
	for bucket, a := range accs {
		delta := a.buy - a.sell
		if err := r.store.UpsertCVDBar(ctx, store.CVDBar{
			Coin:   r.coin,
			Bucket: bucket,
			Buy:    a.buy,
			Sell:   a.sell,
			Delta:  delta,
		}); err != nil {
			return err
		}
	}
	return r.refreshBaseline(ctx)
}

// refreshBaseline recomputes the CVD anchor (sum of all deltas BEFORE the
// 7-day retention window). This is the Superior-style trick: the curve never
// re-anchors as the window slides.
//
// Implementation: we keep a 7-day rolling window. Anything older is dropped
// from cvd_bars (via separate retention job) and its sum is captured here.
func (r *Recorder) refreshBaseline(ctx context.Context) error {
	const retentionMs = int64(7 * 24 * 3600 * 1000)
	cutoff := time.Now().UnixMilli() - retentionMs

	sum, err := r.store.SumCVDBefore(ctx, r.coin, cutoff)
	if err != nil {
		return err
	}
	return r.store.SetCVDBaseline(ctx, r.coin, sum)
}

func alignDown(ts, stepMs int64) int64 {
	return (ts / stepMs) * stepMs
}
