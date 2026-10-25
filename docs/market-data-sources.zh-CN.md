# 市场数据来源与口径

> 跨所持仓指标、爆仓流、以及它们如何进入 AI 决策与离线回放

本文说明 nofx 自己采集哪些市场数据、从哪里采、以什么口径落库、以及下游如何使用。所有接口均为公开接口，**不需要任何 API key 或环境变量**。

---

## 一、数据来源

| 数据 | 交易所 | 接口 | 认证 | 频率 |
|------|--------|------|------|------|
| 资金费率 / 标记价 / 指数价（全市场） | Binance | `GET /fapi/v1/premiumIndex` | 无 | 60s |
| 持仓量（逐 symbol） | Binance | `GET /fapi/v1/openInterest` | 无 | 60s |
| 资金费率历史 | Binance | `GET /fapi/v1/fundingRate` | 无 | 按需 |
| 爆仓流（全市场） | Binance | WS `!forceOrder@arr` | 无 | 实时 |
| 行情 / OI / 资金费 / 标记价（全市场） | Bybit | `GET /v5/market/tickers?category=linear` | 无 | 60s |
| 结算周期 | Bybit | `GET /v5/market/instruments-info` | 无 | 启动 + 6h |
| 爆仓流（逐 symbol） | Bybit | WS `allLiquidation.{symbol}` | 无 | 实时 |
| 资金费 / OI / 溢价 | Hyperliquid | `POST /info` `type=metaAndAssetCtxs` | 无 | 复用 60s 缓存 |

> **Hyperliquid 爆仓流未覆盖。** 官方没有全市场清算频道：公共 `trades` 流只带 `users` 地址、不带清算标记，`userFills` 只能按地址查询，而协议清算账户没有可自动发现的公开地址（HLP vault 的 `userFills` 返回空）。因此爆仓窗口只统计 Binance 与 Bybit。HL 的清算风险仍由 `position_profile` 的强平簇（基于真实地址持仓）覆盖。

符号映射：

- 归一化符号（内部主键）由 `market.Normalize` 产生，例如 `BTC` → `BTCUSDT`，`xyz:NVDA` 保持原样。
- xyz（HIP-3 股票/商品/指数）只在 Hyperliquid 上有，Binance / Bybit 自动跳过；跨所读数需要至少两个交易所，因此 xyz 资产不会产生跨所段落（HL 原生持仓信号仍然覆盖）。

---

## 二、落库

两张表，定义在 `store/market_metrics.go`，启动时由 GORM AutoMigrate（PostgreSQL 已存在表则跳过）。

### `market_venue_metrics`

每个交易所、每个 symbol、每个时间点一行。

| 列 | 含义 |
|----|------|
| `venue` / `symbol` / `venue_symbol` | 交易所、归一化符号、交易所原始符号 |
| `timestamp` | 该行读数的时间（交易所返回时间优先） |
| `mark_price` / `oi_base` / `oi_usd` | 标记价、币本位持仓量、美元持仓量 |
| `funding_rate_1h_pct` | **已归一到每小时**的资金费率（百分数） |
| `funding_interval_h` | 原始结算周期；Bybit 取不到时按 8h 并记录该值 |
| `premium_pct` | `(mark - index) / index * 100` |
| `source_ok` | 降级行为 false，读取端会丢弃 |

- 唯一索引 `(venue, symbol, timestamp)`，写入使用 `ON CONFLICT DO NOTHING`，重试幂等。
- 采集器每 60s 刷新内存读数，每 5 次轮询（5 分钟）落一批库。
- 保留 30 天，由采集器每天清理一次。

### `market_liquidations`

| 列 | 含义 |
|----|------|
| `venue` / `symbol` / `venue_symbol` | 同上 |
| `timestamp` | 强平成交时间 |
| `side` | **统一为 `long` / `short`（被强平的一侧）** |
| `price` / `qty` / `notional_usd` | 成交价、数量、名义额 |

- 唯一索引 `(venue, venue_symbol, timestamp, side, price, qty)`，WS 重连补发的重复打印不会重复计数。
- 缓冲 2s 或 500 条一批写入。
- 保留 30 天。

> 只从今天开始向前累积。需要更长的历史请另行导入交易所归档（如 `data.binance.vision`），本期不提供批量回补工具。

---

## 二之二、K 线来源：跟随执行所

决策、图表、下单取价和回放都使用**交易员执行所的原生 K 线**，不再经过 CoinAnk 或任何第三方代理。

| 场景 | 来源 |
|------|------|
| AI 决策/指标/网格箱体 | 执行所原生：HL → `candleSnapshot`；Binance → `/fapi/v1/klines`；Bybit → `/v5/market/kline`；OKX → `/api/v5/market/candles` |
| 图表（加密） | 跟随当前 trader 的交易所（HL 交易员即 HL） |
| 下单前取价 | 执行所原生，禁止跨所回退 |
| 回放/评估 | `--venue` 指定，默认 binance |

