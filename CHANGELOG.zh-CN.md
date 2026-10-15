# 更新日志

NOFX 项目的所有重要更改都将记录在此文件中。

本文件格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)，
本项目遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

**语言:** [English](CHANGELOG.md) | [中文](CHANGELOG.zh-CN.md)

---

## [未发布]

### 新增
- 多语言文档系统（英文/中文/俄语/乌克兰语）
- 完整的快速开始指南（Docker、自定义 API）
- 架构文档，包含系统设计细节
- 用户指南，包含 FAQ 和故障排除
- 社区文档，包含悬赏计划
- 策略配置的指标周期现在真正进入计算层。此前 `ema_periods`/`rsi_periods`/`atr_periods`
  只是以文本形式回显到 prompt，实际计算值仍是硬编码（20/50、7/14）；现在 prompt 按真实周期
  标签输出配置序列（如 `EMA(30)`），MACD 也支持自定义快慢周期
  （`macd_fast_period`/`macd_slow_period`，默认 12/26）。
- AI prompt 新增决策时点市场微观结构：盘口价差、买卖失衡、前 10 档深度、最大挂单支撑/阻力、
  0-100 流动性评分，以及由主时间帧 K 线计算的 VWAP（无额外请求）。加密币读取 Binance 盘口，
  xyz dex 资产路由到 Hyperliquid L2 快照。通过 `indicators.enable_microstructure` 开关控制
  （默认开启）。
- 开仓前代码强制的盘口滑点防护：按本次下单名义值走簿估算，预计成本超过 0.5% 或盘口深度不足
  （无法成交）时拒绝开仓。取盘口失败时 fail-open —— 数据缺口不会阻塞交易。
- 代码强制日损停机（`risk_control.max_daily_loss`，默认为初始余额的 10%）：当日净已实现盈亏
  现在会真正暂停交易（AI prompt 一直宣称该保护，但代码从未执行）。暂停时长复用
  `StopTradingTime`。
- 回撤利润保护监控可配置（`drawdown_monitoring_enabled`、`drawdown_check_interval_sec`、
  `min_profit_threshold_pct`、`drawdown_close_threshold_pct`）；默认值保持此前的
  60 秒 / 5% / 40% 行为，且设置无需重启即可生效。
- 模拟盘现在会落库模拟订单/成交/持仓，使 DB 级保护（连亏停手、冷却、回撤停机）与 AI 的
  交易历史在模拟模式可用；模拟余额计入已实现盈亏、手续费与占用保证金。
- Telegram bot token 加密存储（`crypto.EncryptedString`），与交易所凭证一致。历史明文
  token 仍可读取，并在下次保存时自动加密。
- 新增两个趋势/状态类指标：**ADX**（含方向指标 `ADX(14)`、`DI+(14)`、`DI-(14)`）与
  **Supertrend**（拖尾带 + `SUPERTREND_DIR` = +1 上行 / -1 下行）。为控制 prompt 体积，
  ADX/Supertrend 仅输出主时间帧。通过 `adx_periods`（默认 [14]）、
  `supertrend_period` / `supertrend_multiplier`（默认 10 / 3.0）配置，用
  `enable_adx` / `enable_supertrend` 开启。
- 系统 prompt 新增**指标协同规则**，且仅针对**已启用**的指标生成——模型绝不会被要求用
  它收不到的数据做确认。规则按角色组织：趋势判断（ADX/DI、Supertrend、EMA）→ 动能确认
  （MACD、RSI）→ 入场时机（布林带）→ 风险与仓位（ATR）→ 信号确认（成交量、持仓量、
  资金费）→ 执行质量（微观结构），并以"单一指标不构成入场理由"作结。ADX 与 Supertrend
  也已加入可用指标清单。

### 变更
- 最大保证金使用率改为策略工作室可编辑（10–100%，此前只读显示），但**代码仍是权威**：
  `ClampLimits` 归一化每次保存的配置，下单前的风控 gate 在执行拒绝前也会调用同一个
  `store.NormalizeMarginUsageLimit`——任何写入路径都无法关闭该上限。未设置/非法值现在表示
  "使用产品默认值（90%）"，而不是被静默收敛到最小值 10%，顺带修掉了"客户端未提交该字段会被
  悄悄收紧到 10%"的隐患。
