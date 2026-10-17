# Changelog

All notable changes to the NOFX project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

**Languages:** [English](CHANGELOG.md) | [中文](CHANGELOG.zh-CN.md)

---

## [Unreleased]

### Added
- Code-enforced stock session gate: new entries in stock perps are skipped while the venue shows no
  activity, which is what a closed cash market looks like on Hyperliquid — a stock perp quotes around
  the clock while its underlying does not, so an entry taken then faces an overnight gap that a stop
  cannot protect. The signal is the venue itself, via `risk_control.stock_quiet_minutes` (default 30,
  clamped 5-120): with the trades feed running, silence past the window blocks the entry; without the
  feed, a thin or wide book does; with neither signal the entry is allowed, because a data gap must
  never stop trading (the same fail-open rule the slippage guard follows). Stock classification goes
  through the venue's category map, so newly listed stocks are covered while commodities, indices and
  FX are not. Close and reduce are never blocked, crypto symbols are untouched, and the prompt is
  told when a stock looks closed so the model does not propose an entry that cannot execute.
  Toggle: `risk_control.block_cold_stock_entries` (nil = enabled).
- Strategy studio controls for both: a "Positioning" card in the indicator editor's exchange-context
  grid (default on, matching the backend's nil=on), and an "Entry order mode" picker with its wait in
  seconds in the risk editor. The wait field is only meaningful for post-only entries, and both values
  are still normalized by the backend, so an invalid value can never change how orders are submitted.
- Optional post-only (maker) entries: with `risk_control.entry_order_mode = "maker"` the opening order
  first rests an ALO limit on the passive side of the book (a buy joins the best bid, a sell the best
  ask, so it cannot cross and be rejected) to pay the maker fee instead of the taker fee. If it has
  not filled within `maker_entry_timeout_sec` (default 20s, clamped to 5-60s) the remainder is
  cancelled and the aggressive order is submitted as before; a rejected post-only order (it would
  have crossed, or HIP-3 does not support ALO) falls back the same way. A partial fill is kept as-is
  rather than topped up — the risk gate approves a maximum, not a requirement — and records plus the
  stop/target use the filled size, never the requested one. The AI's absolute stop/target levels are
  kept unchanged: a passive fill is at or better than the reference price, so the stop only moves
  closer and the target further. Opening orders only — closes stay aggressive so a protective exit
  can never wait. Unset or unrecognized values mean "market", so a typo cannot change execution.
- Hyperliquid-native positioning in the AI prompt: the hourly funding rate with its percentile
  against the coin's own 7-day funding history (the venue's own crowding read, and the only one
  that also exists for HIP-3 stock/commodity perps), open interest against price over 1h/4h
  (new longs / new shorts / short covering / long capitulation), and the mark-vs-oracle premium.
  Hyperliquid publishes no account-level long/short ratio, so the prompt labels these as
  venue-native proxies. Every clause is omitted when its samples are insufficient rather than
  printed as zero — a printed 0 reads as "no positioning" instead of "unknown". The funding
  baseline is cached per coin for 30 minutes (failures included), so the feature costs at most one
  extra request per coin per half hour. Toggle: `indicators.enable_positioning` (default on;
  nil means enabled, the same convention as `enable_microstructure`).
- Hyperliquid trade-flow watcher: one websocket client (the SDK reconnects and resubscribes on its
  own) streams the venue's public trades for the coins actually being analysed — positions plus
  surviving candidates, applied as a diff each cycle so the feed does not churn — and accumulates
  the aggressor-side buy/sell notional into a rolling hour. The clause is only rendered while the
  newest print is recent (15 min), so a dead feed omits it rather than reporting a stale ratio; a
  subscription the venue rejects (for example an unlisted HIP-3 name) backs off for 30 minutes
  instead of retrying every cycle. Best-effort by design: the server starts it, shuts it down on
  exit, and behaves exactly as before when the connection cannot be established.
