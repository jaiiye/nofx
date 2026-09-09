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
// ============================================================================

const (
	// 默认成功响应 TTL
	defaultResponseTTL = 10 * time.Minute
	// 热力图数据变化缓慢，缓存更久
	defaultHeatmapTTL = 15 * time.Minute
	// signal-lab 同属慢变量
	defaultSignalLabTTL = 15 * time.Minute
	// 方向/信号看板决定候选币，TTL 不宜过长
	defaultLeaderboardTTL = 5 * time.Minute
	// 失败响应最长缓存时长
	maxNegativeTTL = 10 * time.Minute
)

const (
	envCacheEnabled = "NOFX_VERGEX_CACHE"
	envDefaultTTL   = "NOFX_VERGEX_CACHE_TTL_MIN"
	envHeatmapTTL   = "NOFX_VERGEX_HEATMAP_TTL_MIN"
	envSignalLabTTL = "NOFX_VERGEX_SIGNAL_LAB_TTL_MIN"
	envLeadTTL      = "NOFX_VERGEX_LEADERBOARD_TTL_MIN"
	envNegativeTTL  = "NOFX_VERGEX_NEGATIVE_TTL_MIN"
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
	policy := paidcache.Policy{
		Enabled:     paidcache.EnvEnabled(envCacheEnabled, true),
		DefaultTTL:  defaultResponseTTL,
		NegativeTTL: paidcache.DefaultNegativeTTL,
		PathTTL:     map[string]time.Duration{},
	}
	policy.PathTTL[CostLiquidationHeatmapPath] = paidcache.EnvMinutes(envHeatmapTTL, defaultHeatmapTTL)
	policy.PathTTL[SignalLabPath] = paidcache.EnvMinutes(envSignalLabTTL, defaultSignalLabTTL)
	policy.PathTTL[SignalRankingPath] = paidcache.EnvMinutes(envLeadTTL, defaultLeaderboardTTL)

	if v := paidcache.EnvMinutes(envDefaultTTL, 0); v > 0 {
		policy.DefaultTTL = v
		// 未单独指定时，按端点 TTL 同步跟随全局设置
		if _, ok := os.LookupEnv(envHeatmapTTL); !ok {
			policy.PathTTL[CostLiquidationHeatmapPath] = v
		}
		if _, ok := os.LookupEnv(envSignalLabTTL); !ok {
			policy.PathTTL[SignalLabPath] = v
		}
		if _, ok := os.LookupEnv(envLeadTTL); !ok {
			policy.PathTTL[SignalRankingPath] = minDuration(v, defaultLeaderboardTTL)
		}
	}
	if v := paidcache.EnvMinutes(envNegativeTTL, 0); v > 0 {
		policy.NegativeTTL = minDuration(v, maxNegativeTTL)
	}
	return policy
}

// CacheTTLFor 返回指定 vergex 端点的缓存时长
func CacheTTLFor(path string) time.Duration {
	return vergexCachePolicy().TTLFor(path)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
