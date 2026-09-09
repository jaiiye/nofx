package nofxos

import (
	"context"
	"testing"
)

func TestCacheTTLForMatchesEndpointPrefix(t *testing.T) {
	policy := nofxosCachePolicy()

	// 单币种数据走 coin TTL，查询参数不应影响匹配
	if got := cacheTTLFor("/api/coin/BTC?include=oi"); got != policy.PathTTL["/api/coin"] {
		t.Fatalf("coin endpoint TTL = %v, want %v", got, policy.PathTTL["/api/coin"])
	}
	// 排行榜类走 ranking TTL
	if got := cacheTTLFor("/api/oi/top-ranking?limit=20&duration=1h"); got != policy.PathTTL["/api/oi"] {
		t.Fatalf("oi ranking TTL = %v, want %v", got, policy.PathTTL["/api/oi"])
	}
	if got := cacheTTLFor("/api/price/ranking?duration=1h"); got != policy.PathTTL["/api/price"] {
		t.Fatalf("price ranking TTL = %v, want %v", got, policy.PathTTL["/api/price"])
	}
	// 未匹配前缀走默认 TTL
	if got := cacheTTLFor("/api/unknown/thing"); got != policy.DefaultTTL {
		t.Fatalf("unknown endpoint TTL = %v, want %v", got, policy.DefaultTTL)
	}
}

func TestDoRequestPaidCachesRepeatedCalls(t *testing.T) {
	cache := GlobalCache()
	cache.Invalidate()

	var calls int
	client := &Client{}
	fetch := func() ([]byte, error) {
		calls++
		return []byte(`{"ok":true}`), nil
	}

	for i := 0; i < 3; i++ {
		body, err := client.doRequestPaid("/api/oi/top-ranking?limit=20", fetch)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(body) != `{"ok":true}` {
			t.Fatalf("unexpected body: %s", body)
		}
	}
	if calls != 1 {
		t.Fatalf("expected 1 paid request, got %d", calls)
	}
	if _, _, saved := GlobalCache().Stats(); saved < 2 {
		t.Fatalf("expected at least 2 saved requests, got %d", saved)
	}
}

func TestDoRequestPaidPropagatesErrors(t *testing.T) {
	client := &Client{}
	cacheTTLFor("/api/unknown/thing") // 保证策略初始化路径被覆盖
	_, err := client.doRequestPaid("/api/coin/ERRTEST?include=oi", func() ([]byte, error) {
		return nil, context.DeadlineExceeded
	})
	if err == nil {
		t.Fatal("expected error to be propagated")
	}
}
