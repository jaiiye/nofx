package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"nofx/logger"
	"nofx/market"
	"nofx/provider/hyperliquid"
	"nofx/provider/nofxos"
	"nofx/security"
	"nofx/store"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ai500AliasOnce keeps the deprecation notice for the old source name to one log
// line per process.
var ai500AliasOnce sync.Once

// ============================================================================
// Type Definitions
// ============================================================================

// PositionInfo position information
type PositionInfo struct {
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"` // "long" or "short"
	EntryPrice       float64 `json:"entry_price"`
	MarkPrice        float64 `json:"mark_price"`
	Quantity         float64 `json:"quantity"`
	Leverage         int     `json:"leverage"`
	UnrealizedPnL    float64 `json:"unrealized_pnl"`
	UnrealizedPnLPct float64 `json:"unrealized_pnl_pct"`
	PeakPnLPct       float64 `json:"peak_pnl_pct"` // Historical peak profit percentage
	LiquidationPrice float64 `json:"liquidation_price"`
	MarginUsed       float64 `json:"margin_used"`
	UpdateTime       int64   `json:"update_time"` // Position update timestamp (milliseconds)
}

// AccountInfo account information
type AccountInfo struct {
	TotalEquity      float64 `json:"total_equity"`      // Account equity
	AvailableBalance float64 `json:"available_balance"` // Available balance
	UnrealizedPnL    float64 `json:"unrealized_pnl"`    // Unrealized profit/loss
	TotalPnL         float64 `json:"total_pnl"`         // Total profit/loss
	TotalPnLPct      float64 `json:"total_pnl_pct"`     // Total profit/loss percentage
	MarginUsed       float64 `json:"margin_used"`       // Used margin
	MarginUsedPct    float64 `json:"margin_used_pct"`   // Margin usage rate
	PositionCount    int     `json:"position_count"`    // Number of positions
}

// CandidateCoin candidate coin (from coin pool)
type CandidateCoin struct {
	Symbol  string   `json:"symbol"`
	Sources []string `json:"sources"` // Sources: "ai500" and/or "oi_top"
}

// OITopData open interest growth top data (for AI decision reference)
type OITopData struct {
	Rank              int     // OI Top ranking
	OIDeltaPercent    float64 // Open interest change percentage (1 hour)
	OIDeltaValue      float64 // Open interest change value
	PriceDeltaPercent float64 // Price change percentage
}

// TradingStats trading statistics (for AI input)
type TradingStats struct {
	TotalTrades    int     `json:"total_trades"`     // Total number of trades (closed)
	WinRate        float64 `json:"win_rate"`         // Win rate (%)
	ProfitFactor   float64 `json:"profit_factor"`    // Profit factor
	SharpeRatio    float64 `json:"sharpe_ratio"`     // Sharpe ratio
	TotalPnL       float64 `json:"total_pnl"`        // Total profit/loss
	AvgWin         float64 `json:"avg_win"`          // Average win
	AvgLoss        float64 `json:"avg_loss"`         // Average loss
	MaxDrawdownPct float64 `json:"max_drawdown_pct"` // Maximum drawdown (%)
}

// RecentOrder recently completed order (for AI input)
type RecentOrder struct {
	Symbol       string  `json:"symbol"`        // Trading pair
	Side         string  `json:"side"`          // long/short
	EntryPrice   float64 `json:"entry_price"`   // Entry price
	ExitPrice    float64 `json:"exit_price"`    // Exit price
	RealizedPnL  float64 `json:"realized_pnl"`  // Realized profit/loss
	PnLPct       float64 `json:"pnl_pct"`       // Profit/loss percentage
	EntryTime    string  `json:"entry_time"`    // Entry time
	ExitTime     string  `json:"exit_time"`     // Exit time
	HoldDuration string  `json:"hold_duration"` // Hold duration, e.g. "2h30m"
}