- Documentation system with multi-language support (EN/CN/RU/UK)
- Complete getting-started guides (Docker, Custom API)
- Architecture documentation with system design details
- User guides with FAQ and troubleshooting
- Community documentation with bounty programs
- Strategy-configured indicator periods now actually reach the calculation layer. Previously
  `ema_periods`/`rsi_periods`/`atr_periods` were only echoed into the prompt as text while the
  computed values stayed hardcoded (20/50, 7/14); the prompt now prints the configured series
  with true period labels (e.g. `EMA(30)`), and MACD gains configurable fast/slow periods
  (`macd_fast_period`/`macd_slow_period`, default 12/26).
- Decision-time market microstructure in the AI prompt: order book spread, imbalance,
  top-10 depth, largest resting bid/ask levels, a 0-100 liquidity score, and VWAP derived from
  the primary-timeframe klines (no extra requests). Crypto symbols read the Binance book; xyz
  dex assets route to the Hyperliquid L2 snapshot. Toggle via
  `indicators.enable_microstructure` (default on).
- Code-enforced pre-trade venue slippage guard on opening orders: the venue book is walked for
  the sized notional and the order is rejected above a 0.5% projected cost or an unfillable
  (too thin) book. Book-fetch failures fail open — the guard never blocks on a data gap.
- Code-enforced daily loss halt (`risk_control.max_daily_loss`, default 10% of the initial
  balance): the day's net realized PnL now actually pauses trading, as the AI prompt always
  claimed but the code never enforced. Pause duration reuses `StopTradingTime`.
- Drawdown profit-protection monitor is configurable (`drawdown_monitoring_enabled`,
  `drawdown_check_interval_sec`, `min_profit_threshold_pct`, `drawdown_close_threshold_pct`);
  defaults preserve the previous 60s / 5% / 40% behavior, and settings apply without a restart.
- Paper trading now persists simulated orders/fills/positions, so DB-backed protections
  (losing closes, cooldown, drawdown halt) and the AI's trade history work in paper mode; the
  simulated balance tracks realized PnL, fees and used margin.
- Telegram bot token is encrypted at rest (`crypto.EncryptedString`), like exchange
  credentials. Legacy plaintext tokens keep working and re-encrypt on the next save.
- Two new indicators for trend/regime work: **ADX** with its directional indicators
  (`ADX(14)`, `DI+(14)`, `DI-(14)`) and **Supertrend** (trailing band plus
  `SUPERTREND_DIR` = +1 up / -1 down). ADX/Supertrend are emitted for the primary timeframe
  only to keep the prompt budget in check. Configure via `adx_periods` (default [14]),
  `supertrend_period` / `supertrend_multiplier` (default 10 / 3.0), and enable with
  `enable_adx` / `enable_supertrend`.
- The system prompt now carries an **indicator confluence playbook** generated for the
  ENABLED indicators only, so the model is never told to confirm with data it does not
  receive. Rules are role-based — trend gate (ADX/DI, Supertrend, EMA) → momentum (MACD,
  RSI) → entry timing (Bollinger Bands) → risk/sizing (ATR) → confirmation (volume, OI,
  funding) → execution quality (microstructure) — and end with the principle that a single
  indicator is never a reason to enter. ADX and Supertrend are also listed among the
  available inputs.
- Indicator periods are editable in the strategy studio. `ema_periods`, `rsi_periods`,
  `atr_periods`, `adx_periods`, `boll_periods`, `macd_fast_period`/`macd_slow_period`,
  `boll_multiplier` and `supertrend_period`/`supertrend_multiplier` reach the calculations but
  had no UI, which left an hourly strategy stuck on the 20/50 EMA defaults — the prompt could
  only ever report the defaults. Period lists accept comma-separated values (e.g. `50,200`,
  committed on blur so typing is not normalised per keystroke).
- `klines.prompt_display_bars` caps how many bars and indicator values are printed per
  timeframe in the prompt (default 40, editable 5–200). The analysis window stays
  `primary_count`, so a 200-bar window with EMA200 costs no prompt space — only the recent
  shape the model needs is paid for.
- Every decision cycle logs its prompt size with a rough token estimate
  (`📏 prompt size: … KB … ≈ N tokens | <tf> primary, P positions, C candidates, T timeframes`),
  making a context that is outgrowing the model's window visible before the provider rejects it.