- prompt 优化（每次决策体积约 -38%：参考上下文 37KB → 23KB）。K 线表改为紧凑 CSV 行，且
  有效位不再把低价币四舍五入成 `0.0000`；候选币只保留主时间帧的完整 K 线表（持仓币保留全部
  时间帧）；微观结构从 7 行压到 3 行；schema 字典去掉公式行；置信度门槛只声明一次而非三处。
- prompt 一致性修复：激进模式不再硬编码置信度 70（改为引用配置门槛，与硬约束一致）；短线
  模式改用与其快进快出设计相符的频率指引，不再与"持仓 ≥ 30-60 分钟"的日内口径矛盾；移除
  与配置化序列口径冲突的固定周期 `current_ema20`/`current_macd`/`current_rsi7` 单点值；
  候选币数量标题改为实际展示数；单币策略的 JSON 示例与硬约束引用同一个仓位上限；MACD
  快慢周期与 BOLL 倍数随周期一并列出。
- 重组文档结构为逻辑分类
- 更新所有 README 文件，添加适当的导航链接
- 风控 gate 的最大持仓判定改为"预期净持仓数"：同一周期内已平仓位在交易所持仓接口延迟时
  不会再被误判为"仓位已满"，"先平后开"的调仓得以执行。

### 变更（破坏性）
- `mcp.AIClient.CallWithMessages` 新增首个 `context.Context` 参数：
  `CallWithMessages(ctx, systemPrompt, userPrompt)`。迁移时传入 `context.Background()`
  或请求/交易员上下文；自行实现 `mcp.AIClient` 的下游代码需同步修改方法签名。
- Telegram bot 现在与 Web 端共享 NOFXi agent（`nofx/agent`），不再运行自己那套单工具
  REST agent —— `telegram/agent` 与 `telegram/session` 已删除。影响：bot 获得完整的
  NOFXi 工具面；"设置 → Telegram → AI 模型"（`POST /api/telegram/model`）暂时不再生效，
  bot 与 Web agent 使用相同的模型选择逻辑。
- Agentic（V2）循环的管理类工具收敛为"每域一个入口"：`skill_exchange_management`、
  `skill_model_management`、`skill_strategy_management`、`skill_trader_management`，
  由内置 skill JSON 定义自动生成，取代平铺的 `get_*`/`manage_*` 工具对。legacy 路由栈
  继续使用平铺工具；非管理域工具保持不变。

### 修复
- Hyperliquid xyz dex（HIP-3）订单现在按它实际结算的保证金池来判定。xyz dex 拥有独立的 USDC
  保证金账户（与主永续账户分离），此前的下单额度按"合并权益"计算，会放行交易所随后以
  `Insufficient margin to place order` 拒绝的订单（实盘在 `xyz:SILVER` 上出现）。现在 xyz 标的
  的可用额与权益改用该 dex 自身的可用保证金与权益；其它标的逻辑不变。同时暴露了 xyz dex 的
  可用额 `xyzDexAvailable`（`withdrawable` 字段此前只解析、从未使用）；空池时提前给出可执行
  报错（"请向 xyz dex 转入 USDC"），交易所的原始拒绝信息也会附带同样的指引。xyz 资产的杠杆
  改为尝试用手工签名的 HIP-3 `updateLeverage` 下发（失败仅告警并沿用交易所默认杠杆——HIP-3
  的杠杆支持无公开文档，该路径待实盘验证）。
- 布林带现在真正使用配置的 `boll_periods` 与新增的 `boll_multiplier`（默认 2.0）。此前周期
  只以文本形式回显到 prompt，实际计算固定为 20 / 2.0 —— 与 EMA/RSI/ATR 之前踩过的是同一个
  "死配置"陷阱。现在 prompt 按真实参数标签输出（`BOLL_UPPER(30,2.5)` …）。
- Hyperliquid IOC 订单现在会先核对交易所返回的成交状态再报 `FILLED`（传递真实订单号、
  成交均价与成交量；未成交返回错误）。此前过期的 IOC 单会被报成已成交并带占位订单号，
  系统会跟踪一个并不存在的仓位。
- OKX 止损/止盈条件单在 net 模式下不再携带 `posSide` —— 此前会被交易所拒绝，导致仓位裸奔。
- Aster 不再对非幂等的 POST 请求（下单）做重试。此前请求超时但交易所已接单时，重试可能
  造成重复下单。
