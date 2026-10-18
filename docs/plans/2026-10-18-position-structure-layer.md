# nofx 本地「持仓结构层」方案 —— 用公开数据自建，不接 Claw402 付费信号

**Goal:** 用 Hyperliquid 公开数据自建 Claw402/Vergex 那套「成本 + 强平结构」信号（heatmap / 单市场结构读数 / 横截面排名），作为 prompt 上下文与可选的代码级窄门，全程可离线评估、默认关闭、可逐阶段回滚。

**Architecture:** `market/` 新增后台持仓索引器（沿用 `HLFlowWatcher` 单例 + `main.go` 启动范式）→ 纯函数分桶/指标（`market/position_profile.go`）→ GORM 快照落库（`store/market_structure.go`）→ prompt 紧凑块（`kernel/engine_prompt.go`）→ 窄门（`trader/structure_gate.go`，纯函数 + fail-open）→ 离线 A/B（`internal/backtest` + `nofx evaluate`）。

**Tech Stack:** Go 1.x（本机 `/home/jaiiye/sdk/go1.25.11/bin`）、GORM/SQLite、`sonirico/go-hyperliquid v0.36.0`（已 vendored）、React/TS（仅一个开关卡片）。

---

## 0. 结论先行

**自建，不接入付费。** 三个商品在代码层的对应关系与我们的落点：

| Claw402 商品（$0.002/次） | 真实语义 | 我们的落点 |
|---|---|---|
| `signal-ranking`（Signal Lab 类目，composite z 全市场排序） | 横截面因子排名 | **P5** 因子板（用我们已有的数据 + 自建结构因子） |
| `signal-lab`（单市场结构化读数：bias/confidence/关键位/级联脆弱度） | 单标的摘要 | **P2** 结构块（P1 指标 + 紧凑渲染） |
| `cost-liquidation-heatmap`（成本/强平燃料分布） | 地址级持仓的分布 | **P0+P1** 自建索引 + 分桶 |

**为什么不接付费**（三条硬理由）：① 运行成本 —— 上游每周期 1 次 board + 每标的 4 次调用，按我们 10 候选 + ≤8 持仓、15 分钟周期算是 **$14/天（≈$420/月）**，3 分钟周期 **$70/天**；② 语义风险 —— 上游把 board 的 bias 当**权威方向**（`enforceVergexSignalPolicy` 直接丢弃冲突开仓），等于让付费黑箱覆盖 LLM 判断，而它的 5 个因子名/权重不公开；③ 与既有原则冲突 —— 我们刚把 NofxOS 那套从策略工作室关掉（`IndicatorEditor.update()` 强制 `nofxos_api_key:''`），且本仓库的原则是"新信号优先从每轮已取数据派生、不做每标的扇出"。

**被否决的替代方案**：混合（只买 board 当冷启动桥，≈$0.19/天）—— 只在"这周就要有信号"时值得，且不能把它的 bias 当权威，只作候选排序先验；纯 P5a（不含持仓索引，只用 funding/OI/趋势做横截面排名）—— 可作为并行小步先落地，但它的因子有一半来自结构，缺了结构说服力不足。

---

## 1. 语义映射（先对齐，这是整个方案的核心）

四个数字的含义与对开仓方向的真实含义（上游 FAQ 的原话是 "show whether a move has **fuel** ahead of it or **walls** against it"）：

| 量 | 含义 | 对多头开仓 | 对空头开仓 |
|---|---|---|---|
| `longCost` | **开仓价**落在该价桶的多头名义 | 成本簇 = 该价位的**支撑/墙**（持有者护盘） | 阻力 |
| `shortCost` | 开仓价落在该价桶的空头名义 | 上方**阻力/抛压** | 支撑 |
| `longLiq` | **强平价**落在该价桶的多头名义 | 价格跌到此处 → 多头被强平 → **向下燃料（危险）** | 顺风 |
| `shortLiq` | 强平价落在该价桶的空头名义 | 向上**挤空燃料（顺风）** | 危险 |

