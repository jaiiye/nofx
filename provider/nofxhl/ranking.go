package nofxhl

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"nofx/provider/nofxos"
)

// OIRanking returns the OI ranking. Prefers real history from
// oi_snapshots (latest frame vs the frame closest to 24h ago); falls
// back to a live metaAndAssetCtxs snapshot when history is thin.
func (c *Client) OIRanking(ctx context.Context, duration string, limit int) (*nofxos.OIRankingData, error) {
	ctx, cancel := fetchCtx(ctx)
	defer cancel()
	if limit <= 0 {
		limit = 10
	}
	if out, err := c.oiRankingFromHistory(ctx, limit); err == nil && len(out.TopPositions) > 0 {
		return out, nil
	}
	return c.oiRankingFromLive(ctx, limit)
}

// oiRankingFromHistory computes per-coin OI delta between the latest
// snapshot and the snapshot closest to 24h ago.
func (c *Client) oiRankingFromHistory(ctx context.Context, limit int) (*nofxos.OIRankingData, error) {
	pool, err := c.getPool(ctx)
	if err != nil {
		return nil, err
	}
	nowMs := time.Now().UnixMilli()

	// Latest frame per coin.
	nowRows, err := pool.Query(ctx, `
		SELECT DISTINCT ON (coin) coin, t, oi_usd
		FROM oi_snapshots
		WHERE t > $1
		ORDER BY coin, t DESC
	`, nowMs-2*3600*1000)
	if err != nil {
		return nil, err
	}
	defer nowRows.Close()
	latest := map[string]struct {
		T     int64
		OIUSD float64
	}{}
	for nowRows.Next() {
		var coin string
		var t int64
		var oi float64
		if err := nowRows.Scan(&coin, &t, &oi); err != nil {
			return nil, err
		}
		latest[coin] = struct {
			T     int64
			OIUSD float64
		}{t, oi}
	}
	if err := nowRows.Err(); err != nil {
		return nil, err
	}
	if len(latest) == 0 {
		return nil, fmt.Errorf("no recent oi_snapshots")
	}

	// Baseline frame per coin: the newest snapshot older than 24h.
	prevRows, err := pool.Query(ctx, `
		SELECT DISTINCT ON (coin) coin, oi_usd
		FROM oi_snapshots
		WHERE t <= $1 AND t > $2
		ORDER BY coin, t DESC
	`, nowMs-24*3600*1000, nowMs-48*3600*1000)
	if err != nil {
		return nil, err
	}
	defer prevRows.Close()
	prev := map[string]float64{}
	for prevRows.Next() {
		var coin string
		var oi float64
		if err := prevRows.Scan(&coin, &oi); err != nil {
			return nil, err
		}
		prev[coin] = oi
	}
	if err := prevRows.Err(); err != nil {
		return nil, err
	}

	prices, _ := c.priceSnapshot(ctx)
	out := &nofxos.OIRankingData{
		Duration:  "24h",
		TimeRange: "oi_snapshots: latest vs ~24h ago",
		FetchedAt: time.Now(),
	}
	type row struct {
		coin      string
		now, prev float64
	}
	var rows2 []row
	for coin, cur := range latest {
		base, ok := prev[coin]
		if !ok || base <= 0 {
			continue
		}
		rows2 = append(rows2, row{coin, cur.OIUSD, base})
	}
	if len(rows2) == 0 {
		return nil, fmt.Errorf("no oi baseline pairs")
	}
	sort.Slice(rows2, func(i, j int) bool {
		return (rows2[i].now - rows2[i].prev) > (rows2[j].now - rows2[j].prev)
	})
	rank := 0
	for _, r := range rows2 {
		if rank >= limit {
			break
		}
		rank++
		delta := r.now - r.prev
		out.TopPositions = append(out.TopPositions, nofxos.OIPosition{
			Symbol: r.coin, Rank: rank,
			Price: prices[r.coin], CurrentOI: r.now,
			OIDelta: delta, OIDeltaPercent: delta / r.prev * 100,
			OIDeltaValue: delta, // USD-value approximation (snapshots store oi_usd)
		})
	}
	return out, nil
}