要点：

- **不做跨所回退**。Binance 的一根插针不是 HL 止损/强平的触发路径；取不到就省略或报错，不静默换源。xyz 资产始终由 HL 提供。
- **HL 周期**：支持 `1m/3m/5m/15m/30m/1h/2h/4h/8h/12h/1d/1w/1M`；**没有 6h/3d**，配置时会明确报错，不再近似成 4h/1d。
- **HL 保留期**：每个周期约 5000 根（3m ≈ 10 天，1h ≈ 208 天），更早的历史无法翻页取得。回放窗口超过保留期时，`--venue hyperliquid` 会提示；需要更长历史请显式使用 `--venue binance` 作为参考。
- **HL 成交量**是币本位，与 Binance `Volume` 口径一致。
- 同一 `(venue, symbol, timeframe, limit)` 有 8 秒缓存，图表与决策周期共享同一份快照。

---

## 二之三、Hyperliquid 全市场数据

- universe/screener 与排行在 HL 交易员下使用 `metaAndAssetCtxs`（默认 dex + xyz dex，一次覆盖约 370 个资产）。
- 价格排行 24h 用 `prevDayPx` 立即可用；1h/4h 需要本地快照。
- **OI 排行需要历史**：HL 没有 OI 历史接口，只能每 5 分钟快照自建；冷启动 1 小时内该段省略，不借用 Binance。
- **Taker Flow Ranking 对 HL 不可用**：HL K 线没有主动买卖拆分；HL 交易员使用逐币的 trades 流（`hl_positioning`）中的 taker flow。
- 持仓结构（`market_position_bins`）是 HL 原生，且只对 HL 执行所注入 prompt；其他所不会误读 HL 的持仓簇。
- 爆仓流仍是 Binance + Bybit 的**跨所信号**，标题已明确标注“不含 Hyperliquid”；HL 的清算风险由持仓结构的强平簇承担。
- `market_position_bins` 现在与其他市场表一起每天清理，保留 30 天。

---

## 三、关键口径

### 3.1 资金费率归一化

不同交易所结算周期不同，直接比较会得出错误结论：

| 交易所 | 原始周期 | 归一化 |
|--------|----------|--------|
| Hyperliquid | 1 小时 | 直接使用 |
| Binance | 8 小时 | `rate / 8` |
| Bybit | 8h / 4h / 1h | `rate / interval`，interval 来自 `instruments-info` |

统一输出为 **每小时百分数**（`%/h`）。取不到周期时按 8h 处理，并在 prompt 中标注 `[interval assumed]`。

### 3.2 跨所 OI 不可相加

同一仓位可能在 A 所开、B 所对冲，两边都计入 OI。因此：

- 每个交易所的 OI 单独列示；
- 合计值只用于展示，并附带「对冲仓位会被重复计算」的说明；
- 拥挤度使用 **OI 加权资金费率**，而不是合计 OI。

### 3.3 爆仓 side 语义

两所对同一事件的描述相反，映射在 provider 层完成并有表驱动单测：

| 交易所 | 原始字段 | 含义 | 归一化 |
|--------|----------|------|--------|
| Binance | `o.S = SELL` | 多头被卖出强平 | `long` |
| Binance | `o.S = BUY` | 空头被买回强平 | `short` |
| Bybit | `S = Buy` | 多头被强平 | `long` |
| Bybit | `S = Sell` | 空头被强平 | `short` |

### 3.4 级联判定

- 主窗口 1 小时，短窗口 15 分钟，内存保留 12 小时。
- 基线取过去完整 1 小时窗口的中位数与 MAD（中位数绝对偏差），空窗口不计入。
- `z = 0.6745 × (当前窗口 - 中位数) / MAD`；MAD 为 0 时退化为相对倍数减一。
- 只有 `z ≥ 3`、打印数 ≥ 20、且单边占比 ≥ 70% 时标记为级联。
- 样本不足（少于 6 个完整窗口）时不输出 z，也不会标记级联。

---

## 四、配置开关

两个指针开关，`nil` 表示启用（与 `EnablePositioning` 一致）：

```json
{
  "enable_cross_venue": true,
  "enable_liquidation_flow": true
}
```

- 关闭后：不再采集、不再渲染 prompt 段落、不参与回放归因。
- 采集器本身在 `main.go` 启动，无法通过策略配置热关闭（多个 trader 共享同一份采集）。

**不需要任何 API key 或 claw402 钱包。** claw402 仅用于 AI 模型调用，数据链路已完全切断。

---

## 四之二、从 NofxOS / claw402 迁移

旧版本把候选池、OI 排行、涨跌榜和每币量化数据交给 NofxOS（付费 key 或 claw402 钱包）。这些已全部替换为公开接口：