关键派生量（POC/VA 是 market profile 术语，套在"成本/强平"两种分布上）：

- `POC` = 名义最集中的桶；`value area` = 含 ~70% 名义的连续区间；`magnet` = 离现价最近的显著簇（价格有被吸附倾向）；`distanceToPOC%` 越大，均值回归拖拽越强。
- `cascadeScore` = 现价下方 Δ 内的 `longLiq` / 总名义（向下级联脆弱度）；`squeezeScore` = 上方 Δ 内的 `shortLiq` / 总名义。
- `costWallAbove/Below` = 最近显著 `shortCost`/`longCost` 簇的密集度与距离（阻力/支撑强度）。
- 合成：`structureScore(long) = w1·squeezeScore − w2·cascadeScore + w3·supportWall − w4·resistanceWall − w5·distToPOC`（权重进 `ClampLimits` 常量，先取等权，P4 用离线 IC 调）。

---

## 2. 现状与缺口（本仓库事实，已核对）

**已有可直接复用**：

- `provider/hyperliquid/`：`l2Book`、`metaAndAssetCtxs`（`dex:""`/`"xyz"`）、`fundingHistory`、`candleSnapshot`、`allMids`、`meta`、`userNonFundingLedgerUpdates`、`userFees`。
- 任意地址持仓的先例：`api/handler_hyperliquid_wallet.go:114` 已按 query 的 address 查 `clearinghouseState`（含 `entryPx/szi/leverage/liquidationPx`）。
- 唯一 WS：`market/hl_flow_service.go`（SDK，**只订 `trades`**），`main.go:160` 启动，符号集由 `kernel/engine_analysis.go:344` 的 `market.UpdateHLFlowSymbols` 驱动；SDK 负责重连，仓库侧有 15 分钟陈旧阈值 / 30 分钟重订节流。
- 限速与后台循环：`trader/connector/ratelimit.go`（令牌桶）+ `trader/syncloop.Run(stop, interval, name, fn)`（指数退避）。
- 落库范式：`store/equity.go`（TraderID + 索引 Timestamp + `TableName` + PG 跳过 AutoMigrate + `Save` 用 `Omit("ID")` + `GetLatest` 升序 + `CleanOldRecords`）。
- 配置：`IndicatorConfig` 在 `ai_config` 下自动持久化；`*bool` 指针 + accessor（`PositioningEnabled()`）避免零值陷阱；数值参数走 `NormalizeXxx` + 常量 + `ClampLimits`。
- prompt：per-symbol 块以 `format<X>(ctx, symbol)` 追加（候选 `engine_prompt.go:886`、持仓 `:1204`），无数据返回 `""`；`EstimateTokens` 必须同步（`extrasPerCoin`），`kernel/estimate_drift_test.go` 会在低估时报错。
- 门：`EvaluateRiskGate`（纯函数）+ `checkOpenRiskGate`（薄包装），fail-open 于 I/O 错误、fail-closed 于计算出的敌意信号；阈值常量有测试钉住（`maxVenueSlippagePct` 模式）。
- 离线评估：`nofx evaluate`（回放 `decision_records` → win rate / PF / Sharpe / Sortino / DD / bootstrap p 值）、`nofx sweep`、`nofx trade-report`；`internal/backtest` 用可注入 `KlineSource`，**确定性、无模型调用**。
- 模拟盘：`PaperTrading` 已是"同一决策链路 + 模拟成交"的 substrate。

**缺口（本方案要补）**：地址级持仓索引（完全没有）；WS `trades` 消息里的地址未捕获（`parseTrade` 只取 side/px/sz/time）；Hyperliquid REST 无共享限速客户端（各调用点自建裸 `http.Client{Timeout:30s}`）；`CleanOldRecords` 全仓库**无调用点**（保留策略需自己接）；`EstimateTokens` 不认识新数据块。

---

## 3. 分阶段路线图（每阶段独立可构建/可测/可回滚，各自一个提交 + 双语 CHANGELOG）

### P0 — 地址发现与持仓采集（先让时钟开始走）

