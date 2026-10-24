// Strategy Studio Types
export interface Strategy {
  id: string;
  name: string;
  description: string;
  is_active: boolean;
  is_default: boolean;
  is_public: boolean;           // 是否在策略市场公开
  config_visible: boolean;      // 配置参数是否公开可见
  config: StrategyConfig;
  created_at: string;
  updated_at: string;
}

// 策略使用统计
export interface StrategyStats {
  clone_count: number;          // 被克隆次数
  active_users: number;         // 当前使用人数
  top_performers?: StrategyPerformer[];  // 收益排行
}

// 策略使用者收益排行
export interface StrategyPerformer {
  user_id: string;
  user_name: string;            // 脱敏后的用户名
  total_pnl_pct: number;        // 总收益率
  total_pnl: number;            // 总收益金额
  win_rate: number;             // 胜率
  trade_count: number;          // 交易次数
  using_since: string;          // 使用开始时间
  rank: number;                 // 排名
}

export interface PromptSectionsConfig {
  role_definition?: string;
  trading_frequency?: string;
  entry_standards?: string;
  decision_process?: string;
}

export interface StrategyConfig {
  // Strategy type: "ai_trading" (default) or "grid_trading"
  strategy_type?: 'ai_trading' | 'grid_trading';
  // Language setting: "zh" for Chinese, "en" for English
  // Determines the language used for data formatting and prompt generation
  language?: 'zh' | 'en';
  // Trading mode variant injected into the system prompt. Empty keeps the
  // balanced baseline. Also accepted nested under ai_config.
  trading_mode?: 'aggressive' | 'conservative' | 'scalping';
  // AI trading configuration. Legacy flat fields below are accepted only for
  // old data returned before the schema was split by strategy type.
  ai_config?: AIStrategyConfig;
  coin_source?: CoinSourceConfig;
  indicators?: IndicatorConfig;
  custom_prompt?: string;
  risk_control?: RiskControlConfig;
  prompt_sections?: PromptSectionsConfig;
  protections?: ProtectionConfig;
  // Grid trading configuration (only used when strategy_type is 'grid_trading')
  grid_config?: GridStrategyConfig | null;
  publish_config?: PublishStrategyConfig;
}

export interface AIStrategyConfig {
  trading_mode?: 'aggressive' | 'conservative' | 'scalping';
  coin_source: CoinSourceConfig;
  indicators: IndicatorConfig;
  custom_prompt?: string;
  risk_control: RiskControlConfig;
  prompt_sections?: PromptSectionsConfig;
  protections?: ProtectionConfig;
}

// Code-enforced circuit breakers (modelled on Freqtrade protections).
// All checks run BEFORE any opening order and cannot be bypassed by the AI.
export interface ProtectionConfig {
  // Halt new opens while the selected curve is this % below its window peak.
  max_drawdown_pct?: number;
  max_drawdown_lookback_h?: number;
  // Which curve the drawdown breaker watches: equity (wallet + unrealized) or balance (realized only).
  drawdown_basis?: 'equity' | 'balance';
  // Halt new opens after N losing closes within the lookback window.
  losing_closes_guard_count?: number;
  losing_closes_guard_lookback_h?: number;
  // Block re-opening the same symbol for this long after it was closed.
  cooldown_minutes?: number;
  // Drop a symbol after N closes within the window with non-positive total PnL.
  low_profit_lookback_h?: number;
  low_profit_min_trades?: number;
}

export interface PublishStrategyConfig {
  is_public: boolean;
  config_visible: boolean;
}

// Grid trading specific configuration
export interface GridStrategyConfig {
  // Trading pair (e.g., "BTCUSDT")
  symbol: string;
  // Number of grid levels (5-50)
  grid_count: number;
  // Total investment in USDT
  total_investment: number;
  // Leverage (1-20)
  leverage: number;
  // Upper price boundary (0 = auto-calculate from ATR)
  upper_price: number;
  // Lower price boundary (0 = auto-calculate from ATR)
  lower_price: number;
  // Use ATR to auto-calculate bounds
  use_atr_bounds: boolean;
  // ATR multiplier for bound calculation (default 2.0)
  atr_multiplier: number;
  // Position distribution: "uniform" | "gaussian" | "pyramid"
  distribution: 'uniform' | 'gaussian' | 'pyramid';
  // Maximum drawdown percentage before emergency exit
  max_drawdown_pct: number;
  // Stop loss percentage per position
  stop_loss_pct: number;
  // Daily loss limit percentage
  daily_loss_limit_pct: number;
  // Use maker-only orders for lower fees
  use_maker_only: boolean;
  // Enable automatic grid direction adjustment based on box breakouts
  enable_direction_adjust?: boolean;
  // Direction bias ratio for long_bias/short_bias modes (default 0.7 = 70%/30%)
  direction_bias_ratio?: number;
}

export interface CoinSourceConfig {
  source_type: 'static' | 'ai500' | 'oi_top' | 'oi_low' | 'hyper_all' | 'hyper_main' | 'hyper_rank';
  static_coins?: string[];
  excluded_coins?: string[];   // 排除的币种列表
  use_ai500: boolean;
  ai500_limit?: number;
  use_oi_top: boolean;
  oi_top_limit?: number;
  use_oi_low: boolean;
  oi_low_limit?: number;
  use_hyper_all?: boolean;
  use_hyper_main?: boolean;
  hyper_main_limit?: number;
  hyper_rank_category?: 'stock' | 'commodity' | 'index' | 'forex' | 'pre_ipo' | 'crypto' | 'all';
  hyper_rank_direction?: 'gainers' | 'losers' | 'volume';
  hyper_rank_limit?: number;
  // Note: API URLs are now built automatically using nofxos_api_key from IndicatorConfig
}

