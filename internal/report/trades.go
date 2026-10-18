// Package report summarises closed trades the way the research literature does:
// gross vs net, normalised by exposure, split by holding time, and compared with
// a random-entry baseline that holds for the same durations at the same cost.
//
// The distinctions it encodes are not cosmetic:
//   - gross vs net separates "the edge was eaten by costs" from "there was no edge";
//   - per-hold buckets make the turnover/cost trade-off visible instead of hiding it
//     in an average;
//   - the baseline is paired on exposure and holding time, so a strategy that is
//     simply "long in an up market" cannot look skilful.
//
// Everything here is pure: no network, no database.
package report

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// Trade is one closed position, reduced to what the report needs.
type Trade struct {
	Symbol   string
	Side     string // "LONG" / "SHORT"
	EntryMs  int64
	ExitMs   int64
	Qty      float64
	EntryPx  float64
	ExitPx   float64
	GrossPnL float64 // price move only, before fees and funding
	Fees     float64 // trading fees actually paid
	Funding  float64 // funding paid (positive = cost)

	AIInitiated bool   // closed by the AI rather than by a venue stop/target
	CloseKind   string // AI / protective (for display)
}

// Notional is the position's entry notional.
func (t Trade) Notional() float64 { return math.Abs(t.EntryPx * t.Qty) }

// NetPnL is gross minus fees and funding.
func (t Trade) NetPnL() float64 { return t.GrossPnL - t.Fees - t.Funding }

// Hold is how long the position was open (0 when either timestamp is missing).
func (t Trade) Hold() time.Duration {
	if t.EntryMs <= 0 || t.ExitMs <= 0 || t.ExitMs < t.EntryMs {
		return 0
	}
	return time.Duration(t.ExitMs-t.EntryMs) * time.Millisecond
}

// NetReturnPct is the net result as a percentage of the entry notional.
func (t Trade) NetReturnPct() float64 {
	notional := t.Notional()
	if notional <= 0 {
		return 0
	}
	return t.NetPnL() / notional * 100
}

func (t Trade) dir() float64 {
	if strings.EqualFold(t.Side, "SHORT") {
		return -1
	}
	return 1
}

// Summary aggregates a set of trades.
type Summary struct {
	Trades  int
	Wins    int
	Gross   float64
	Fees    float64
	Funding float64
	Net     float64

	GrossPF float64 // sum(winning gross) / |sum(losing gross)|
	NetPF   float64 // same, on net
	WinRate float64 // 0-100

	// FeesPctOfNotional is fees / total entry notional × 100: the headline "what
	// does a round trip cost me" number.
	FeesPctOfNotional float64
	AIShare           float64 // 0-100, share of AI-initiated closes
	AvgHold           time.Duration
}

// Bucket is one holding-time band.
type Bucket struct {
	Label   string
	Summary Summary
}

// Report is the whole picture for one trader over one window.
type Report struct {
	Overall    Summary
	Buckets    []Bucket
	AI         Summary
	Protective Summary
}

// bucketBounds are the holding-time bands. The first five are the ones that
// separate churn from a held position; the last catches everything longer.
var bucketBounds = []struct {
	label string
	max   time.Duration // 0 = unbounded
}{
	{"<30m", 30 * time.Minute},
	{"30m-2h", 2 * time.Hour},
	{"2-12h", 12 * time.Hour},
	{"12-24h", 24 * time.Hour},
	{"1-3d", 72 * time.Hour},
	{">3d", 0},
}

// unknownHoldLabel is its own bucket: a trade without timestamps must not be
// read as a sub-30-minute round trip.
const unknownHoldLabel = "n/a"

// Build aggregates trades into the overall picture, the holding-time buckets and
// the AI-vs-protective split.
func Build(trades []Trade) Report {
	rep := Report{Overall: summarise(trades)}

	byBucket := make([][]Trade, len(bucketBounds)+1)
	for _, t := range trades {
		byBucket[bucketIndex(t.Hold())] = append(byBucket[bucketIndex(t.Hold())], t)
	}
	for i := 0; i < len(byBucket); i++ {
		if len(byBucket[i]) == 0 {
			continue
		}
		label := unknownHoldLabel
		if i < len(bucketBounds) {
			label = bucketBounds[i].label
		}
		rep.Buckets = append(rep.Buckets, Bucket{Label: label, Summary: summarise(byBucket[i])})
	}

	var ai, protective []Trade
	for _, t := range trades {
		if t.AIInitiated {
			ai = append(ai, t)
		} else {
			protective = append(protective, t)
		}
	}
	if len(ai) > 0 {
		rep.AI = summarise(ai)
	}
	if len(protective) > 0 {
		rep.Protective = summarise(protective)
	}
	return rep
}

func bucketIndex(hold time.Duration) int {
	if hold <= 0 {
		return len(bucketBounds)
	}
	for i, band := range bucketBounds {
		if band.max == 0 || hold < band.max {
			return i
		}
	}
	return len(bucketBounds) - 1
}