### Changed
- The default candidate board is now the venue's **volume ranking, top 10**, instead of the top
  gainers (top 5). Liquidity is the right funnel for a trend strategy — the most traded assets have
  tight books and track their underlying best — while trend and timing stay with the indicators. The
  gainers board is the day's most extended names, which is the worst place to enter a trend, and for
  stocks its 24h change mixes the overnight gap with the session move. Existing strategies keep the
  direction and count they have stored; the "trend" preset now ships volume/10, while the "breakout"
  preset deliberately remains on the gainers board.
- Baseline writes are down to two methods: SetPnLBaseline (absolute baseline plus reconciliation point)
  and AdvancePnLBaseline (advance the point only). A deposit no longer goes through a relative
  `initial_balance + ?` update, so the in-memory baseline and the stored row cannot diverge.
- PnL and its baseline are consolidated into a single path; redundancy and fake fallbacks are gone:
  - The never-implemented `cashFlowLedger` (`CashFlowSince`) interface and its `externalCashFlowSince`
    wrapper are deleted; cash-flow detection is one rule in one place: the wallet move that the recorded
    realized PnL does not explain.
  - One implementation each for money in (`trader.BaselineFromWallet`) and for measuring against a
    baseline (`trader.PnLAgainstBaseline`) — the same formula used to be written out separately in the
    decision loop, the account endpoint and the equity curve. An unusable baseline (<= 0) now reports no
    PnL, fixing "baseline 0 counts the whole equity as profit" (`/api/account` returned
    `total_pnl = equity` in that case).
  - The equity curve drops its "fall back to the trader's configured balance" crutch
    (`resolveSnapshotBaseline`, the `configuredBalance` parameter, the leaderboard's prefetched balance
    map): a point without a stored baseline is reconstructed, and an unreadable position series fails
    that trader's chart instead of silently measuring it against the configured balance — the path that
    made the pre-deposit segment look like a loss.
  - Two client-side recomputations are deleted: `ComparisonChart` deriving `total_pnl_pct` from its first
    equity point (not cash-flow neutral), and `EquityChart`'s per-point baseline/PnL fallback chain plus
    its `account.initial_balance` fallback (the backend returns both for every point). The debug
    `console.log` calls in that chart fetch go with them.
- Indicators and their parameters are no longer two separate blocks: the `Indicator parameters`
  panel is folded into `Optional technical indicators`, one row per indicator, so enabling an
  indicator reveals its own inputs on the same row (EMA/RSI/ATR/ADX/BOLL periods, MACD fast/slow,
  BOLL and Supertrend multipliers) instead of toggling above and hunting for the period below.
  Disabled rows show their default parameters. The display cap (`prompt_display_bars`) belongs to
  no single indicator, so it moved into `Candles` next to the bar count. Field names, the JSON
  contract and the backend normalization are unchanged.
- Max margin usage is now editable in the strategy studio (10–100%, previously a read-only
  display), while the code stays authoritative: `ClampLimits` normalizes every saved config,
  and the pre-trade risk gate applies the same `store.NormalizeMarginUsageLimit` before
  rejecting — so no write path can disable the cap. Unset/invalid values now mean the product
  default (90%) instead of being silently clamped to the 10% minimum, which also fixes a
  latent tightening for clients that omitted the field.
- Prompt optimization (~38% smaller per decision: 37KB → 23KB on the reference
  context). Candle tables use compact CSV rows with significant digits that no longer
  round low-priced coins to `0.0000`; candidate coins keep the full table for the primary
  timeframe only (positions keep every timeframe); microstructure folds from 7 lines to 3;
  the schema dictionary drops formula lines; the confidence threshold is stated once
  instead of three times.
- Prompt consistency fixes: the aggressive mode no longer hardcodes a confidence of 70
  (it now quotes the configured threshold, which the hard constraints use); scalping mode
  gets frequency guidance that matches its fast-exit design instead of the day-trade
  "hold ≥ 30-60 minutes" text; the fixed-period `current_ema20`/`current_macd`/`current_rsi7`
  readouts (which contradicted the configured-period series) are gone; the candidate-count
  header reports the actually displayed number; the single-symbol JSON example quotes the
  same position cap as the hard constraints; MACD fast/slow and the BOLL multiplier are
  listed with their periods.
