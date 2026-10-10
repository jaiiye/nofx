package store

import "testing"

// The default strategy is self-hosted: the "hl_pool" candidate source
// (candidate_active from the local data plane) with static coins as an
// always-available fallback. Paid sources (claw402/vergex) are opt-in.

func TestDefaultStrategyUsesSelfHostedPool(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	assertSelfHostedDefault(t, cfg)
	ind := cfg.Indicators
	if ind.NofxOSAPIKey != "" {
		t.Fatalf("default should not include a NofxOS API key")
	}
	if ind.EnableQuantData || ind.EnableQuantOI || ind.EnableQuantNetflow || ind.EnableOIRanking || ind.EnableNetFlowRanking || ind.EnablePriceRanking {
		t.Fatalf("default strategy must not enable NofxOS datasets: %+v", ind)
	}
	if !ind.EnableRawKlines {
		t.Fatalf("raw Hyperliquid klines must stay enabled")
	}
}

func TestSelfHostedDefaultSurvivesClampAndNormalize(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	cfg.CoinSource.UseAI500 = true
	cfg.ClampLimits()
	assertSelfHostedDefault(t, cfg)
	if cfg.CoinSource.UseAI500 {
		t.Fatalf("self-hosted strategy must clear stale AI500 flag: %+v", cfg.CoinSource)
	}
}

func TestEmptyCoinSourceInfersSelfHostedPool(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	cfg.CoinSource = CoinSourceConfig{}
	cfg.NormalizeProductSchema()
	assertSelfHostedDefault(t, cfg)
}

func TestVergexFieldsClearedOnSelfHostedDefault(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	cs := cfg.CoinSource
	if cs.VergexLimit != 0 || cs.VergexMarketType != "" || cs.VergexChain != "" {
		t.Fatalf("self-hosted default must clear vergex markers so it cannot infer back to vergex_signal: %+v", cs)
	}
}

func assertSelfHostedDefault(t *testing.T, cfg StrategyConfig) {
	t.Helper()
	cs := cfg.CoinSource
	if cs.SourceType != "hl_pool" {
		t.Fatalf("coin source type = %q, want hl_pool (%+v)", cs.SourceType, cs)
	}
	if cs.HLPoolLimit != 10 {
		t.Fatalf("hl_pool_limit = %d, want 10", cs.HLPoolLimit)
	}
	if len(cs.StaticCoins) == 0 {
		t.Fatalf("self-hosted default must carry a static-coin fallback: %+v", cs)
	}
}
