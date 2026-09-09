package paidcache

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testCache() *Cache {
	return New(Policy{
		Enabled:     true,
		DefaultTTL:  time.Minute,
		NegativeTTL: time.Minute,
		PathTTL:     map[string]time.Duration{"/heatmap": 15 * time.Minute},
	})
}

func TestCacheServesRepeatedCallsFromCache(t *testing.T) {
	cache := testCache()

	var calls atomic.Int64
	fetch := func(context.Context) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"ok":true}`), nil
	}

	for i := 0; i < 5; i++ {
		body, err := cache.Do(context.Background(), "k1", time.Minute, fetch)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(body) != `{"ok":true}` {
			t.Fatalf("unexpected body: %s", body)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 paid request, got %d", got)
	}
	if _, _, saved := cache.Stats(); saved != 4 {
		t.Fatalf("expected 4 saved requests, got %d", saved)
	}
}

func TestCacheExpiresAfterTTL(t *testing.T) {
	cache := testCache()

	var calls atomic.Int64
	fetch := func(context.Context) ([]byte, error) {
		calls.Add(1)
		return []byte(`{}`), nil
	}

	if _, err := cache.Do(context.Background(), "k", 10*time.Millisecond, fetch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := cache.Do(context.Background(), "k", 10*time.Millisecond, fetch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected refetch after TTL, got %d calls", got)
	}
}

func TestCacheNegativeCachingAvoidsRepeatedPaidRetries(t *testing.T) {
	cache := testCache()

	var calls atomic.Int64
	fetch := func(context.Context) ([]byte, error) {
		calls.Add(1)
		return nil, errors.New("invalid marketType")
	}

	for i := 0; i < 3; i++ {
		if _, err := cache.Do(context.Background(), "bad", time.Minute, fetch); err == nil {
			t.Fatal("expected error to be returned")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected error to be cached (1 paid request), got %d", got)
	}
}

func TestCacheSingleflightDedupesConcurrentCalls(t *testing.T) {
	cache := testCache()

	release := make(chan struct{})
	var calls atomic.Int64
	fetch := func(context.Context) ([]byte, error) {
		calls.Add(1)
		<-release
		return []byte(`{}`), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.Do(context.Background(), "concurrent", time.Minute, fetch); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected singleflight to issue 1 request, got %d", got)
	}
	if _, _, saved := cache.Stats(); saved != 7 {
		t.Fatalf("expected 7 saved requests, got %d", saved)
	}
}

func TestCacheDisabledBypassesCache(t *testing.T) {
	cache := New(Policy{Enabled: false, DefaultTTL: time.Minute, NegativeTTL: time.Minute})

	var calls atomic.Int64
	fetch := func(context.Context) ([]byte, error) {
		calls.Add(1)
		return []byte(`{}`), nil
	}
	for i := 0; i < 3; i++ {
		if _, err := cache.Do(context.Background(), "k", time.Minute, fetch); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected cache disabled to always fetch, got %d", got)
	}
}

func TestPolicyTTLForUsesPathOverride(t *testing.T) {
	cache := testCache()
	if got := cache.policy.TTLFor("/heatmap"); got != 15*time.Minute {
		t.Fatalf("expected path TTL override, got %v", got)
	}
	if got := cache.policy.TTLFor("/unknown"); got != time.Minute {
		t.Fatalf("expected default TTL, got %v", got)
	}
}

func TestKeyIncludesParams(t *testing.T) {
	params := url.Values{"symbol": {"xyz:CL"}, "marketType": {"hip3_perp"}}
	if got := Key("/heatmap", params); got != "/heatmap?marketType=hip3_perp&symbol=xyz%3ACL" {
		t.Fatalf("unexpected key: %s", got)
	}
	if got := Key("/heatmap", nil); got != "/heatmap" {
		t.Fatalf("unexpected key: %s", got)
	}
}

func TestInvalidateClearsEntries(t *testing.T) {
	cache := testCache()
	var calls atomic.Int64
	fetch := func(context.Context) ([]byte, error) {
		calls.Add(1)
		return []byte(`{}`), nil
	}
	if _, err := cache.Do(context.Background(), "k", time.Minute, fetch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cache.Invalidate()
	if _, err := cache.Do(context.Background(), "k", time.Minute, fetch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected refetch after invalidate, got %d", got)
	}
}
