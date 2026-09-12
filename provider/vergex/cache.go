package vergex

import (
	"os"
	"sync"
	"time"

	"nofx/provider/paidcache"
)

// ============================================================================
// claw402 付费响应缓存（vergex 数据源）
//
// 通用实现见 nofx/provider/paidcache；这里只定义 vergex 各端点的 TTL 策略。
// 缓存必须放在进程级：GetFullDecision 每轮会重建 StrategyEngine，
// 实例级缓存会完全失效。
//
// 配置使用统一变量 NOFX_PAID_*（见 paidcache），未配置时使用下方默认值。
// ============================================================================

const (
	// 默认成功响应 TTL（30 分钟决策周期：约一个周期）
	defaultResponseTTL = 30 * time.Minute
	// 热力图 / signal-lab 属慢变量，缓存更久（约 1.5 个周期）
	defaultDetailTTL = 45 * time.Minute
	// 看板决定候选币，TTL 取半个周期
	defaultRankingTTL = 15 * time.Minute
	// 失败响应最长缓存时长
	maxNegativeTTL = 10 * time.Minute
)

var (
	cacheOnce   sync.Once
	globalCache *paidcache.Cache
)

// GlobalCache 返回进程级共享缓存（多个 engine / trader 复用，最大化省流）
func GlobalCache() *paidcache.Cache {
	cacheOnce.Do(func() {
		globalCache = paidcache.New(vergexCachePolicy())
	})
	return globalCache
}

// vergexCachePolicy 构建 vergex 端点的缓存策略
func vergexCachePolicy() paidcache.Policy {
	detailTTL := paidcache.EnvMinutes(paidcache.EnvDetailTTLMin, defaultDetailTTL)
	signalLabTTL := paidcache.EnvMinutes(paidcache.EnvDetailTTLMin, defaultDetailTTL)
	rankingTTL := paidcache.EnvMinutes(paidcache.EnvRankingTTLMin, defaultRankingTTL)

	defaultTTL := paidcache.EnvMinutes(paidcache.EnvTTLMin, defaultResponseTTL)
	// 仅当显式设置了总时长时，未单独指定的分类时长才跟随总时长
	if anyEnvSet(paidcache.EnvTTLMin) {
		if !anyEnvSet(paidcache.EnvDetailTTLMin) {
			detailTTL = defaultTTL
		}
		if !anyEnvSet(paidcache.EnvDetailTTLMin) {
			signalLabTTL = defaultTTL
		}
		if !anyEnvSet(paidcache.EnvRankingTTLMin) {
			rankingTTL = minDuration(defaultTTL, defaultRankingTTL)
		}
	}

	policy := paidcache.Policy{
		Enabled:     paidcache.EnvEnabled(paidcache.EnvEnable, true),
		DefaultTTL:  defaultTTL,
		NegativeTTL: paidcache.EnvMinutes(paidcache.EnvNegativeTTLMin, paidcache.DefaultNegativeTTL),
		PathTTL: map[string]time.Duration{
			CostLiquidationHeatmapPath: detailTTL,
			SignalLabPath:              signalLabTTL,
			SignalRankingPath:          rankingTTL,
		},
	}
	if policy.NegativeTTL > maxNegativeTTL {
		policy.NegativeTTL = maxNegativeTTL
	}
	return policy
}

// CacheTTLFor 返回指定 vergex 端点的缓存时长
func CacheTTLFor(path string) time.Duration {
	return vergexCachePolicy().TTLFor(path)
}

// DetailSymbolLimit 每轮最多为多少个标的拉取付费详情（默认 10）
func DetailSymbolLimit() int {
	return paidcache.EnvInt(paidcache.EnvDetailSymbols, 10)
}

func anyEnvSet(keys ...string) bool {
	for _, key := range keys {
		if _, ok := os.LookupEnv(key); ok {
			return true
		}
	}
	return false
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
