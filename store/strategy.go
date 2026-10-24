package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Hard limits to prevent token explosion in AI requests
const (
	// MaxCandidateCoins bounds the candidate board. Ten coins x two timeframes
	// of full series is the single largest prompt block, so the per-series
	// display cap (DefaultPromptDisplayBars) is what keeps that block in check.
	MaxCandidateCoins = 10
	MaxPositions      = 3
	MaxTimeframes     = 4
	MinKlineCount     = 10
	// MaxKlineCount bounds the analysis window. It matches how many candles the
	// market layer actually fetches (200), so an hourly strategy can build
	// EMA200 / multi-day structure; the prompt prints only the most recent bars
	// (see kernel.promptDisplayLimit), so a long window costs no extra tokens.
	MaxKlineCount     = 200
	MinLeverage       = 1
	MaxBTCETHLeverage = 20
	MaxAltLeverage    = 20
	MinPositionRatio  = 0.5
	MaxPositionRatio  = 10.0
	MinRiskReward     = 1.0
	MaxRiskReward     = 10.0
	MinMarginUsage    = 0.1
	MaxMarginUsage    = 1.0
	// DefaultMarginUsage is the product default for max margin usage. An unset
	// or invalid value maps to it, so a missing field can never silently
	// tighten the cap to MinMarginUsage nor disable the check.
	DefaultMarginUsage = 0.9
	MinPositionSize    = 10.0
	MaxPositionSize    = 1000.0
	MinConfidence      = 50
	MaxConfidence      = 100
	// DefaultPromptDisplayBars is how many bars / indicator values the prompt
	// prints per timeframe when the config does not say. The analysis window can
	// be far longer; only this much of it reaches the model. Twenty is enough to
	// read slope and a fresh cross without printing forty points of every series
	// for every candidate.
	DefaultPromptDisplayBars = 20
	// Token-estimate costs, calibrated against the prompt the engine actually
	// builds (kernel/estimate_drift_test.go measures the two against each other).
	// The estimate is a pre-flight guard, so it is allowed to overshoot slightly —
	// it must never undershoot.
	promptKlineRowChars       = 50 // compact CSV row (timestamp + OHLC + volume)
	promptIndicatorValueChars = 9  // one formatted value inside a series
	promptIndicatorLabelChars = 16 // "EMA(200): " style label
	promptGaugeChars          = 30 // ATR / ADX / DI / SUPERTREND single value
	promptMicrostructureChars = 170
	promptPositioningChars    = 210
	// Cross-venue: header, one line per venue (up to three), a spread/crowding
	// line and the total caveat. Measured at ~503 chars for a three-venue,
	// cascade-adjacent worst case (kernel/venue_prompt_test.go).
	promptCrossVenueChars = 520
	// Liquidation flow: header, the window line and an optional cascade line.
	// Measured at ~238 chars.
	promptLiquidationChars = 260
	// The holder-structure block is a header, four reading lines and up to five
	// bucket rows. Kept above the rendered size on purpose: the estimate must never
	// undershoot, and kernel/estimate_drift_test.go fails if it does.
	promptPositionProfileChars    = 1100
	promptVenueContextChars       = 120 // OI + funding lines per coin
	promptSystemBaseCharsEN       = 7400
	promptSystemBaseCharsZH       = 6900
	promptSystemCharsPerIndicator = 300 // one confluence group per enabled indicator
	// Hyperliquid ranking default: the venue's volume board. Liquidity is the
	// right funnel for a trend strategy — the most traded assets have tight
	// books and track their underlying best — while trend and timing stay with
	// the indicators. The gainers board is the day's most extended names, which
	// is the worst place to enter a trend (and its 24h change mixes the
	// overnight gap with the session move).
	DefaultHyperRankDirection = "volume"
	DefaultHyperRankLimit     = 10
	// Entry order modes. The default is the historical behaviour (aggressive
	// IOC), so an unset or unrecognized value can never change how orders are
	// submitted.
	EntryOrderModeMarket = "market"
	EntryOrderModeMaker  = "maker"
	// Maker entry wait bounds: long enough to catch a passive fill, short enough
	// not to stall the decision loop (decisions execute sequentially).
	DefaultMakerEntryTimeoutSec = 20
	MinMakerEntryTimeoutSec     = 5
	MaxMakerEntryTimeoutSec     = 60
	// Stock session gate bounds. 30 minutes of venue silence is a conservative
	// read of "the cash market is closed"; a quiet midday lull can reach it too,
	// so the value is clamped to a sane range rather than left open.
	DefaultStockQuietMinutes = 30
	MinStockQuietMinutes     = 5
	MaxStockQuietMinutes     = 120
	// Correlated-exposure cap. Positions on the same side whose primary-timeframe
	// returns correlate at or above the threshold count as ONE bet: three names at
	// rho >= 0.8 are one bet with three fees, not three independent trades.
	DefaultMaxCorrelatedPositions = 2
	DefaultCorrelationThreshold   = 0.8
	MinCorrelationThreshold       = 0.5
	MaxCorrelationThreshold       = 0.99
)

// NormalizeEntryOrderMode maps any stored value onto a mode the order path
// understands. Unknown or empty values fall back to "market": a typo must not
// silently switch live trading to resting maker orders.
func NormalizeEntryOrderMode(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), EntryOrderModeMaker) {
		return EntryOrderModeMaker
	}
	return EntryOrderModeMarket
}

// NormalizeMakerEntryTimeoutSec clamps the maker-entry wait into bounds. Unset
// or invalid values map to the default rather than to the minimum, so omitting
// the field means "use the default".
func NormalizeMakerEntryTimeoutSec(v int) int {
	if v <= 0 {
		return DefaultMakerEntryTimeoutSec
	}
	if v < MinMakerEntryTimeoutSec {
		return MinMakerEntryTimeoutSec
	}
	if v > MaxMakerEntryTimeoutSec {
		return MaxMakerEntryTimeoutSec
	}
	return v
}

// NormalizeStockQuietMinutes clamps the stock quiet window into bounds. Unset or
// invalid values map to the default rather than the minimum, so omitting the
// field means "use the default".
func NormalizeStockQuietMinutes(v int) int {
	if v <= 0 {
		return DefaultStockQuietMinutes
	}
	if v < MinStockQuietMinutes {
		return MinStockQuietMinutes
	}
	if v > MaxStockQuietMinutes {
		return MaxStockQuietMinutes
	}
	return v
}

// NormalizeMarginUsageLimit clamps a max-margin-usage fraction (0.9 = 90%)
// into the product bounds. Unset or invalid values (<= 0) map to
// DefaultMarginUsage rather than the minimum: omitting the field must mean
// "use the default", not "silently cap margin at 10%", and the pre-trade gate
// uses this same function so no write path can disable the cap with a zero.
func NormalizeMarginUsageLimit(v float64) float64 {
	if v <= 0 {
		return DefaultMarginUsage
	}
	if v < MinMarginUsage {
		return MinMarginUsage
	}
	if v > MaxMarginUsage {
		return MaxMarginUsage
	}
	return v
}

// ClampLimits enforces product-level limits on strategy config to prevent token overflow.
func (c *StrategyConfig) ClampLimits() {
	c.NormalizeProductSchema()

	// Clamp coin source limits
	if c.CoinSource.AI500Limit > MaxCandidateCoins {
		c.CoinSource.AI500Limit = MaxCandidateCoins
	}
	if c.CoinSource.OITopLimit > MaxCandidateCoins {
		c.CoinSource.OITopLimit = MaxCandidateCoins
	}
	if c.CoinSource.OILowLimit > MaxCandidateCoins {
		c.CoinSource.OILowLimit = MaxCandidateCoins
	}
	if c.CoinSource.HyperRankLimit > MaxCandidateCoins {
		c.CoinSource.HyperRankLimit = MaxCandidateCoins
	}

	// Fill protection defaults (zero fields -> DefaultProtectionConfig)
	c.Protections = c.Protections.Normalized()

	// Clamp static coins
	if len(c.CoinSource.StaticCoins) > MaxCandidateCoins {
		c.CoinSource.StaticCoins = c.CoinSource.StaticCoins[:MaxCandidateCoins]
	}

	// Clamp kline count
	if c.Indicators.Klines.PrimaryCount < MinKlineCount {
		c.Indicators.Klines.PrimaryCount = MinKlineCount
	}
	if c.Indicators.Klines.PrimaryCount > MaxKlineCount {
		c.Indicators.Klines.PrimaryCount = MaxKlineCount
	}
	if c.Indicators.Klines.LongerCount > MaxKlineCount {
		c.Indicators.Klines.LongerCount = MaxKlineCount
	}

	// Clamp timeframes
	if len(c.Indicators.Klines.SelectedTimeframes) > MaxTimeframes {
		c.Indicators.Klines.SelectedTimeframes = c.Indicators.Klines.SelectedTimeframes[:MaxTimeframes]
	}

	// Clamp max positions
	if c.RiskControl.MaxPositions < 1 {
		c.RiskControl.MaxPositions = 1
	}
	if c.RiskControl.MaxPositions > MaxPositions {
		c.RiskControl.MaxPositions = MaxPositions
	}

	// Normalize the entry order mode and its wait bound, so no write path can
	// leave an unrecognized mode or an out-of-bounds timeout stored.
	c.RiskControl.EntryOrderMode = NormalizeEntryOrderMode(c.RiskControl.EntryOrderMode)
	c.RiskControl.MakerEntryTimeoutSec = NormalizeMakerEntryTimeoutSec(c.RiskControl.MakerEntryTimeoutSec)
	c.RiskControl.StockQuietMinutes = NormalizeStockQuietMinutes(c.RiskControl.StockQuietMinutes)
	c.RiskControl.CorrelationThreshold = NormalizeCorrelationThreshold(c.RiskControl.CorrelationThreshold)
	if c.RiskControl.MaxCorrelatedPositions > MaxPositions {
		c.RiskControl.MaxCorrelatedPositions = MaxPositions
	}

	// Clamp leverage limits to the same bounds as the manual config UI.
	if c.RiskControl.BTCETHMaxLeverage < MinLeverage {
		c.RiskControl.BTCETHMaxLeverage = MinLeverage
	}
	if c.RiskControl.BTCETHMaxLeverage > MaxBTCETHLeverage {
		c.RiskControl.BTCETHMaxLeverage = MaxBTCETHLeverage
	}
	if c.RiskControl.AltcoinMaxLeverage < MinLeverage {
		c.RiskControl.AltcoinMaxLeverage = MinLeverage
	}
	if c.RiskControl.AltcoinMaxLeverage > MaxAltLeverage {
		c.RiskControl.AltcoinMaxLeverage = MaxAltLeverage
	}

	// Clamp position value ratio limits.
	if c.RiskControl.BTCETHMaxPositionValueRatio < MinPositionRatio {
		c.RiskControl.BTCETHMaxPositionValueRatio = MinPositionRatio
	}
	if c.RiskControl.BTCETHMaxPositionValueRatio > MaxPositionRatio {
		c.RiskControl.BTCETHMaxPositionValueRatio = MaxPositionRatio
	}
	if c.RiskControl.AltcoinMaxPositionValueRatio < MinPositionRatio {
		c.RiskControl.AltcoinMaxPositionValueRatio = MinPositionRatio
	}
	if c.RiskControl.AltcoinMaxPositionValueRatio > MaxPositionRatio {
		c.RiskControl.AltcoinMaxPositionValueRatio = MaxPositionRatio
	}

	// Clamp risk parameters and entry requirements.
	if c.RiskControl.MinRiskRewardRatio < MinRiskReward {
		c.RiskControl.MinRiskRewardRatio = MinRiskReward
	}
	if c.RiskControl.MinRiskRewardRatio > MaxRiskReward {
		c.RiskControl.MinRiskRewardRatio = MaxRiskReward
	}
	c.RiskControl.MaxMarginUsage = NormalizeMarginUsageLimit(c.RiskControl.MaxMarginUsage)
	if c.RiskControl.MinPositionSize < MinPositionSize {
		c.RiskControl.MinPositionSize = MinPositionSize
	}
	if c.RiskControl.MinPositionSize > MaxPositionSize {
		c.RiskControl.MinPositionSize = MaxPositionSize
	}
	if c.RiskControl.MinConfidence < MinConfidence {
		c.RiskControl.MinConfidence = MinConfidence
	}
	if c.RiskControl.MinConfidence > MaxConfidence {
		c.RiskControl.MinConfidence = MaxConfidence
	}
}

