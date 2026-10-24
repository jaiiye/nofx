# Market Data Sources and Conventions

> Cross-venue positioning, liquidation flow, and how they reach the AI decision and the offline replay

This page documents which market data nofx collects itself, where it comes from, how it is stored, and how downstream code uses it. Every endpoint is public — **no API key or environment variable is required**.

---

## 1. Sources

| Data | Venue | Endpoint | Auth | Cadence |
|------|-------|----------|------|---------|
| Funding / mark / index (whole market) | Binance | `GET /fapi/v1/premiumIndex` | none | 60s |
| Open interest (per symbol) | Binance | `GET /fapi/v1/openInterest` | none | 60s |
| Funding history | Binance | `GET /fapi/v1/fundingRate` | none | on demand |
| Liquidations (whole market) | Binance | WS `!forceOrder@arr` | none | real time |
| Ticker / OI / funding / mark (whole market) | Bybit | `GET /v5/market/tickers?category=linear` | none | 60s |
| Settlement interval | Bybit | `GET /v5/market/instruments-info` | none | startup + 6h |
| Liquidations (per symbol) | Bybit | WS `allLiquidation.{symbol}` | none | real time |
| Funding / OI / premium | Hyperliquid | `POST /info` `type=metaAndAssetCtxs` | none | reuses the 60s cache |

> **Hyperliquid liquidations are not covered.** The venue has no market-wide liquidation channel: the public `trades` stream carries `users` addresses but no liquidation flag, `userFills` is per address, and the protocol's liquidator account has no publicly discoverable address (the HLP vault's `userFills` is empty). Liquidation windows therefore cover Binance and Bybit only. HL liquidation risk is still represented by the `position_profile` clusters, which are built from real holder positions.

Symbol mapping:

- The internal key comes from `market.Normalize`: `BTC` → `BTCUSDT`, `xyz:NVDA` stays as-is.
- xyz assets (HIP-3 stocks, commodities, indices) exist only on Hyperliquid, so Binance/Bybit are skipped. A cross-venue reading needs at least two venues, so xyz assets never produce that section (the HL-native positioning reading still applies).

---

## 2. Storage

Two tables, defined in `store/market_metrics.go`, created by GORM AutoMigrate at startup (skipped for pre-existing PostgreSQL tables).

### `market_venue_metrics`

One row per venue, symbol and timestamp.

| Column | Meaning |
|--------|---------|
| `venue` / `symbol` / `venue_symbol` | venue, normalized symbol, venue's own symbol |
| `timestamp` | reading time (venue timestamp when provided) |
| `mark_price` / `oi_base` / `oi_usd` | mark price, base-unit OI, USD OI |
| `funding_rate_1h_pct` | funding **normalized to an hourly percentage** |
| `funding_interval_h` | original settlement interval; defaults to 8h when Bybit does not report one |
| `premium_pct` | `(mark - index) / index * 100` |
| `source_ok` | false for a degraded read; readers drop those rows |

- Unique index on `(venue, symbol, timestamp)` with `ON CONFLICT DO NOTHING`, so retries are idempotent.
- The collector refreshes in memory every 60s and persists every fifth poll (5 minutes).
- 30-day retention, cleaned once a day by the collector.

### `market_liquidations`

| Column | Meaning |
|--------|---------|
| `venue` / `symbol` / `venue_symbol` | as above |
| `timestamp` | liquidation fill time |
| `side` | **normalized to `long` / `short` (the liquidated side)** |
| `price` / `qty` / `notional_usd` | fill price, size, notional |

- Unique index on `(venue, venue_symbol, timestamp, side, price, qty)`, so a websocket replay after a reconnect does not double-count.
- Buffered and written every 2s or 500 rows.
- 30-day retention.

> History accumulates forward from the day this ships. Importing exchange archives (e.g. `data.binance.vision`) is not part of this feature.

---

## 3. Conventions

### 3.1 Funding normalization

Venues settle on different schedules, so raw rates are not comparable:

| Venue | Raw interval | Normalization |
|-------|--------------|---------------|
| Hyperliquid | 1 hour | used as-is |
| Binance | 8 hours | `rate / 8` |
| Bybit | 8h / 4h / 1h | `rate / interval`, interval from `instruments-info` |

Output is always **percent per hour** (`%/h`). When the interval cannot be resolved, 8h is assumed and the prompt marks it `[interval assumed]`.

### 3.2 Cross-venue OI is not additive

The same position can be opened on one venue and hedged on another, so both count it. Therefore:

- each venue's OI is listed separately;
- the total is shown for display only, with the caveat that hedged positions double-count;
- crowding uses **OI-weighted funding**, not the OI total.

### 3.3 Liquidation side semantics

The two venues describe the same event with opposite aggressor semantics. Mapping happens in the provider and is pinned by table-driven tests:

