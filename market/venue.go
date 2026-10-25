package market

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"nofx/provider/bybit"
	"nofx/provider/hyperliquid"
	"nofx/provider/okx"
)

// Venue is the exchange a trader executes on. Klines, order books and the
// decision context follow it: a Hyperliquid trader sees Hyperliquid candles,
// a Binance trader sees Binance candles. There is deliberately no silent
// cross-venue fallback — a mismatched candle invalidates stop/target levels.
type Venue string

const (
	VenueBinance     Venue = "binance"
	VenueBybit       Venue = "bybit"
	VenueOKX         Venue = "okx"
	VenueHyperliquid Venue = "hyperliquid"
)

// VenueFromExchange maps a trader's exchange string onto a data venue.
// Exchanges without a native kline adapter fall back to Binance as an explicit
// reference feed; the caller logs that choice.
func VenueFromExchange(exchange string) Venue {
	switch strings.ToLower(strings.TrimSpace(exchange)) {
	case "hyperliquid", "hyperliquid-xyz", "xyz":
		return VenueHyperliquid
	case "bybit":
		return VenueBybit
	case "okx":
		return VenueOKX
	default:
		return VenueBinance
	}
}

func (v Venue) String() string { return string(v) }

// klineCacheTTL lets a chart request and the same cycle's decision share one
// upstream snapshot without serving visibly stale candles.
const klineCacheTTL = 8 * time.Second

type klineCacheKey struct {
	Venue     Venue
	Symbol    string
	Timeframe string
	Limit     int
}

type klineCacheEntry struct {
	klines []Kline
	at     time.Time
}

var klineCache struct {
	mu    sync.Mutex
	items map[klineCacheKey]klineCacheEntry
}

// GetKlinesForVenue returns candles from the execution venue. xyz assets are
// always served by Hyperliquid, the only venue that lists them.
func GetKlinesForVenue(ctx context.Context, venue Venue, symbol, timeframe string, limit int) ([]Kline, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	symbol = Normalize(symbol)
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	if limit <= 0 {
		limit = 200
	}
	if IsXyzDexAsset(symbol) {
		venue = VenueHyperliquid
	}

	key := klineCacheKey{Venue: venue, Symbol: symbol, Timeframe: timeframe, Limit: limit}
	now := time.Now()
	klineCache.mu.Lock()
	if entry, ok := klineCache.items[key]; ok && now.Sub(entry.at) < klineCacheTTL {
		klineCache.mu.Unlock()
		return append([]Kline(nil), entry.klines...), nil
	}
	klineCache.mu.Unlock()

	klines, err := fetchKlines(ctx, venue, symbol, timeframe, limit)
	if err != nil {
		return nil, err
	}
	klineCache.mu.Lock()
	if klineCache.items == nil {
		klineCache.items = make(map[klineCacheKey]klineCacheEntry)
	}
	klineCache.items[key] = klineCacheEntry{klines: append([]Kline(nil), klines...), at: now}
	klineCache.mu.Unlock()
	return klines, nil
}

// ValidateTimeframesForVenue rejects intervals the venue does not offer, before
// any request is made. Hyperliquid has no 6h/3d; silently following a nearby
// interval would change what the strategy is actually trading.
func ValidateTimeframesForVenue(venue Venue, timeframes []string) error {
	if venue != VenueHyperliquid {
		return nil
	}
	var unsupported []string
	for _, tf := range timeframes {
		if hyperliquid.MapTimeframe(tf) == "" {
			unsupported = append(unsupported, tf)
		}
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("hyperliquid does not support timeframe(s): %s", strings.Join(unsupported, ", "))
	}
	return nil
}

// GetKlinesRangeForVenue returns closed candles in [start, end] from the
// execution venue, paging when the venue caps one response.
func GetKlinesRangeForVenue(ctx context.Context, venue Venue, symbol, timeframe string, start, end time.Time) ([]Kline, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	symbol = Normalize(symbol)
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	if !end.After(start) {
		return nil, fmt.Errorf("end time must be after start time")
	}
	if IsXyzDexAsset(symbol) {
		venue = VenueHyperliquid
	}
	switch venue {
	case VenueHyperliquid:
		return hyperliquidRange(ctx, symbol, timeframe, start, end)
	case VenueBybit:
		return bybitRange(ctx, symbol, timeframe, start, end)
	case VenueOKX:
		return okxRange(ctx, symbol, timeframe, start, end)
	default:
		return GetKlinesRange(symbol, timeframe, start, end)
	}
}