- Bybit 两处裸 `http.Get`（下单主路径上的数量步长查询、盘口查询）改用 30 秒超时的 HTTP
  client；此前连接无响应会无限期挂起调用路径。
- `AutoTrader.Run()` 在所有退出路径上都会取消 context，grid 初始化失败或 K 线对齐等待被打断
  时，不再遗留 order-sync 与回撤监控 goroutine 永久轮询。
- 公开的批量净值历史端点现在与单交易员版本一样校验 `show_in_competition`，私有交易员的
  净值曲线不再可通过 ID 读取。
- agent 的 `get_positions`/`get_balance` 工具会返回每个交易员的查询失败信息（`query_errors`），
  不再返回可能诱导错误交易决策的"空数据"。
- 写入失败不再静默：交易员启停落库、创建/更新后的重载、以及 15 处会话/工作流状态保存现在
  都会记录失败日志，而不是直接丢弃。
- 交易模式现在真正生效。实盘循环此前硬编码传入 `"balanced"`，而 `writeModeVariant`
  并不识别该值，导致**模式段落永远不会输出**，用户配置的交易风格完全不起作用。
  现在模式从 `strategy.trading_mode` 读取，策略编辑器新增风格选择器
  （平衡 / 激进 / 保守 / 短线），预览与测试运行使用与实盘一致的模式。
- LLM 重试退避现在可被取消：`CallWithMessages` 不再在 `context.Background()` 上等待，
  `AutoTrader.Stop()` 会立即取消进行中的 LLM 调用，而不再等待重试循环结束。
- Agent 不再在每条消息到达时覆写共享的 `Agent.aiClient` 字段。现在每个请求解析自己的
  AI 客户端并注入到 turn context（`withTurnAIClient`/`turnAIClient`），并发的多个 store
  用户不会再互相刷新对方的模型选择；后台任务继续使用受锁保护的默认客户端。
- `AutoTrader.runCtx`/`runCancel` 改为在 `isRunningMutex` 保护下读写，消除 `Run()` 与
  `Stop()` 之间的数据竞争。

### 移除
- 删除无用的 `kernel.PromptBuilder`（`kernel/prompt_builder.go`）。这是遗留的 prompt
  模板，其动作命名（`OPEN_NEW`/`PARTIAL_CLOSE`）与主流程不一致且无任何引用。
- 删除无用的 `kernel.FormatContextForAI`/`FormatContextDataOnly`（`kernel/formatter.go`，
  636 行）。这是第二套中英双语上下文格式化器，全仓库零调用；它与实际使用的 prompt 构建器
  重复且已经脱节（仍是旧的冗长 K 线表、无指标协同规则、无微观结构），导致每次 prompt 改动
  都要考虑两处。`engine_prompt.go` 现在是唯一的 prompt 路径。

---

## [3.0.0] - 2025-10-30

### 新增 - 重大架构变革 🚀

**系统完全重新设计 - 基于 Web 的配置平台**

这是一个**重大破坏性更新**，将 NOFX 从基于静态配置的系统完全转变为现代化的 Web 交易平台。

#### 数据库驱动架构
- SQLite 集成，取代静态 JSON 配置
- 持久化存储，自动时间戳
- 外键关系和触发器确保数据一致性
- 为 AI 模型、交易所、交易员和系统配置分离表结构

#### 基于 Web 的配置界面
- 完整的 Web 配置管理（无需编辑 JSON）
- 通过 Web 界面设置 AI 模型（DeepSeek/Qwen API 密钥）
- 交易所管理（Binance/Hyperliquid 凭证）
- 动态创建交易员（结合任意 AI 模型和交易所）
- 实时控制（无需重启即可启动/停止交易员）

#### 灵活架构
- 关注点分离（AI 模型和交易所独立）
- 混合搭配能力（无限组合）
- 可扩展设计（支持无限交易员）
- 清洁起点（无默认交易员）

#### 增强的 API 层
- RESTful 设计，完整的 CRUD 操作
- 新端点：
  - `GET/PUT /api/models` - AI 模型配置
  - `GET/PUT /api/exchanges` - 交易所配置
  - `POST/DELETE /api/traders` - 交易员管理
  - `POST /api/traders/:id/start|stop` - 交易员控制
- 更新所有 API 端点文档

