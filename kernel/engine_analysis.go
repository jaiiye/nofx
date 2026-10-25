package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"nofx/logger"
	"nofx/market"
	"nofx/mcp"
	"nofx/store"
	"regexp"
	"strings"
	"time"
)

// ============================================================================
// Pre-compiled regular expressions (performance optimization)
// ============================================================================

var (
	// Safe regex: precisely match ```json code blocks
	reJSONFence      = regexp.MustCompile(`(?is)` + "```json\\s*(\\[\\s*\\{.*?\\}\\s*\\])\\s*```")
	reJSONArray      = regexp.MustCompile(`(?is)\[\s*\{.*?\}\s*\]`)
	reArrayHead      = regexp.MustCompile(`^\[\s*\{`)
	reArrayOpenSpace = regexp.MustCompile(`^\[\s+\{`)
	reInvisibleRunes = regexp.MustCompile("[\u200B\u200C\u200D\uFEFF]")

	// XML tag extraction (supports any characters in reasoning chain)
	reReasoningTag = regexp.MustCompile(`(?s)<reasoning>(.*?)</reasoning>`)
	reDecisionTag  = regexp.MustCompile(`(?s)<decision>(.*?)</decision>`)
)

// ============================================================================
// Entry Functions - Main API
// ============================================================================

// GetFullDecisionWithStrategy uses StrategyEngine to get AI decision (unified prompt generation)
func GetFullDecisionWithStrategy(ctx *Context, mcpClient mcp.AIClient, engine *StrategyEngine, variant string) (*FullDecision, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	if engine == nil {
		defaultConfig := store.GetDefaultStrategyConfig("en")
		engine = NewStrategyEngine(&defaultConfig)
	}

	// Clamp strategy limits to prevent token overflow
	engineConfig := engine.GetConfig()
	engineConfig.ClampLimits()

	// Token estimation check — block if exceeding the specific model's context limit
	estimate := engineConfig.EstimateTokens()

	// Determine context limit for the specific model being used
	contextLimit := 131072 // safe default (strictest common limit)
	var providerName string
	if embedder, ok := mcpClient.(mcp.ClientEmbedder); ok {
		base := embedder.BaseClient()
		providerName = base.Provider
		contextLimit = store.GetContextLimitForClient(base.Provider, base.Model)
	}

	if estimate.Total > contextLimit {
		logger.Errorf("🚫 Token estimate %d exceeds %s context limit %d — blocking analysis",
			estimate.Total, providerName, contextLimit)
		return nil, fmt.Errorf("estimated %d tokens exceeds model context limit of %d; reduce coins, timeframes, or K-line count",
			estimate.Total, contextLimit)
	}
	if estimate.Total*100/contextLimit >= 80 {
		logger.Infof("⚠️  Token estimate %d — approaching %s context limit %d",
			estimate.Total, providerName, contextLimit)
	}

	// 1. Fetch market data using strategy config
	if len(ctx.MarketDataMap) == 0 {
		if err := fetchMarketDataWithStrategy(ctx, engine); err != nil {
			return nil, fmt.Errorf("failed to fetch market data: %w", err)
		}
	}
	pruneCandidateCoinsWithoutMarketData(ctx)

	// 2. Build System Prompt using strategy engine
	riskConfig := engine.GetRiskControlConfig()
	systemPrompt := engine.BuildSystemPrompt(ctx.Account.TotalEquity, variant)

	// 3. Build User Prompt using strategy engine
	userPrompt := engine.BuildUserPrompt(ctx)

	// Prompt size is the main cost driver of a decision cycle, and the only way
	// to notice a context that is outgrowing the model's window before the
	// provider rejects it. ~3.5 characters per token for this mixed
	// numbers/prose/Chinese content.
	promptBytes := len(systemPrompt) + len(userPrompt)
	logger.Infof("📏 prompt size: %.1f KB (system %.1f + user %.1f) ≈ %d tokens | %s primary, %d positions, %d candidates, %d timeframes",
		float64(promptBytes)/1024,
		float64(len(systemPrompt))/1024,
		float64(len(userPrompt))/1024,
		promptBytes/3500*1000,
		engine.GetConfig().Indicators.Klines.PrimaryTimeframe,
		len(ctx.Positions), len(ctx.CandidateCoins), len(ctx.Timeframes))

	// 4. Call AI API
	aiCtx := ctx.GoCtx
	if aiCtx == nil {
		aiCtx = context.Background()
	}
	aiCallStart := time.Now()
	aiResponse, err := mcpClient.CallWithMessages(aiCtx, systemPrompt, userPrompt)
	aiCallDuration := time.Since(aiCallStart)
	if err != nil {
		// Keep the prompts even when the call failed: the cycle is recorded as a
		// failure, and those prompts are what make the failure diagnosable — an
		// empty completion in particular only means something next to its request.
		return &FullDecision{
			SystemPrompt:        systemPrompt,
			UserPrompt:          userPrompt,
			AIRequestDurationMs: aiCallDuration.Milliseconds(),
		}, fmt.Errorf("AI API call failed: %w", err)
	}

	// 5. Parse AI response
	decision, err := parseFullDecisionResponse(
		aiResponse,
		ctx.Account.TotalEquity,
		riskConfig.BTCETHMaxLeverage,
		riskConfig.AltcoinMaxLeverage,
		riskConfig.BTCETHMaxPositionValueRatio,
		riskConfig.AltcoinMaxPositionValueRatio,
		// Risk/reward is validated against the decision-time market price.
		func(symbol string) float64 {
			if data, ok := ctx.MarketDataMap[symbol]; ok && data != nil {
				return data.CurrentPrice
			}
			return 0
		},
		riskConfig.MinRiskRewardRatio,
	)

	if decision != nil {
		decision.Timestamp = time.Now()
		decision.SystemPrompt = systemPrompt
		decision.UserPrompt = userPrompt
		decision.AIRequestDurationMs = aiCallDuration.Milliseconds()
		decision.RawResponse = aiResponse
	}

	if err != nil {
		return decision, fmt.Errorf("failed to parse AI response: %w", err)
	}

	return decision, nil
}

