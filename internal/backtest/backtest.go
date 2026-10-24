// Package backtest replays recorded AI decisions offline, with no model calls,
// to estimate what the strategy would have earned. It is deterministic given a
// seed, so results are reproducible and testable.
//
// It evaluates what the model *decided* (every open action in the decision
// log), not what was actually executed, so blocked/rejected opens are included
// — the question it answers is "was the AI's call any good?".
package backtest

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"

	"nofx/internal/report"
	"nofx/market"
	"nofx/store"
	"nofx/trader/connector"
)

// KlineSource fetches closed candles for a symbol/timeframe within a range.
// Injected so the engine can be tested without the network.
type KlineSource func(symbol, timeframe string, start, end time.Time) ([]market.Kline, error)

// Regime is the market state at entry, supplied by the caller from stored
// cross-venue and liquidation snapshots. It never changes the simulation; it
// only labels a trade so the report can attribute results to the tape it was
// taken on.
type Regime struct {
	Cascade     bool
	CrowdedSide string // "long", "short", or ""
}

// RegimeSource looks up the regime that was known when the decision was taken.
type RegimeSource func(symbol string, at time.Time) Regime

// Config controls a replay.
type Config struct {
	TraderID       string
	From, To       time.Time
	Timeframe      string // candles used for simulation (default 15m)
	Fees           connector.FeeModel
	MaxHoldCandles int // force-exit after this many candles (default 96)
	Samples        int // bootstrap samples for the significance test (default 1000)
	Seed           int64

	// When > 0, override the recorded stop-loss / take-profit with a fixed
	// percentage distance from the entry price. Used by the parameter sweep.
	OverrideStopPct float64
	OverrideTakePct float64

	// RegimeSource optionally labels each trade with the tape it was taken on
	// (liquidation cascade, crowding). Nil leaves the replay byte-for-byte
	// unchanged; attribution is the only consumer.
	RegimeSource RegimeSource
}

func (c Config) withDefaults() Config {
	if c.Timeframe == "" {
		c.Timeframe = "15m"
	}
	if c.MaxHoldCandles <= 0 {
		c.MaxHoldCandles = 96
	}
	if c.Samples <= 0 {
		c.Samples = 1000
	}
	if c.Fees == nil {
		c.Fees = connector.FeeModelFor("")
	}
	return c
}

// Trade is one simulated round trip derived from a recorded open decision.
type Trade struct {
	Symbol      string
	Side        string // "long" | "short"
	EntryTime   time.Time
	ExitTime    time.Time
	EntryPrice  float64
	ExitPrice   float64
	Quantity    float64
	Notional    float64
	GrossPnL    float64
	Fee         float64
	NetPnL      float64
	ExitReason  string // take_profit | stop_loss | timeout
	Confidence  int
	HoldCandles int
	// Entry regime (attribution only; empty when no source was supplied).
	EntryCascade     bool
	EntryCrowdedSide string
}

// Metrics summarizes a replay (all PnL figures are net of fees unless named).
type Metrics struct {
	Trades         int
	Wins           int
	Losses         int
	WinRate        float64
	GrossPnL       float64
	TotalFee       float64
	NetPnL         float64
	AvgNetPnL      float64
	ProfitFactor   float64
	Expectancy     float64
	MaxDrawdownPct float64
	Sharpe         float64
	Sortino        float64
}

// SymbolStat is per-symbol attribution.
type SymbolStat struct {
	Symbol    string
	Trades    int
	Wins      int
	NetPnL    float64
	AvgNetPnL float64
	WinRate   float64
}

// ReasonStat is attribution by exit reason.
type ReasonStat struct {
	Reason string
	Trades int
	Wins   int
	NetPnL float64
}

// ConfidenceStat is attribution by the AI's stated confidence bucket.
type ConfidenceStat struct {
	Bucket  string
	Trades  int
	Wins    int
	NetPnL  float64
	WinRate float64
}

// RegimeStat is attribution by the tape at entry.
type RegimeStat struct {
	Regime  string
	Trades  int
	Wins    int
	NetPnL  float64
	WinRate float64
}

// Significance reports whether the strategy beat random entries over the same
// symbols and holding periods (a Jesse-style skill-vs-luck check).
type Significance struct {
	MeanNetPnL   float64
	BaselineMean float64
	PValue       float64 // fraction of bootstrap means >= observed (small = skilled)
	Samples      int
}