// NormalizeProductSchema keeps saved strategy JSON aligned with the product
// editor schema. LLMs may emit user-facing labels such as "AI500"; persistence
// must use the exact frontend/backend enum values.
func (c *StrategyConfig) NormalizeProductSchema() {
	c.StrategyType = normalizeStrategyType(c.StrategyType)
	c.CoinSource.SourceType = normalizeCoinSourceType(c.CoinSource.SourceType)
	if c.CoinSource.SourceType == "" {
		c.CoinSource.SourceType = inferCoinSourceType(c.CoinSource)
	}

	switch c.CoinSource.SourceType {
	case "ai500":
		c.CoinSource.UseAI500 = true
		c.CoinSource.UseOITop = false
		c.CoinSource.UseOILow = false
		c.CoinSource.UseHyperAll = false
		c.CoinSource.UseHyperMain = false
		if c.CoinSource.AI500Limit <= 0 {
			c.CoinSource.AI500Limit = 3
		}
	case "oi_top":
		c.CoinSource.UseAI500 = false
		c.CoinSource.UseOITop = true
		c.CoinSource.UseOILow = false
		c.CoinSource.UseHyperAll = false
		c.CoinSource.UseHyperMain = false
		if c.CoinSource.OITopLimit <= 0 {
			c.CoinSource.OITopLimit = 3
		}
	case "oi_low":
		c.CoinSource.UseAI500 = false
		c.CoinSource.UseOITop = false
		c.CoinSource.UseOILow = true
		c.CoinSource.UseHyperAll = false
		c.CoinSource.UseHyperMain = false
		if c.CoinSource.OILowLimit <= 0 {
			c.CoinSource.OILowLimit = 3
		}
	case "static":
		c.CoinSource.UseAI500 = false
		c.CoinSource.UseOITop = false
		c.CoinSource.UseOILow = false
		c.CoinSource.UseHyperAll = false
		c.CoinSource.UseHyperMain = false
	case "hyper_all":
		c.CoinSource.UseAI500 = false
		c.CoinSource.UseOITop = false
		c.CoinSource.UseOILow = false
		c.CoinSource.UseHyperAll = true
		c.CoinSource.UseHyperMain = false
	case "hyper_main":
		c.CoinSource.UseAI500 = false
		c.CoinSource.UseOITop = false
		c.CoinSource.UseOILow = false
		c.CoinSource.UseHyperAll = false
		c.CoinSource.UseHyperMain = true
		if c.CoinSource.HyperMainLimit <= 0 {
			c.CoinSource.HyperMainLimit = 30
		}
	case "hyper_rank":
		c.CoinSource.UseAI500 = false
		c.CoinSource.UseOITop = false
		c.CoinSource.UseOILow = false
		c.CoinSource.UseHyperAll = false
		c.CoinSource.UseHyperMain = false
		if c.CoinSource.HyperRankCategory == "" {
			c.CoinSource.HyperRankCategory = "stock"
		}
		if c.CoinSource.HyperRankDirection == "" {
			c.CoinSource.HyperRankDirection = DefaultHyperRankDirection
		}
		if c.CoinSource.HyperRankLimit <= 0 {
			c.CoinSource.HyperRankLimit = DefaultHyperRankLimit
		}
	default:
		c.CoinSource.SourceType = "hyper_rank"
		c.CoinSource.UseAI500 = false
		c.CoinSource.UseOITop = false
		c.CoinSource.UseOILow = false
		c.CoinSource.UseHyperAll = false
		c.CoinSource.UseHyperMain = false
		if c.CoinSource.HyperRankCategory == "" {
			c.CoinSource.HyperRankCategory = "stock"
		}
		if c.CoinSource.HyperRankDirection == "" {
			c.CoinSource.HyperRankDirection = DefaultHyperRankDirection
		}
		if c.CoinSource.HyperRankLimit <= 0 {
			c.CoinSource.HyperRankLimit = DefaultHyperRankLimit
		}
	}

	c.CoinSource.StaticCoins = normalizeSymbols(c.CoinSource.StaticCoins)
	c.CoinSource.ExcludedCoins = normalizeSymbols(c.CoinSource.ExcludedCoins)
	c.Indicators.Klines.PrimaryTimeframe = normalizeTimeframe(c.Indicators.Klines.PrimaryTimeframe)
	c.Indicators.Klines.LongerTimeframe = normalizeTimeframe(c.Indicators.Klines.LongerTimeframe)
	c.Indicators.Klines.SelectedTimeframes = normalizeTimeframes(c.Indicators.Klines.SelectedTimeframes)
	if len(c.Indicators.Klines.SelectedTimeframes) > 0 {
		c.Indicators.Klines.EnableMultiTimeframe = true
	}

	c.ensurePrimaryTimeframe()
}

// ensurePrimaryTimeframe keeps the primary timeframe in the selected list. The
// primary frame drives candle-close alignment and is the only frame that prints
// ADX / Supertrend, so a write path that selects a primary outside the list
// (API, agent, imported config) silently loses those gauges and under-counts the
// prompt timeframes. The strategy studio enforces this in its timeframe
// toggles; the stored config has to as well.
func (c *StrategyConfig) ensurePrimaryTimeframe() {
	primary := c.Indicators.Klines.PrimaryTimeframe
	if primary == "" {
		return
	}

	selected := c.Indicators.Klines.SelectedTimeframes
	for _, tf := range selected {
		if tf == primary {
			return
		}
	}

	if len(selected) == 0 {
		c.Indicators.Klines.SelectedTimeframes = []string{primary}
		return
	}

	next := append([]string{primary}, selected...)
	if len(next) > MaxTimeframes {
		next = next[:MaxTimeframes]
	}
	c.Indicators.Klines.SelectedTimeframes = next
}

func normalizeStrategyType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "grid", "grid_strategy", "grid-trading", "grid trading", "grid_trading", "网格", "网格策略", "网格交易":
		return "grid_trading"
	case "", "ai", "ai_strategy", "ai-trading", "ai trading", "ai_trading", "ai策略", "ai 策略", "ai交易策略", "ai智能策略":
		return "ai_trading"
	default:
		return value
	}
}

func normalizeCoinSourceType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	compact := strings.NewReplacer(" ", "", "_", "", "-", "", "数据源", "", "选币", "", "币种", "").Replace(value)
	switch {
	case compact == "":
		return ""
	case strings.Contains(compact, "ai500"):
		return "ai500"
	case strings.Contains(compact, "oitop") || strings.Contains(value, "oi top") || strings.Contains(value, "持仓量最高") || strings.Contains(value, "持仓量靠前"):
		return "oi_top"
	case strings.Contains(compact, "oilow") || strings.Contains(value, "oi low") || strings.Contains(value, "持仓量最低") || strings.Contains(value, "持仓量较低"):
		return "oi_low"
	case strings.Contains(compact, "hyperrank") || strings.Contains(compact, "dynamicranking") || strings.Contains(value, "动态榜单") || strings.Contains(value, "涨幅榜"):
		return "hyper_rank"
	case strings.Contains(compact, "hyperall"):
		return "hyper_all"
	case strings.Contains(compact, "hypermain"):
		return "hyper_main"
	case strings.Contains(value, "static") || strings.Contains(value, "固定") || strings.Contains(value, "静态"):
		return "static"
	default:
		return value
	}
}

func inferCoinSourceType(source CoinSourceConfig) string {
	switch {
	case len(source.StaticCoins) > 0:
		return "static"
	case source.UseAI500:
		return "ai500"
	case source.UseOITop:
		return "oi_top"
	case source.UseOILow:
		return "oi_low"
	case source.UseHyperAll:
		return "hyper_all"
	case source.UseHyperMain:
		return "hyper_main"
	case source.HyperRankCategory != "" || source.HyperRankDirection != "" || source.HyperRankLimit > 0:
		return "hyper_rank"
	default:
		return "hyper_rank"
	}
}