// ============================================================================
// Market Data Fetching
// ============================================================================

// fetchMicrostructure fetches and analyzes the decision-time book for one
// symbol. Failures degrade gracefully (nil result, the prompt omits the
// section). The book follows the execution venue; xyz dex assets always route
// to Hyperliquid.
func fetchMicrostructure(venue market.Venue, symbol string, primaryKlines []market.KlineBar) *market.MarketMicrostructure {
	book, err := market.GetOrderBookDepthForVenue(venue, symbol, 20)
	if err != nil {
		logger.Infof("⚠️ Microstructure unavailable for %s: %v", symbol, err)
		return nil
	}
	return market.AnalyzeMicrostructureForVenue(venue, book, primaryKlines)
}

// loadMicrostructure fetches the decision-time book for a symbol and stores
// the analysis in the context when successful.
func loadMicrostructure(ctx *Context, venue market.Venue, symbol string, data *market.Data, primaryTimeframe string) {
	if ctx.MicrostructureMap == nil {
		ctx.MicrostructureMap = make(map[string]*market.MarketMicrostructure)
	}
	var primaryBars []market.KlineBar
	if tfData, ok := data.TimeframeData[primaryTimeframe]; ok {
		primaryBars = tfData.Klines
	}
	if m := fetchMicrostructure(venue, symbol, primaryBars); m != nil {
		ctx.MicrostructureMap[symbol] = m
	}
}

// loadPositioning stores the Hyperliquid-native positioning reading for a
// symbol. It never fails the cycle: a symbol without a venue context, or one
// whose histories are still too short, is simply absent from the map and the
// prompt omits it.
func loadPositioning(ctx *Context, symbol string) {
	if ctx.PositioningMap == nil {
		ctx.PositioningMap = make(map[string]*market.Positioning)
	}
	if reading, ok := market.HyperliquidPositioning(symbol); ok && reading != nil {
		ctx.PositioningMap[symbol] = reading
	}
}

// loadCrossVenue stores the per-venue OI/funding reading. The collector keeps
// it in memory, so this costs nothing upstream; a reading older than a few
// polls is absent rather than shown as current.
func loadCrossVenue(ctx *Context, symbol string) {
	if ctx.CrossVenueMap == nil {
		ctx.CrossVenueMap = make(map[string]*market.CrossVenue)
	}
	if reading, ok := market.VenueMetricsForSymbol(symbol); ok && reading != nil {
		ctx.CrossVenueMap[symbol] = reading
	}
}

