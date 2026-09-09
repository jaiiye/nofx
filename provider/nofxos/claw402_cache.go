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
//
// 配置使用统一变量 NOFX_PAID_*（见 paidcache），未配置时使用下方默认值。
// ============================================================================

const (
	// 单币种数据（quant/OI 明细）：变化较慢（约 1.5 个周期）
	defaultCoinTTL = 45 * time.Minute
	// 排行榜类：半个周期
	defaultRankingTTL = 15 * time.Minute
	// 默认兜底（约一个周期）
	defaultNofxosTTL = 30 * time.Minute
	// 失败响应最长缓存时长
	maxNegativeTTL = 10 * time.Minute
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
	coinTTL := paidcache.EnvMinutes(paidcache.EnvDetailTTLMin, defaultCoinTTL)
	rankingTTL := paidcache.EnvMinutes(paidcache.EnvRankingTTLMin, defaultRankingTTL)

	defaultTTL := paidcache.EnvMinutes(paidcache.EnvTTLMin, defaultNofxosTTL)
	// 仅当显式设置了总时长时，未单独指定的分类时长才跟随总时长
	if anyEnvSet(paidcache.EnvTTLMin) {
		if !anyEnvSet(paidcache.EnvDetailTTLMin) {
			coinTTL = defaultTTL
		}
		if !anyEnvSet(paidcache.EnvRankingTTLMin) {
			rankingTTL = defaultTTL
		}
	}

	policy := paidcache.Policy{
		Enabled:     paidcache.EnvEnabled(paidcache.EnvEnable, true),
		DefaultTTL:  defaultTTL,
		NegativeTTL: paidcache.EnvMinutes(paidcache.EnvNegativeTTLMin, paidcache.DefaultNegativeTTL),
		PathTTL: map[string]time.Duration{
			"/api/coin":    coinTTL,
			"/api/oi":      rankingTTL,
			"/api/netflow": rankingTTL,
			"/api/price":   rankingTTL,
			"/api/ai500":   rankingTTL,
		},
	}
	if policy.NegativeTTL > maxNegativeTTL {
		policy.NegativeTTL = maxNegativeTTL
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