func normalizeSymbols(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range splitLooseStringList(values) {
		value = strings.ToUpper(strings.TrimSpace(value))
		value = strings.Trim(value, "，,;； ")
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func normalizeTimeframes(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range splitLooseStringList(values) {
		tf := normalizeTimeframe(value)
		if tf == "" || seen[tf] {
			continue
		}
		seen[tf] = true
		out = append(out, tf)
	}
	return out
}

func splitLooseStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	joined := strings.TrimSpace(strings.Join(values, ","))
	if strings.HasPrefix(joined, "[") && strings.HasSuffix(joined, "]") {
		var parsed []string
		if err := json.Unmarshal([]byte(joined), &parsed); err == nil {
			return parsed
		}
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			var parsed []string
			if err := json.Unmarshal([]byte(value), &parsed); err == nil {
				parts = append(parts, parsed...)
				continue
			}
		}
		value = strings.Trim(value, "[]")
		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n'
		}) {
			part = strings.Trim(strings.TrimSpace(part), "\"'")
			if part != "" {
				parts = append(parts, part)
			}
		}
	}
	return parts
}

func normalizeTimeframe(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, "\"'，,。 ")
	if value == "" {
		return ""
	}
	aliases := map[string]string{
		"1分钟":  "1m",
		"3分钟":  "3m",
		"5分钟":  "5m",
		"15分钟": "15m",
		"30分钟": "30m",
		"1小时":  "1h",
		"2小时":  "2h",
		"4小时":  "4h",
		"6小时":  "6h",
		"8小时":  "8h",
		"12小时": "12h",
		"1天":   "1d",
		"3天":   "3d",
		"1周":   "1w",
	}
	if alias, ok := aliases[value]; ok {
		return alias
	}
	allowed := map[string]bool{
		"1m": true, "3m": true, "5m": true, "15m": true, "30m": true,
		"1h": true, "2h": true, "4h": true, "6h": true, "8h": true, "12h": true,
		"1d": true, "3d": true, "1w": true,
	}
	if !allowed[value] {
		return ""
	}
	return value
}

// MergeStrategyConfig applies a partial JSON-style patch onto a full strategy config.
// Nested objects are merged recursively so omitted fields keep their previous values.
func MergeStrategyConfig(base StrategyConfig, patch map[string]any) (StrategyConfig, error) {
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return StrategyConfig{}, err
	}

	var mergedMap map[string]any
	if err := json.Unmarshal(baseJSON, &mergedMap); err != nil {
		return StrategyConfig{}, err
	}

	normalizeStrategyConfigPatch(patch)
	if fmt.Sprint(patch["strategy_type"]) == "grid_trading" {
		ensureDefaultGridConfigMap(mergedMap)
	}
	mergeJSONMaps(mergedMap, patch)

	mergedJSON, err := json.Marshal(mergedMap)
	if err != nil {
		return StrategyConfig{}, err
	}

	var merged StrategyConfig
	if err := json.Unmarshal(mergedJSON, &merged); err != nil {
		return StrategyConfig{}, err
	}
	return merged, nil
}

func DefaultGridStrategyConfig() GridStrategyConfig {
	return GridStrategyConfig{
		Symbol:                "BTCUSDT",
		GridCount:             10,
		TotalInvestment:       1000,
		Leverage:              5,
		UpperPrice:            0,
		LowerPrice:            0,
		UseATRBounds:          true,
		ATRMultiplier:         2.0,
		Distribution:          "gaussian",
		MaxDrawdownPct:        15,
		StopLossPct:           5,
		DailyLossLimitPct:     10,
		UseMakerOnly:          true,
		EnableDirectionAdjust: false,
		DirectionBiasRatio:    0.7,
	}
}

func ensureDefaultGridConfigMap(config map[string]any) {
	if config == nil {
		return
	}
	if existing, ok := config["grid_config"].(map[string]any); ok && len(existing) > 0 {
		return
	}
	defaultGrid := DefaultGridStrategyConfig()
	raw, err := json.Marshal(defaultGrid)
	if err != nil {
		return
	}
	var gridMap map[string]any
	if err := json.Unmarshal(raw, &gridMap); err != nil {
		return
	}
	config["grid_config"] = gridMap
}

func normalizeStrategyConfigPatch(patch map[string]any) {
	if patch == nil {
		return
	}

	if gridConfig, hasGrid := patch["grid_config"]; hasGrid && gridConfig != nil {
		if _, hasType := patch["strategy_type"]; !hasType {
			patch["strategy_type"] = "grid_trading"
		}
	}

	aiKeys := []string{"coin_source", "indicators", "risk_control", "prompt_sections", "custom_prompt"}
	for _, key := range aiKeys {
		value, ok := patch[key]
		if !ok {
			continue
		}
		aiConfig, _ := patch["ai_config"].(map[string]any)
		if aiConfig == nil {
			aiConfig = map[string]any{}
			patch["ai_config"] = aiConfig
		}
		aiConfig[key] = value
		delete(patch, key)
	}

	if fmt.Sprint(patch["strategy_type"]) == "grid_trading" {
		delete(patch, "ai_config")
	}

	if _, hasType := patch["strategy_type"]; hasType {
		return
	}
	if gridConfig, hasGrid := patch["grid_config"]; hasGrid && gridConfig != nil {
		patch["strategy_type"] = "grid_trading"
	}
}

func mergeJSONMaps(dst, src map[string]any) {
	for key, srcVal := range src {
		srcMap, srcIsMap := srcVal.(map[string]any)
		dstMap, dstIsMap := dst[key].(map[string]any)
		if srcIsMap && dstIsMap {
			mergeJSONMaps(dstMap, srcMap)
			continue
		}
		dst[key] = srcVal
	}
}

func StrategyClampWarnings(before, after StrategyConfig, lang string) []string {
	if lang != "zh" {
		lang = "en"
	}
	warnings := make([]string, 0, 8)
	appendInt := func(labelZH, labelEN string, from, to int) {
		if from == to {
			return
		}
		if lang == "zh" {
			warnings = append(warnings, fmt.Sprintf("%s 已从 %d 调整为 %d", labelZH, from, to))
			return
		}
		warnings = append(warnings, fmt.Sprintf("%s adjusted from %d to %d", labelEN, from, to))
	}
	appendFloat := func(labelZH, labelEN string, from, to float64) {
		if from == to {
			return
		}
		if lang == "zh" {
			warnings = append(warnings, fmt.Sprintf("%s 已从 %.2f 调整为 %.2f", labelZH, from, to))
			return
		}
		warnings = append(warnings, fmt.Sprintf("%s adjusted from %.2f to %.2f", labelEN, from, to))
	}

	appendInt("最大持仓数", "max_positions", before.RiskControl.MaxPositions, after.RiskControl.MaxPositions)
	appendInt("BTC/ETH 最大杠杆", "btc_eth_max_leverage", before.RiskControl.BTCETHMaxLeverage, after.RiskControl.BTCETHMaxLeverage)
	appendInt("山寨币最大杠杆", "altcoin_max_leverage", before.RiskControl.AltcoinMaxLeverage, after.RiskControl.AltcoinMaxLeverage)
	appendFloat("BTC/ETH 最大仓位价值倍数", "btc_eth_max_position_value_ratio", before.RiskControl.BTCETHMaxPositionValueRatio, after.RiskControl.BTCETHMaxPositionValueRatio)
	appendFloat("山寨币最大仓位价值倍数", "altcoin_max_position_value_ratio", before.RiskControl.AltcoinMaxPositionValueRatio, after.RiskControl.AltcoinMaxPositionValueRatio)
	appendFloat("最小盈亏比", "min_risk_reward_ratio", before.RiskControl.MinRiskRewardRatio, after.RiskControl.MinRiskRewardRatio)
	appendFloat("最大保证金使用率", "max_margin_usage", before.RiskControl.MaxMarginUsage, after.RiskControl.MaxMarginUsage)
	appendFloat("最小开仓金额", "min_position_size", before.RiskControl.MinPositionSize, after.RiskControl.MinPositionSize)
	appendInt("最低置信度", "min_confidence", before.RiskControl.MinConfidence, after.RiskControl.MinConfidence)
	return warnings
}

// StrategyStore strategy storage
type StrategyStore struct {
	db *gorm.DB
}