// loadLiquidationFlow stores the liquidation-flow reading. Omitting it is the
// normal quiet-market case, so a missing entry carries no implication.
func loadLiquidationFlow(ctx *Context, symbol string) {
	if ctx.LiquidationMap == nil {
		ctx.LiquidationMap = make(map[string]*market.LiquidationReading)
	}
	if reading, ok := market.LiquidationFlowForSymbol(symbol); ok && reading != nil {
		ctx.LiquidationMap[symbol] = reading
	}
}

// fetchMarketDataWithStrategy fetches market data using strategy config (multiple timeframes)
func fetchMarketDataWithStrategy(ctx *Context, engine *StrategyEngine) error {
	config := engine.GetConfig()
	ctx.MarketDataMap = make(map[string]*market.Data)

	timeframes := config.Indicators.Klines.SelectedTimeframes
	primaryTimeframe := config.Indicators.Klines.PrimaryTimeframe
	klineCount := config.Indicators.Klines.PrimaryCount

	// Compatible with old configuration
	if len(timeframes) == 0 {
		if primaryTimeframe != "" {
			timeframes = append(timeframes, primaryTimeframe)
		} else {
			timeframes = append(timeframes, "3m")
		}
		if config.Indicators.Klines.LongerTimeframe != "" {
			timeframes = append(timeframes, config.Indicators.Klines.LongerTimeframe)
		}
	}
	if primaryTimeframe == "" {
		primaryTimeframe = timeframes[0]
	}
	if klineCount <= 0 {
		klineCount = 30
	}

	logger.Infof("📊 Strategy timeframes: %v, Primary: %s, Kline count: %d", timeframes, primaryTimeframe, klineCount)

	// Configured indicator periods reach the calculation layer so prompt
	// values match what the user configured in the strategy studio.
	indicatorParams := &market.IndicatorParams{
		EMAPeriods: config.Indicators.EMAPeriods,
		RSIPeriods: config.Indicators.RSIPeriods,
		ATRPeriods: config.Indicators.ATRPeriods,
		MACDFast:   config.Indicators.MACDFastPeriod,
		MACDSlow:   config.Indicators.MACDSlowPeriod,

		ADXPeriods:           config.Indicators.ADXPeriods,
		BOLLPeriods:          config.Indicators.BOLLPeriods,
		BOLLMultiplier:       config.Indicators.BOLLMultiplier,
		SupertrendPeriod:     config.Indicators.SupertrendPeriod,
		SupertrendMultiplier: config.Indicators.SupertrendMultiplier,
	}

	// Decision-time order book metrics (nil flag = enabled).
	microEnabled := config.Indicators.EnableMicrostructure == nil || *config.Indicators.EnableMicrostructure
	ctx.MicrostructureMap = make(map[string]*market.MarketMicrostructure)

	// Hyperliquid-native positioning and holder structure are only meaningful
	// for a Hyperliquid execution venue; a Binance/Bybit trader must not read
	// HL holder clusters as their own order flow.
	positioningEnabled := config.Indicators.PositioningEnabled() && engine.Venue() == market.VenueHyperliquid
	structureEnabled := config.Indicators.PositionStructureEnabled() && engine.Venue() == market.VenueHyperliquid
	ctx.PositioningMap = make(map[string]*market.Positioning)

	// Cross-venue positioning and the liquidation flow (both nil flags =
	// enabled). They read the background collectors' in-memory readings, so
	// enabling them adds no per-cycle venue requests.
	crossVenueEnabled := config.Indicators.CrossVenueEnabled()
	liquidationEnabled := config.Indicators.LiquidationFlowEnabled()
	ctx.CrossVenueMap = make(map[string]*market.CrossVenue)
	ctx.LiquidationMap = make(map[string]*market.LiquidationReading)

	// 1. First fetch data for position coins (must fetch)
	for _, pos := range ctx.Positions {
		data, err := market.GetWithTimeframesForVenue(engine.Venue(), pos.Symbol, timeframes, primaryTimeframe, klineCount, indicatorParams)
		if err != nil {
			logger.Infof("⚠️  Failed to fetch market data for position %s: %v", pos.Symbol, err)
			continue
		}
		ctx.MarketDataMap[pos.Symbol] = data
		if microEnabled {
			loadMicrostructure(ctx, engine.Venue(), pos.Symbol, data, primaryTimeframe)
		}
		if positioningEnabled {
			loadPositioning(ctx, pos.Symbol)
		}
		if crossVenueEnabled {
			loadCrossVenue(ctx, pos.Symbol)
		}
		if liquidationEnabled {
			loadLiquidationFlow(ctx, pos.Symbol)
		}
	}

	// 2. Fetch data for all candidate coins
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		positionSymbols[pos.Symbol] = true
	}

	const minOIThresholdMillions = 15.0 // 15M USD minimum open interest value

	for _, coin := range ctx.CandidateCoins {
		if _, exists := ctx.MarketDataMap[coin.Symbol]; exists {
			continue
		}

		data, err := market.GetWithTimeframesForVenue(engine.Venue(), coin.Symbol, timeframes, primaryTimeframe, klineCount, indicatorParams)
		if err != nil {
			logger.Infof("⚠️  Failed to fetch market data for %s: %v", coin.Symbol, err)
			continue
		}

		// Liquidity filter. The OI value (OI x price) is the historical
		// measure; HIP-3 assets (stocks, commodities, indices) have no Binance
		// OI, so they are judged on the venue's 24h traded notional instead of
		// being exempted from the filter entirely.
		isExistingPosition := positionSymbols[coin.Symbol]
		if !isExistingPosition && data.CurrentPrice > 0 {
			liquidityUSD := 0.0
			source := ""
			if data.OpenInterest != nil && data.OpenInterest.Latest > 0 {
				liquidityUSD = data.OpenInterest.Latest * data.CurrentPrice
				source = "OI value"
			} else if data.DayNotionalVolume > 0 {
				liquidityUSD = data.DayNotionalVolume
				source = "24h notional volume"
			}
			if liquidityUSD > 0 {
				liquidityMillions := liquidityUSD / 1_000_000
				if liquidityMillions < minOIThresholdMillions {
					logger.Infof("⚠️  %s %s too low (%.2fM USD < %.1fM), skipping coin",
						coin.Symbol, source, liquidityMillions, minOIThresholdMillions)
					continue
				}
			}
		}

		// Fetch the book only for coins that survive the filter, so skipped
		// candidates don't cost an extra REST call.
		if microEnabled {
			loadMicrostructure(ctx, engine.Venue(), coin.Symbol, data, primaryTimeframe)
		}
		if positioningEnabled {
			loadPositioning(ctx, coin.Symbol)
		}
		if crossVenueEnabled {
			loadCrossVenue(ctx, coin.Symbol)
		}
		if liquidationEnabled {
			loadLiquidationFlow(ctx, coin.Symbol)
		}

		ctx.MarketDataMap[coin.Symbol] = data
	}

	logger.Infof("📊 Successfully fetched multi-timeframe market data for %d coins", len(ctx.MarketDataMap))

	// Steer the trades feed and the holder-position index to the coins actually
	// being analysed (positions plus surviving candidates). Both apply only the
	// difference, so this is cheap to call every cycle.
	symbols := make([]string, 0, len(ctx.MarketDataMap))
	for symbol := range ctx.MarketDataMap {
		symbols = append(symbols, symbol)
	}
	if positioningEnabled {
		market.UpdateHLFlowSymbols(symbols)
	}
	if structureEnabled {
		market.UpdatePositionIndexSymbols(symbols)
	}
	if crossVenueEnabled {
		market.UpdateVenueMetricSymbols(symbols)
	}
	if liquidationEnabled {
		market.UpdateLiquidationSymbols(symbols)
	}

	return nil
}