// Result is the full replay output.
type Result struct {
	Config       Config
	Opens        int
	Skipped      int
	Trades       []Trade
	Metrics      Metrics
	BySymbol     []SymbolStat
	ByReason     []ReasonStat
	ByConfidence []ConfidenceStat
	ByRegime     []RegimeStat `json:",omitempty"`
	Significance Significance
	Notes        []string
}

// LoadRecords fetches a trader's decision records in [from, to].
func LoadRecords(st *store.Store, traderID string, from, to time.Time) ([]*store.DecisionRecord, error) {
	return st.Decision().GetRecordsByRange(traderID, from, to)
}

// Evaluate replays the open actions in the given decision records.
func Evaluate(records []*store.DecisionRecord, cfg Config, src KlineSource) (*Result, error) {
	cfg = cfg.withDefaults()
	tf, err := market.TFDuration(cfg.Timeframe)
	if err != nil {
		return nil, fmt.Errorf("unsupported timeframe %q: %w", cfg.Timeframe, err)
	}

	res := &Result{Config: cfg}
	res.Notes = append(res.Notes,
		"entry = first candle open strictly after the decision timestamp (no look-ahead)",
		fmt.Sprintf("intrabar: stop-loss assumed before take-profit within a candle; force-exit after %d candles", cfg.MaxHoldCandles))

	candles, err := loadCandles(records, cfg, tf, src)
	if err != nil {
		return nil, err
	}

	for _, rec := range records {
		for _, d := range rec.Decisions {
			if !isOpen(d.Action) {
				continue
			}
			res.Opens++
			tr, ok := simulate(rec.Timestamp, d, candles[market.Normalize(d.Symbol)], cfg)
			if !ok {
				res.Skipped++
				continue
			}
			res.Trades = append(res.Trades, tr)
		}
	}

	res.Metrics = summarize(res.Trades)
	res.BySymbol = bySymbol(res.Trades)
	res.ByReason = byReason(res.Trades)
	res.ByConfidence = byConfidence(res.Trades)
	if cfg.RegimeSource != nil {
		res.ByRegime = byRegime(res.Trades)
	}
	res.Significance = bootstrap(res.Trades, candles, cfg)
	return res, nil
}

func isOpen(action string) bool {
	return action == "open_long" || action == "open_short"
}

// loadCandles fetches (once per symbol) the candles the replay needs, extended
// past To so holds that run to the timeout still have data.
func loadCandles(records []*store.DecisionRecord, cfg Config, tf time.Duration, src KlineSource) (map[string][]market.Kline, error) {
	want := map[string]bool{}
	for _, rec := range records {
		for _, d := range rec.Decisions {
			if isOpen(d.Action) && d.Symbol != "" {
				want[market.Normalize(d.Symbol)] = true
			}
		}
	}
	end := cfg.To.Add(time.Duration(cfg.MaxHoldCandles) * tf)
	out := make(map[string][]market.Kline, len(want))
	for sym := range want {
		ks, err := src(sym, cfg.Timeframe, cfg.From, end)
		if err != nil {
			return nil, fmt.Errorf("fetch klines for %s: %w", sym, err)
		}
		out[sym] = ks
	}
	return out, nil
}

