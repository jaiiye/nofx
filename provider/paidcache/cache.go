// Package paidcache 提供按次计费（x402 / claw402）HTTP 响应的通用缓存。
//
// 背景：claw402 采用按次计费，nofx 在每轮决策中会对每个候选标的重复拉取
// 行情/信号数据，且失败时会遍历参数组合重试——同一周期可产生数十上百次计费
// 请求。本包提供进程级 TTL 缓存，供 vergex / nofxos 等付费数据源复用：
//
//  1. 按端点 TTL（慢变量缓存更久）
//  2. 负缓存：失败响应短暂缓存，避免对损坏参数反复付费重试
//  3. singleflight：并发的同 key 请求只发一次，其余等待结果
package paidcache

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxCacheEntries 缓存条目上限，防止长时间运行后内存无界增长
const maxCacheEntries = 4096

// DefaultNegativeTTL 失败响应的默认缓存时长（30 分钟周期：短缓存以便尽快重试）
const DefaultNegativeTTL = 5 * time.Minute

// Policy 定义某个数据源的缓存策略（是否开启、各端点 TTL）
type Policy struct {
	Enabled     bool
	DefaultTTL  time.Duration
	NegativeTTL time.Duration
	PathTTL     map[string]time.Duration
}

// TTLFor 返回指定端点的缓存时长
func (p Policy) TTLFor(path string) time.Duration {
	if ttl, ok := p.PathTTL[path]; ok && ttl > 0 {
		return ttl
	}
	return p.DefaultTTL
}

type cacheEntry struct {
	body      []byte
	err       error
	expiresAt time.Time
}

type inflightCall struct {
	done chan struct{}
	body []byte
	err  error
}

// Cache 缓存按次计费接口的响应
type Cache struct {
	policy   Policy
	mu       sync.Mutex
	entries  map[string]*cacheEntry
	inflight map[string]*inflightCall

	hits  atomic.Int64
	miss  atomic.Int64
	saved atomic.Int64 // 被省掉的付费请求数
}

// New 创建一个缓存实例
func New(policy Policy) *Cache {
	if policy.NegativeTTL <= 0 {
		policy.NegativeTTL = DefaultNegativeTTL
	}
	if policy.DefaultTTL < 0 {
		policy.DefaultTTL = 0
	}
	return &Cache{
		policy:   policy,
		entries:  make(map[string]*cacheEntry),
		inflight: make(map[string]*inflightCall),
	}
}

// Enabled 缓存是否开启
func (c *Cache) Enabled() bool { return c != nil && c.policy.Enabled }

// Stats 返回命中/未命中/节省的付费请求数
func (c *Cache) Stats() (hits, misses, saved int64) {
	if c == nil {
		return 0, 0, 0
	}
	return c.hits.Load(), c.miss.Load(), c.saved.Load()
}

// Do 先查缓存，未命中（或已过期）才真正发起付费请求
func (c *Cache) Do(ctx context.Context, key string, ttl time.Duration, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	if !c.Enabled() || ttl <= 0 {
		return fetch(ctx)
	}

	now := time.Now()

	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		c.hits.Add(1)
		c.saved.Add(1)
		if entry.err != nil {
			return nil, entry.err
		}
		return cloneBytes(entry.body), nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			c.saved.Add(1)
			if call.err != nil {
				return nil, call.err
			}
			return cloneBytes(call.body), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &inflightCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	c.miss.Add(1)
	body, err := fetch(ctx)

	c.mu.Lock()
	entry := &cacheEntry{body: cloneBytes(body), err: err}
	if err != nil {
		entry.expiresAt = time.Now().Add(minDuration(ttl, c.policy.NegativeTTL))
	} else {
		entry.expiresAt = time.Now().Add(ttl)
	}
	c.storeLocked(key, entry)
	delete(c.inflight, key)
	c.mu.Unlock()

	call.body, call.err = body, err
	close(call.done)

	if err != nil {
		return nil, err
	}
	return cloneBytes(body), nil
}

// storeLocked 写入缓存并在超限时淘汰最快过期的条目（调用方需持有锁）
func (c *Cache) storeLocked(key string, entry *cacheEntry) {
	c.entries[key] = entry
	if len(c.entries) <= maxCacheEntries {
		return
	}
	oldestKey := ""
	var oldest time.Time
	first := true
	for k, v := range c.entries {
		if first || v.expiresAt.Before(oldest) {
			oldestKey, oldest, first = k, v.expiresAt, false
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// Invalidate 清空缓存（测试或强制刷新场景使用）
func (c *Cache) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cacheEntry)
	c.inflight = make(map[string]*inflightCall)
}

// Key 生成缓存键（path + 规范化后的查询参数）
func Key(path string, params url.Values) string {
	if len(params) == 0 {
		return path
	}
	return path + "?" + params.Encode()
}

func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// ============================================================================
// 统一环境变量（按数据类型划分，取代旧的 NOFX_VERGEX_* / NOFX_NOFXOS_*）
//
// 未配置时使用各数据源代码中定义的默认值。
// ============================================================================

const (
	// EnvEnable 总开关：0/off/false 关闭全部付费响应缓存
	EnvEnable = "NOFX_PAID_CACHE"
	// EnvTTLMin 默认缓存时长（分钟），未单独指定分类时长时生效
	EnvTTLMin = "NOFX_PAID_CACHE_TTL_MIN"
	// EnvDetailTTLMin 慢变量详情数据（heatmap / signal-lab / 单币种数据）
	EnvDetailTTLMin = "NOFX_PAID_DETAIL_TTL_MIN"
	// EnvRankingTTLMin 排行榜类数据（信号榜 / OI / NetFlow / 涨幅榜）
	EnvRankingTTLMin = "NOFX_PAID_RANKING_TTL_MIN"
	// EnvNegativeTTLMin 失败响应缓存时长（分钟）
	EnvNegativeTTLMin = "NOFX_PAID_NEGATIVE_TTL_MIN"
	// EnvDetailSymbols 每轮最多为多少个标的拉取付费详情（0 = 不限制）
	EnvDetailSymbols = "NOFX_PAID_DETAIL_MAX_SYMBOLS"
)

// EnvEnabled 读取布尔开关（0/false/off/no/disabled 关闭），未提供时取 defaultOn
func EnvEnabled(key string, defaultOn bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return defaultOn
	case "0", "false", "off", "no", "disabled":
		return false
	default:
		return true
	}
}

// EnvMinutes 读取以分钟为单位的时长配置，未提供或非法时取 fallback
func EnvMinutes(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	minutes, err := strconv.ParseFloat(raw, 64)
	if err != nil || minutes < 0 {
		return fallback
	}
	return time.Duration(minutes * float64(time.Minute))
}

// EnvInt 读取整型配置，未提供或非法时取 fallback
func EnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}