export interface IndicatorConfig {
  klines: KlineConfig;
  // Raw OHLCV kline data - required for AI analysis
  enable_raw_klines: boolean;
  // Technical indicators (optional)
  enable_ema: boolean;
  enable_macd: boolean;
  enable_rsi: boolean;
  enable_atr: boolean;
  enable_boll: boolean;
  enable_adx: boolean;
  enable_supertrend: boolean;
  enable_volume: boolean;
  enable_oi: boolean;
  enable_funding_rate: boolean;
  // Order book microstructure metrics; absent (undefined) = enabled, only
  // an explicit false disables it (matches the backend *bool nil=on default).
  enable_microstructure?: boolean;
  // Hyperliquid-native positioning (funding percentile, OI quadrants, taker
  // flow). Same nil=on convention as microstructure.
  enable_positioning?: boolean;
  // Holder cost basis / liquidation fuel. The opposite convention: absent =
  // disabled, because it runs a background index that polls the venue per
  // address.
  enable_position_structure?: boolean;
  // Per-venue OI/funding and the public liquidation streams. Same nil=on
  // convention as microstructure: only an explicit false disables them.
  enable_cross_venue?: boolean;
  enable_liquidation_flow?: boolean;
  ema_periods?: number[];
  rsi_periods?: number[];
  atr_periods?: number[];
  macd_fast_period?: number;
  macd_slow_period?: number;
  adx_periods?: number[];
  supertrend_period?: number;
  supertrend_multiplier?: number;
  boll_periods?: number[];
  boll_multiplier?: number;
  external_data_sources?: ExternalDataSource[];

  // ========== NofxOS 数据源统一配置 ==========
  // Unified NofxOS API Key - used for all NofxOS data sources
  nofxos_api_key?: string;

  // 量化数据源（资金流向、持仓变化、价格变化）
  enable_quant_data?: boolean;
  enable_quant_oi?: boolean;

  // OI 排行数据（市场持仓量增减排行）
  enable_oi_ranking?: boolean;
  oi_ranking_duration?: string;  // "1h", "4h", "24h"
  oi_ranking_limit?: number;

  // NetFlow 排行数据（机构/散户资金流向排行）
  enable_netflow_ranking?: boolean;
  netflow_ranking_duration?: string;  // "1h", "4h", "24h"
  netflow_ranking_limit?: number;

  // Price 排行数据（涨跌幅排行）
  enable_price_ranking?: boolean;
  price_ranking_duration?: string;  // "1h", "4h", "24h" or "1h,4h,24h"
  price_ranking_limit?: number;
}

export interface KlineConfig {
  primary_timeframe: string;
  primary_count: number;
  longer_timeframe?: string;
  longer_count?: number;
  enable_multi_timeframe: boolean;
  // 新增：支持选择多个时间周期
  selected_timeframes?: string[];
  // 展示上限：prompt 中每个时间周期最多打印多少根 K 线/多少个指标值
  // （分析窗口仍是 primary_count，仅影响 prompt 体积）。0/未填 = 默认 20。
  prompt_display_bars?: number;
}

export interface ExternalDataSource {
  name: string;
  type: 'api' | 'webhook';
  url: string;
  method: string;
  headers?: Record<string, string>;
  data_path?: string;
  refresh_secs?: number;
}

export interface RiskControlConfig {
  // Max number of coins held simultaneously (CODE ENFORCED)
  max_positions: number;

  // Trading Leverage - exchange leverage for opening positions (AI guided)
  btc_eth_max_leverage: number;    // BTC/ETH max exchange leverage
  altcoin_max_leverage: number;    // Altcoin max exchange leverage

  // Position Value Ratio - single position notional value / account equity (CODE ENFORCED)
  // Max position value = equity × this ratio
  btc_eth_max_position_value_ratio?: number;     // default: 5 (BTC/ETH max position = 5x equity)
  altcoin_max_position_value_ratio?: number;     // default: 1 (Altcoin max position = 1x equity)

  // Risk Parameters
  max_margin_usage: number;        // Max margin utilization, e.g. 0.9 = 90% (CODE ENFORCED)
  min_position_size: number;       // Min position size in USDT (CODE ENFORCED)
  min_risk_reward_ratio: number;   // Min take_profit / stop_loss ratio (AI guided)
  min_confidence: number;          // Min AI confidence to open position (AI guided)

  // Entry order mode: "market" (default) submits the aggressive order as before;
  // "maker" rests a post-only limit on the passive side to pay the maker fee and
  // falls back to the market order after maker_entry_timeout_sec. Opening orders
  // only — protective exits stay aggressive.
  entry_order_mode?: 'market' | 'maker';
  maker_entry_timeout_sec?: number; // default 20, clamped to 5-60 by the backend

  // Minimum holding period for discretionary AI exits; 0 = off (default), a
  // positive value is enforced. Stop-loss/take-profit and the drawdown monitor
  // are never delayed.
  min_hold_minutes?: number;

  // Stock session gate: block NEW entries in stock perps while the venue shows
  // no activity (its cash market is most likely closed). Absent = enabled;
  // close/reduce are never blocked. stock_quiet_minutes default 30, clamped 5-120.
  block_cold_stock_entries?: boolean;
  stock_quiet_minutes?: number;

  // Correlated-exposure cap (code enforced): same-side positions whose returns
  // correlate at or above the threshold count as one bet. 0 = default cap (2),
  // negative disables; correlation_threshold default 0.8, clamped 0.5-0.99.
  max_correlated_positions?: number;
  correlation_threshold?: number;
}