// Strategy strategy configuration
type Strategy struct {
	ID            string    `gorm:"primaryKey" json:"id"`
	UserID        string    `gorm:"column:user_id;not null;default:'';index" json:"user_id"`
	Name          string    `gorm:"not null" json:"name"`
	Description   string    `gorm:"default:''" json:"description"`
	IsActive      bool      `gorm:"column:is_active;default:false;index" json:"is_active"`
	IsDefault     bool      `gorm:"column:is_default;default:false" json:"is_default"`
	IsPublic      bool      `gorm:"column:is_public;default:false;index" json:"is_public"`    // whether visible in strategy market
	ConfigVisible bool      `gorm:"column:config_visible;default:true" json:"config_visible"` // whether config details are visible
	Config        string    `gorm:"not null;default:'{}'" json:"config"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (Strategy) TableName() string { return "strategies" }

// StrategyConfig strategy configuration details (JSON structure)
type StrategyConfig struct {
	// Strategy type: "ai_trading" (default) or "grid_trading"
	StrategyType string `json:"strategy_type,omitempty"`

	// language setting: "zh" for Chinese, "en" for English
	// This determines the language used for data formatting and prompt generation
	Language string `json:"language,omitempty"`

	// TradingMode selects the prompt mode variant injected into the system
	// prompt: "aggressive", "conservative" or "scalping". Empty keeps the
	// balanced baseline (no mode section).
	TradingMode string `json:"trading_mode,omitempty"`
	// AI trading configuration fields are kept on the Go struct for engine
	// compatibility, but JSON persistence nests them under ai_config.
	CoinSource     CoinSourceConfig     `json:"-"`
	Indicators     IndicatorConfig      `json:"-"`
	CustomPrompt   string               `json:"-"`
	RiskControl    RiskControlConfig    `json:"-"`
	PromptSections PromptSectionsConfig `json:"-"`
	Protections    ProtectionConfig     `json:"-"`

	// Grid trading configuration (only used when StrategyType == "grid_trading")
	GridConfig *GridStrategyConfig `json:"grid_config,omitempty"`

	// Publish settings are shared by AI and grid strategies. The database still
	// stores the authoritative booleans on Strategy, but config JSON may carry
	// this object for agent/frontend schema consistency.
	PublishConfig *PublishStrategyConfig `json:"publish_config,omitempty"`
}

// AIStrategyConfig contains fields only used by AI trading strategies.
type AIStrategyConfig struct {
	TradingMode    string               `json:"trading_mode,omitempty"`
	CoinSource     CoinSourceConfig     `json:"coin_source"`
	Indicators     IndicatorConfig      `json:"indicators"`
	CustomPrompt   string               `json:"custom_prompt,omitempty"`
	RiskControl    RiskControlConfig    `json:"risk_control"`
	PromptSections PromptSectionsConfig `json:"prompt_sections,omitempty"`
	// Protections are code-enforced circuit breakers (drawdown halt,
	// losing-streak halt, per-symbol cooldown, low-profit pair block).
	// Zero fields are replaced by DefaultProtectionConfig on load.
	Protections ProtectionConfig `json:"protections,omitempty"`
}

// PublishStrategyConfig contains settings shared by all strategy types.
type PublishStrategyConfig struct {
	IsPublic      bool `json:"is_public"`
	ConfigVisible bool `json:"config_visible"`
}

// MarshalJSON writes the product-facing strategy schema:
// strategy_type + grid_config or ai_config + shared publish_config.
func (c StrategyConfig) MarshalJSON() ([]byte, error) {
	strategyType := strings.TrimSpace(c.StrategyType)
	if strategyType == "" {
		strategyType = "ai_trading"
	}

	out := struct {
		StrategyType  string                 `json:"strategy_type"`
		Language      string                 `json:"language,omitempty"`
		TradingMode   string                 `json:"trading_mode,omitempty"`
		AIConfig      *AIStrategyConfig      `json:"ai_config,omitempty"`
		GridConfig    *GridStrategyConfig    `json:"grid_config,omitempty"`
		PublishConfig *PublishStrategyConfig `json:"publish_config,omitempty"`
	}{
		StrategyType:  strategyType,
		Language:      c.Language,
		TradingMode:   c.TradingMode,
		PublishConfig: c.PublishConfig,
	}

	if strategyType == "grid_trading" {
		out.GridConfig = c.GridConfig
	} else {
		out.AIConfig = &AIStrategyConfig{
			TradingMode:    c.TradingMode,
			CoinSource:     c.CoinSource,
			Indicators:     c.Indicators,
			CustomPrompt:   c.CustomPrompt,
			RiskControl:    c.RiskControl,
			PromptSections: c.PromptSections,
			Protections:    c.Protections.Normalized(),
		}
	}

	return json.Marshal(out)
}

// UnmarshalJSON accepts both the new nested schema and old flat configs. Old
// top-level AI fields are normalized into the Go compatibility fields.
func (c *StrategyConfig) UnmarshalJSON(data []byte) error {
	type rawStrategyConfig struct {
		StrategyType  string                 `json:"strategy_type"`
		Language      string                 `json:"language"`
		TradingMode   string                 `json:"trading_mode"`
		AIConfig      *AIStrategyConfig      `json:"ai_config"`
		GridConfig    *GridStrategyConfig    `json:"grid_config"`
		PublishConfig *PublishStrategyConfig `json:"publish_config"`

		CoinSource     *CoinSourceConfig     `json:"coin_source"`
		Indicators     *IndicatorConfig      `json:"indicators"`
		CustomPrompt   *string               `json:"custom_prompt"`
		RiskControl    *RiskControlConfig    `json:"risk_control"`
		PromptSections *PromptSectionsConfig `json:"prompt_sections"`
		Protections    *ProtectionConfig     `json:"protections"`
	}

	var raw rawStrategyConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	c.StrategyType = raw.StrategyType
	c.Language = raw.Language
	c.TradingMode = raw.TradingMode
	c.GridConfig = raw.GridConfig
	c.PublishConfig = raw.PublishConfig

	if raw.AIConfig != nil {
		c.CoinSource = raw.AIConfig.CoinSource
		c.Indicators = raw.AIConfig.Indicators
		c.CustomPrompt = raw.AIConfig.CustomPrompt
		c.RiskControl = raw.AIConfig.RiskControl
		c.PromptSections = raw.AIConfig.PromptSections
		c.Protections = raw.AIConfig.Protections
		if raw.AIConfig.TradingMode != "" {
			c.TradingMode = raw.AIConfig.TradingMode
		}
	} else {
		if raw.CoinSource != nil {
			c.CoinSource = *raw.CoinSource
		}
		if raw.Indicators != nil {
			c.Indicators = *raw.Indicators
		}
		if raw.CustomPrompt != nil {
			c.CustomPrompt = *raw.CustomPrompt
		}
		if raw.RiskControl != nil {
			c.RiskControl = *raw.RiskControl
		}
		if raw.PromptSections != nil {
			c.PromptSections = *raw.PromptSections
		}
		if raw.Protections != nil {
			c.Protections = *raw.Protections
		}
	}

	if strings.TrimSpace(c.StrategyType) == "" && c.GridConfig != nil {
		c.StrategyType = "grid_trading"
	}
	return nil
}

// GridStrategyConfig grid trading specific configuration
type GridStrategyConfig struct {
	// Trading pair (e.g., "BTCUSDT")
	Symbol string `json:"symbol"`
	// Number of grid levels (5-50)
	GridCount int `json:"grid_count"`
	// Total investment in USDT
	TotalInvestment float64 `json:"total_investment"`
	// Leverage (1-20)
	Leverage int `json:"leverage"`
	// Upper price boundary (0 = auto-calculate from ATR)
	UpperPrice float64 `json:"upper_price"`
	// Lower price boundary (0 = auto-calculate from ATR)
	LowerPrice float64 `json:"lower_price"`
	// Use ATR to auto-calculate bounds
	UseATRBounds bool `json:"use_atr_bounds"`
	// ATR multiplier for bound calculation (default 2.0)
	ATRMultiplier float64 `json:"atr_multiplier"`
	// Position distribution: "uniform" | "gaussian" | "pyramid"
	Distribution string `json:"distribution"`
	// Maximum drawdown percentage before emergency exit
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`
	// Stop loss percentage per position
	StopLossPct float64 `json:"stop_loss_pct"`
	// Daily loss limit percentage
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct"`
	// Use maker-only orders for lower fees
	UseMakerOnly bool `json:"use_maker_only"`
	// Enable automatic grid direction adjustment based on box breakouts
	EnableDirectionAdjust bool `json:"enable_direction_adjust"`
	// Direction bias ratio for long_bias/short_bias modes (default 0.7 = 70%/30%)
	DirectionBiasRatio float64 `json:"direction_bias_ratio"`
}

// PromptSectionsConfig editable sections of System Prompt
type PromptSectionsConfig struct {
	// role definition (title + description)
	RoleDefinition string `json:"role_definition,omitempty"`
	// trading frequency awareness
	TradingFrequency string `json:"trading_frequency,omitempty"`
	// entry standards
	EntryStandards string `json:"entry_standards,omitempty"`
	// decision process
	DecisionProcess string `json:"decision_process,omitempty"`
}

// CoinSourceConfig coin source configuration
type CoinSourceConfig struct {
	// source type shown in the product editor: "static" | "ai500" | "oi_top" | "oi_low"
	SourceType string `json:"source_type"`
	// static coin list (used when source_type = "static")
	StaticCoins []string `json:"static_coins,omitempty"`
	// excluded coins list (filtered out from all sources)
	ExcludedCoins []string `json:"excluded_coins,omitempty"`
	// whether to use AI500 coin pool
	UseAI500 bool `json:"use_ai500"`
	// AI500 coin pool maximum count
	AI500Limit int `json:"ai500_limit,omitempty"`
	// whether to use OI Top (OI increase ranking, suitable for long positions)
	UseOITop bool `json:"use_oi_top"`
	// OI Top maximum count
	OITopLimit int `json:"oi_top_limit,omitempty"`
	// whether to use OI Low (OI decrease ranking, suitable for short positions)
	UseOILow bool `json:"use_oi_low"`
	// OI Low maximum count
	OILowLimit int `json:"oi_low_limit,omitempty"`
	// whether to use Hyperliquid All coins (all available perp pairs)
	UseHyperAll bool `json:"use_hyper_all"`
	// whether to use Hyperliquid Main coins (top N by 24h volume)
	UseHyperMain bool `json:"use_hyper_main"`
	// Hyperliquid Main maximum count (default 20)
	HyperMainLimit int `json:"hyper_main_limit,omitempty"`
	// Hyperliquid dynamic ranking category: stock, commodity, index, forex, pre_ipo, crypto, all
	HyperRankCategory string `json:"hyper_rank_category,omitempty"`
	// Hyperliquid dynamic ranking direction: volume (default), gainers, losers
	HyperRankDirection string `json:"hyper_rank_direction,omitempty"`
	// Hyperliquid dynamic ranking maximum count. Defaults to 10 (the volume
	// board) and is hard capped at MaxCandidateCoins for AI context safety.
	HyperRankLimit int `json:"hyper_rank_limit,omitempty"`
	// Note: API URLs are now built automatically using NofxOSAPIKey from IndicatorConfig
}