// oiRankingFromLive builds the ranking from one metaAndAssetCtxs call.
// HL's free info API has no per-coin historical OI, so OIDelta fields
// are zero and the ranking is by absolute OI size.
func (c *Client) oiRankingFromLive(ctx context.Context, limit int) (*nofxos.OIRankingData, error) {
	_, universe := c.priceSnapshot(ctx)
	if len(universe) == 0 {
		return nil, fmt.Errorf("nofxhl: asset ctxs unavailable")
	}
	out := &nofxos.OIRankingData{
		Duration:  "24h",
		TimeRange: "live metaAndAssetCtxs",
		FetchedAt: time.Now(),
	}
	type row struct {
		symbol string
		oiUSD  float64
		mark   float64
		chgPct float64
	}
	var rows []row
	for _, u := range universe {
		oi := u.Ctx.OI()
		mark := u.Ctx.Mark()
		if oi == 0 || mark == 0 {
			continue
		}
		chg := 0.0
		if prev := u.Ctx.PrevDay(); prev > 0 {
			chg = (mark - prev) / prev * 100
		}
		rows = append(rows, row{u.Name, oi * mark, mark, chg})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].oiUSD > rows[j].oiUSD })
	rank := 0
	for _, r := range rows {
		if rank >= limit {
			break
		}
		rank++
		out.TopPositions = append(out.TopPositions, nofxos.OIPosition{
			Symbol: r.symbol, Rank: rank, Price: r.mark,
			CurrentOI: r.oiUSD, PriceDeltaPercent: r.chgPct,
		})
	}
	// Low positions: thinnest OI (shape parity with nofxos responses).
	for i := len(rows) - 1; i >= 0 && rank < limit*2; i-- {
		rank++
		r := rows[i]
		out.LowPositions = append(out.LowPositions, nofxos.OIPosition{
			Symbol: r.symbol, Rank: rank, Price: r.mark,
			CurrentOI: r.oiUSD, PriceDeltaPercent: r.chgPct,
		})
	}
	return out, nil
}

// PriceRanking returns gainers/losers from live prevDayPx.
//
// The kernel config may request several durations ("1h,4h,24h") but
// the free HL info API exposes only 24h price change (prevDayPx), so
// only the "24h" key carries data — the formatter skips keys that are
// absent, and the remaining config keys deliberately produce nothing
// rather than mislabelled 24h data.
func (c *Client) PriceRanking(ctx context.Context, durations string, limit int) (*nofxos.PriceRankingData, error) {
	ctx, cancel := fetchCtx(ctx)
	defer cancel()
	if limit <= 0 {
		limit = 10
	}
	if durations == "" {
		durations = "24h"
	}

	_, universe := c.priceSnapshot(ctx)
	if len(universe) == 0 {
		return nil, fmt.Errorf("nofxhl: asset ctxs unavailable")
	}
	type item struct {
		symbol string
		mark   float64
		chgPct float64
		oiUSD  float64
	}
	var rows []item
	for _, u := range universe {
		mark := u.Ctx.Mark()
		prev := u.Ctx.PrevDay()
		if mark == 0 || prev == 0 {
			continue
		}
		rows = append(rows, item{u.Name, mark, (mark - prev) / prev * 100, u.Ctx.OI() * mark})
	}

	// PriceRankingItem.PriceDelta is decimal format: 0.0723 = 7.23%.
	toItem := func(r item) nofxos.PriceRankingItem {
		return nofxos.PriceRankingItem{
			Pair: r.symbol + "-USD", Symbol: r.symbol,
			PriceDelta: r.chgPct / 100, Price: r.mark, OI: r.oiUSD,
		}
	}

	out := &nofxos.PriceRankingData{
		Durations: map[string]*nofxos.PriceRankingDuration{},
		FetchedAt: time.Now(),
	}
	// Populate every configured key with the 24h data only when the key
	// IS "24h" (see doc comment). Keys like "1h"/"4h" are left absent.
	for _, key := range splitDurations(durations) {
		if key != "24h" {
			continue
		}
		d := &nofxos.PriceRankingDuration{}
		sort.Slice(rows, func(i, j int) bool { return rows[i].chgPct > rows[j].chgPct })
		for i := 0; i < limit && i < len(rows); i++ {
			d.Top = append(d.Top, toItem(rows[i]))
		}
		for i := len(rows) - 1; i >= 0 && len(d.Low) < limit; i-- {
			if rows[i].chgPct < 0 {
				d.Low = append(d.Low, toItem(rows[i]))
			}
		}
		out.Durations[key] = d
		break
	}
	if len(out.Durations) == 0 {
		// No "24h" requested — provide it anyway so the prompt has price
		// context, but only under a 24h key.
		d := &nofxos.PriceRankingDuration{}
		sort.Slice(rows, func(i, j int) bool { return rows[i].chgPct > rows[j].chgPct })
		for i := 0; i < limit && i < len(rows); i++ {
			d.Top = append(d.Top, toItem(rows[i]))
		}
		for i := len(rows) - 1; i >= 0 && len(d.Low) < limit; i-- {
			if rows[i].chgPct < 0 {
				d.Low = append(d.Low, toItem(rows[i]))
			}
		}
		out.Durations["24h"] = d
	}
	return out, nil
}

func splitDurations(durations string) []string {
	out := []string{}
	for _, part := range strings.Split(durations, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		out = []string{"24h"}
	}
	return out
}