> 这一阶段**不改变任何交易行为**，只落库。因为结构数据无法回溯（地址级历史在公开 API 上拿不到），采集必须先开始。

**Files:** 新增 `market/hl_position_index.go`、`store/market_structure.go`；修改 `market/hl_flow_service.go`、`store/strategy.go`、`kernel/engine_analysis.go`、`main.go`。

**实现要点：**

1. **地址发现**：在 `parseTrade` 路径上捕获成交双方地址，进 per-market 地址集（TTL + 上限）。
   - 先确认 SDK 的 `hyperliquid.Trade` 是否暴露 `users`；**若无**，退路是自建 `market/hl_trades_raw.go`（`wss://api.hyperliquid.xyz/ws`，仓库已有 coinank 手写 WS 先例），只订 `trades` 并取 `users`。
2. **持仓拉取**：`clearinghouseState{user, dex?}` per address，`connector.NewLimiter(rate, burst)` 限速，逐地址解析 `assetPositions[].position{coin,szi,entryPx,positionValue,leverage,liquidationPx}`；按名义值降序取 Top-N（每市场可配，默认 1500），其余计入 `excluded`。
3. **刷新循环**：`syncloop.Run` + 单例（`StartHLPositionIndex` / `SetHLPositionIndex` / `Stop`），在 `main.go` 与 `HLFlowWatcher` 并列启动；符号集由 `engine_analysis.go`（`UpdateHLFlowSymbols` 之后一行）同源驱动（候选 ≤10 + 持仓 ≤8，核心 perp + `xyz` dex）。
4. **落库**：`store/market_structure.go` 两张表（见 §4）。
5. **配置**：`IndicatorConfig` 增 `EnablePositionStructure *bool`（**nil = 关**，与 microstructure 的 nil=on 相反，因为它要跑后台索引）+ `PositionStructureEnabled()` accessor + `ClampLimits` 里的常量（刷新间隔 60–900s，bin 数 20–200，band 5–40%，TopN 100–5000）。

**Tests:** 地址集去重/TTL 上限；`clearinghouseState` 解析（表格 + 手写 fixture，含缺 `liquidationPx`、`szi` 为负、非目标 coin）；store 往返 + retention；空/单持仓退化。

**Acceptance:** 默认关时**逐字节不影响** prompt 与决策；开启后 `data.db` 每刷新周期新增行；索引器日志给出覆盖度；`go build/vet/test ./...` 全绿。

**Commit:** `feat(market,store): index Hyperliquid holder positions for structure snapshots`

---

### P1 — 分桶与结构指标（纯函数）

**Files:** 新增 `market/position_profile.go` + `market/position_profile_test.go`。

**实现要点：** 单一纯函数
`func BuildPositionProfile(positions []AddressPosition, markPrice float64, bandPct float64, binStep float64) *PositionProfile`
→ `bins[]{px, lo, hi, longCost, shortCost, longLiq, shortLiq}` + `POC/VA/magnet/distToPOC/cascade/squeeze/costWall{Above,Below}/coverage`。
无持仓 → `nil`（上层据此省略整块）。分桶边界用 mark price 归一（`bandPct` 对称），权重默认名义价值（`|szi|·markPrice`），`weightSource` 字段自证口径。

**Tests:** 手写 fixture（含跨 band 外、单桶、恰好落边界）；零/负 mark 与 size 的退化；POC/VA 的已知答案；阈值常量钉住；单调性（把燃料堆到下方 → `cascade` 上升）。

**Commit:** `feat(market): cost-basis and liquidation profile from holder positions`

---

### P2 — 注入 prompt（严格 token 预算）

**Files:** `kernel/engine_prompt.go`、`kernel/engine.go`、`store/strategy.go`、`web/src/components/strategy/IndicatorEditor.tsx`、`web/src/types/strategy.ts`。

**实现要点：**