- Reorganized documentation structure into logical categories
- Updated all README files with proper navigation links
- The risk gate judges the max-positions rule by the expected net count: positions closed
  earlier in the same cycle no longer read as "position full" while the exchange position API
  lags, so close-then-open rebalances execute.

### Changed (Breaking)
- `mcp.AIClient.CallWithMessages` now takes a leading `context.Context` parameter:
  `CallWithMessages(ctx, systemPrompt, userPrompt)`. Pass `context.Background()` (or a
  request/trader context) when migrating. Out-of-tree implementations of `mcp.AIClient`
  must update their method signature.
- The agentic (V2) loop now exposes one converged tool per management domain —
  `skill_exchange_management`, `skill_model_management`, `skill_strategy_management`,
  `skill_trader_management` — generated from the embedded skill JSON definitions, instead of
  the flat `get_*`/`manage_*` pairs. The legacy routing stack keeps the flat tools. Tools
  defined outside a management domain are unchanged.
- The Telegram bot now shares the NOFXi agent (`nofx/agent`) with the web UI instead of
  running its own single-tool REST agent — `telegram/agent` and `telegram/session` are
  removed. Consequences: the bot exposes the full NOFXi tool surface, and the
  "Telegram → AI Model" setting (`POST /api/telegram/model`) is currently ignored — the
  bot uses the same model selection as the web agent.

### Fixed
- The cash-flow ledger could not read its own reconciliation point: GetPnLBaseline's anonymous struct
  had no column tags and GORM derives `pn_l_baseline_wallet` from the field name ("PnL" breaks its
  naming rule), which matched no column — so every call returned zeros with no error. Every process
  restart therefore treated the ledger as never reconciled and re-anchored it to the current wallet,
  which permanently missed any deposit made while the process was down (it stayed booked as profit).
  The column names are explicit now, with a store-level regression test.
- Switching exchange no longer books the balance difference between two accounts as a deposit: the
  reconciliation point belonged to the previous account while only initial_balance was reset. A 150 →
  500 switch turned the baseline into 850 and the dashboard read −350 (≈−41%). The ledger is re-anchored
  on an exchange change (the declared capital stays, the starting wallet is marked unobserved), so the
  next cycle adopts the new account's balance without folding the difference; the startup auto-fetch
  path (a new trader, or a balance reset to 0) anchors the ledger the same way.
- The PostgreSQL `traders` table now gets every column added after it was created (`paper_trading`,
  `pnl_baseline_wallet`, `pnl_baseline_at_ms`, `funding_synced_at_ms`). The PG path skips AutoMigrate and
  only hand-added `paper_trading`, so on a pre-existing PG table the cash-flow ledger could not read its
  own state: it bailed out on every cycle (a single warning in the log) and deposits were counted as
  profit again. Columns are now ensured per field through the Migrator (the `HasColumn`/`AddColumn`
  pattern the equity table already uses), replacing the hand-written `ALTER TABLE`.
- A deposit made before the cash-flow ledger existed is no longer booked as profit forever. The
  first reconciliation used to only record the current wallet as the starting point without
  touching `initial_balance`, so capital added before the ledger stayed inside the baseline: a live
  account with 75.39 of capital, an 85.97 deposit and a real result of −8.36 kept showing **+99.71%**
  on the dashboard and in the prompt. The first reconciliation now re-derives the baseline once as
  wallet − net realized PnL on record (75.39 → 161.36 for that account), logs
  `💰 PnL baseline repaired` so it can be confirmed, and the incremental reconciliation resumes
  from there.