func pruneCandidateCoinsWithoutMarketData(ctx *Context) {
	if ctx == nil || len(ctx.CandidateCoins) == 0 || len(ctx.MarketDataMap) == 0 {
		return
	}
	kept := make([]CandidateCoin, 0, len(ctx.CandidateCoins))
	for _, coin := range ctx.CandidateCoins {
		if _, ok := ctx.MarketDataMap[coin.Symbol]; ok {
			kept = append(kept, coin)
			continue
		}
		logger.Infof("⚠️  Skipping candidate %s in AI prompt: no valid market/K-line data", coin.Symbol)
	}
	ctx.CandidateCoins = kept
}

// ============================================================================
// AI Response Parsing
// ============================================================================

func parseFullDecisionResponse(aiResponse string, accountEquity float64, btcEthLeverage, altcoinLeverage int, btcEthPosRatio, altcoinPosRatio float64, entryPriceFor func(symbol string) float64, minRewardRatio float64) (*FullDecision, error) {
	cotTrace := extractCoTTrace(aiResponse)

	decisions, err := extractDecisions(aiResponse)
	if err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: []Decision{},
		}, fmt.Errorf("failed to extract decisions: %w", err)
	}

	if err := validateDecisions(decisions, accountEquity, btcEthLeverage, altcoinLeverage, btcEthPosRatio, altcoinPosRatio, entryPriceFor, minRewardRatio); err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: decisions,
		}, fmt.Errorf("decision validation failed: %w", err)
	}

	return &FullDecision{
		CoTTrace:  cotTrace,
		Decisions: decisions,
	}, nil
}

