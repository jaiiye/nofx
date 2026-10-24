package market

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Liquidation flow.
//
// A single large print is noise; a burst is information. The reading below is
// built so the two are distinguishable: the headline window is compared against
// the same symbol's own recent windows, and only a statistically unusual,
// one-sided burst is flagged as a cascade.
const (
	// LiquidationHeadlineWindow is the window the prompt reports first.
	LiquidationHeadlineWindow = time.Hour
	// LiquidationShortWindow is the recent burst window.
	LiquidationShortWindow = 15 * time.Minute
	// liquidationRetention is how much history the baseline is computed over.
	liquidationRetention = 12 * time.Hour
	// liquidationMaxPrints bounds memory for very active markets.
	liquidationMaxPrints = 50000
	// liquidationBaselineMinBuckets is the minimum number of complete windows
	// before a z-score is meaningful.
	liquidationBaselineMinBuckets = 6
	// liquidationCascadeZ is the burst threshold.
	liquidationCascadeZ = 3.0
	// liquidationCascadeMinPrints keeps a couple of large prints from reading as
	// a cascade.
	liquidationCascadeMinPrints = 20
	// liquidationCascadeSharePct is the one-sidedness required to name a side.
	liquidationCascadeSharePct = 70.0
)

// LiquidationPrint is one liquidation, normalized to the liquidated side.
type LiquidationPrint struct {
	TimestampMs int64
	Venue       string
	Symbol      string
	VenueSymbol string
	Side        string // "long" or "short"
	Price       float64
	Qty         float64
	NotionalUSD float64
}

// LiquidationReading is the derived flow view for one symbol.
type LiquidationReading struct {
	Symbol         string  `json:"symbol"`
	LongUSD        float64 `json:"longUsd"`
	ShortUSD       float64 `json:"shortUsd"`
	TotalUSD       float64 `json:"totalUsd"`
	LongSharePct   float64 `json:"longSharePct"`
	PrintCount     int     `json:"printCount"`
	LargestUSD     float64 `json:"largestUsd"`
	ShortWindowUSD float64 `json:"shortWindowUsd"`
	BaselineUSD    float64 `json:"baselineUsd"`
	ZScore         float64 `json:"zScore"`
	BaselineOK     bool    `json:"baselineOk"`
	Cascade        bool    `json:"cascade"`
	CascadeSide    string  `json:"cascadeSide"`
	ComputedAtMs   int64   `json:"computedAtMs"`
}

// LiquidationWindow keeps recent liquidation prints per symbol in timestamp order.
type LiquidationWindow struct {
	mu        sync.RWMutex
	prints    []LiquidationPrint
	retention time.Duration
	maxPrints int
}

func NewLiquidationWindow() *LiquidationWindow {
	return &LiquidationWindow{
		retention: liquidationRetention,
		maxPrints: liquidationMaxPrints,
	}
}

// Add records one print and prunes anything past the retention window.
func (w *LiquidationWindow) Add(print LiquidationPrint) {
	if w == nil || print.TimestampMs <= 0 || print.NotionalUSD <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.prints = append(w.prints, print)
	cutoff := time.Now().Add(-w.retention).UnixMilli()
	drop := 0
	for drop < len(w.prints) && w.prints[drop].TimestampMs < cutoff {
		drop++
	}
	if overflow := len(w.prints) - drop - w.maxPrints; overflow > 0 {
		drop += overflow
	}
	if drop > 0 {
		w.prints = append(w.prints[:0], w.prints[drop:]...)
	}
}

// Len reports the number of retained prints.
func (w *LiquidationWindow) Len() int {
	if w == nil {
		return 0
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.prints)
}

// LastTimestampMs reports the newest print's time, or 0 for an empty window.
// It lets the watcher evict windows for symbols it no longer watches instead of
// keeping their prints for the life of the process.
func (w *LiquidationWindow) LastTimestampMs() int64 {
	if w == nil {
		return 0
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.prints) == 0 {
		return 0
	}
	return w.prints[len(w.prints)-1].TimestampMs
}