// IndicatorConfig indicator configuration
type IndicatorConfig struct {
	// K-line configuration
	Klines KlineConfig `json:"klines"`
	// raw kline data (OHLCV) - always enabled, required for AI analysis
	EnableRawKlines bool `json:"enable_raw_klines"`
	// technical indicator switches
	EnableEMA         bool `json:"enable_ema"`
	EnableMACD        bool `json:"enable_macd"`
	EnableRSI         bool `json:"enable_rsi"`
	EnableATR         bool `json:"enable_atr"`
	EnableBOLL        bool `json:"enable_boll"` // Bollinger Bands
	EnableVolume      bool `json:"enable_volume"`
	EnableOI          bool `json:"enable_oi"`           // open interest
	EnableFundingRate bool `json:"enable_funding_rate"` // funding rate
	EnableADX         bool `json:"enable_adx"`          // ADX trend strength (+DI/-DI)
	EnableSupertrend  bool `json:"enable_supertrend"`   // Supertrend band + direction
	// Microstructure book metrics (spread/imbalance/depth/liquidity). Nil =
	// enabled: the pointer avoids old configs silently disabling the feature
	// on unmarshal (bool zero-value trap).
	EnableMicrostructure *bool `json:"enable_microstructure,omitempty"`
	// Hyperliquid-native positioning metrics (funding percentile, open-interest
	// quadrants, taker buy/sell flow). Nil = enabled, the same pointer
	// convention as EnableMicrostructure so an existing config cannot silently
	// turn the feature off on unmarshal.
	EnablePositioning *bool `json:"enable_positioning,omitempty"`
	// Cross-venue positioning (per-venue OI, funding spread, crowding). Nil =
	// enabled: it costs one market-wide request per venue per minute.
	EnableCrossVenue *bool `json:"enable_cross_venue,omitempty"`
	// Liquidation flow (Binance/Bybit public liquidation streams). Nil =
	// enabled: it is a websocket, not a polling cost.
	EnableLiquidationFlow *bool `json:"enable_liquidation_flow,omitempty"`
	// Holder cost/liquidation structure (where holders entered and where they get
	// liquidated), built from publicly readable venue positions. Nil = disabled:
	// unlike the two flags above, this one runs a background index that polls the
	// venue per address, so it is opt-in rather than on for every legacy config.
	EnablePositionStructure *bool `json:"enable_position_structure,omitempty"`
	// EMA period configuration
	EMAPeriods []int `json:"ema_periods,omitempty"` // default [20, 50]
	// RSI period configuration
	RSIPeriods []int `json:"rsi_periods,omitempty"` // default [7, 14]
	// ATR period configuration
	ATRPeriods []int `json:"atr_periods,omitempty"` // default [14]
	// MACD period configuration (fast/slow EMA; default 12 / 26, fast must be < slow)
	MACDFastPeriod int `json:"macd_fast_period,omitempty"`
	MACDSlowPeriod int `json:"macd_slow_period,omitempty"`
	// ADX period configuration (default [14]); emits ADX plus DI+/DI-.
	ADXPeriods []int `json:"adx_periods,omitempty"`
	// Supertrend configuration (default period 10, multiplier 3.0); emits the
	// trailing band plus its direction (+1 up / -1 down).
	SupertrendPeriod     int     `json:"supertrend_period,omitempty"`
	SupertrendMultiplier float64 `json:"supertrend_multiplier,omitempty"`
	// BOLL period configuration (period, standard deviation multiplier)
	BOLLPeriods []int `json:"boll_periods,omitempty"` // default [20]
	// BOLL standard-deviation multiplier (default 2.0; 2.5 widens the bands)
	BOLLMultiplier float64 `json:"boll_multiplier,omitempty"`
	// external data sources
	ExternalDataSources []ExternalDataSource `json:"external_data_sources,omitempty"`

	// ========== NofxOS Unified API Configuration ==========
	// Unified API Key for all NofxOS data sources
	NofxOSAPIKey string `json:"nofxos_api_key,omitempty"`

	// quantitative data sources (exchange-native, free)
	EnableQuantData bool `json:"enable_quant_data"` // whether to enable quantitative data
	EnableQuantOI   bool `json:"enable_quant_oi"`   // whether to show OI data

	// OI ranking data (market-wide open interest increase/decrease rankings)
	EnableOIRanking   bool   `json:"enable_oi_ranking"`             // whether to enable OI ranking data
	OIRankingDuration string `json:"oi_ranking_duration,omitempty"` // duration: 1h, 4h, 24h
	OIRankingLimit    int    `json:"oi_ranking_limit,omitempty"`    // number of entries (default 10)

	// NetFlow ranking data (market-wide fund flow rankings - institution/personal)
	// Taker flow ranking (market-wide buy/sell imbalance). The JSON keys keep
	// their historical "netflow" names so existing strategies keep loading.
	EnableTakerFlowRanking   bool   `json:"enable_netflow_ranking"`
	TakerFlowRankingDuration string `json:"netflow_ranking_duration,omitempty"`
	TakerFlowRankingLimit    int    `json:"netflow_ranking_limit,omitempty"`

	// Price ranking data (market-wide gainers/losers)
	EnablePriceRanking   bool   `json:"enable_price_ranking"`             // whether to enable price ranking data
	PriceRankingDuration string `json:"price_ranking_duration,omitempty"` // durations: "1h" or "1h,4h,24h"
	PriceRankingLimit    int    `json:"price_ranking_limit,omitempty"`    // number of entries per ranking (default 10)
}

// PositioningEnabled reports whether the Hyperliquid-native positioning
// metrics should be computed and rendered. Nil means enabled.
func (c *IndicatorConfig) PositioningEnabled() bool {
	return c.EnablePositioning == nil || *c.EnablePositioning
}

// PositionStructureEnabled reports whether the holder cost/liquidation structure
// should be indexed and rendered. Nil means disabled: the index polls the venue
// per address, so it is opt-in.
func (c *IndicatorConfig) PositionStructureEnabled() bool {
	return c.EnablePositionStructure != nil && *c.EnablePositionStructure
}

// CrossVenueEnabled reports whether the per-venue OI/funding view should be
// collected and rendered. Nil means enabled.
func (c *IndicatorConfig) CrossVenueEnabled() bool {
	return c.EnableCrossVenue == nil || *c.EnableCrossVenue
}

// LiquidationFlowEnabled reports whether the liquidation streams should be
// collected and rendered. Nil means enabled.
func (c *IndicatorConfig) LiquidationFlowEnabled() bool {
	return c.EnableLiquidationFlow == nil || *c.EnableLiquidationFlow
}

// KlineConfig K-line configuration
type KlineConfig struct {
	// primary timeframe: "1m", "3m", "5m", "15m", "1h", "4h"
	PrimaryTimeframe string `json:"primary_timeframe"`
	// primary timeframe K-line count
	PrimaryCount int `json:"primary_count"`
	// longer timeframe
	LongerTimeframe string `json:"longer_timeframe,omitempty"`
	// longer timeframe K-line count
	LongerCount int `json:"longer_count,omitempty"`
	// whether to enable multi-timeframe analysis
	EnableMultiTimeframe bool `json:"enable_multi_timeframe"`
	// selected timeframe list (new: supports multi-timeframe selection)
	SelectedTimeframes []string `json:"selected_timeframes,omitempty"`
	// PromptDisplayBars caps how many bars / indicator values are printed per
	// timeframe in the AI prompt. The analysis window (PrimaryCount) can be far
	// longer — an hourly strategy needs 200 bars for EMA200 — but the model only
	// needs the recent shape, so printing all of it would inflate every prompt
	// without adding information. 0 = the product default (40).
	PromptDisplayBars int `json:"prompt_display_bars,omitempty"`
}

// EffectivePromptDisplayBars returns the display cap the prompt writer applies:
// the configured value when set, otherwise the product default. This is the cap
// on BOTH the candle table and the indicator series, and it is what the token
// estimate has to use — the analysis window (`primary_count`) can be much larger
// than what is printed.
func (k KlineConfig) EffectivePromptDisplayBars() int {
	if k.PromptDisplayBars > 0 {
		return k.PromptDisplayBars
	}
	return DefaultPromptDisplayBars
}

// ExternalDataSource external data source configuration
type ExternalDataSource struct {
	Name        string            `json:"name"`   // data source name
	Type        string            `json:"type"`   // type: "api" | "webhook"
	URL         string            `json:"url"`    // API URL
	Method      string            `json:"method"` // HTTP method
	Headers     map[string]string `json:"headers,omitempty"`
	DataPath    string            `json:"data_path,omitempty"`    // JSON data path
	RefreshSecs int               `json:"refresh_secs,omitempty"` // refresh interval (seconds)
}