func extractCoTTrace(response string) string {
	if match := reReasoningTag.FindStringSubmatch(response); match != nil && len(match) > 1 {
		if reasoning := strings.TrimSpace(match[1]); reasoning != "" {
			logger.Infof("✓ Extracted reasoning chain using <reasoning> tag")
			return reasoning
		}
		// An EMPTY tag must not swallow the fallbacks below: that turned a response
		// with text into a blank chain of thought in the UI. Drop the empty block so
		// the raw tags do not end up displayed as the reasoning either.
		response = reReasoningTag.ReplaceAllString(response, "")
	}

	if decisionIdx := strings.Index(response, "<decision>"); decisionIdx > 0 {
		logger.Infof("✓ Extracted content before <decision> tag as reasoning chain")
		return strings.TrimSpace(response[:decisionIdx])
	}

	jsonStart := strings.Index(response, "[")
	if jsonStart > 0 {
		logger.Infof("⚠️  Extracted reasoning chain using old format ([ character separator)")
		return strings.TrimSpace(response[:jsonStart])
	}

	return strings.TrimSpace(response)
}

func extractDecisions(response string) ([]Decision, error) {
	s := removeInvisibleRunes(response)
	s = strings.TrimSpace(s)
	s = fixMissingQuotes(s)

	var jsonPart string
	if match := reDecisionTag.FindStringSubmatch(s); match != nil && len(match) > 1 {
		jsonPart = strings.TrimSpace(match[1])
		logger.Infof("✓ Extracted JSON using <decision> tag")
	} else {
		jsonPart = s
		logger.Infof("⚠️  <decision> tag not found, searching JSON in full text")
	}

	if m := reJSONFence.FindStringSubmatch(jsonPart); m != nil && len(m) > 1 {
		return parseDecisionArray(m[1], response)
	}

	jsonContent := strings.TrimSpace(reJSONArray.FindString(jsonPart))
	if jsonContent == "" {
		logger.Infof("⚠️  [SafeFallback] AI didn't output JSON decision, entering safe wait mode")

		cotSummary := jsonPart
		if len(cotSummary) > 240 {
			cotSummary = cotSummary[:240] + "..."
		}

		fallbackDecision := Decision{
			Symbol:    "ALL",
			Action:    "wait",
			Reasoning: fmt.Sprintf("Model didn't output structured JSON decision, entering safe wait; summary: %s", cotSummary),
		}

		return []Decision{fallbackDecision}, nil
	}

	return parseDecisionArray(jsonContent, response)
}