// Context trading context (complete information passed to AI)
type Context struct {
	CurrentTime       string                                  `json:"current_time"`
	RuntimeMinutes    int                                     `json:"runtime_minutes"`
	CallCount         int                                     `json:"call_count"`
	Account           AccountInfo                             `json:"account"`
	Positions         []PositionInfo                          `json:"positions"`
	CandidateCoins    []CandidateCoin                         `json:"candidate_coins"`
	TradingStats      *TradingStats                           `json:"trading_stats,omitempty"`
	RecentOrders      []RecentOrder                           `json:"recent_orders,omitempty"`
	MarketDataMap     map[string]*market.Data                 `json:"-"`
	MultiTFMarket     map[string]map[string]*market.Data      `json:"-"`
	MicrostructureMap map[string]*market.MarketMicrostructure `json:"-"` // Decision-time book metrics per symbol
	// Hyperliquid-native positioning per symbol (funding percentile, OI
	// quadrants, taker flow). Derived from venue data only; see
	// market.HyperliquidPositioning.
	PositioningMap     map[string]*market.Positioning     `json:"-"`
	PositionProfileMap map[string]*market.PositionProfile `json:"-"` // holder cost/liquidation structure per symbol
	// Cross-venue OI/funding per symbol. Venues are kept separate because
	// summing OI across venues double-counts hedged exposure.
	CrossVenueMap map[string]*market.CrossVenue `json:"-"`
	// Liquidation flow per symbol (1h/15m windows, cascade flag).
	LiquidationMap     map[string]*market.LiquidationReading `json:"-"`
	OITopDataMap       map[string]*OITopData                 `json:"-"`
	QuantDataMap       map[string]*QuantData                 `json:"-"`
	OIRankingData      *market.OIRankingData                 `json:"-"` // Market-wide OI ranking data
	NetFlowRankingData *nofxos.NetFlowRankingData            `json:"-"` // Market-wide taker-flow ranking data
	PriceRankingData   *market.PriceRankingData              `json:"-"` // Market-wide price gainers/losers
	BTCETHLeverage     int                                   `json:"-"`
	AltcoinLeverage    int                                   `json:"-"`
	Timeframes         []string                              `json:"-"`
	// GoCtx is an optional stdlib context used to cancel the LLM call and
	// its retry backoff. Nil means context.Background().
	GoCtx context.Context `json:"-"`
}

// Decision AI trading decision
type Decision struct {
	Symbol string `json:"symbol"`
	Action string `json:"action"` // Standard: "open_long", "open_short", "close_long", "close_short", "hold", "wait"
	// Grid actions: "place_buy_limit", "place_sell_limit", "cancel_order", "cancel_all_orders", "pause_grid", "resume_grid", "adjust_grid"

	// Opening position parameters
	Leverage        int     `json:"leverage,omitempty"`
	PositionSizeUSD float64 `json:"position_size_usd,omitempty"`
	StopLoss        float64 `json:"stop_loss,omitempty"`
	TakeProfit      float64 `json:"take_profit,omitempty"`

	// Grid trading parameters
	Price      float64 `json:"price,omitempty"`       // Limit order price (for grid)
	Quantity   float64 `json:"quantity,omitempty"`    // Order quantity (for grid)
	LevelIndex int     `json:"level_index,omitempty"` // Grid level index
	OrderID    string  `json:"order_id,omitempty"`    // Order ID (for cancel)

	// Common parameters
	Confidence int     `json:"confidence,omitempty"` // Confidence level (0-100)
	RiskUSD    float64 `json:"risk_usd,omitempty"`   // Maximum USD risk
	Reasoning  string  `json:"reasoning"`

	// TriggerLevel is the price a wait/hold is waiting on: the level that must
	// break or hold before the next entry is valid. Recorded so a no-trade cycle
	// stays actionable (and reviewable) instead of just "nothing happened".
	TriggerLevel float64 `json:"trigger_level,omitempty"`
}

// FullDecision AI's complete decision (including chain of thought)
type FullDecision struct {
	SystemPrompt        string     `json:"system_prompt"`
	UserPrompt          string     `json:"user_prompt"`
	CoTTrace            string     `json:"cot_trace"`
	Decisions           []Decision `json:"decisions"`
	RawResponse         string     `json:"raw_response"`
	Timestamp           time.Time  `json:"timestamp"`
	AIRequestDurationMs int64      `json:"ai_request_duration_ms,omitempty"`
}

// QuantData quantitative data structure (fund flow, position changes, price changes)
type QuantData struct {
	Symbol      string             `json:"symbol"`
	Price       float64            `json:"price"`
	Netflow     *NetflowData       `json:"netflow,omitempty"`
	OI          map[string]*OIData `json:"oi,omitempty"`
	PriceChange map[string]float64 `json:"price_change,omitempty"`
}

type NetflowData struct {
	Institution *FlowTypeData `json:"institution,omitempty"`
	Personal    *FlowTypeData `json:"personal,omitempty"`
}

type FlowTypeData struct {
	Future map[string]float64 `json:"future,omitempty"`
	Spot   map[string]float64 `json:"spot,omitempty"`
}

type OIData struct {
	CurrentOI float64 `json:"current_oi"`
	// NetLong / NetShort are the venue's reported long and short exposure.
	// They arrive with the OI payload, so surfacing them costs no extra request;
	// their ratio is the long/short reading the prompt uses.
	NetLong  float64                 `json:"net_long,omitempty"`
	NetShort float64                 `json:"net_short,omitempty"`
	Delta    map[string]*OIDeltaData `json:"delta,omitempty"`
}

type OIDeltaData struct {
	OIDelta        float64 `json:"oi_delta"`
	OIDeltaValue   float64 `json:"oi_delta_value"`
	OIDeltaPercent float64 `json:"oi_delta_percent"`
}