- Equity-curve snapshots written before the baseline was stored per row are reconstructed point by
  point (that point's wallet − net realized PnL by then) instead of inheriting the current baseline.
  Applying one baseline to the whole curve showed a deposit as fake profit after it and as a fake
  loss before it; each segment now reports its own trading result.
- Indicator values the analysis window cannot produce are omitted from the prompt instead of being
  printed as `0`. A period longer than the available bars used to reach the model as a fabricated
  zero (`ATR(300): 0.0000`, `ADX(14): 0.0`, `SUPERTREND_DIR: 0`), which reads as "zero volatility /
  no trend" rather than "not computable" — and the model acts on it. Each omission is logged once
  per indicator (e.g. `ATR(300)` needs 301 bars, the window has 200) so a strategy configured
  beyond its window is visible instead of silently degrading. The legacy longer-term builder now
  shares the same availability-guarded path instead of duplicating the series construction.
- The primary timeframe is now guaranteed to be one of the analysed frames. ADX / Supertrend are
  printed for the primary frame only and the primary drives candle-close alignment, so a config
  written through the API or the agent (the strategy studio already keeps them consistent) could
  pick a primary outside `selected_timeframes` and silently drop those gauges from the prompt
  while the trader still waited for that frame's close. Normalization now inserts the primary at
  the front of the list, capped at `MaxTimeframes`.
- The indicator playbook no longer reads as an ADX veto. It said "> 25 = trending, < 20 = range
  where breakout entries are low quality", and a live model turned that into a hard gate and quoted
  it back ("无一满足趋势市（>25）…突破质量差") while holding for hours of cycles in a row. ADX is
  now described as trend *strength* the model weighs — "context, not a permission gate": 20-25 is a
  tradeable transition, and below 20 prefer the range edges or a fresh reversal over breakout
  continuation. The confluence principle also states that alignment does not require every
  indicator to sit at an extreme, and that a quiet, low-volatility tape is not by itself a reason
  to stay flat.
- The equity curve now stays cash-flow neutral across deposits and withdrawals. Each
  snapshot records the PnL baseline in effect when it was taken
  (`trader_equity_snapshots.initial_balance`, added to existing tables), and both
  `/api/equity-history` and `/api/equity-history-batch` measure every point against its own
  baseline. Previously the whole history was rebased onto the current baseline — so any
  deposit made the earlier part of the curve read as a large loss (and a withdrawal as a large
  gain) — and the reported PnL was the point's *unrealized* PnL divided by the first
  snapshot's wallet balance, which is not a return at all.
- The dashboard's equity chart loads from the new owner-scoped
  `GET /api/account/equity-history` instead of the public competition route. Private traders
  are deliberately hidden from the public route (404, to avoid leaking their existence), which
  the chart surfaced to its own owner as "API not found" — so the equity curve never rendered
  for anyone who had not enabled "show in competition".
- The equity chart no longer recomputes PnL on the client from a single account-wide baseline
  (nor falls back to a hardcoded 1000 USDT, which NaN-ed small accounts into a fictional large
  loss). It uses the per-point `total_pnl` / `total_pnl_pct` / `initial_balance` the API returns;
  the previous field names (`pnl`/`pnl_pct`) never matched the API and were always undefined.
- Hyperliquid xyz dex (HIP-3) orders are now judged against the pool they actually settle
  against. The xyz dex keeps its own USDC margin account, separate from the main perp
  account, so sizing an `xyz:*` order off the combined equity could approve an order the
  venue then rejects with `Insufficient margin to place order` (observed live on
  `xyz:SILVER`). Available/equity for xyz symbols now come from the dex's own free margin and
  equity; other symbols are unchanged. The dex's free margin is also exposed as
  `xyzDexAvailable` (the `withdrawable` field was parsed but never surfaced), an opening order
  on an empty dex fails early with an actionable message ("transfer USDC into the xyz dex"),
  and the raw venue rejection is wrapped with the same guidance. Leverage for xyz assets is
  now attempted via a hand-signed HIP-3 `updateLeverage` (failure logs and proceeds on the
  venue default — HIP-3 leverage support is not documented and this path is pending live
  verification).
- Bollinger Bands now actually use the configured `boll_periods` and the new
  `boll_multiplier` (default 2.0). Previously the period was only echoed into the prompt as
  text while the computed bands stayed hardcoded at 20 / 2.0 — the same dead-configuration
  trap that previously affected EMA/RSI/ATR. The prompt now prints the configured bands with
  true labels (`BOLL_UPPER(30,2.5)` …).
- Hyperliquid IOC orders are now verified against the venue response before being reported as
  `FILLED` (real order id, average price and filled quantity are propagated; unfilled orders
  return an error). Previously an expired IOC order was reported as filled with a placeholder
  id, leaving the system tracking a position that did not exist.
- OKX stop-loss / take-profit conditional orders no longer carry `posSide` in net mode — the
  venue rejected them, which left positions unprotected.
- Aster no longer retries non-idempotent POST requests (order placement). A request that timed
  out after the venue accepted it could previously double the order on retry.
- Bybit's two bare `http.Get` calls (quantity-step lookup on the order-placement path, and the
  order book) now use the 30s-timeout HTTP client; an unresponsive connection previously hung
  the calling path indefinitely.