// simulate walks the candles forward from the decision time to a stop-loss,
// take-profit, or the hold limit. It is the conservative directional model.
func simulate(decidedAt time.Time, d store.DecisionAction, candles []market.Kline, cfg Config) (Trade, bool) {
	if d.Quantity <= 0 || len(candles) == 0 {
		return Trade{}, false
	}
	side := "long"
	dir := 1.0
	if d.Action == "open_short" {
		side, dir = "short", -1.0
	}

	// First candle that opens strictly after the decision.
	start := sort.Search(len(candles), func(i int) bool { return candles[i].OpenTime > decidedAt.UnixMilli() })
	if start >= len(candles) {
		return Trade{}, false
	}
	entry := candles[start].Open
	if entry <= 0 {
		return Trade{}, false
	}

	t := Trade{
		Symbol: d.Symbol, Side: side, EntryTime: time.UnixMilli(candles[start].OpenTime).UTC(),
		EntryPrice: entry, Quantity: d.Quantity, Notional: entry * d.Quantity,
		Confidence: d.Confidence,
	}
	if cfg.RegimeSource != nil {
		regime := cfg.RegimeSource(d.Symbol, decidedAt)
		t.EntryCascade = regime.Cascade
		t.EntryCrowdedSide = regime.CrowdedSide
	}

	// Effective protective levels: recorded, or overridden to a fixed
	// percentage from entry (for the parameter sweep).
	sl, tp := d.StopLoss, d.TakeProfit
	if cfg.OverrideStopPct > 0 {
		if side == "long" {
			sl = entry * (1 - cfg.OverrideStopPct/100)
		} else {
			sl = entry * (1 + cfg.OverrideStopPct/100)
		}
	}
	if cfg.OverrideTakePct > 0 {
		if side == "long" {
			tp = entry * (1 + cfg.OverrideTakePct/100)
		} else {
			tp = entry * (1 - cfg.OverrideTakePct/100)
		}
	}

	exit, reason := 0.0, "timeout"
	last := start + cfg.MaxHoldCandles
	if last >= len(candles) {
		last = len(candles) - 1
	}
	for i := start; i <= last; i++ {
		c := candles[i]
		if hitStop(c, side, sl) {
			exit, reason = sl, "stop_loss"
			t.HoldCandles = i - start + 1
			t.ExitTime = time.UnixMilli(c.OpenTime).UTC()
			break
		}
		if hitTarget(c, side, tp) {
			exit, reason = tp, "take_profit"
			t.HoldCandles = i - start + 1
			t.ExitTime = time.UnixMilli(c.OpenTime).UTC()
			break
		}
	}
	if reason == "timeout" {
		exit = candles[last].Close
		t.HoldCandles = last - start + 1
		t.ExitTime = time.UnixMilli(candles[last].OpenTime).UTC()
	}

	t.ExitPrice = exit
	t.ExitReason = reason
	t.GrossPnL = (exit - entry) * d.Quantity * dir
	t.Fee = cfg.Fees.Estimate(entry*d.Quantity, false).Amount + cfg.Fees.Estimate(exit*d.Quantity, false).Amount
	t.NetPnL = t.GrossPnL - t.Fee
	return t, true
}

// hitStop / hitTarget check whether a candle's range touches a protective
// level. Stop-loss is evaluated first so a candle that spans both is scored
// as the worst case.
func hitStop(c market.Kline, side string, sl float64) bool {
	if sl <= 0 {
		return false
	}
	if side == "long" {
		return c.Low <= sl
	}
	return c.High >= sl
}

func hitTarget(c market.Kline, side string, tp float64) bool {
	if tp <= 0 {
		return false
	}
	if side == "long" {
		return c.High >= tp
	}
	return c.Low <= tp
}

func summarize(trades []Trade) Metrics {
	m := Metrics{Trades: len(trades)}
	if len(trades) == 0 {
		return m
	}
	var win, loss float64
	for _, t := range trades {
		m.GrossPnL += t.GrossPnL
		m.TotalFee += t.Fee
		m.NetPnL += t.NetPnL
		if t.NetPnL > 0 {
			m.Wins++
			win += t.NetPnL
		} else if t.NetPnL < 0 {
			m.Losses++
			loss += -t.NetPnL
		}
	}
	m.WinRate = float64(m.Wins) / float64(m.Trades) * 100
	m.AvgNetPnL = m.NetPnL / float64(m.Trades)
	m.Expectancy = m.AvgNetPnL
	if loss > 0 {
		m.ProfitFactor = win / loss
	}

	// Sharpe/Sortino on per-trade net return over notional; equity curve for
	// drawdown starts at a nominal 10k so the peak is well-defined.
	returns := make([]float64, 0, len(trades))
	equity := make([]float64, 0, len(trades)+1)
	equity = append(equity, 10000)
	e := 10000.0
	for _, t := range trades {
		if t.Notional > 0 {
			returns = append(returns, t.NetPnL/t.Notional)
		}
		e += t.NetPnL
		equity = append(equity, e)
	}
	m.Sharpe = report.Sharpe(returns, 1)
	m.Sortino = report.Sortino(returns, 1)
	m.MaxDrawdownPct = report.MaxDrawdownPct(equity)
	return m
}

func bySymbol(trades []Trade) []SymbolStat {
	agg := map[string]*SymbolStat{}
	for _, t := range trades {
		s := agg[t.Symbol]
		if s == nil {
			s = &SymbolStat{Symbol: t.Symbol}
			agg[t.Symbol] = s
		}
		s.Trades++
		s.NetPnL += t.NetPnL
		if t.NetPnL > 0 {
			s.Wins++
		}
	}
	out := make([]SymbolStat, 0, len(agg))
	for _, s := range agg {
		s.AvgNetPnL = s.NetPnL / float64(s.Trades)
		s.WinRate = float64(s.Wins) / float64(s.Trades) * 100
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NetPnL > out[j].NetPnL })
	return out
}