func fixMissingQuotes(jsonStr string) string {
	jsonStr = strings.ReplaceAll(jsonStr, "\u201c", "\"")
	jsonStr = strings.ReplaceAll(jsonStr, "\u201d", "\"")
	jsonStr = strings.ReplaceAll(jsonStr, "\u2018", "'")
	jsonStr = strings.ReplaceAll(jsonStr, "\u2019", "'")

	jsonStr = strings.ReplaceAll(jsonStr, "［", "[")
	jsonStr = strings.ReplaceAll(jsonStr, "］", "]")
	jsonStr = strings.ReplaceAll(jsonStr, "｛", "{")
	jsonStr = strings.ReplaceAll(jsonStr, "｝", "}")
	jsonStr = strings.ReplaceAll(jsonStr, "：", ":")
	jsonStr = strings.ReplaceAll(jsonStr, "，", ",")

	jsonStr = strings.ReplaceAll(jsonStr, "【", "[")
	jsonStr = strings.ReplaceAll(jsonStr, "】", "]")
	jsonStr = strings.ReplaceAll(jsonStr, "〔", "[")
	jsonStr = strings.ReplaceAll(jsonStr, "〕", "]")
	jsonStr = strings.ReplaceAll(jsonStr, "、", ",")

	jsonStr = strings.ReplaceAll(jsonStr, "　", " ")

	return jsonStr
}

// parseDecisionArray is the single place where model output becomes decisions:
// a structural guard, then one json.Unmarshal. The parser is the authority on
// what is JSON, so nothing scans the document on the happy path — a valid array
// containing "~" or "5,000" inside a reasoning string parses like any other.
// The diagnostics below only run once a parse has actually failed.
func parseDecisionArray(jsonContent, fullResponse string) ([]Decision, error) {
	jsonContent = strings.TrimSpace(jsonContent)
	jsonContent = compactArrayOpen(jsonContent)
	jsonContent = fixMissingQuotes(jsonContent)

	if !reArrayHead.MatchString(jsonContent) {
		if strings.HasPrefix(jsonContent, "[") && !strings.Contains(jsonContent[:min(20, len(jsonContent))], "{") {
			return nil, fmt.Errorf("not a valid decision array (must contain objects {}), actual content: %s\nFull response:\n%s",
				jsonContent[:min(50, len(jsonContent))], fullResponse)
		}
		return nil, fmt.Errorf("JSON must start with [{ (whitespace allowed), actual: %s\nFull response:\n%s",
			jsonContent[:min(20, len(jsonContent))], fullResponse)
	}

	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonContent), &decisions); err != nil {
		return nil, fmt.Errorf("%s\nJSON content: %s\nFull response:\n%s",
			explainJSONParseError(jsonContent, err), jsonContent, fullResponse)
	}
	return decisions, nil
}

// explainJSONParseError turns encoding/json's failure into an actionable message
// by inspecting the offending bytes only — never the whole document, and never
// the inside of a string (a valid document produces no error at all).
func explainJSONParseError(jsonContent string, err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			field = "(element)"
		}
		return fmt.Sprintf("decision field %q must be %s, got %s — write a bare number, not a string or an expression",
			field, typeErr.Type, typeErr.Value)
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) && syntaxErr.Offset > 0 {
		// Offset is the 1-based position just past the offending byte.
		off := int(syntaxErr.Offset) - 1
		if off >= 0 && off < len(jsonContent) {
			return fmt.Sprintf("invalid JSON at byte %d near %q%s: %v",
				syntaxErr.Offset, excerptAround(jsonContent, off), jsonSyntaxHint(jsonContent, off), err)
		}
	}
	return fmt.Sprintf("invalid JSON: %v", err)
}

func excerptAround(s string, off int) string {
	start := off - 20
	if start < 0 {
		start = 0
	}
	end := off + 20
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// jsonSyntaxHint names the two failure modes the model actually produces, using
// the bytes around the error only.
func jsonSyntaxHint(s string, off int) string {
	switch s[off] {
	case '~':
		return " (range symbol: give one precise value)"
	case '*', '/':
		return " (expression: compute the value yourself)"
	}
	// A thousand separator makes the parser land on the first digit after the
	// comma — {"x":1,000} fails at '0' looking for the next object key.
	if off >= 2 && isASCIIDigit(s[off]) && s[off-1] == ',' && isASCIIDigit(s[off-2]) &&
		off+2 < len(s) && isASCIIDigit(s[off+1]) && isASCIIDigit(s[off+2]) {
		return " (thousand separator: write 1000, not 1,000)"
	}
	return ""
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func removeInvisibleRunes(s string) string {
	return reInvisibleRunes.ReplaceAllString(s, "")
}

func compactArrayOpen(s string) string {
	return reArrayOpenSpace.ReplaceAllString(strings.TrimSpace(s), "[{")
}