// Reading derives the flow view at now. It returns nil when the headline window
// has no prints at all, which callers render as "omitted" rather than zero.
func (w *LiquidationWindow) Reading(symbol string, now time.Time) *LiquidationReading {
	if w == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()

	w.mu.RLock()
	prints := append([]LiquidationPrint(nil), w.prints...)
	w.mu.RUnlock()
	if len(prints) == 0 {
		return nil
	}

	reading := &LiquidationReading{Symbol: symbol, ComputedAtMs: nowMs}
	headlineStart := nowMs - LiquidationHeadlineWindow.Milliseconds()
	shortStart := nowMs - LiquidationShortWindow.Milliseconds()

	buckets := make(map[int64]float64)
	for _, print := range prints {
		if print.TimestampMs > nowMs {
			continue
		}
		if print.TimestampMs >= headlineStart {
			reading.PrintCount++
			reading.TotalUSD += print.NotionalUSD
			switch print.Side {
			case "long":
				reading.LongUSD += print.NotionalUSD
			case "short":
				reading.ShortUSD += print.NotionalUSD
			}
			if print.NotionalUSD > reading.LargestUSD {
				reading.LargestUSD = print.NotionalUSD
			}
		}
		if print.TimestampMs >= shortStart {
			reading.ShortWindowUSD += print.NotionalUSD
		}
		// Buckets are only used for the baseline; the current partial bucket is
		// excluded below.
		if print.TimestampMs >= nowMs-liquidationRetention.Milliseconds() {
			buckets[(print.TimestampMs-nowMs)/LiquidationHeadlineWindow.Milliseconds()] += print.NotionalUSD
		}
	}
	if reading.PrintCount == 0 {
		return nil
	}
	if reading.TotalUSD > 0 {
		reading.LongSharePct = reading.LongUSD / reading.TotalUSD * 100
	}

	if median, mad, ok := baselineStats(buckets); ok {
		reading.BaselineUSD = median
		reading.BaselineOK = true
		reading.ZScore = robustZ(reading.TotalUSD, median, mad)
	}

	if reading.BaselineOK && reading.ZScore >= liquidationCascadeZ && reading.PrintCount >= liquidationCascadeMinPrints {
		reading.Cascade = true
		switch {
		case reading.LongSharePct >= liquidationCascadeSharePct:
			reading.CascadeSide = "long"
		case reading.LongSharePct <= 100-liquidationCascadeSharePct:
			reading.CascadeSide = "short"
		}
		if reading.CascadeSide == "" {
			reading.Cascade = false
		}
	}

	return reading
}

// baselineStats returns the median and median absolute deviation of complete
// headline windows in the retained history. The current (partial) bucket and
// zero buckets are excluded: including them would make quiet periods look like
// a baseline of zero and turn any print into an unbounded z-score.
func baselineStats(buckets map[int64]float64) (median float64, mad float64, ok bool) {
	values := make([]float64, 0, len(buckets))
	for index, total := range buckets {
		if index >= 0 {
			continue
		}
		if total <= 0 {
			continue
		}
		values = append(values, total)
	}
	if len(values) < liquidationBaselineMinBuckets {
		return 0, 0, false
	}

	median = medianOf(values)
	deviations := make([]float64, len(values))
	for i, value := range values {
		deviations[i] = math.Abs(value - median)
	}
	return median, medianOf(deviations), true
}

func medianOf(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

// robustZ uses the median absolute deviation rather than the standard
// deviation: liquidation notional is heavy-tailed, so one historic burst would
// inflate a standard deviation enough to hide the next one.
func robustZ(value, median, mad float64) float64 {
	if mad > 0 {
		return 0.6745 * (value - median) / mad
	}
	// A degenerate MAD means the recent windows were nearly identical. Fall back
	// to the relative excess so a 4x window still reads as clearly unusual.
	if median > 0 {
		return value/median - 1
	}
	return 0
}