1. `kernel.Context` 增 `PositionProfileMap map[string]*market.PositionProfile \`json:"-"\``（与现有 map 一致，不序列化）。
2. 在 `fetchMarketDataWithStrategy`（`engine_analysis.go`，microstructure/positioning 同处）填充：只对当前候选+持仓取**最近一次落库快照**（`store` 查询，不额外打 API）+ 陈旧阈值（>2×刷新间隔 → 视为缺失）。
3. `formatPositionProfile(ctx, symbol)`：≤6 行摘要（POC/VA/距离、上/下燃料占比、成本墙、cascade/squeeze、覆盖度、快照年龄）+ 最多 5 行价桶表；插到候选 `:886` 与持仓 `:1204` 两处。
4. **`EstimateTokens` 同步**：`promptPositionProfileChars` 常量 + `extrasPerCoin` 分支（受同一开关 gate），保持 `kernel/estimate_drift_test.go`（估算 ≥ 实测）与 `kernel/prompt_budget_test.go` 绿。
5. `writeIndicatorPlaybook` 增一条受开关 gate 的角色规则（信息角色：结构/燃料），避免"重复指标"那类冗余。
6. UI：`marketContextCards` 加一张卡（`enable_position_structure`，hint 写清"需要后台索引"）+ TS 可选字段；nil=关 的判定写成 `config[key] === true`。

**Tests:** 渲染块精确断言；缺失/陈旧 → 返回 `""` 且 prompt **逐字节等于**开关关闭时；漂移与预算测试；前端 `tsc --noEmit` + `build`。

**Commit:** `feat(kernel,web): render holder cost/liquidation structure into the decision prompt`

---

### P3 — 代码级窄门（warn 先行，fail-open）

**Files:** 新增 `trader/structure_gate.go` + `trader/structure_gate_test.go`；修改 `trader/auto_trader_orders.go`。

**实现要点：**

- 纯函数 `EvaluateStructureGate(in StructureGateInput) StructureGateResult`（形状对齐 `EvaluateRiskGate`：`Allowed/Reason/AdjustedSizeUSD`），薄包装 `func (at *AutoTrader) checkStructureGate(symbol, action string, ctx *kernel.Context) error`。
- **fail-open**：快照缺失/陈旧/覆盖度过低 → `nil`（数据缺口永不冻结交易，与 slippage/daily-loss 一致）；只在"计算出的敌意结构"上拒绝：多头进场撞上近处高 `longLiq` 级联簇、空头撞上高 `shortLiq` 挤空簇、或进场站在主导成本墙的错误一侧。
- 模式开关 `structure_gate_mode: "off" | "warn" | "block"`，**P3 默认 `warn`**（只记日志 + prompt 提示），因为此时还没有离线证据；`block` 由 P4 结论决定。
- 调用点：`auto_trader_orders.go` 的 open_long/open_short（`checkOpenRiskGate` 之后）。
- 阈值一律常量 + 测试钉住。

**Tests:** 纯函数表驱动（含"数据缺失必须放行"）；阈值 pin；`warn` 模式下**永不**阻断的回归测试。

**Commit:** `feat(trader): structure gate (warn-first, fail-open)`

---

### P4 — 离线 A/B（决定性验证，够样本才做）

**Files:** `internal/backtest/*`、`cli_evaluate.go`。

**实现要点：** 复用现有重放：`LoadRecords` → 对每条**开仓**决策，按 (symbol, ts) 取当时最近的结构快照 → 用 P3 的**同一个纯函数**判定 → 过滤被阻断的开仓 → 重算 win rate/PF/Sharpe/Sortino/DD + 与随机进场对比的 bootstrap p 值，三种模式并排输出（`--structure-gate=off|warn|block`）。
现有 `Evaluate` 只遍历 `isOpen` 且逐笔独立评分，所以"**只删开仓**"的窄门天然适配这条链路，不需要新建回测引擎。

**门槛（写死，避免自我说服）：** 样本 ≥200 条开仓决策且快照覆盖 ≥30 天；只有满足 `Sharpe 提升 且 p < 0.1 且 DD 不恶化` 才允许把默认切到 `block`；否则保持 `warn` 并记录结论。