- `AutoTrader.Run()` cancels its context on every exit path, so a failed grid initialization or
  an interrupted candle-alignment wait can no longer leave order-sync and drawdown-monitor
  goroutines polling forever.
- The public batch equity-history endpoint now honours `show_in_competition` like the
  single-trader variant, so private traders' curves are no longer readable by id.
- The agent's `get_positions`/`get_balance` tools surface per-trader query failures
  (`query_errors`) instead of returning a false empty book that could invite trading decisions.
- Lost writes are no longer silent: trader start/stop persistence, trader reloads after
  create/update, and 15 session/workflow state saves now log failures instead of dropping them.
- Trading mode now actually reaches the system prompt. The live loop previously passed a
  hardcoded `"balanced"` that `writeModeVariant` did not recognise, so **no mode section was
  ever emitted** and the configured trading style had no effect. The mode is now read from
  `strategy.trading_mode`, the strategy editor exposes a mode selector
  (Balanced / Aggressive / Conservative / Scalping), and preview/test-run use the same mode
  as the live trader.
- LLM retry backoff is now cancellable: `CallWithMessages` no longer waits on
  `context.Background()`, and `AutoTrader.Stop()` cancels an in-flight LLM call instead of
  waiting for the retry loop to finish.
- `AutoTrader.runCtx`/`runCancel` are now published under `isRunningMutex` to remove a data
  race between `Run()` and `Stop()`.
- The agent no longer reassigns the shared `Agent.aiClient` field on every incoming message.
  Each request resolves its own AI client once and injects it into the turn context
  (`withTurnAIClient`/`turnAIClient`), so concurrent store users can no longer clobber each
  other's model selection. Background jobs keep using the mutex-guarded default client.

### Removed
- Dead `kernel.PromptBuilder` (`kernel/prompt_builder.go`), an unused legacy prompt template
  whose action names (`OPEN_NEW`/`PARTIAL_CLOSE`) did not match the live pipeline.
- Dead `kernel.FormatContextForAI`/`FormatContextDataOnly` (`kernel/formatter.go`, 636 lines),
  a second bilingual context formatter with zero callers anywhere in the repo. It duplicated
  the live prompt builder and had already drifted (old verbose candle tables, no indicator
  confluence rules, no microstructure), so every prompt change had to be reasoned about
  twice. `engine_prompt.go` is the single prompt path.

---

## [3.0.0] - 2025-10-30

### Added - Major Architecture Transformation 🚀

**Complete System Redesign - Web-Based Configuration Platform**

This is a **major breaking update** that completely transforms NOFX from a static config-based system to a modern web-based trading platform.

#### Database-Driven Architecture
- SQLite integration replacing static JSON config
- Persistent storage with automatic timestamps
- Foreign key relationships and triggers for data consistency
- Separate tables for AI models, exchanges, traders, and system config

#### Web-Based Configuration Interface
- Complete web-based configuration management (no more JSON editing)
- AI Model setup through web interface (DeepSeek/Qwen API keys)
- Exchange management (Binance/Hyperliquid credentials)
- Dynamic trader creation (combine any AI model with any exchange)
- Real-time control (start/stop traders without system restart)

#### Flexible Architecture
- Separation of concerns (AI models and exchanges independent)
- Mix & match capability (unlimited combinations)
- Scalable design (support for unlimited traders)
- Clean slate approach (no default traders)

#### Enhanced API Layer
- RESTful design with complete CRUD operations
- New endpoints:
  - `GET/PUT /api/models` - AI model configuration
  - `GET/PUT /api/exchanges` - Exchange configuration
  - `POST/DELETE /api/traders` - Trader management
  - `POST /api/traders/:id/start|stop` - Trader control
- Updated documentation for all API endpoints

#### Modernized Codebase
- Type safety with proper separation of configuration types
- Database abstraction with prepared statements
- Comprehensive error handling and validation
- Better code organization (database, API, business logic)