func summarise(trades []Trade) Summary {
	s := Summary{Trades: len(trades)}
	if len(trades) == 0 {
		return s
	}

	var notional, holds float64
	ai := 0
	netPnLs := make([]float64, 0, len(trades))
	for _, t := range trades {
		s.Gross += t.GrossPnL
		s.Fees += t.Fees
		s.Funding += t.Funding
		net := t.NetPnL()
		s.Net += net
		netPnLs = append(netPnLs, net)
		notional += t.Notional()
		holds += t.Hold().Hours()
		if net > 0 {
			s.Wins++
		}
		if t.AIInitiated {
			ai++
		}
	}

	s.WinRate = float64(s.Wins) / float64(len(trades)) * 100
	s.AIShare = float64(ai) / float64(len(trades)) * 100
	s.GrossPF = profitFactor(grossPnLs(trades))
	s.NetPF = profitFactor(netPnLs)
	if notional > 0 {
		s.FeesPctOfNotional = s.Fees / notional * 100
	}
	s.AvgHold = time.Duration(holds / float64(len(trades)) * float64(time.Hour))
	return s
}

func grossPnLs(trades []Trade) []float64 {
	out := make([]float64, 0, len(trades))
	for _, t := range trades {
		out = append(out, t.GrossPnL)
	}
	return out
}

// profitFactor is sum of gains divided by the absolute sum of losses. It is +Inf
// when there are no losses and 0 when there are no gains.
func profitFactor(pnls []float64) float64 {
	var gains, losses float64
	for _, p := range pnls {
		if p > 0 {
			gains += p
		} else {
			losses -= p
		}
	}
	switch {
	case losses == 0 && gains == 0:
		return 0
	case losses == 0:
		return math.Inf(1)
	default:
		return gains / losses
	}
}

// Series is one symbol's closed-bar price path, used by the baseline.
type Series struct {
	BarMs  int64
	Closes []float64
}

// Baseline is the random-entry comparison: for every trade it draws random entry
// points on the same price path, holds for the SAME number of bars, and charges
// the same per-leg fee. The strategy's mean net return per trade is then placed
// in that distribution.
type Baseline struct {
	Runs    int
	Covered int // trades with a usable path
	Total   int

	StrategyNetPct float64 // mean net return per covered trade, %
	RandomMedian   float64
	RandomP25      float64
	RandomP75      float64
	Percentile     float64 // share of random means below the strategy's, %
	FeePerLeg      float64 // fraction of notional charged per leg
}

// EffectiveFeePerLeg derives the fee rate actually paid, from the trades
// themselves (two legs per trade). Falls back to 0 when there is no notional.
func EffectiveFeePerLeg(trades []Trade) float64 {
	var fees, notional float64
	for _, t := range trades {
		fees += t.Fees
		notional += t.Notional()
	}
	if notional <= 0 {
		return 0
	}
	return fees / (2 * notional)
}

// RandomBaseline compares the trades with random entries on their own price
// paths, matched on holding duration and cost. Trades without a usable path are
// excluded from both sides of the comparison (and counted in Covered/Total), so
// the strategy mean is computed on exactly the same trades.
func RandomBaseline(trades []Trade, series map[string]Series, runs int, seed int64) *Baseline {
	if runs <= 0 {
		runs = 200
	}
	feePerLeg := EffectiveFeePerLeg(trades)

	covered := make([]Trade, 0, len(trades))
	paths := make(map[string]Series, len(series))
	for _, t := range trades {
		s, ok := series[strings.ToUpper(t.Symbol)]
		if !ok || s.BarMs <= 0 || len(s.Closes) < 3 {
			continue
		}
		bars := int(math.Round(float64(t.Hold()) / float64(time.Duration(s.BarMs)*time.Millisecond)))
		if bars < 1 {
			bars = 1
		}
		if bars >= len(s.Closes) {
			continue
		}
		covered = append(covered, t)
		paths[strings.ToUpper(t.Symbol)] = s
	}
	base := &Baseline{Runs: runs, Covered: len(covered), Total: len(trades), FeePerLeg: feePerLeg}
	if len(covered) == 0 {
		return base
	}

	var strategySum float64
	for _, t := range covered {
		strategySum += t.NetReturnPct()
	}
	base.StrategyNetPct = strategySum / float64(len(covered))

	rng := rand.New(rand.NewSource(seed))
	means := make([]float64, 0, runs)
	for run := 0; run < runs; run++ {
		var sum float64
		for _, t := range covered {
			s := paths[strings.ToUpper(t.Symbol)]
			bars := int(math.Round(float64(t.Hold()) / float64(time.Duration(s.BarMs)*time.Millisecond)))
			if bars < 1 {
				bars = 1
			}
			if bars >= len(s.Closes) {
				continue
			}
			start := rng.Intn(len(s.Closes) - bars)
			entry, exit := s.Closes[start], s.Closes[start+bars]
			if entry <= 0 {
				continue
			}
			gross := (exit - entry) / entry
			sum += (gross*t.dir() - 2*feePerLeg) * 100
		}
		means = append(means, sum/float64(len(covered)))
	}

	sort.Float64s(means)
	base.RandomMedian = quantile(means, 0.50)
	base.RandomP25 = quantile(means, 0.25)
	base.RandomP75 = quantile(means, 0.75)
	below := sort.SearchFloat64s(means, base.StrategyNetPct)
	base.Percentile = float64(below) / float64(len(means)) * 100
	return base
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Round(q * float64(len(sorted)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