// RiskControlConfig risk control configuration
type RiskControlConfig struct {
	// Max number of coins held simultaneously (CODE ENFORCED)
	MaxPositions int `json:"max_positions"`

	// BTC/ETH exchange leverage for opening positions (AI guided)
	BTCETHMaxLeverage int `json:"btc_eth_max_leverage"`
	// Altcoin exchange leverage for opening positions (AI guided)
	AltcoinMaxLeverage int `json:"altcoin_max_leverage"`

	// BTC/ETH single position max value = equity × this ratio (CODE ENFORCED, default: 5)
	BTCETHMaxPositionValueRatio float64 `json:"btc_eth_max_position_value_ratio"`
	// Altcoin single position max value = equity × this ratio (CODE ENFORCED, default: 1)
	AltcoinMaxPositionValueRatio float64 `json:"altcoin_max_position_value_ratio"`

	// Max margin utilization (e.g. 0.9 = 90%) (CODE ENFORCED)
	MaxMarginUsage float64 `json:"max_margin_usage"`
	// Min position size in USDT (CODE ENFORCED)
	MinPositionSize float64 `json:"min_position_size"`

	// Min take_profit / stop_loss ratio (AI guided)
	MinRiskRewardRatio float64 `json:"min_risk_reward_ratio"`
	// Min AI confidence to open position (AI guided)
	MinConfidence int `json:"min_confidence"`
	// MinHoldMinutes is the code-enforced minimum holding period for
	// discretionary AI exits. 0 disables the rule (the default); a positive value
	// is enforced, and the strategy studio exposes it so the operator decides.
	// Exchange-side stop-loss / take-profit and the drawdown monitor are never
	// affected: their job is to protect the position, while this rule stops the
	// churn of opening and closing within one or two decision cycles.
	MinHoldMinutes int `json:"min_hold_minutes,omitempty"`

	// Entry order mode. "" or "market" (default) submits the aggressive IOC
	// order exactly as before; "maker" first rests a post-only (ALO) limit on
	// the passive side of the book to pay the maker fee, and falls back to the
	// market order when it has not filled within MakerEntryTimeoutSec. Opening
	// orders only — protective exits must never wait for a resting order.
	EntryOrderMode       string `json:"entry_order_mode,omitempty"`
	MakerEntryTimeoutSec int    `json:"maker_entry_timeout_sec,omitempty"`

	// Stock session gate (CODE ENFORCED): block NEW positions in stock perps when
	// the venue shows no activity, which is what a closed cash market looks like
	// on Hyperliquid. A stop cannot protect an entry taken into an overnight gap,
	// and the underlying is not trading while the perp quote drifts. Close and
	// reduce are never blocked. Nil enabled flag = enabled.
	BlockColdStockEntries *bool `json:"block_cold_stock_entries,omitempty"`
	// StockQuietMinutes is how long a stock may go without a venue trade before
	// new entries are blocked. 0 = default, clamped to [5, 120].
	StockQuietMinutes int `json:"stock_quiet_minutes,omitempty"`

	// Correlated exposure cap (CODE ENFORCED): same-side positions above the
	// correlation threshold count as one bet. 0 = the default cap, negative
	// disables the check. The correlation is measured on the primary timeframe's
	// closed bars, so it costs no extra request.
	MaxCorrelatedPositions int     `json:"max_correlated_positions,omitempty"`
	CorrelationThreshold   float64 `json:"correlation_threshold,omitempty"` // 0 = default 0.8

	// Drawdown profit-protection monitor (CODE ENFORCED). Nil enabled flag
	// and zero thresholds mean "keep the historical defaults" so configs
	// stored before these fields existed keep monitoring active.
	DrawdownMonitoringEnabled *bool   `json:"drawdown_monitoring_enabled,omitempty"`  // nil = enabled
	DrawdownCheckIntervalSec  int     `json:"drawdown_check_interval_sec,omitempty"`  // 0 = default 60, clamped to [15, 300]
	MinProfitThresholdPct     float64 `json:"min_profit_threshold_pct,omitempty"`     // 0 = default 2.0 (price move %)
	DrawdownCloseThresholdPct float64 `json:"drawdown_close_threshold_pct,omitempty"` // 0 = default 30.0 (% of peak profit)
}

// MakerEntry reports whether opening orders should be attempted as passive
// maker orders and how long to wait for the fill before falling back to the
// market order. Both values are normalized at read time, so a config written by
// any path (API, agent, import) behaves the same.
func (r RiskControlConfig) MakerEntry() (bool, time.Duration) {
	mode := NormalizeEntryOrderMode(r.EntryOrderMode)
	return mode == EntryOrderModeMaker,
		time.Duration(NormalizeMakerEntryTimeoutSec(r.MakerEntryTimeoutSec)) * time.Second
}

// StockEntryGate reports whether new stock entries are blocked while the venue
// looks inactive, and the quiet window that defines "inactive". Nil enabled
// flag means enabled.
func (r RiskControlConfig) StockEntryGate() (bool, int) {
	enabled := r.BlockColdStockEntries == nil || *r.BlockColdStockEntries
	return enabled, NormalizeStockQuietMinutes(r.StockQuietMinutes)
}

// NormalizeCorrelationThreshold clamps the cluster threshold into a range where
// "correlated" still means something (below 0.5 almost everything clusters;
// above 0.99 only identical series do).
func NormalizeCorrelationThreshold(v float64) float64 {
	if v <= 0 {
		return DefaultCorrelationThreshold
	}
	if v < MinCorrelationThreshold {
		return MinCorrelationThreshold
	}
	if v > MaxCorrelationThreshold {
		return MaxCorrelationThreshold
	}
	return v
}

// CorrelatedExposureCap reports whether the same-side cluster cap is active, the
// maximum number of positions allowed in one cluster, and the correlation
// threshold that defines a cluster. A negative cap disables the check.
func (r RiskControlConfig) CorrelatedExposureCap() (enabled bool, cap int, threshold float64) {
	if r.MaxCorrelatedPositions < 0 {
		return false, 0, 0
	}
	cap = r.MaxCorrelatedPositions
	if cap == 0 {
		cap = DefaultMaxCorrelatedPositions
	}
	if cap > MaxPositions {
		cap = MaxPositions
	}
	return true, cap, NormalizeCorrelationThreshold(r.CorrelationThreshold)
}

// NewStrategyStore creates a new StrategyStore
func NewStrategyStore(db *gorm.DB) *StrategyStore {
	return &StrategyStore{db: db}
}

func (s *StrategyStore) initTables() error {
	// AutoMigrate will add missing columns without dropping existing data
	return s.db.AutoMigrate(&Strategy{})
}

func (s *StrategyStore) initDefaultData() error {
	// No longer pre-populate strategies - create on demand when user configures
	return nil
}

// GetDefaultStrategyConfig returns the default strategy configuration for the given language
func GetDefaultStrategyConfig(lang string) StrategyConfig {
	// Normalize language to "zh" or "en"
	normalizedLang := "en"
	if lang == "zh" {
		normalizedLang = "zh"
	}

	config := StrategyConfig{
		Language: normalizedLang,
		CoinSource: CoinSourceConfig{
			SourceType:         "hyper_rank",
			UseAI500:           false,
			AI500Limit:         3,
			UseOITop:           false,
			OITopLimit:         3,
			UseOILow:           false,
			OILowLimit:         3,
			UseHyperAll:        false,
			UseHyperMain:       false,
			HyperMainLimit:     30,
			HyperRankCategory:  "stock",
			HyperRankDirection: DefaultHyperRankDirection,
			HyperRankLimit:     DefaultHyperRankLimit,
		},
		Indicators: IndicatorConfig{
			Klines: KlineConfig{
				PrimaryTimeframe:     "5m",
				PrimaryCount:         20,
				LongerTimeframe:      "4h",
				LongerCount:          10,
				EnableMultiTimeframe: true,
				SelectedTimeframes:   []string{"5m", "15m", "1h"},
			},
			EnableRawKlines:      true, // Required - raw OHLCV data for AI analysis
			EnableEMA:            false,
			EnableMACD:           false,
			EnableRSI:            false,
			EnableATR:            false,
			EnableBOLL:           false,
			EnableVolume:         true,
			EnableOI:             true,
			EnableFundingRate:    true,
			EMAPeriods:           []int{20, 50},
			RSIPeriods:           []int{7, 14},
			ATRPeriods:           []int{14},
			MACDFastPeriod:       12,
			MACDSlowPeriod:       26,
			ADXPeriods:           []int{14},
			SupertrendPeriod:     10,
			SupertrendMultiplier: 3.0,
			BOLLPeriods:          []int{20},
			BOLLMultiplier:       2.0,
			// Hyperliquid strategies must use native Hyperliquid market data by default.
			// NofxOS datasets do not cover all Hyperliquid XYZ assets, so keep them off.
			NofxOSAPIKey:             "",
			EnableQuantData:          false,
			EnableQuantOI:            false,
			EnableOIRanking:          false,
			OIRankingDuration:        "1h",
			OIRankingLimit:           10,
			EnableTakerFlowRanking:   false,
			TakerFlowRankingDuration: "1h",
			TakerFlowRankingLimit:    10,
			EnablePriceRanking:       false,
			PriceRankingDuration:     "1h,4h,24h",
			PriceRankingLimit:        10,
		},
		RiskControl: RiskControlConfig{
			MaxPositions:                 3,   // Max 3 coins simultaneously (CODE ENFORCED)
			BTCETHMaxLeverage:            5,   // BTC/ETH exchange leverage (AI guided)
			AltcoinMaxLeverage:           5,   // Altcoin exchange leverage (AI guided)
			BTCETHMaxPositionValueRatio:  5.0, // BTC/ETH: max position = 5x equity (CODE ENFORCED)
			AltcoinMaxPositionValueRatio: 1.0, // Altcoin: max position = 1x equity (CODE ENFORCED)
			MaxMarginUsage:               0.9, // Max 90% margin usage (CODE ENFORCED)
			MinPositionSize:              12,  // Min 12 USDT per position (CODE ENFORCED)
			MinRiskRewardRatio:           3.0, // Min 3:1 profit/loss ratio (AI guided)
			MinConfidence:                75,  // Min 75% confidence (AI guided)

			DrawdownCheckIntervalSec:  60,
			MinProfitThresholdPct:     2.0,  // price %, not margin (CODE ENFORCED)
			DrawdownCloseThresholdPct: 30.0, // % of peak profit given back
			// DrawdownMonitoringEnabled stays nil (= enabled) in defaults.
		},
	}

	if lang == "zh" {
		config.PromptSections = PromptSectionsConfig{
			RoleDefinition: `# 你是一个专业的 Hyperliquid USDC 多资产交易AI

你的任务是根据提供的市场数据做出交易决策。你可以分析并交易 Hyperliquid 上线的 USDC 永续合约，包括美股、大宗商品和加密资产。你是一个经验丰富的量化交易员，擅长跨资产技术分析和风险管理。`,
			TradingFrequency: `# ⏱️ 交易频率意识

- 优秀交易员：每天2-4笔 ≈ 每小时0.1-0.2笔
- 每小时超过2笔 = 过度交易
- 单笔持仓时间 ≥ 30-60分钟
如果你发现自己每个周期都在交易 → 标准太低；如果持仓不到30分钟就平仓 → 太冲动。`,
			EntryStandards: `# 🎯 入场标准（严格）

只在多个信号共振时入场。自由使用任何有效的分析方法，避免单一指标、信号矛盾、横盘震荡、或平仓后立即重新开仓等低质量行为。`,
			DecisionProcess: `# 📋 决策流程

1. 检查持仓 → 是否止盈/止损
2. 扫描候选币种 + 多时间框架 → 是否存在强信号
3. 先写思维链，再输出结构化JSON`,
		}
	} else {
		config.PromptSections = PromptSectionsConfig{
			RoleDefinition: `# You are a professional Hyperliquid USDC multi-asset trading AI

Your task is to make trading decisions based on the provided market data. You can analyze and trade Hyperliquid-listed USDC perpetual markets, including US equities, commodities and crypto assets. You are an experienced quantitative trader skilled in cross-asset technical analysis and risk management.`,
			TradingFrequency: `# ⏱️ Trading Frequency Awareness

- Excellent trader: 2-4 trades per day ≈ 0.1-0.2 trades per hour
- >2 trades per hour = overtrading
- Single position holding time ≥ 30-60 minutes
If you find yourself trading every cycle → standards are too low; if closing positions in <30 minutes → too impulsive.`,
			EntryStandards: `# 🎯 Entry Standards (Strict)

Only enter positions when multiple signals resonate. Freely use any effective analysis methods, avoid low-quality behaviors such as single indicators, contradictory signals, sideways oscillation, or immediately restarting after closing positions.`,
			DecisionProcess: `# 📋 Decision Process

1. Check positions → whether to take profit/stop loss
2. Scan candidate coins + multi-timeframe → whether strong signals exist
3. Write chain of thought first, then output structured JSON`,
		}
	}

	return config
}