**Tests:** 合成记录 + 快照 → 确定性过滤结果；CLI 冒烟（temp dir + `store.New`，当前无 CLI 测试，属于补课）。

**Commit:** `feat(backtest): offline A/B for the structure gate`

---

### P5 — 横截面因子板（便宜版 signal-ranking）

**Files:** 新增 `market/structure_board.go` + 测试；修改 `kernel/engine.go`（候选排序）、`kernel/engine_prompt.go`（一行板摘要）。

**实现要点：** 对候选域做**横截面 z-score** 合成：① 成本位置（成本簇相对现价的在盈占比）② 燃料失衡（`squeeze − cascade`）③ 级联脆弱度 ④ 资金费率分位/OI 象限（已有 `market/hl_positioning.go`）⑤ 趋势（已有指标）。产出 `bias` + `score` + `rank`。
落点：候选排序处（`getHyperRankCoins` 的 `sort.SliceStable` + `clampHyperRankLimit` 是现成范式）+ prompt 顶部一行板摘要。
**先测 IC 再信任**：新增一个小工具按 (symbol, ts) 计算每个因子与后续 N 根收益的信息系数，IC 不显著/为负的因子直接删掉（不要用未经检验的因子去排序候选）。

**Commit:** `feat(market,kernel): cross-sectional structure board for candidate ordering`

---

### P6 — 覆盖度自证与保留策略

**Files:** `store/market_structure.go`、`market/hl_position_index.go`、（顺带）`store/equity.go`、`store/decision.go`。

**实现要点：** 每行落 `addressesSeen / fetched / included / excluded / weightSource / refreshAgeMs`，prompt 里如实写覆盖度（像上游 `cost{includedPositions,excludedPositions}` 那样自证）；接通两张新表的 `CleanOldRecords`（现有两处是死代码，可另开一个 cleanup goroutine 一并修）。

**Commit:** `feat(store,market): structure coverage accounting and snapshot retention`

---

## 4. 存储设计（快照优先，绝不按需打付费接口）

```
market_position_bins       // 原始分布快照（每次刷新每市场一行，bins 走 JSON，沿用 decision_records 的 blob 风格）
  id, market_type, symbol, ts(idx), refresh_at, mark_price, bin_step, band_pct,
  addresses_seen, positions_included, positions_excluded, weight_source, bins_json

market_structure_metrics   // 扁平指标（派生层，板/窄门/回放都读它）
  id, market_type, symbol, ts(idx), mark_price,
  poc, va_low, va_high, dist_to_poc_pct, magnet_px,
  liq_fuel_up_pct, liq_fuel_down_pct, cost_wall_up_pct, cost_wall_down_pct,
  cascade_score, squeeze_score, coverage_pct, snapshot_age_ms
```

- 索引：`(symbol, ts desc)`、`(ts desc)`；`GetLatest(symbol, limit)` 升序返回（照抄 `store/equity.go`）。
- 回放读法：`(symbol, ts ≤ 决策时间)` 取最近一行。
- 保留：默认 45 天（可配），由 P6 的清理任务执行。
- **不落地址明细**（隐私 + 体量）；地址→持仓只在内存用 TTL 缓存。

---

## 5. 风险与边界

- **覆盖度偏差（最大风险）**：地址只能从近期成交里发现，长期不动的巨鲸会漏 → 分布系统性偏薄。对策：地址集 TTL 拉长、成交流全量订阅、如实上报覆盖度、把覆盖度低于阈值时的快照视为**不可用于门控**（prompt 仍可展示但标注）。
- **限速与成本**：地址轮询是唯一的重活。对策：限速器 + Top-N + 每市场并发上限 + 刷新与决策周期解耦（默认 5 分钟/市场），且**只覆盖我们真正决策的 ≤18 个市场**。
- **`liquidationPx` 只对未平仓有效** → 结构是"当下快照"，不是预测；prompt 里必须写成现在时。
- **无法回溯**：结构历史只能从 P0 开始积累，所以 P4 之前的门只能是 `warn`。
- **不做**（明确出界）：地址级历史胜率矩阵（上游 `holder-winrate-map`，需要逐地址 `userFills` 重建回合，体量与隐私都超标）、whale feed、HIP-3 之外的其他链。