// ============================================================================
// StrategyEngine - Core Strategy Execution Engine
// ============================================================================

// StrategyEngine strategy execution engine
type StrategyEngine struct {
	config       *store.StrategyConfig
	nofxosClient *nofxos.Client
}

// NewStrategyEngine creates strategy execution engine.
// claw402WalletKey is optional — if provided, nofxos data requests are routed through claw402.
func NewStrategyEngine(config *store.StrategyConfig, claw402WalletKey ...string) *StrategyEngine {
	// Create NofxOS client with API key from config
	apiKey := config.Indicators.NofxOSAPIKey
	if apiKey == "" {
		apiKey = nofxos.DefaultAuthKey
	}
	client := nofxos.NewClient(nofxos.DefaultBaseURL, apiKey)

	// If claw402 wallet key is provided (from trader's AI config), route through claw402
	walletKey := ""
	if len(claw402WalletKey) > 0 {
		walletKey = claw402WalletKey[0]
	}
	if walletKey == "" {
		walletKey = os.Getenv("CLAW402_WALLET_KEY")
	}
	if walletKey != "" {
		claw402URL := os.Getenv("CLAW402_URL")
		if claw402URL == "" {
			claw402URL = "https://claw402.ai"
		}
		claw402Client, err := nofxos.NewClaw402DataClient(claw402URL, walletKey, &logger.MCPLogger{})
		if err == nil {
			client.SetClaw402(claw402Client)
			logger.Infof("🔗 NofxOS data routed through claw402 (%s)", claw402URL)
		} else {
			logger.Warnf("⚠️ Failed to init claw402 data client: %v (using direct nofxos.ai)", err)
		}
	}

	return &StrategyEngine{
		config:       config,
		nofxosClient: client,
	}
}

func (e *StrategyEngine) usesHyperliquidNativeUniverse() bool {
	if e == nil || e.config == nil {
		return false
	}
	source := e.config.CoinSource
	if source.SourceType == "hyper_all" || source.SourceType == "hyper_main" || source.SourceType == "hyper_rank" || source.UseHyperAll || source.UseHyperMain {
		return true
	}
	for _, symbol := range source.StaticCoins {
		if market.IsXyzDexAsset(symbol) {
			return true
		}
	}
	return false
}

// GetRiskControlConfig gets risk control configuration
func (e *StrategyEngine) GetRiskControlConfig() store.RiskControlConfig {
	return e.config.RiskControl
}

// GetLanguage returns the language from config or falls back to auto-detection
func (e *StrategyEngine) GetLanguage() Language {
	switch e.config.Language {
	case "zh":
		return LangChinese
	case "en":
		return LangEnglish
	default:
		// Fall back to auto-detection from prompt content for backward compatibility
		return detectLanguage(e.config.PromptSections.RoleDefinition)
	}
}

// GetConfig gets complete strategy configuration
func (e *StrategyEngine) GetConfig() *store.StrategyConfig {
	return e.config
}

// ============================================================================
// Candidate Coins
// ============================================================================

