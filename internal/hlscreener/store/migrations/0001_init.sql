-- nofx-hl-screener schema
-- 设计原则: 时间序列用 bucket-based 分区友好, 状态表用普通表
-- 时间戳全部 epoch ms

-- ============================================================================
-- 标的基础元数据 (snapshot 自 HL meta, 低频更新)
-- ============================================================================
CREATE TABLE IF NOT EXISTS coin_meta (
  coin          TEXT PRIMARY KEY,        -- "BTC", "ETH"
  sz_decimals   INT  NOT NULL,
  max_leverage  INT  NOT NULL,
  only_perp     BOOL NOT NULL DEFAULT TRUE,
  mark_px       NUMERIC NOT NULL,
  oracle_px     NUMERIC NOT NULL,
  updated_at    BIGINT NOT NULL           -- epoch ms
);
CREATE INDEX IF NOT EXISTS idx_coin_meta_updated ON coin_meta(updated_at);

-- ============================================================================
-- 行情 K 线 (1m 粒度, 由 collector 从 HL candleSnapshot 攒)
-- ============================================================================
CREATE TABLE IF NOT EXISTS ohlc_1m (
  coin  TEXT NOT NULL,
  t     BIGINT NOT NULL,                   -- bar open time (epoch ms, align to minute)
  o     NUMERIC NOT NULL,
  h     NUMERIC NOT NULL,
  l     NUMERIC NOT NULL,
  c     NUMERIC NOT NULL,
  v     NUMERIC NOT NULL,                  -- 成交量(基础币)
  PRIMARY KEY (coin, t)
);
-- 自动转 5/15/60/240/1440: 在 collector 周期内用 SQL window 或者前端
CREATE INDEX IF NOT EXISTS idx_ohlc_1m_coin_t ON ohlc_1m(coin, t DESC);

-- ============================================================================
-- 资金费率 (per funding event, HL 每 1h 或 4h 结算)
-- ============================================================================
CREATE TABLE IF NOT EXISTS funding (
  coin       TEXT NOT NULL,
  t          BIGINT NOT NULL,              -- 结算时刻
  rate       NUMERIC NOT NULL,             -- 8h 等价
  mark_px    NUMERIC NOT NULL,
  PRIMARY KEY (coin, t)
);
CREATE INDEX IF NOT EXISTS idx_funding_coin_t ON funding(coin, t DESC);

-- ============================================================================
-- OI (open interest, snapshot 自 HL meta, 周期采集)
-- ============================================================================
CREATE TABLE IF NOT EXISTS oi_snapshots (
  coin       TEXT NOT NULL,
  t          BIGINT NOT NULL,
  oi_usd     NUMERIC NOT NULL,
  PRIMARY KEY (coin, t)
);

-- ============================================================================
-- CVD 桶 (5 分钟粒度, 来自 trade tape, baseline 锚定)
-- ============================================================================
CREATE TABLE IF NOT EXISTS cvd_bars (
  coin      TEXT NOT NULL,
  bucket    BIGINT NOT NULL,               -- 5m-aligned epoch ms
  buy       NUMERIC NOT NULL DEFAULT 0,    -- 主动买成交量
  sell      NUMERIC NOT NULL DEFAULT 0,    -- 主动卖成交量
  delta     NUMERIC NOT NULL DEFAULT 0,    -- buy - sell
  PRIMARY KEY (coin, bucket)
);
CREATE INDEX IF NOT EXISTS idx_cvd_coin_bucket ON cvd_bars(coin, bucket DESC);

-- baseline (CVD 起点, 滑动窗口前的累计值, 来自 Superior 模式)
CREATE TABLE IF NOT EXISTS cvd_baselines (
  coin       TEXT PRIMARY KEY,
  baseline   NUMERIC NOT NULL,             -- 当前 baseline (pre-window 的 sum)
  updated_at BIGINT NOT NULL
);

-- ============================================================================
-- 清算热力图 (liq heatmap, snapshot 自 collector)
-- ============================================================================
CREATE TABLE IF NOT EXISTS liq_levels (
  coin            TEXT NOT NULL,
  bin_start       NUMERIC NOT NULL,         -- 价格 bin 下界
  bin_end         NUMERIC NOT NULL,
  liquidation_usd NUMERIC NOT NULL,         -- 该 bin 名义清算价值
  positions_count INT  NOT NULL,
  first_seen      BIGINT NOT NULL,          -- 我们第一次看到该 bin 有流量的时刻
  last_seen       BIGINT NOT NULL,
  PRIMARY KEY (coin, bin_start)
);
CREATE INDEX IF NOT EXISTS idx_liq_coin_firstseen ON liq_levels(coin, first_seen DESC);

-- ============================================================================
-- 标的候选池 (screener 产物)
-- ============================================================================
CREATE TABLE IF NOT EXISTS candidate_pool (
  coin          TEXT NOT NULL,
  rank          INT  NOT NULL,             -- 1 = 最强候选
  score         NUMERIC NOT NULL,          -- 0-100 综合分
  reasons       JSONB NOT NULL,            -- 通过/拒绝的层 + 数值
  in_pool       BOOL NOT NULL,             -- 是否在交易候选中
  generated_at  BIGINT NOT NULL,
  expires_at    BIGINT NOT NULL,           -- 通常 4h 后失效
  PRIMARY KEY (coin, generated_at)
);
CREATE INDEX IF NOT EXISTS idx_pool_generated ON candidate_pool(generated_at DESC, in_pool);

-- 当前生效的池子 (materialized view 替代: 由 screener 周期刷新)
CREATE TABLE IF NOT EXISTS candidate_active (
  coin         TEXT PRIMARY KEY,
  score        NUMERIC NOT NULL,
  reasons      JSONB NOT NULL,
  activated_at BIGINT NOT NULL,
  expires_at   BIGINT NOT NULL
);

-- ============================================================================
-- Collector 状态 (用于 leader election / 防止重复记录)
-- ============================================================================
CREATE TABLE IF NOT EXISTS collector_state (
  id           INT PRIMARY KEY DEFAULT 1,
  started_at   BIGINT NOT NULL,
  last_tick    BIGINT NOT NULL,
  last_run     JSONB NOT NULL DEFAULT '{}',
  CHECK (id = 1)
);

-- ============================================================================
-- 24h/7d 滚动统计 (供 screener 5 层过滤使用, 周期刷新)
-- ============================================================================
CREATE TABLE IF NOT EXISTS rolling_stats (
  coin          TEXT NOT NULL,
  window_label  TEXT NOT NULL,              -- '24h', '7d'
  vol_usd       NUMERIC NOT NULL,
  oi_usd        NUMERIC NOT NULL,
  atr_pct       NUMERIC NOT NULL,
  funding_pct   NUMERIC NOT NULL,           -- 最新 8h funding
  cvd_24h       NUMERIC NOT NULL,           -- 24h CVD delta
  oi_change_24h NUMERIC NOT NULL,           -- 24h OI 变化 (%)
  generated_at  BIGINT NOT NULL,
  PRIMARY KEY (coin, window_label)
);

-- ============================================================================
-- Audit / logs
-- ============================================================================
CREATE TABLE IF NOT EXISTS screener_runs (
  id           BIGSERIAL PRIMARY KEY,
  started_at   BIGINT NOT NULL,
  ended_at     BIGINT NOT NULL,
  total_coins  INT NOT NULL,
  survivors    INT NOT NULL,
  layer_stats  JSONB NOT NULL
);