// Create create a strategy
func (s *StrategyStore) Create(strategy *Strategy) error {
	return s.db.Create(strategy).Error
}

// Update update a strategy
func (s *StrategyStore) Update(strategy *Strategy) error {
	return s.db.Model(&Strategy{}).
		Where("id = ? AND user_id = ?", strategy.ID, strategy.UserID).
		Updates(map[string]interface{}{
			"name":           strategy.Name,
			"description":    strategy.Description,
			"config":         strategy.Config,
			"is_public":      strategy.IsPublic,
			"config_visible": strategy.ConfigVisible,
			"updated_at":     time.Now().UTC(),
		}).Error
}

// Delete delete a strategy
func (s *StrategyStore) Delete(userID, id string) error {
	// do not allow deleting system default strategy
	var st Strategy
	if err := s.db.Where("id = ?", id).First(&st).Error; err == nil {
		if st.IsDefault {
			return fmt.Errorf("cannot delete system default strategy")
		}
		if st.IsActive {
			return fmt.Errorf("cannot delete active strategy")
		}
	}

	// Check if any trader references this strategy
	var count int64
	if err := s.db.Model(&Trader{}).
		Where("user_id = ? AND strategy_id = ?", userID, id).
		Count(&count).Error; err == nil && count > 0 {
		return fmt.Errorf("cannot delete strategy in use by %d trader(s) - reassign those traders first", count)
	}

	return s.db.Where("id = ? AND user_id = ?", id, userID).Delete(&Strategy{}).Error
}

// List get user's strategy list
func (s *StrategyStore) List(userID string) ([]*Strategy, error) {
	var strategies []*Strategy
	err := s.db.Where("user_id = ? OR is_default = ?", userID, true).
		Order("is_default DESC, created_at DESC").
		Find(&strategies).Error
	if err != nil {
		return nil, err
	}
	return strategies, nil
}

// ListPublic get all public strategies for the strategy market
func (s *StrategyStore) ListPublic() ([]*Strategy, error) {
	var strategies []*Strategy
	err := s.db.Where("is_public = ?", true).
		Order("created_at DESC").
		Find(&strategies).Error
	if err != nil {
		return nil, err
	}
	return strategies, nil
}