// GetCandidateCoins gets candidate coins based on strategy configuration
func (e *StrategyEngine) GetCandidateCoins() ([]CandidateCoin, error) {
	var candidates []CandidateCoin
	symbolSources := make(map[string][]string)

	coinSource := e.config.CoinSource

	switch coinSource.SourceType {
	case "static":
		for _, symbol := range coinSource.StaticCoins {
			symbol = market.Normalize(symbol)
			candidates = append(candidates, CandidateCoin{
				Symbol:  symbol,
				Sources: []string{"static"},
			})
		}

		return e.filterExcludedCoins(candidates), nil

	case "screener", "ai500":
		// "ai500" is kept as a configuration alias: the vendor pool is gone, and
		// the local screener is the documented replacement.
		if coinSource.SourceType == "ai500" {
			ai500AliasOnce.Do(func() {
				logger.Warnf("⚠️  coin source 'ai500' now uses the local screener (free exchange data); update the config to 'screener'")
			})
			if !coinSource.UseAI500 {
				return e.filterExcludedCoins(e.staticFallback()), nil
			}
		}
		coins, err := e.getScreenerCoins(coinSource.AI500Limit)
		if err != nil {
			logger.Warnf("⚠️  Screener unavailable: %v", err)
			return e.filterExcludedCoins(e.staticFallback()), nil
		}
		return e.filterExcludedCoins(coins), nil

	case "oi_top":
		// Check use_oi_top flag; if false, fall back to static coins
		if !coinSource.UseOITop {
			logger.Infof("⚠️  source_type is 'oi_top' but use_oi_top is false, falling back to static coins")
			for _, symbol := range coinSource.StaticCoins {
				symbol = market.Normalize(symbol)
				candidates = append(candidates, CandidateCoin{
					Symbol:  symbol,
					Sources: []string{"static"},
				})
			}
			return e.filterExcludedCoins(candidates), nil
		}
		coins, err := e.getOITopCoins(coinSource.OITopLimit)
		if err != nil {
			logger.Warnf("⚠️  OI top ranking unavailable: %v", err)
			return e.filterExcludedCoins(e.staticFallback()), nil
		}
		// Empty list is a normal condition, return directly
		return e.filterExcludedCoins(coins), nil

	case "oi_low":
		// OI decrease ranking, suitable for short positions
		if !coinSource.UseOILow {
			logger.Infof("⚠️  source_type is 'oi_low' but use_oi_low is false, falling back to static coins")
			for _, symbol := range coinSource.StaticCoins {
				symbol = market.Normalize(symbol)
				candidates = append(candidates, CandidateCoin{
					Symbol:  symbol,
					Sources: []string{"static"},
				})
			}
			return e.filterExcludedCoins(candidates), nil
		}
		coins, err := e.getOILowCoins(coinSource.OILowLimit)
		if err != nil {
			logger.Warnf("⚠️  OI low ranking unavailable: %v", err)
			return e.filterExcludedCoins(e.staticFallback()), nil
		}
		// Empty list is a normal condition, return directly
		return e.filterExcludedCoins(coins), nil

	case "hyper_all":
		// All Hyperliquid perp coins
		if !coinSource.UseHyperAll {
			logger.Infof("⚠️  source_type is 'hyper_all' but use_hyper_all is false, falling back to static coins")
			for _, symbol := range coinSource.StaticCoins {
				symbol = market.Normalize(symbol)
				candidates = append(candidates, CandidateCoin{
					Symbol:  symbol,
					Sources: []string{"static"},
				})
			}
			return e.filterExcludedCoins(candidates), nil
		}
		coins, err := e.getHyperAllCoins()
		if err != nil {
			return nil, err
		}
		return e.filterExcludedCoins(coins), nil

	case "hyper_main":
		// Top N Hyperliquid coins by 24h volume
		if !coinSource.UseHyperMain {
			logger.Infof("⚠️  source_type is 'hyper_main' but use_hyper_main is false, falling back to static coins")
			for _, symbol := range coinSource.StaticCoins {
				symbol = market.Normalize(symbol)
				candidates = append(candidates, CandidateCoin{
					Symbol:  symbol,
					Sources: []string{"static"},
				})
			}
			return e.filterExcludedCoins(candidates), nil
		}
		coins, err := e.getHyperMainCoins(coinSource.HyperMainLimit)
		if err != nil {
			return nil, err
		}
		return e.filterExcludedCoins(coins), nil

	case "hyper_rank":
		coins, err := e.getHyperRankCoins(coinSource.HyperRankCategory, coinSource.HyperRankDirection, coinSource.HyperRankLimit)
		if err != nil {
			return nil, err
		}
		return e.filterExcludedCoins(coins), nil

	case "mixed":
		if coinSource.UseAI500 {
			poolCoins, err := e.getScreenerCoins(coinSource.AI500Limit)
			if err != nil {
				logger.Infof("⚠️  Failed to get screener coins: %v", err)
			} else {
				for _, coin := range poolCoins {
					symbolSources[coin.Symbol] = append(symbolSources[coin.Symbol], "screener")
				}
			}
		}

		if coinSource.UseOITop {
			oiCoins, err := e.getOITopCoins(coinSource.OITopLimit)
			if err != nil {
				logger.Infof("⚠️  Failed to get OI Top: %v", err)
			} else {
				for _, coin := range oiCoins {
					symbolSources[coin.Symbol] = append(symbolSources[coin.Symbol], "oi_top")
				}
			}
		}

		if coinSource.UseOILow {
			oiLowCoins, err := e.getOILowCoins(coinSource.OILowLimit)
			if err != nil {
				logger.Infof("⚠️  Failed to get OI Low: %v", err)
			} else {
				for _, coin := range oiLowCoins {
					symbolSources[coin.Symbol] = append(symbolSources[coin.Symbol], "oi_low")
				}
			}
		}

		if coinSource.UseHyperAll {
			hyperCoins, err := e.getHyperAllCoins()
			if err != nil {
				logger.Infof("⚠️  Failed to get Hyperliquid All coins: %v", err)
			} else {
				for _, coin := range hyperCoins {
					symbolSources[coin.Symbol] = append(symbolSources[coin.Symbol], "hyper_all")
				}
			}
		}

		if coinSource.UseHyperMain {
			hyperMainCoins, err := e.getHyperMainCoins(coinSource.HyperMainLimit)
			if err != nil {
				logger.Infof("⚠️  Failed to get Hyperliquid Main coins: %v", err)
			} else {
				for _, coin := range hyperMainCoins {
					symbolSources[coin.Symbol] = append(symbolSources[coin.Symbol], "hyper_main")
				}
			}
		}

		for _, symbol := range coinSource.StaticCoins {
			symbol = market.Normalize(symbol)
			if _, exists := symbolSources[symbol]; !exists {
				symbolSources[symbol] = []string{"static"}
			} else {
				symbolSources[symbol] = append(symbolSources[symbol], "static")
			}
		}

		for symbol, sources := range symbolSources {
			candidates = append(candidates, CandidateCoin{
				Symbol:  symbol,
				Sources: sources,
			})
		}
		return e.filterExcludedCoins(candidates), nil

	default:
		return nil, fmt.Errorf("unknown coin source type: %s", coinSource.SourceType)
	}
}