| Venue | Raw field | Meaning | Normalized |
|-------|-----------|---------|------------|
| Binance | `o.S = SELL` | a long was sold out | `long` |
| Binance | `o.S = BUY` | a short was bought back | `short` |
| Bybit | `S = Buy` | a long was liquidated | `long` |
| Bybit | `S = Sell` | a short was liquidated | `short` |

### 3.4 Cascade detection

- Headline window 1 hour, burst window 15 minutes, 12 hours retained in memory.
- The baseline is the median and MAD of complete 1-hour windows; empty windows are excluded.
- `z = 0.6745 × (current - median) / MAD`; when MAD is 0 it falls back to the relative excess over the median.
- A cascade requires `z ≥ 3`, at least 20 prints, and ≥ 70% on one side.
- With fewer than six complete windows there is no z and no cascade.

---

## 4. Configuration

Two pointer flags in `IndicatorConfig`; `nil` means enabled (same convention as `EnablePositioning`):

```json
{
  "enable_cross_venue": true,
  "enable_liquidation_flow": true
}
```

- Disabled: no collection, no prompt section, no replay attribution.
- The collectors start in `main.go` and are shared by all traders, so they are not hot-switched per strategy.

---

## 5. In the prompt

Each position and candidate gains two sections (omitted entirely when absent, never rendered as zero):

```text
**CROSS-VENUE OI / FUNDING (per venue; not additive)**
- Binance: OI $315.00M | 62% of total | +0.0010%/h (settles every 8h)
- Bybit: OI $126.00M | 25% of total | +0.0020%/h (settles every 8h)
- Hyperliquid: OI $63.00M | 12% of total | +0.0120%/h
- funding spread +0.0110%/h (hyperliquid/binance) | OI-weighted funding marks longs crowded
- Total $504.00M across venues: hedged positions can be counted twice, so this is not market-wide exposure.

**LIQUIDATION FLOW (public venue streams)**
- 1h long $4.00M / short $500.0K | long share 89% | 15m $3.20M | largest $800.0K | z=4.2
- CASCADE: longs are being liquidated right now. This is fuel already burning — do not chase into it.
```

The matching playbook rules are written only when the flags are enabled, and state explicitly that:

- the cross-venue total is not market exposure;
- a funding gap is a sizing/patience signal, not a direction;
- a cascade is fuel already spent — do not chase the end of it.

This data **does not gate any order**: no new block logic exists; it only changes what the model sees.

---

## 6. Offline replay

```bash
nofx evaluate --trader <id> --from 2026-01-01 [--to 2026-02-01] --regime
```

`--regime` labels every trade with the tape at entry and attributes results by label:

```text
-- by entry regime --
  cascade        trades=12   win= 33.3%  net=-420.10
  crowded_long   trades=40   win= 55.0%  net= 180.40
  crowded_short  trades=18   win= 61.1%  net=  95.20
  calm           trades=90   win= 52.2%  net= 310.00
```

- Labels come from point-in-time reads of stored rows, so no future data leaks in.
- Off by default; even when on it never changes entry, exit, sizing, or any PnL figure.
- Cascade label: the 15 minutes before entry must be at least 3× the median of the previous four non-empty 15-minute windows, with at least 10 prints.

---

## 7. Degradation and limits

| Scenario | Behavior |
|----------|----------|
| Binance region block (451) | only that venue is missing; Bybit + Hyperliquid continue; the reason is logged once |
| Bybit interval unavailable | 8h is assumed and recorded, the prompt marks it as assumed, and retries back off to 10 minutes |
| Venue does not list the symbol | that venue is absent from the section; fewer than two venues omits the whole section |
| Hyperliquid liquidations | not covered (no public market-wide feed); HL liquidation risk comes from the `position_profile` clusters |
| Websocket disconnect | exponential backoff (30s cap); duplicate prints from the replay are deduped by the unique index |
| Store write failure | in-memory readings keep updating, the failure is logged once, the prompt is unaffected |
| Disk usage | both tables keep 30 days; `market_venue_metrics` is about 11k rows/day (3 venues × 13 symbols × 288) |

---

## 8. Code map

| Concern | File |
|---------|------|
| Tables and reads | `store/market_metrics.go` |
| Binance client | `provider/binance/{client,market,ws}.go` |
| Bybit client | `provider/bybit/{client,market,ws}.go` |
| Cross-venue math | `market/venue_metrics.go` |
| Cross-venue collector | `market/venue_metrics_service.go` |
| Liquidation window | `market/liquidation_flow.go` |
| Liquidation collector | `market/liquidation_service.go` |
| Boot wiring | `kernel/market_metrics.go`, `main.go` |
| Prompt rendering | `kernel/engine_prompt.go` (`formatCrossVenue` / `formatLiquidationFlow`) |
| Replay attribution | `internal/backtest/regime_source.go` |