#### 现代化代码库
- 类型安全，适当分离配置类型
- 数据库抽象，使用预处理语句
- 全面的错误处理和验证
- 更好的代码组织（数据库、API、业务逻辑）

### 变更
- **破坏性变更**：不再使用旧的 `config.json` 文件
- 必须通过 Web 界面进行配置
- 设置更简单，用户体验更好
- 配置更改无需重启服务器

### 为什么重要
- 🎯 **用户体验**：配置和管理更容易
- 🔧 **灵活性**：创建 AI 模型和交易所的任意组合
- 📊 **可扩展性**：支持复杂的多交易员设置
- 🔒 **可靠性**：数据库确保数据持久性和一致性
- 🚀 **面向未来**：为高级功能奠定基础

---

## [2.0.2] - 2025-10-29

### 修复 - 关键错误修复：交易历史和性能分析

#### 盈亏计算 - 重大错误修复
- **修复**：盈亏现在计算为实际 USDT 金额，而不是仅百分比
- 之前忽略了仓位大小和杠杆（例如，100 USDT @ 5% = 1000 USDT @ 5%）
- 现在：`盈亏 (USDT) = 仓位价值 × 价格变化 % × 杠杆`
- 影响：胜率、盈利因子和夏普比率现在准确

#### 仓位跟踪 - 缺失关键数据
- **修复**：持仓记录现在存储数量和杠杆
- 之前只存储价格和时间
- 这对准确的盈亏计算至关重要

#### 仓位键逻辑 - 多空冲突
- **修复**：从 `symbol` 改为 `symbol_side` 格式
- 现在正确区分多头和空头仓位
- 示例：`BTCUSDT_long` vs `BTCUSDT_short`

#### 夏普比率计算 - 代码优化
- **变更**：用 `math.Sqrt` 替换自定义牛顿法
- 更可靠、可维护和高效

### 为什么重要
- 历史交易统计现在显示真实的 USDT 盈亏
- 不同杠杆交易之间的性能比较准确
- AI 自学习机制接收正确的反馈
- 多仓位跟踪（同时多空）正常工作

---

## [2.0.2] - 2025-10-29

### 修复 - Aster 交易所精度错误

- 修复 Aster 交易所精度错误（代码 -1111）
- 改进价格和数量格式化以匹配交易所要求
- 添加详细的精度处理日志用于调试
- 增强所有订单函数的精度处理

#### 技术细节
- 添加 `formatFloatWithPrecision` 函数
- 根据交易所规范格式化价格和数量
- 删除尾随零以优化 API 请求

---

## [2.0.1] - 2025-10-29

### 修复 - ComparisonChart 数据处理

- 修复 ComparisonChart 数据处理逻辑
- 从 cycle_number 切换到时间戳分组
- 解决后端重启时图表冻结问题
- 改进图表数据显示（按时间顺序显示所有历史数据）
- 增强调试日志

---

## [2.0.0] - 2025-10-28

### 新增 - 重大更新

- AI 自学习机制（历史反馈、性能分析）
- 多交易员竞赛模式（Qwen vs DeepSeek）
- 币安风格 UI（完整界面仿制）
- 性能比较图表（实时 ROI 比较）
- 风险控制优化（每币种仓位限制调整）

### 修复

- 修复硬编码初始余额问题
- 修复多交易员数据同步问题
- 优化图表数据对齐（使用 cycle_number）

---

## [1.0.0] - 2025-10-27

### 新增 - 初始版本

- 基础 AI 交易功能
- 决策日志系统
- 简单的 Web 界面
- 支持币安合约
- DeepSeek 和 Qwen AI 模型集成

---

## 如何使用本更新日志

### 用户
- 查看 [未发布] 部分了解即将推出的功能
- 查看版本部分了解变更内容
- 遵循破坏性变更的迁移指南

### 贡献者
进行更改时，将它们添加到 [未发布] 部分的相应类别下：
- **新增** - 新功能
- **变更** - 现有功能的变更
- **弃用** - 即将删除的功能
- **移除** - 已删除的功能
- **修复** - 错误修复
- **安全** - 安全修复

发布新版本时，将 [未发布] 项目移动到带日期的新版本部分。

---

## 链接

- [文档](docs/README.md)
- [贡献指南](CONTRIBUTING.md)
- [安全策略](SECURITY.md)
- [GitHub 仓库](https://github.com/NoFxAiOS/nofx)

---

**最后更新:** 2025-11-01