// filterExcludedCoins removes excluded coins from the candidates list
func (e *StrategyEngine) filterExcludedCoins(candidates []CandidateCoin) []CandidateCoin {
	if len(e.config.CoinSource.ExcludedCoins) == 0 {
		return candidates
	}

	// Build excluded set for O(1) lookup
	excluded := make(map[string]bool)
	for _, coin := range e.config.CoinSource.ExcludedCoins {
		normalized := market.Normalize(coin)
		excluded[normalized] = true
	}

	// Filter out excluded coins
	filtered := make([]CandidateCoin, 0, len(candidates))
	for _, c := range candidates {
		if !excluded[c.Symbol] {
			filtered = append(filtered, c)
		} else {
			logger.Infof("🚫 Excluded coin: %s", c.Symbol)
		}
	}

	return filtered
}

// getScreenerCoins returns the local screener's top-scored contracts. It is the
// free replacement for the vendor AI500 pool: the score is a documented
// composite of turnover, open interest and volatility, computed from public
// tickers.
func (e *StrategyEngine) getScreenerCoins(limit int) ([]CandidateCoin, error) {
	if limit <= 0 {
		limit = 30
	}
	service := market.CurrentUniverse()
	if service == nil {
		return nil, fmt.Errorf("market universe service is not running")
	}
	e.ensureUniverse(service)

	coins := service.Screener(limit)
	if len(coins) == 0 {
		return nil, fmt.Errorf("screener universe is empty")
	}
	candidates := make([]CandidateCoin, 0, len(coins))
	for _, coin := range coins {
		candidates = append(candidates, CandidateCoin{
			Symbol:  coin.Symbol,
			Sources: []string{"screener"},
		})
	}
	return candidates, nil
}

func (e *StrategyEngine) ensureUniverse(service *market.UniverseService) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	service.Ensure(ctx)
}

func (e *StrategyEngine) getOITopCoins(limit int) ([]CandidateCoin, error) {
	return e.getOIRankedCoins(limit, true)
}

func (e *StrategyEngine) getOILowCoins(limit int) ([]CandidateCoin, error) {
	return e.getOIRankedCoins(limit, false)
}

// getOIRankedCoins reads the local market-wide OI ranking. The 1h window is the
// same one the vendor source used; when the process has not observed it yet the
// caller gets an explicit error so it can fall back instead of silently
// returning an empty candidate list.
func (e *StrategyEngine) getOIRankedCoins(limit int, top bool) ([]CandidateCoin, error) {
	if limit <= 0 {
		limit = 10
	}
	service := market.CurrentUniverse()
	if service == nil {
		return nil, fmt.Errorf("market universe service is not running")
	}
	e.ensureUniverse(service)

	topItems, lowItems, ok := service.OIRanking(time.Hour, limit)
	if !ok {
		return nil, fmt.Errorf("1h open-interest history is not available yet")
	}
	items, source := topItems, "oi_top"
	if !top {
		items, source = lowItems, "oi_low"
	}
	candidates := make([]CandidateCoin, 0, len(items))
	for _, item := range items {
		candidates = append(candidates, CandidateCoin{
			Symbol:  item.Symbol,
			Sources: []string{source},
		})
	}
	return candidates, nil
}

// staticFallback keeps a failed candidate source from stopping trading when the
// user configured a static list.
func (e *StrategyEngine) staticFallback() []CandidateCoin {
	coins := make([]CandidateCoin, 0, len(e.config.CoinSource.StaticCoins))
	for _, symbol := range e.config.CoinSource.StaticCoins {
		if canonical := market.Normalize(symbol); canonical != "" {
			coins = append(coins, CandidateCoin{Symbol: canonical, Sources: []string{"static"}})
		}
	}
	return coins
}