| 旧配置 / 字段 | 现在 |
|---|---|
| `nofxos_api_key` | 删除；出现在旧策略 JSON 中会被忽略 |
| `source_type: "ai500"` | 本地 screener 的别名（启动时警告一次）；新配置建议写 `"screener"` |
| `enable_quant_netflow` | 删除；旧的键会被忽略 |
| `enable_netflow_ranking` | 键保留，但内容是 **Taker Flow Ranking**（主动买卖失衡），不再是机构/散户资金流 |
| `netflow_ranking_duration` / `netflow_ranking_limit` | 键保留，作用于 Taker Flow Ranking |
| 每币 `netflow` 字段 | 删除（免费数据无法复现机构/散户口径） |
| 每币 `net_long` / `net_short` | 改为 Binance 公开的账户多空比与大户持仓多空比 |

**冷启动**：进程启动后会用 Binance 的 `openInterestHist` 与 K 线为成交额最高的 120 个合约回填 24 小时的小时级历史，因此 `oi_top` / `oi_low` 不会在重启后空窗一小时。

---

## 五、进入 prompt 的形式

每个持仓与候选币会多出两段（缺失时整段省略，不输出零）：

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

对应的 playbook 规则只在开关启用时写入 system prompt，并且明确说明：

- 跨所合计不可当作全市场持仓；
- 资金费差只用于降杠杆/等待，不构成方向；
- 级联是已消耗的燃料，不追末端。

本条数据**不参与任何下单门控**：没有新增 block 逻辑，只影响模型看到的信息。

---

## 六、离线回放

```bash
nofx evaluate --trader <id> --from 2026-01-01 [--to 2026-02-01] --regime
```

`--regime` 会为每笔交易打上入场时的市场状态标签，并按标签归因：

```text
-- by entry regime --
  cross_venue_cascade trades=12 win= 33.3%  net=-420.10
  crowded_long   trades=40   win= 55.0%  net= 180.40
  crowded_short  trades=18   win= 61.1%  net=  95.20
  calm           trades=90   win= 52.2%  net= 310.00
```

> `cross_venue_cascade` 的爆仓数据来自 Binance/Bybit；HL 没有公开全量清算流，所以这个标签明确标注为跨所，而不是“本所级联”。

- 标签来自落库的 point-in-time 读取，不会使用未来数据。
- 默认不开启；开启也不改变入场、出场、仓位或任何 PnL 数字，只增加归因维度。
- 级联标签判定：入场前 15 分钟强平额 ≥ 前 4 个非空 15 分钟窗口中位数的 3 倍，且打印数 ≥ 10。

---

## 七、降级与限制

| 场景 | 行为 |
|------|------|
| Binance 地区限制（451） | 仅该所缺失；决策/图表不跨所回退，按交易所分别降级 |
| Hyperliquid K 线保留期 | 每周期约 5000 根（3m ≈ 10 天，1h ≈ 208 天）；更早回放需显式 `--venue binance` |
| Hyperliquid OI 排行 | 无历史接口，冷启动 1 小时内省略，不借用 Binance |
| 跨所回退 | 决策/图表/执行均不回退；失败即省略或报错 |
| Bybit/OKX 的 OI、资金费、订单簿 | 尚未适配，暂用 Binance 参考并只警告一次（K 线已是原生） |
| Bybit 结算周期取不到 | 按 8h 处理，行上记录 `funding_interval_h=8`，prompt 标注为假设值 |
| 某个 venue 无该 symbol | 该 venue 不出现在跨所段落；少于两个 venue 时整段省略 |
| Hyperliquid 爆仓 | 官方无全量清算频道，未覆盖；HL 的清算风险由 `position_profile` 的强平簇覆盖 |
| WS 断线 | 指数退避重连（上限 30s），重连后重复打印由唯一索引去重 |
| 落库失败 | 内存读数继续更新，日志记录一次；不影响 prompt 的当前读数 |
| 磁盘占用 | 两张表均保留 30 天；`market_venue_metrics` 约 11k 行/天（3 venue × 13 symbol × 288 次/天） |

---

## 八、代码位置

| 关注点 | 文件 |
|--------|------|
| K 线 venue 路由 / 缓存 | `market/venue.go` |
| 交易所 K 线客户端 | `provider/{binance,bybit,okx,hyperliquid}` |
| universe（HL/参考） | `market/universe.go` |
| 表与读取 | `store/market_metrics.go` |
| Binance 客户端 | `provider/binance/{client,market,ws}.go` |
| Bybit 客户端 | `provider/bybit/{client,market,ws}.go` |
| 跨所纯计算 | `market/venue_metrics.go` |
| 跨所采集器 | `market/venue_metrics_service.go` |
| 爆仓窗口纯计算 | `market/liquidation_flow.go` |
| 爆仓采集器 | `market/liquidation_service.go` |
| 启动接线 | `kernel/market_metrics.go`、`main.go` |
| prompt 渲染 | `kernel/engine_prompt.go`（`formatCrossVenue` / `formatLiquidationFlow`） |
| 回放归因 | `internal/backtest/regime_source.go` |
