package nofxos

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"nofx/provider/paidcache"
)

// ============================================================================
// claw402 路由的 nofxos 请求缓存
//
// nofxos 通过 claw402 代理时同样按次计费，且调用非常密集：
// GetCoinData 每个候选标的一次（12 次/轮）、GetOITopPositions 每轮两次、
// OI/NetFlow/Price 排行各一次。这里复用 paidcache 统一缓存，
// 仅在请求走 claw402 时生效（直连 nofxos.ai 不计费，不改变原行为）。
// ============================================================================

const (
	// 排行榜类：分钟级更新
	defaultRankingTTL = 10 * time.Minute
	// 单币种数据（quant/OI 明细）：变化较慢
	defaultCoinTTL = 15 * time.Minute
	// 默认兜底
	defaultNofxosTTL = 10 * time.Minute
	// 失败响应最长缓存时长
	maxNegativeTTL = 10 * time.Minute
)

const (
	envCacheEnabled = "NOFX_NOFXOS_CACHE"
	envDefaultTTL   = "NOFX_NOFXOS_CACHE_TTL_MIN"
	envCoinTTL      = "NOFX_NOFXOS_COIN_TTL_MIN"
	envRankingTTL   = "NOFX_NOFXOS_RANKING_TTL_MIN"
	envNegativeTTL  = "NOFX_NOFXOS_NEGATIVE_TTL_MIN"
)

var (
	nofxosCacheOnce sync.Once
	nofxosCache     *paidcache.Cache
)

// GlobalCache 返回 nofxos 的进程级共享缓存
func GlobalCache() *paidcache.Cache {
	nofxosCacheOnce.Do(func() {
		nofxosCache = paidcache.New(nofxosCachePolicy())
	})
	return nofxosCache
}

// nofxosCachePolicy 构建 nofxos 端点的缓存策略（按 nofxos 原始路径前缀匹配）
func nofxosCachePolicy() paidcache.Policy {
	defaultTTL := paidcache.EnvMinutes(envDefaultTTL, defaultNofxosTTL)
	coinTTL := paidcache.EnvMinutes(envCoinTTL, defaultCoinTTL)
	rankingTTL := paidcache.EnvMinutes(envRankingTTL, defaultRankingTTL)
	if _, ok := os.LookupEnv(envDefaultTTL); !ok {
		// 未显式设置总时长时，分类默认值跟随总时长
		coinTTL, rankingTTL = defaultTTL, defaultTTL
	}
	policy := paidcache.Policy{
		Enabled:     paidcache.EnvEnabled(envCacheEnabled, true),
		DefaultTTL:  defaultTTL,
		NegativeTTL: paidcache.DefaultNegativeTTL,
		PathTTL: map[string]time.Duration{
			"/api/coin":    coinTTL,
			"/api/oi":      rankingTTL,
			"/api/netflow": rankingTTL,
			"/api/price":   rankingTTL,
			"/api/ai500":   rankingTTL,
		},
	}
	if v := paidcache.EnvMinutes(envNegativeTTL, 0); v > 0 {
		policy.NegativeTTL = minDuration(v, maxNegativeTTL)
	}
	return policy
}

// doRequestPaid 经过缓存层发起 claw402 付费请求
func (c *Client) doRequestPaid(endpoint string, fetch func() ([]byte, error)) ([]byte, error) {
	cache := GlobalCache()
	return cache.Do(context.Background(), endpoint, cacheTTLFor(endpoint), func(context.Context) ([]byte, error) {
		return fetch()
	})
}

// cacheTTLFor 按端点前缀匹配 TTL（端点带查询参数，无法精确匹配常量）
func cacheTTLFor(endpoint string) time.Duration {
	policy := nofxosCachePolicy()
	if ttl, ok := policy.PathTTL[endpoint]; ok {
		return ttl
	}
	path := endpoint
	if idx := strings.IndexAny(path, "?"); idx >= 0 {
		path = path[:idx]
	}
	for prefix, ttl := range policy.PathTTL {
		if strings.HasPrefix(path, prefix) {
			return ttl
		}
	}
	return policy.DefaultTTL
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