// rankingWindow maps a configured duration onto the local ranking window.
func rankingWindow(duration string) (time.Duration, bool) {
	switch strings.ToLower(strings.TrimSpace(duration)) {
	case "5m":
		return 5 * time.Minute, true
	case "15m":
		return 15 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "4h":
		return 4 * time.Hour, true
	case "12h":
		return 12 * time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

// getHyperAllCoins returns all available Hyperliquid perpetual coins
func (e *StrategyEngine) getHyperAllCoins() ([]CandidateCoin, error) {
	ctx := context.Background()
	symbols, err := hyperliquid.GetAllCoinSymbols(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get Hyperliquid coins: %w", err)
	}

	var candidates []CandidateCoin
	for _, symbol := range symbols {
		// Add USDT suffix for compatibility
		normalizedSymbol := market.Normalize(symbol + "USDT")
		candidates = append(candidates, CandidateCoin{
			Symbol:  normalizedSymbol,
			Sources: []string{"hyper_all"},
		})
	}
	logger.Infof("✅ Loaded %d Hyperliquid coins (hyper_all)", len(candidates))
	return candidates, nil
}

// getHyperMainCoins returns top N Hyperliquid coins by 24h volume
func (e *StrategyEngine) getHyperMainCoins(limit int) ([]CandidateCoin, error) {
	if limit <= 0 {
		limit = 20
	}

	ctx := context.Background()
	symbols, err := hyperliquid.GetMainCoinSymbols(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get Hyperliquid main coins: %w", err)
	}

	var candidates []CandidateCoin
	for _, symbol := range symbols {
		// Add USDT suffix for compatibility
		normalizedSymbol := market.Normalize(symbol + "USDT")
		candidates = append(candidates, CandidateCoin{
			Symbol:  normalizedSymbol,
			Sources: []string{"hyper_main"},
		})
	}
	logger.Infof("✅ Loaded %d Hyperliquid main coins (hyper_main) by 24h volume", len(candidates))
	return candidates, nil
}

func clampHyperRankLimit(limit int) int {
	if limit <= 0 {
		return store.DefaultHyperRankLimit
	}
	if limit > store.MaxCandidateCoins {
		return store.MaxCandidateCoins
	}
	return limit
}

func (e *StrategyEngine) getHyperRankCoins(category, direction string, limit int) ([]CandidateCoin, error) {
	category = strings.ToLower(strings.TrimSpace(category))
	if category == "" {
		category = "stock"
	}
	direction = strings.ToLower(strings.TrimSpace(direction))
	if direction == "" {
		// Keep this in step with the config default: an unset direction must not
		// resolve differently here than in the stored strategy.
		direction = store.DefaultHyperRankDirection
	}
	limit = clampHyperRankLimit(limit)

	ctx := context.Background()
	var ranked []struct {
		symbol string
		info   hyperliquid.CoinInfo
		cat    string
	}

	if category == "crypto" || category == "all" {
		coins, err := hyperliquid.GetPerpDexCoins(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("failed to get Hyperliquid crypto ranking: %w", err)
		}
		for _, coin := range coins {
			ranked = append(ranked, struct {
				symbol string
				info   hyperliquid.CoinInfo
				cat    string
			}{symbol: market.Normalize(coin.Symbol + "USDT"), info: coin, cat: "crypto"})
		}
	}

	if category != "crypto" {
		coins, err := hyperliquid.GetPerpDexCoins(ctx, "xyz")
		if err != nil {
			return nil, fmt.Errorf("failed to get Hyperliquid XYZ ranking: %w", err)
		}
		for _, coin := range coins {
			base := strings.TrimPrefix(coin.Symbol, "xyz:")
			cat := hyperliquid.XYZCategory(base)
			if category != "all" && cat != category {
				continue
			}
			ranked = append(ranked, struct {
				symbol string
				info   hyperliquid.CoinInfo
				cat    string
			}{symbol: hyperliquid.FormatCoinForAPI("xyz:" + base), info: coin, cat: cat})
		}
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		switch direction {
		case "losers":
			return ranked[i].info.Change24hPct < ranked[j].info.Change24hPct
		case "volume":
			return ranked[i].info.Volume24h > ranked[j].info.Volume24h
		default:
			return ranked[i].info.Change24hPct > ranked[j].info.Change24hPct
		}
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	candidates := make([]CandidateCoin, 0, len(ranked))
	source := fmt.Sprintf("hyper_rank_%s_%s", category, direction)
	for _, item := range ranked {
		candidates = append(candidates, CandidateCoin{Symbol: item.symbol, Sources: []string{source}})
	}
	logger.Infof("✅ Loaded %d Hyperliquid rank coins (%s/%s, capped at %d)", len(candidates), category, direction, limit)
	return candidates, nil
}

// ============================================================================
// External & Quant Data
// ============================================================================

// FetchMarketData fetches market data based on strategy configuration
func (e *StrategyEngine) FetchMarketData(symbol string) (*market.Data, error) {
	return market.Get(symbol)
}

// FetchExternalData fetches external data sources
func (e *StrategyEngine) FetchExternalData() (map[string]interface{}, error) {
	externalData := make(map[string]interface{})

	for _, source := range e.config.Indicators.ExternalDataSources {
		data, err := e.fetchSingleExternalSource(source)
		if err != nil {
			logger.Infof("⚠️  Failed to fetch external data source [%s]: %v", source.Name, err)
			continue
		}
		externalData[source.Name] = data
	}

	return externalData, nil
}

func (e *StrategyEngine) fetchSingleExternalSource(source store.ExternalDataSource) (interface{}, error) {
	// SSRF Protection: Validate URL before making request
	if err := security.ValidateURL(source.URL); err != nil {
		return nil, fmt.Errorf("external source URL validation failed: %w", err)
	}

	timeout := time.Duration(source.RefreshSecs) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	// Use SSRF-safe HTTP client
	client := security.SafeHTTPClient(timeout)

	req, err := http.NewRequest(source.Method, source.URL, nil)
	if err != nil {
		return nil, err
	}

	for k, v := range source.Headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if source.DataPath != "" {
		result = extractJSONPath(result, source.DataPath)
	}

	return result, nil
}

func extractJSONPath(data interface{}, path string) interface{} {
	parts := strings.Split(path, ".")
	current := data

	for _, part := range parts {
		if m, ok := current.(map[string]interface{}); ok {
			current = m[part]
		} else {
			return nil
		}
	}

	return current
}

// FetchQuantData fetches quantitative data for a single coin
func (e *StrategyEngine) FetchQuantData(symbol string) (*QuantData, error) {
	if !e.config.Indicators.EnableQuantData {
		return nil, nil
	}
	if e.usesHyperliquidNativeUniverse() || market.IsXyzDexAsset(symbol) {
		logger.Infof("⏭️  Skipping NofxOS quant data for Hyperliquid symbol %s; using native Hyperliquid klines/mark data only", symbol)
		return nil, nil
	}

	// Use nofxos client with unified API key
	include := "oi,price"
	if e.config.Indicators.EnableQuantNetflow {
		include = "netflow,oi,price"
	}

	nofxosData, err := e.nofxosClient.GetCoinData(symbol, include)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch quant data: %w", err)
	}

	if nofxosData == nil {
		return nil, nil
	}

	return convertQuantData(nofxosData), nil
}

// convertQuantData maps the provider payload onto the prompt-facing type. It is
// pure so the field mapping (including the long/short exposure that travels
// with OI) can be tested without the network.
func convertQuantData(nofxosData *nofxos.QuantData) *QuantData {
	if nofxosData == nil {
		return nil
	}

	quantData := &QuantData{
		Symbol:      nofxosData.Symbol,
		Price:       nofxosData.Price,
		PriceChange: nofxosData.PriceChange,
	}

	// Convert OI data
	if nofxosData.OI != nil {
		quantData.OI = make(map[string]*OIData)
		for exchange, oiData := range nofxosData.OI {
			if oiData != nil {
				kData := &OIData{
					CurrentOI: oiData.CurrentOI,
					NetLong:   oiData.NetLong,
					NetShort:  oiData.NetShort,
				}
				if oiData.Delta != nil {
					kData.Delta = make(map[string]*OIDeltaData)
					for dur, delta := range oiData.Delta {
						if delta != nil {
							kData.Delta[dur] = &OIDeltaData{
								OIDelta:        delta.OIDelta,
								OIDeltaValue:   delta.OIDeltaValue,
								OIDeltaPercent: delta.OIDeltaPercent,
							}
						}
					}
				}
				quantData.OI[exchange] = kData
			}
		}
	}

	// Convert Netflow data
	if nofxosData.Netflow != nil {
		quantData.Netflow = &NetflowData{}
		if nofxosData.Netflow.Institution != nil {
			quantData.Netflow.Institution = &FlowTypeData{
				Future: nofxosData.Netflow.Institution.Future,
				Spot:   nofxosData.Netflow.Institution.Spot,
			}
		}
		if nofxosData.Netflow.Personal != nil {
			quantData.Netflow.Personal = &FlowTypeData{
				Future: nofxosData.Netflow.Personal.Future,
				Spot:   nofxosData.Netflow.Personal.Spot,
			}
		}
	}

	return quantData
}

// FetchQuantDataBatch batch fetches quantitative data
func (e *StrategyEngine) FetchQuantDataBatch(symbols []string) map[string]*QuantData {
	result := make(map[string]*QuantData)

	if !e.config.Indicators.EnableQuantData {
		return result
	}

	for _, symbol := range symbols {
		data, err := e.FetchQuantData(symbol)
		if err != nil {
			logger.Infof("⚠️  Failed to fetch quantitative data for %s: %v", symbol, err)
			continue
		}
		if data != nil {
			result[symbol] = data
		}
	}

	return result
}

// FetchOIRankingData builds the market-wide OI ranking from the local universe.
func (e *StrategyEngine) FetchOIRankingData() *market.OIRankingData {
	indicators := e.config.Indicators
	if !indicators.EnableOIRanking {
		return nil
	}
	if e.usesHyperliquidNativeUniverse() {
		logger.Infof("⏭️  Skipping OI ranking for Hyperliquid strategy; native Hyperliquid universe is the source of truth")
		return nil
	}

	duration := indicators.OIRankingDuration
	if duration == "" {
		duration = "1h"
	}
	window, windowOK := rankingWindow(duration)
	if !windowOK {
		logger.Warnf("⚠️  Unsupported OI ranking duration %q, skipping", duration)
		return nil
	}

	limit := indicators.OIRankingLimit
	if limit <= 0 {
		limit = 10
	}

	service := market.CurrentUniverse()
	if service == nil {
		return nil
	}
	e.ensureUniverse(service)

	top, low, ok := service.OIRanking(window, limit)
	if !ok {
		logger.Infof("⏭️  OI ranking (%s) unavailable: the local snapshot history is too short", duration)
		return nil
	}
	logger.Infof("✓ OI ranking ready: %d top, %d low (%s)", len(top), len(low), duration)
	return &market.OIRankingData{Duration: duration, Top: top, Low: low, FetchedAt: time.Now()}
}

// FetchNetFlowRankingData fetches market-wide NetFlow ranking data
func (e *StrategyEngine) FetchNetFlowRankingData() *nofxos.NetFlowRankingData {
	indicators := e.config.Indicators
	if !indicators.EnableNetFlowRanking {
		return nil
	}
	if e.usesHyperliquidNativeUniverse() {
		logger.Infof("⏭️  Skipping NofxOS netflow ranking for Hyperliquid strategy; native Hyperliquid universe is the source of truth")
		return nil
	}

	duration := indicators.NetFlowRankingDuration
	if duration == "" {
		duration = "1h"
	}

	limit := indicators.NetFlowRankingLimit
	if limit <= 0 {
		limit = 10
	}

	logger.Infof("💰 Fetching NetFlow ranking data (duration: %s, limit: %d)", duration, limit)

	data, err := e.nofxosClient.GetNetFlowRanking(duration, limit)
	if err != nil {
		logger.Warnf("⚠️  Failed to fetch NetFlow ranking data: %v", err)
		return nil
	}

	logger.Infof("✓ NetFlow ranking data ready: inst_in=%d, inst_out=%d, retail_in=%d, retail_out=%d",
		len(data.InstitutionFutureTop), len(data.InstitutionFutureLow),
		len(data.PersonalFutureTop), len(data.PersonalFutureLow))

	return data
}

// FetchPriceRankingData builds the market-wide price ranking from the local
// universe. The 24h window comes from the venue ticker and is always available;
// shorter windows need local snapshot history and are omitted until it exists.
func (e *StrategyEngine) FetchPriceRankingData() *market.PriceRankingData {
	indicators := e.config.Indicators
	if !indicators.EnablePriceRanking {
		return nil
	}
	if e.usesHyperliquidNativeUniverse() {
		logger.Infof("⏭️  Skipping price ranking for Hyperliquid strategy; native Hyperliquid universe is the source of truth")
		return nil
	}

	durations := indicators.PriceRankingDuration
	if durations == "" {
		durations = "1h"
	}

	limit := indicators.PriceRankingLimit
	if limit <= 0 {
		limit = 10
	}

	service := market.CurrentUniverse()
	if service == nil {
		return nil
	}
	e.ensureUniverse(service)

	data := &market.PriceRankingData{
		Durations: make(map[string]*market.PriceRankingDuration),
		FetchedAt: time.Now(),
	}
	for _, duration := range strings.Split(durations, ",") {
		duration = strings.TrimSpace(duration)
		window, ok := rankingWindow(duration)
		if !ok || duration == "" {
			continue
		}
		top, low, ok := service.PriceRanking(window, limit)
		if !ok {
			logger.Infof("⏭️  Price ranking (%s) unavailable: the local snapshot history is too short", duration)
			continue
		}
		data.Durations[duration] = &market.PriceRankingDuration{Top: top, Low: low}
	}
	if len(data.Durations) == 0 {
		return nil
	}
	logger.Infof("✓ Price ranking ready for %d durations", len(data.Durations))
	return data
}

// ============================================================================
// Helper Functions
// ============================================================================

// detectLanguage detects language from text content
// Returns LangChinese if text contains Chinese characters, otherwise LangEnglish
func detectLanguage(text string) Language {
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			return LangChinese
		}
	}
	return LangEnglish
}
