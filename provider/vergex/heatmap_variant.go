package vergex

import "sync"

// ============================================================================
// heatmap 查询变体记忆
//
// xyz 标的所属 marketType 不确定，原实现每轮决策都会遍历
// marketType × chain 组合逐个重试（最多 4×3=12 次付费请求），
// 直到命中为止。这里把"上次成功的组合"按标的记下来，
// 下一轮直接优先尝试，探测成本从"每轮 N 次"降为"每标的 1 次"。
// ============================================================================

var (
	variantMu   sync.Mutex
	variantMemo = make(map[string]Query)
)

// RememberHeatmapVariant 记录某个标的热力图请求成功的查询变体
func RememberHeatmapVariant(symbol string, q Query) {
	key := QuerySymbol(symbol)
	if key == "" {
		return
	}
	variantMu.Lock()
	variantMemo[key] = Query{
		MarketType: q.MarketType,
		Symbol:     q.Symbol,
		Chain:      q.Chain,
		LiqBand:    q.LiqBand,
		Category:   q.Category,
	}
	variantMu.Unlock()
}

// LookupHeatmapVariant 返回该标的上次成功的查询变体
func LookupHeatmapVariant(symbol string) (Query, bool) {
	key := QuerySymbol(symbol)
	if key == "" {
		return Query{}, false
	}
	variantMu.Lock()
	defer variantMu.Unlock()
	q, ok := variantMemo[key]
	return q, ok
}

// HeatmapVariantCount 已记住的标的数（可观测用）
func HeatmapVariantCount() int {
	variantMu.Lock()
	defer variantMu.Unlock()
	return len(variantMemo)
}