// Get get a single strategy
func (s *StrategyStore) Get(userID, id string) (*Strategy, error) {
	var st Strategy
	err := s.db.Where("id = ? AND (user_id = ? OR is_default = ?)", id, userID, true).
		First(&st).Error
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// GetActive get user's currently active strategy
func (s *StrategyStore) GetActive(userID string) (*Strategy, error) {
	var st Strategy
	err := s.db.Where("user_id = ? AND is_active = ?", userID, true).First(&st).Error
	if err == gorm.ErrRecordNotFound {
		// no active strategy, return system default strategy
		return s.GetDefault()
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// GetDefault get system default strategy
func (s *StrategyStore) GetDefault() (*Strategy, error) {
	var st Strategy
	err := s.db.Where("is_default = ?", true).First(&st).Error
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// SetActive set active strategy (will first deactivate other strategies)
func (s *StrategyStore) SetActive(userID, strategyID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// first deactivate all strategies for the user
		if err := tx.Model(&Strategy{}).Where("user_id = ?", userID).
			Update("is_active", false).Error; err != nil {
			return err
		}

		// activate specified strategy
		return tx.Model(&Strategy{}).
			Where("id = ? AND (user_id = ? OR is_default = ?)", strategyID, userID, true).
			Update("is_active", true).Error
	})
}

// Duplicate duplicate a strategy (used to create custom strategy based on default strategy)
func (s *StrategyStore) Duplicate(userID, sourceID, newID, newName string) error {
	// get source strategy
	source, err := s.Get(userID, sourceID)
	if err != nil {
		return fmt.Errorf("failed to get source strategy: %w", err)
	}

	// create new strategy
	newStrategy := &Strategy{
		ID:          newID,
		UserID:      userID,
		Name:        newName,
		Description: "Created based on [" + source.Name + "]",
		IsActive:    false,
		IsDefault:   false,
		Config:      source.Config,
	}

	return s.Create(newStrategy)
}

// ParseConfig parse strategy configuration JSON
func (s *Strategy) ParseConfig() (*StrategyConfig, error) {
	var config StrategyConfig
	if err := json.Unmarshal([]byte(s.Config), &config); err != nil {
		return nil, fmt.Errorf("failed to parse strategy configuration: %w", err)
	}
	return &config, nil
}

// SetConfig set strategy configuration
func (s *Strategy) SetConfig(config *StrategyConfig) error {
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to serialize strategy configuration: %w", err)
	}
	s.Config = string(data)
	return nil
}

// ============================================================================
// Token Estimation
// ============================================================================

// TokenEstimate holds the result of token estimation
type TokenEstimate struct {
	Total       int            `json:"total"`
	Breakdown   TokenBreakdown `json:"breakdown"`
	ModelLimits []ModelLimit   `json:"model_limits"`
	Suggestions []string       `json:"suggestions"`
}

// TokenBreakdown shows estimated tokens per component
type TokenBreakdown struct {
	SystemPrompt  int `json:"system_prompt"`
	MarketData    int `json:"market_data"`
	RankingData   int `json:"ranking_data"`
	QuantData     int `json:"quant_data"`
	FixedOverhead int `json:"fixed_overhead"`
}

// ModelLimit shows token usage against a specific model's context limit
type ModelLimit struct {
	Name         string `json:"name"`
	ContextLimit int    `json:"context_limit"`
	UsagePct     int    `json:"usage_pct"`
	Level        string `json:"level"` // "ok" | "warning" | "danger"
}

// Context window sizes (tokens) for each model family
const (
	contextLimitDeepSeek = 131_072   // 128K
	contextLimitOpenAI   = 128_000   // 128K
	contextLimitClaude   = 200_000   // 200K
	contextLimitQwen     = 131_072   // 128K
	contextLimitGemini   = 1_000_000 // 1M
	contextLimitGrok     = 131_072   // 128K
	contextLimitKimi     = 131_072   // 128K
	contextLimitMinimax  = 1_000_000 // 1M
)

// ModelContextLimits maps provider names to their context window sizes (in tokens)
var ModelContextLimits = map[string]int{
	"deepseek": contextLimitDeepSeek,
	"openai":   contextLimitOpenAI,
	"claude":   contextLimitClaude,
	"qwen":     contextLimitQwen,
	"gemini":   contextLimitGemini,
	"grok":     contextLimitGrok,
	"kimi":     contextLimitKimi,
	"minimax":  contextLimitMinimax,
}

// GetContextLimit returns the context limit for a given provider
func GetContextLimit(provider string) int {
	if limit, ok := ModelContextLimits[provider]; ok {
		return limit
	}
	return contextLimitDeepSeek // safe default
}

// GetContextLimitForClient returns context limit for a provider+model pair.
// For claw402, the underlying model is inferred from the model name prefix.
func GetContextLimitForClient(provider, model string) int {
	if provider == "claw402" {
		switch {
		case strings.HasPrefix(model, "claude"):
			return ModelContextLimits["claude"]
		case strings.HasPrefix(model, "gpt"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"):
			return ModelContextLimits["openai"]
		case strings.HasPrefix(model, "gemini"):
			return ModelContextLimits["gemini"]
		case strings.HasPrefix(model, "grok"):
			return ModelContextLimits["grok"]
		case strings.HasPrefix(model, "kimi"):
			return ModelContextLimits["kimi"]
		case strings.HasPrefix(model, "qwen"):
			return ModelContextLimits["qwen"]
		case strings.HasPrefix(model, "minimax"):
			return ModelContextLimits["minimax"]
		case strings.HasPrefix(model, "deepseek"):
			return ModelContextLimits["deepseek"]
		default:
			return ModelContextLimits["deepseek"]
		}
	}
	return GetContextLimit(provider)
}

// EstimateTokens estimates the total token count for a strategy configuration.
// This is a pure computation based on config fields — no network calls.
func (c *StrategyConfig) EstimateTokens() TokenEstimate {
	breakdown := TokenBreakdown{}

	// --- System Prompt ---
	// Calibrated against the prompt the engine builds: base text (role, hard
	// rules, output schema, decision process, exit discipline) plus one
	// confluence group per enabled indicator/data switch.
	enabledIndicators := 0
	for _, on := range []bool{
		c.Indicators.EnableEMA,
		c.Indicators.EnableMACD,
		c.Indicators.EnableRSI,
		c.Indicators.EnableATR,
		c.Indicators.EnableBOLL,
		c.Indicators.EnableADX,
		c.Indicators.EnableSupertrend,
		c.Indicators.EnableVolume,
		c.Indicators.EnableOI,
		c.Indicators.EnableFundingRate,
		c.Indicators.PositioningEnabled(),
		c.Indicators.EnableMicrostructure == nil || *c.Indicators.EnableMicrostructure,
	} {
		if on {
			enabledIndicators++
		}
	}
	baseChars := promptSystemBaseCharsEN
	if c.Language == "zh" {
		baseChars = promptSystemBaseCharsZH
	}
	baseChars += enabledIndicators * promptSystemCharsPerIndicator
	// Add prompt sections
	baseChars += len(c.PromptSections.RoleDefinition)
	baseChars += len(c.PromptSections.TradingFrequency)
	baseChars += len(c.PromptSections.EntryStandards)
	baseChars += len(c.PromptSections.DecisionProcess)
	baseChars += len(c.CustomPrompt)

	if c.Language == "zh" {
		breakdown.SystemPrompt = baseChars / 2 // CJK: ~2 chars per token
	} else {
		breakdown.SystemPrompt = baseChars / 4 // English: ~4 chars per token
	}

	// --- Fixed Overhead ---
	// Time, BTC price, account info, section headers
	breakdown.FixedOverhead = 800 / 4 // ~200 tokens

	// --- Market Data ---
	// Mirrors how the engine builds the prompt: the candle table and every
	// indicator series are trimmed to the DISPLAY cap (`prompt_display_bars`), not
	// to the analysis window, and candidates only print the candle table for their
	// primary timeframe while positions print it for every timeframe.
	numCoins := c.getEffectiveCoinCount()
	numTimeframes := c.getEffectiveTimeframeCount()
	if numTimeframes < 1 {
		numTimeframes = 1
	}
	window := c.Indicators.Klines.PrimaryCount
	if window <= 0 {
		window = 20
	}
	displayRows := c.Indicators.Klines.EffectivePromptDisplayBars()
	if displayRows > window {
		displayRows = window
	}
	// Positions carry the full candle table on every timeframe, and their count is
	// not a config field, so the estimate assumes the configured maximum: an
	// understated estimate would let an oversized prompt through.
	positionCoins := MaxPositions

	// Rows that carry a candle table: candidates (primary frame only) plus
	// positions (every frame).
	rowsWithCandles := numCoins + positionCoins*numTimeframes
	klineChars := rowsWithCandles * displayRows * promptKlineRowChars

	// Indicator series per timeframe, using the configured periods and falling
	// back to the canonical defaults the market layer would apply.
	periodCount := func(configured, fallback int) int {
		if configured > 0 {
			return configured
		}
		return fallback
	}
	seriesPerTF := 0
	if c.Indicators.EnableEMA {
		seriesPerTF += periodCount(len(c.Indicators.EMAPeriods), 2)
	}
	if c.Indicators.EnableRSI {
		seriesPerTF += periodCount(len(c.Indicators.RSIPeriods), 2)
	}
	if c.Indicators.EnableMACD {
		seriesPerTF++
	}
	if c.Indicators.EnableBOLL {
		seriesPerTF += 3 * periodCount(len(c.Indicators.BOLLPeriods), 1)
	}
	indicatorCharsPerTF := seriesPerTF * (displayRows*promptIndicatorValueChars + promptIndicatorLabelChars)

	// Single-value gauges: ATR, ADX with DI+/DI-, Supertrend band + direction.
	gauges := 0
	if c.Indicators.EnableATR {
		gauges += periodCount(len(c.Indicators.ATRPeriods), 1)
	}
	if c.Indicators.EnableADX {
		gauges += 3 * periodCount(len(c.Indicators.ADXPeriods), 1)
	}
	if c.Indicators.EnableSupertrend {
		gauges += 2
	}
	indicatorCharsPerTF += gauges * promptGaugeChars

	indicatorChars := (numCoins + positionCoins) * numTimeframes * indicatorCharsPerTF

	// Per-coin sections the prompt emits beside the series.
	extrasPerCoin := 0
	if c.Indicators.EnableOI || c.Indicators.EnableFundingRate {
		extrasPerCoin += promptVenueContextChars
	}
	if c.Indicators.EnableMicrostructure == nil || *c.Indicators.EnableMicrostructure {
		extrasPerCoin += promptMicrostructureChars
	}
	if c.Indicators.PositioningEnabled() {
		extrasPerCoin += promptPositioningChars
	}
	if c.Indicators.CrossVenueEnabled() {
		extrasPerCoin += promptCrossVenueChars
	}
	if c.Indicators.LiquidationFlowEnabled() {
		extrasPerCoin += promptLiquidationChars
	}
	if c.Indicators.PositionStructureEnabled() {
		extrasPerCoin += promptPositionProfileChars
	}

	totalMarketChars := klineChars + indicatorChars + (numCoins+positionCoins)*extrasPerCoin

	breakdown.MarketData = totalMarketChars / 4 // numeric data: ~4 chars per token

	// --- Quant Data ---
	if c.Indicators.EnableQuantData {
		quantCharsPerCoin := 0
		if c.Indicators.EnableQuantOI {
			// OI windows, the long/short line and the taker-flow line.
			quantCharsPerCoin += 420
		}
		breakdown.QuantData = (numCoins * quantCharsPerCoin) / 4
	}

	// --- Ranking Data ---
	rankingChars := 0
	if c.Indicators.EnableOIRanking {
		limit := c.Indicators.OIRankingLimit
		if limit <= 0 {
			limit = 10
		}
		rankingChars += limit * 60
	}
	if c.Indicators.EnableTakerFlowRanking {
		limit := c.Indicators.TakerFlowRankingLimit
		if limit <= 0 {
			limit = 10
		}
		rankingChars += limit * 80
	}
	if c.Indicators.EnablePriceRanking {
		limit := c.Indicators.PriceRankingLimit
		if limit <= 0 {
			limit = 10
		}
		// Count durations (comma-separated)
		numDurations := 1
		if c.Indicators.PriceRankingDuration != "" {
			numDurations = len(strings.Split(c.Indicators.PriceRankingDuration, ","))
		}
		rankingChars += limit * numDurations * 40
	}
	breakdown.RankingData = rankingChars / 4

	// --- Total with 15% safety margin ---
	subtotal := breakdown.SystemPrompt + breakdown.MarketData + breakdown.RankingData + breakdown.QuantData + breakdown.FixedOverhead
	total := subtotal * 115 / 100

	// --- Model limits ---
	modelLimits := make([]ModelLimit, 0, len(ModelContextLimits))
	for name, limit := range ModelContextLimits {
		pct := total * 100 / limit
		level := "ok"
		if pct >= 100 {
			level = "danger"
		} else if pct >= 80 {
			level = "warning"
		}
		modelLimits = append(modelLimits, ModelLimit{
			Name:         name,
			ContextLimit: limit,
			UsagePct:     pct,
			Level:        level,
		})
	}

	// Sort by usage_pct desc, then name asc for deterministic order
	sort.Slice(modelLimits, func(i, j int) bool {
		if modelLimits[i].UsagePct != modelLimits[j].UsagePct {
			return modelLimits[i].UsagePct > modelLimits[j].UsagePct
		}
		return modelLimits[i].Name < modelLimits[j].Name
	})

	// --- Suggestions ---
	var suggestions []string
	// Find the strictest model (smallest context)
	minLimit := 0
	for _, limit := range ModelContextLimits {
		if minLimit == 0 || limit < minLimit {
			minLimit = limit
		}
	}
	if minLimit > 0 && total > minLimit {
		// Per-coin-per-timeframe cost of one more coin (its candle table plus its
		// indicator series).
		savedPerCoin := (numTimeframes*indicatorCharsPerTF + displayRows*promptKlineRowChars + extrasPerCoin) / 4 * 115 / 100
		if numCoins > 1 {
			suggestions = append(suggestions, fmt.Sprintf("Reduce 1 coin to save ~%d tokens", savedPerCoin))
		}
		if numTimeframes > 1 {
			// Dropping a timeframe removes its indicator series for every coin.
			savedPerTF := (numCoins + positionCoins) * indicatorCharsPerTF / 4 * 115 / 100
			suggestions = append(suggestions, fmt.Sprintf("Reduce 1 timeframe to save ~%d tokens", savedPerTF))
		}
		// The display cap is the lever that actually bounds the prompt: the analysis
		// window can be long while the model only needs the recent shape.
		if displayRows > 20 {
			const trimmedRows = 20
			savedRows := rowsWithCandles * (displayRows - trimmedRows) * promptKlineRowChars
			savedValues := (numCoins + positionCoins) * numTimeframes * (displayRows - trimmedRows) * promptIndicatorValueChars * seriesPerTF
			suggestions = append(suggestions, fmt.Sprintf("Reduce the prompt display window from %d to %d bars to save ~%d tokens",
				displayRows, trimmedRows, (savedRows+savedValues)/4*115/100))
		}
	}

	return TokenEstimate{
		Total:       total,
		Breakdown:   breakdown,
		ModelLimits: modelLimits,
		Suggestions: suggestions,
	}
}

// getEffectiveCoinCount returns the estimated number of coins that will be analyzed
func (c *StrategyConfig) getEffectiveCoinCount() int {
	count := 0
	switch c.CoinSource.SourceType {
	case "static":
		count = len(c.CoinSource.StaticCoins)
	case "ai500":
		count = c.CoinSource.AI500Limit
	case "oi_top":
		count = c.CoinSource.OITopLimit
	case "oi_low":
		count = c.CoinSource.OILowLimit
	case "hyper_rank":
		count = c.CoinSource.HyperRankLimit
	case "hyper_main":
		count = c.CoinSource.HyperMainLimit
	case "hyper_all":
		count = c.CoinSource.HyperMainLimit
	default:
		count = c.CoinSource.HyperRankLimit
	}
	if count <= 0 {
		count = 3
	}
	return count
}

// getEffectiveTimeframeCount returns the number of timeframes that will be used
func (c *StrategyConfig) getEffectiveTimeframeCount() int {
	if len(c.Indicators.Klines.SelectedTimeframes) > 0 {
		return len(c.Indicators.Klines.SelectedTimeframes)
	}
	count := 1
	if c.Indicators.Klines.LongerTimeframe != "" {
		count++
	}
	return count
}
