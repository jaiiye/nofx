package market

import (
	"context"
	"testing"
	"time"

	"nofx/provider/hyperliquid"
)

func TestPositionIndexCapturesRotatesAndExpiresAddresses(t *testing.T) {
	idx := NewPositionIndex(nil)
	idx.SetSymbols([]string{"BTCUSDT"})
	symbol := Normalize("BTCUSDT")

	idx.CaptureAddresses("BTCUSDT", []string{"0x1111111111", "0x2222222222", "0x3333333333"}, time.Now().UnixMilli())
	if candidates, _ := idx.AddressCoverage("BTCUSDT"); candidates != 3 {
		t.Fatalf("candidates = %d, want 3", candidates)
	}

	first := idx.candidateAddresses(symbol, 0, 2)
	second := idx.candidateAddresses(symbol, 2, 2)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("pages = %d and %d addresses, want 2 each", len(first), len(second))
	}
	if first[0] == second[0] && first[1] == second[1] {
		t.Fatalf("rotation returned the same page twice: %v / %v", first, second)
	}

	// A print older than the TTL does not make an address a candidate.
	idx.CaptureAddresses("BTCUSDT", []string{"0x4444444444"}, time.Now().Add(-7*time.Hour).UnixMilli())
	if page := idx.candidateAddresses(symbol, 0, 10); len(page) != 3 {
		t.Fatalf("expired address must not be polled, page = %d", len(page))
	}
}

func TestPositionIndexWithoutAddressesDoesNotSnapshot(t *testing.T) {
	idx := NewPositionIndex(func(*PositionSnapshot) error {
		t.Fatal("sink must not be called without positions")
		return nil
	})
	idx.SetSymbols([]string{"BTCUSDT"})

	if err := idx.refreshSymbol(context.Background(), Normalize("BTCUSDT"), map[string]float64{"BTC": 100}); err != nil {
		t.Fatalf("refresh without addresses = %v, want nil", err)
	}
}

func TestPositionIndexKeepsSymbolsOutOfScope(t *testing.T) {
	idx := NewPositionIndex(nil)
	idx.SetSymbols([]string{"BTCUSDT", "xyz:NVDA"})
	idx.CaptureAddresses("BTCUSDT", []string{"0x1111111111"}, time.Now().UnixMilli())
	idx.CaptureAddresses("xyz:NVDA", []string{"0x2222222222"}, time.Now().UnixMilli())

	idx.SetSymbols([]string{"BTCUSDT"})

	if candidates, _ := idx.AddressCoverage("xyz:NVDA"); candidates != 0 {
		t.Fatalf("dropped symbol kept %d candidates, want 0", candidates)
	}
	if candidates, _ := idx.AddressCoverage("BTCUSDT"); candidates != 1 {
		t.Fatalf("kept symbol lost its candidates: %d", candidates)
	}
}

func TestPositionsForCoinReducesAccountState(t *testing.T) {
	state := &hyperliquid.ClearinghouseState{AssetPositions: []hyperliquid.AssetPosition{
		{Position: hyperliquid.Position{Coin: "BTC", Size: "-0.5", EntryPx: "63000", LiquidationPx: "71000"}},
		{Position: hyperliquid.Position{Coin: "ETH", Size: "2", EntryPx: "3000"}},
		{Position: hyperliquid.Position{Coin: "BTC", Size: "0"}},
	}}

	positions := positionsForCoin(state, "0xabc", "BTC")
	if len(positions) != 1 {
		t.Fatalf("positions = %d, want only the sized BTC position", len(positions))
	}
	position := positions[0]
	if position.Side != "short" || position.Size != 0.5 {
		t.Fatalf("negative size must read as a short, got %+v", position)
	}
	if position.EntryPrice != 63000 || position.LiquidationPx != 71000 {
		t.Fatalf("prices = %v / %v", position.EntryPrice, position.LiquidationPx)
	}
	if positionsForCoin(nil, "0xabc", "BTC") != nil {
		t.Fatal("a missing state must yield no positions")
	}
}

func TestPositionDexAndMarketType(t *testing.T) {
	if dex := positionDexFor("xyz:NVDA"); dex != "xyz" {
		t.Fatalf("dex = %q, want xyz", dex)
	}
	if marketType := positionMarketType("xyz:NVDA"); marketType != "hip3_perp" {
		t.Fatalf("market type = %q, want hip3_perp", marketType)
	}
	if dex := positionDexFor("BTCUSDT"); dex != "" {
		t.Fatalf("dex = %q, want the core perps", dex)
	}
	if marketType := positionMarketType("BTCUSDT"); marketType != "core_perp" {
		t.Fatalf("market type = %q, want core_perp", marketType)
	}
}