func byReason(trades []Trade) []ReasonStat {
	agg := map[string]*ReasonStat{}
	for _, t := range trades {
		r := agg[t.ExitReason]
		if r == nil {
			r = &ReasonStat{Reason: t.ExitReason}
			agg[t.ExitReason] = r
		}
		r.Trades++
		r.NetPnL += t.NetPnL
		if t.NetPnL > 0 {
			r.Wins++
		}
	}
	out := make([]ReasonStat, 0, len(agg))
	for _, r := range agg {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NetPnL > out[j].NetPnL })
	return out
}

// byRegime attributes results to the tape each entry was taken on. A cascade
// entry and a calm entry are different bets, so their win rates are not
// comparable until they are separated like this.
func byRegime(trades []Trade) []RegimeStat {
	agg := map[string]*RegimeStat{}
	for _, t := range trades {
		label := regimeLabel(t)
		stat := agg[label]
		if stat == nil {
			stat = &RegimeStat{Regime: label}
			agg[label] = stat
		}
		stat.Trades++
		stat.NetPnL += t.NetPnL
		if t.NetPnL > 0 {
			stat.Wins++
		}
	}
	out := make([]RegimeStat, 0, len(agg))
	for _, stat := range agg {
		stat.WinRate = float64(stat.Wins) / float64(stat.Trades) * 100
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool { return regimeOrder(out[i].Regime) < regimeOrder(out[j].Regime) })
	return out
}

func regimeLabel(t Trade) string {
	if t.EntryCascade {
		return "cascade"
	}
	switch t.EntryCrowdedSide {
	case "long":
		return "crowded_long"
	case "short":
		return "crowded_short"
	default:
		return "calm"
	}
}

func regimeOrder(regime string) int {
	switch regime {
	case "cascade":
		return 0
	case "crowded_long":
		return 1
	case "crowded_short":
		return 2
	default:
		return 3
	}
}

func confidenceBucket(c int) string {
	switch {
	case c >= 90:
		return "90-100"
	case c >= 80:
		return "80-89"
	case c >= 70:
		return "70-79"
	case c >= 60:
		return "60-69"
	default:
		return "<60"
	}
}

func byConfidence(trades []Trade) []ConfidenceStat {
	agg := map[string]*ConfidenceStat{}
	for _, t := range trades {
		b := confidenceBucket(t.Confidence)
		s := agg[b]
		if s == nil {
			s = &ConfidenceStat{Bucket: b}
			agg[b] = s
		}
		s.Trades++
		s.NetPnL += t.NetPnL
		if t.NetPnL > 0 {
			s.Wins++
		}
	}
	order := []string{"90-100", "80-89", "70-79", "60-69", "<60"}
	out := make([]ConfidenceStat, 0, len(agg))
	for _, b := range order {
		if s, ok := agg[b]; ok {
			s.WinRate = float64(s.Wins) / float64(s.Trades) * 100
			out = append(out, *s)
		}
	}
	return out
}

// bootstrap compares the observed mean net PnL against random entries drawn
// from the same symbols with the same holding periods. A small p-value means
// the strategy did better than luck.
func bootstrap(trades []Trade, candles map[string][]market.Kline, cfg Config) Significance {
	if len(trades) == 0 {
		return Significance{}
	}
	var obs float64
	for _, t := range trades {
		obs += t.NetPnL
	}
	obs /= float64(len(trades))

	rng := rand.New(rand.NewSource(cfg.Seed))
	means := make([]float64, 0, cfg.Samples)
	better := 0
	for s := 0; s < cfg.Samples; s++ {
		var sum float64
		var n int
		for _, t := range trades {
			cs := candles[market.Normalize(t.Symbol)]
			hold := t.HoldCandles
			if hold <= 0 || len(cs) < hold+1 {
				continue
			}
			dir := 1.0
			if t.Side == "short" {
				dir = -1.0
			}
			// Random entry candle; exit at the close `hold` candles later.
			i := rng.Intn(len(cs) - hold)
			entry := cs[i].Open
			exit := cs[i+hold].Close
			fee := cfg.Fees.Estimate(entry*t.Quantity, false).Amount + cfg.Fees.Estimate(exit*t.Quantity, false).Amount
			sum += (exit-entry)*t.Quantity*dir - fee
			n++
		}
		if n == 0 {
			continue
		}
		m := sum / float64(n)
		means = append(means, m)
		if m >= obs {
			better++
		}
	}
	sig := Significance{MeanNetPnL: obs, Samples: len(means)}
	if len(means) > 0 {
		sig.BaselineMean = report.Mean(means)
		sig.PValue = float64(better) / float64(len(means))
	} else {
		sig.PValue = math.NaN()
	}
	return sig
}
