# 📊 Token 估算分析与候选币种上限指南

> 版本：v1.0 | 更新：2026-03-27
> 适用：策略配置 · 模型选择 · 候选币种数量决策

---

## 目录

- [Token 估算公式](#-token-估算公式)
- [系统提示词的准确性分析](#-系统提示词的准确性分析)
- [典型配置下的安全币种数量](#-典型配置下的安全币种数量)
- [模型上限参考](#-模型上限参考)
- [MaxCandidateCoins 常量说明](#-maxcandidatecoins-常量说明)

---

## 📐 Token 估算公式

代码入口：`store/strategy.go` → `EstimateTokens()`
精度保障：`kernel/estimate_drift_test.go` 会把"估算值"与**引擎真实构造出的 prompt**逐项对照，
不满足下面的容忍带即测试失败：

- 估算值 **不得低于** 实测值（它是预检安全门，低估算会让超长上下文漏过去）；
- 最坏情况（候选数取上限 + 持仓数取上限，持仓每个时间帧都打印 K 线）下不得超过实测值的 **2 倍**；
- `prompt_display_bars` 变大时，估算与实测必须同步变大。

### 关键前提：打印量由展示上限决定，而非分析窗口

```
display = klines.prompt_display_bars（默认 40，未设置时取 DefaultPromptDisplayBars）
rows    = min(display, primary_count)      # 实际打印的 K 线行数与指标值个数
```

`primary_count`（分析窗口，可到 200）只决定**算多少**，`display` 决定**给模型看多少**。
这一点是估算与 prompt 保持同步的核心：早期版本按 `primary_count` 估算，导致虚高约 10 倍。

### 结构

```
total = (SystemPrompt + FixedOverhead + MarketData + QuantData + RankingData) × 1.15
```
其中 `1.15` 为 15% 安全边际（预检允许略高，不允许偏低）。

### 静态部分（与候选币数量无关）

```
SystemPrompt = baseChars / 2（zh）或 / 4（en）
               baseChars = 6900（en）/ 6400（zh）
                         + 已启用指标数 × 300      # 每个指标在"协同规则"里贡献一组
                         + 自定义提示段落长度

FixedOverhead = 200 tokens（时间戳、账户信息、章节标题）

RankingData   = (OILimit × 60 + NetFlowLimit × 80 + PriceLimit × durations × 40) / 4
```

### 每枚标的的开销

```
# 每时间帧的指标序列字符数
seriesCharsPerTF = 序列数 × (rows × 9 + 16)      # EMA/RSI 各按周期数、MACD 1、BOLL 3
                 + 单值指标数 × 30               # ATR、ADX+DI±、Supertrend 方向

klineChars  = (候选币数 × 1 + 持仓数 × 时间帧数) × rows × 50
              ↑ 候选币只有主帧打印 K 线表；持仓每个帧都打印（最坏情况按 MaxPositions 计）

indicatorChars = (候选币数 + 持仓数) × 时间帧数 × seriesCharsPerTF
extrasPerCoin  = OI/资金费率×120 + 微观结构×170 + 持仓信号×210（按各自开关）

MarketData = (klineChars + indicatorChars + (候选币数+持仓数) × extrasPerCoin) / 4
```

所有常量都在 `store/strategy.go` 的常量块中，注释里标注了它们由实测标定而来。

---

## 📊 估算精度与实测对照

**基准配置**：10 个候选（`hyper_rank` 成交额榜上限）+ 3 个持仓、主帧 1h、`1h/4h/1d` 三帧、
`primary_count = 200`、全部指标 + 微观结构 + 持仓信号（EN）。

| `prompt_display_bars` | 估算 tokens | 实测 tokens（最坏情况：3 持仓） | 比值 |
| --------------------- | ----------- | ------------------------------- | ---- |
| 10                    | ~19,700     | ~14,200                         | 1.39 |
| **40（默认）**        | ~52,100     | ~45,300                         | **1.15** |
| 100                   | ~117,000    | ~82,200                         | 1.42 |
| 200                   | ~225,000    | ~157,600                        | 1.43 |

对照口径：实测 = `BuildSystemPrompt + BuildUserPrompt` 的字节数 ÷ 4（与估算器同一英文规则）。

### 各模型下的可用性（默认 display=40）

| 模型上限                        | 10 候选 + 3 持仓（最坏情况 ~52K） |
| ------------------------------- | --------------------------------- |
| 128K（OpenAI / DeepSeek / Qwen）| 安全（约 40% 上限）               |
| 200K（Claude）                  | 安全（约 26% 上限）               |
| 1M（Gemini / Minimax）          | 安全                              |

**结论**：把展示窗口留在默认 40，`MaxCandidateCoins = 10` 在所有模型上都远未接近上限——
真正的约束是 UI 上限本身，而不是上下文。只有当你把 `prompt_display_bars` 提到 100 以上时，
128K 模型才会开始吃紧（此时应该降候选数或帧数）。

> 旧版本文档里的"最大安全币数 / perCoinTokens"表是按 **优化前** 的 prompt（每个时间帧都打印
> 完整分析窗口的 K 线、每行 80 字符）算出来的，已整体失效并被本节取代。

---

## 🤖 模型上限参考

来源：`store/strategy.go` → `ModelContextLimits`

| 模型     | Context 上限 | 80% 警戒线 |
| -------- | ------------ | ---------- |
| deepseek | 131,072      | 104,858    |
| openai   | 128,000      | 102,400    |
| claude   | 200,000      | 160,000    |
| qwen     | 131,072      | 104,858    |
| gemini   | 1,000,000    | 800,000    |
| grok     | 131,072      | 104,858    |
| kimi     | 131,072      | 104,858    |
| minimax  | 1,000,000    | 800,000    |

---

## 🔒 MaxCandidateCoins 常量说明

来源：`store/strategy.go` 的常量块（`MaxCandidateCoins` / `MaxPositions` / `MaxTimeframes` /
`MinKlineCount` / `MaxKlineCount` / `DefaultPromptDisplayBars`）

```go
const (
    MaxCandidateCoins = 10   // UI 硬限制：用户最多设定的候选币数量
    MaxPositions      = 3    // 最大同时持仓数
    MaxTimeframes     = 4    // 最大时间框架数
    MinKlineCount     = 10   // 最少 K 线数（分析窗口）
    MaxKlineCount     = 200  // 最多 K 线数（分析窗口，与市场层实际拉取量一致）
    DefaultPromptDisplayBars = 40 // prompt 每个时间帧打印多少行/多少个指标值
)
```

### 为什么 MaxCandidateCoins = 10？

- 默认 `display = 40` 时，最坏情况（10 候选 + 3 持仓 + 全指标）约 **~52,000 tokens**：
  在 128K 模型上约占 40%，在 200K 模型上约占 26%
- 因此 10 是保守且安全的 UI 上限：**在所有模型和配置组合下都不会触发上下文限制**
- 需要更多标的时，先降 `prompt_display_bars`/帧数，而不是指望模型上限

### 建议使用范围

| 用户类型            | 建议配置                                        | 最大建议候选数 |
| ------------------- | ----------------------------------------------- | -------------- |
| 新手 / 使用默认配置 | 3TF, display=40, 仅 Volume                      | 10             |
| 进阶 / 启用部分指标 | 3TF, display=40, EMA+MACD+RSI                   | 10             |
| 高级 / 全部指标     | 3-4TF, display=40-100, 全指标                   | 10（display≤40）/ 6-8（display=100） |