func fetchKlines(ctx context.Context, venue Venue, symbol, timeframe string, limit int) ([]Kline, error) {
	switch venue {
	case VenueHyperliquid:
		return hyperliquidKlines(ctx, symbol, timeframe, limit)
	case VenueBybit:
		candles, err := bybit.NewClient().Klines(ctx, symbol, timeframe, limit)
		if err != nil {
			return nil, err
		}
		out := make([]Kline, 0, len(candles))
		for _, candle := range candles {
			out = append(out, Kline{
				OpenTime:    candle.OpenTime,
				Open:        candle.Open,
				High:        candle.High,
				Low:         candle.Low,
				Close:       candle.Close,
				Volume:      candle.Volume,
				QuoteVolume: candle.Turnover,
				CloseTime:   candle.CloseTime,
			})
		}
		return out, nil
	case VenueOKX:
		candles, err := okx.NewClient().Klines(ctx, symbol, timeframe, limit)
		if err != nil {
			return nil, err
		}
		out := make([]Kline, 0, len(candles))
		for _, candle := range candles {
			out = append(out, Kline{
				OpenTime:    candle.OpenTime,
				Open:        candle.Open,
				High:        candle.High,
				Low:         candle.Low,
				Close:       candle.Close,
				Volume:      candle.Volume,
				QuoteVolume: candle.Turnover,
				CloseTime:   candle.CloseTime,
			})
		}
		return out, nil
	default:
		return NewAPIClient().GetKlines(symbol, timeframe, limit)
	}
}

func hyperliquidKlines(ctx context.Context, symbol, timeframe string, limit int) ([]Kline, error) {
	hlInterval := hyperliquid.MapTimeframe(timeframe)
	if hlInterval == "" {
		return nil, fmt.Errorf("hyperliquid does not support timeframe %q", timeframe)
	}
	candles, err := hyperliquid.NewClient().GetCandles(ctx, symbol, hlInterval, limit)
	if err != nil {
		return nil, err
	}
	return hyperliquidCandlesToKlines(candles), nil
}

func hyperliquidCandlesToKlines(candles []hyperliquid.Candle) []Kline {
	out := make([]Kline, 0, len(candles))
	for _, candle := range candles {
		out = append(out, Kline{
			OpenTime:  candle.OpenTime,
			Open:      hyperliquid.ParseVenueNumber(candle.Open),
			High:      hyperliquid.ParseVenueNumber(candle.High),
			Low:       hyperliquid.ParseVenueNumber(candle.Low),
			Close:     hyperliquid.ParseVenueNumber(candle.Close),
			Volume:    hyperliquid.ParseVenueNumber(candle.Volume),
			CloseTime: candle.CloseTime,
		})
	}
	return out
}

// hyperliquidRange pages backwards: one candleSnapshot returns at most ~5000
// bars and, when the window is larger, the newest ones.
func hyperliquidRange(ctx context.Context, symbol, timeframe string, start, end time.Time) ([]Kline, error) {
	hlInterval := hyperliquid.MapTimeframe(timeframe)
	if hlInterval == "" {
		return nil, fmt.Errorf("hyperliquid does not support timeframe %q", timeframe)
	}
	client := hyperliquid.NewClient()
	startMs, endMs := start.UnixMilli(), end.UnixMilli()
	cursor := endMs
	var out []Kline
	seen := make(map[int64]bool)

	for cursor > startMs {
		candles, err := client.GetCandlesRange(ctx, symbol, hlInterval, startMs, cursor)
		if err != nil {
			return nil, err
		}
		if len(candles) == 0 {
			break
		}
		batch := hyperliquidCandlesToKlines(candles)
		oldest := batch[0].OpenTime
		added := 0
		for _, candle := range batch {
			if seen[candle.OpenTime] {
				continue
			}
			seen[candle.OpenTime] = true
			out = append(out, candle)
			added++
		}
		if len(batch) < 5000 || oldest <= startMs || added == 0 {
			break
		}
		cursor = oldest - 1
	}

	sort.Slice(out, func(i, j int) bool { return out[i].OpenTime < out[j].OpenTime })
	startMs, endMs = start.UnixMilli(), end.UnixMilli()
	closed := out[:0]
	for _, candle := range out {
		if candle.OpenTime >= startMs && candle.CloseTime <= endMs {
			closed = append(closed, candle)
		}
	}
	return closed, nil
}

func bybitRange(ctx context.Context, symbol, timeframe string, start, end time.Time) ([]Kline, error) {
	candles, err := bybit.NewClient().KlinesRange(ctx, symbol, timeframe, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make([]Kline, 0, len(candles))
	for _, candle := range candles {
		out = append(out, Kline{
			OpenTime:    candle.OpenTime,
			Open:        candle.Open,
			High:        candle.High,
			Low:         candle.Low,
			Close:       candle.Close,
			Volume:      candle.Volume,
			QuoteVolume: candle.Turnover,
			CloseTime:   candle.CloseTime,
		})
	}
	return out, nil
}

func okxRange(ctx context.Context, symbol, timeframe string, start, end time.Time) ([]Kline, error) {
	candles, err := okx.NewClient().KlinesRange(ctx, symbol, timeframe, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make([]Kline, 0, len(candles))
	for _, candle := range candles {
		out = append(out, Kline{
			OpenTime:    candle.OpenTime,
			Open:        candle.Open,
			High:        candle.High,
			Low:         candle.Low,
			Close:       candle.Close,
			Volume:      candle.Volume,
			QuoteVolume: candle.Turnover,
			CloseTime:   candle.CloseTime,
		})
	}
	return out, nil
}