---

## 6. 验收与验证

**每阶段通用**：
`export PATH=/home/jaiiye/sdk/go1.25.11/bin:$PATH && go build ./... && go vet ./... && go test ./...`；动前端时加 `npx tsc --noEmit -p tsconfig.json && npm run build`；改动文件跑 `gofmt -l`（只格式化本阶段文件）。

**阶段专项**：

- P0：`sqlite3 -readonly data.db "select symbol,ts,positions_included,positions_excluded,coverage_pct from market_structure_metrics order by ts desc limit 10;"`；索引器日志；**默认关时 prompt 逐字节不变**（用 `--diff-prompt` 或对同一 cycle 的 `decision_records.input_prompt` 做 diff）。
- P1：纯函数测试；用真实快照算一次 POC/VA，与订单簿算出的支撑/阻力（`market/microstructure.go` 的 `SupportLevels/ResistanceLevels`）做一次**交叉合理性检查**（不是 oracle，只查方向）。
- P2：`kernel/estimate_drift_test.go`、`kernel/prompt_budget_test.go` 绿；块字节数上限（默认 ≤900 字节/标的）钉住。
- P3：`PaperTrading=true` 先跑一周，只观察 `warn` 日志命中率与命中样本的后续表现。
- P4：`nofx evaluate --trader <id> --from <d> --to <d> --structure-gate=off|warn|block`，三行并排；门槛见 P4。
- P5：IC 报表；排序改动前后 `nofx evaluate` 的 Sharpe/DD 对比。

**文档与提交**：每阶段一个提交（Conventional Commits + 多行 body），`CHANGELOG.md` 与 `CHANGELOG.zh-CN.md` 同步一条；本方案落地为 `docs/plans/2026-10-18-position-structure-layer.md`（仓库既有 `docs/plans/YYYY-MM-DD-*.md` 惯例），P1 完成后再补 `docs/architecture/` 一节。

**默认行为不变原则**：开关未开时，`market_structure_metrics` 可以为空、`PositionProfileMap` 为 nil、prompt 与今天**逐字节一致**；所有回滚 = 关开关或 `git revert` 该阶段提交。

---

## 7. 决定记录（2026-10-18）

1. **"先采集、后见效"：接受。** P0–P2 立刻让 prompt 有结构信息，门控（P3 `block`）最早 30 天快照 + 200 条决策之后才可能有据可依（门槛见 P4）。不走混合方案（不买 board）。
2. **索引覆盖范围：只覆盖决策用到的 ≤18 个市场（候选 ≤10 + 持仓 ≤8）。** P0 已按此实现：符号集由引擎每轮从 `ctx.MarketDataMap` 驱动，配置里没有"全量 universe"开关。将来要扩到核心 perp + `xyz` 全量，是换一个符号来源，不动结构。
3. **语义映射：不需要预先确认，改为实测校准。** §1 表中"可公开验证的事实"（地址级持仓可读、交易所直接给 `liquidationPx`、成本簇=墙、强平簇=燃料）直接采用；三处**建模选择**——① 权重口径（名义 vs 保证金）② 燃料的方向权重（按距离衰减 vs 等权区间）③ 结构分的用途（上下文 → 排序先验 → 代码否决）——不在讨论中定论，而是：
   - P1 只计算**无符号**结构量（POC / value area / magnet / 距 POC 距离 / 上/下燃料占比 / 成本墙密度与距离），不掺方向；
   - 所有方向符号集中在 P5 的**一张常量表**里，翻号是改一行；
   - P4/P5 用 **IC（因子与后续收益的信息系数）** 在自有快照上测每个因子的符号与强度，测出来为负就翻号或删因子（照 P5 的"先测 IC 再信任"）。
   结论：符号的正确性由数据裁决，不由我们先验猜测。