### Changed
- **BREAKING**: Old `config.json` files no longer used
- Configuration must be done through web interface
- Much easier setup and better UX
- No more server restarts for configuration changes

### Why This Matters
- 🎯 **User Experience**: Much easier to configure and manage
- 🔧 **Flexibility**: Create any combination of AI models and exchanges
- 📊 **Scalability**: Support for complex multi-trader setups
- 🔒 **Reliability**: Database ensures data persistence and consistency
- 🚀 **Future-Proof**: Foundation for advanced features

---

## [2.0.2] - 2025-10-29

### Fixed - Critical Bug Fixes: Trade History & Performance Analysis

#### PnL Calculation - Major Error Fixed
- **Fixed**: PnL now calculated as actual USDT amount instead of percentage only
- Previously ignored position size and leverage (e.g., 100 USDT @ 5% = 1000 USDT @ 5%)
- Now: `PnL (USDT) = Position Value × Price Change % × Leverage`
- Impact: Win rate, profit factor, and Sharpe ratio now accurate

#### Position Tracking - Missing Critical Data
- **Fixed**: Open position records now store quantity and leverage
- Previously only stored price and time
- Essential for accurate PnL calculations

#### Position Key Logic - Long/Short Conflict
- **Fixed**: Changed from `symbol` to `symbol_side` format
- Now properly distinguishes between long and short positions
- Example: `BTCUSDT_long` vs `BTCUSDT_short`

#### Sharpe Ratio Calculation - Code Optimization
- **Changed**: Replaced custom Newton's method with `math.Sqrt`
- More reliable, maintainable, and efficient

### Why This Matters
- Historical trade statistics now show real USDT profit/loss
- Performance comparison between different leverage trades is accurate
- AI self-learning mechanism receives correct feedback
- Multi-position tracking (long + short simultaneously) works correctly

---

## [2.0.2] - 2025-10-29

### Fixed - Aster Exchange Precision Error

- Fixed Aster exchange precision error (code -1111)
- Improved price and quantity formatting to match exchange requirements
- Added detailed precision processing logs for debugging
- Enhanced all order functions with proper precision handling

#### Technical Details
- Added `formatFloatWithPrecision` function
- Price and quantity formatted according to exchange specifications
- Trailing zeros removed to optimize API requests

---

## [2.0.1] - 2025-10-29

### Fixed - ComparisonChart Data Processing

- Fixed ComparisonChart data processing logic
- Switched from cycle_number to timestamp grouping
- Resolved chart freezing issue when backend restarts
- Improved chart data display (shows all historical data chronologically)
- Enhanced debugging logs

---

## [2.0.0] - 2025-10-28

### Added - Major Updates

- AI self-learning mechanism (historical feedback, performance analysis)
- Multi-trader competition mode (Qwen vs DeepSeek)
- Binance-style UI (complete interface imitation)
- Performance comparison charts (real-time ROI comparison)
- Risk control optimization (per-coin position limit adjustment)

### Fixed

- Fixed hardcoded initial balance issue
- Fixed multi-trader data sync issue
- Optimized chart data alignment (using cycle_number)

---

## [1.0.0] - 2025-10-27

### Added - Initial Release

- Basic AI trading functionality
- Decision logging system
- Simple Web interface
- Support for Binance Futures
- DeepSeek and Qwen AI model integration

---

## How to Use This Changelog

### For Users
- Check the [Unreleased] section for upcoming features
- Review version sections to understand what changed
- Follow migration guides for breaking changes

### For Contributors
When making changes, add them to the [Unreleased] section under appropriate categories:
- **Added** - New features
- **Changed** - Changes to existing functionality
- **Deprecated** - Features that will be removed
- **Removed** - Features that were removed
- **Fixed** - Bug fixes
- **Security** - Security fixes

When releasing a new version, move [Unreleased] items to a new version section with date.

---

## Links

- [Documentation](docs/README.md)
- [Contributing Guidelines](CONTRIBUTING.md)
- [Security Policy](SECURITY.md)
- [GitHub Repository](https://github.com/NoFxAiOS/nofx)

---

**Last Updated:** 2025-11-01
