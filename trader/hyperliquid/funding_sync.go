package hyperliquid

import (
	"fmt"
	"time"

	"nofx/logger"
	"nofx/market"
	"nofx/provider/hyperliquid"
	"nofx/store"
)

// fundingHistoryLookback bounds the first sync so a fresh trader does not pull
// an unbounded history.
const fundingHistoryLookback = 24 * time.Hour

// SyncFundingPayments applies the venue's funding payments to the open position
// records. Hyperliquid charges funding hourly, so a 3-5 day hold accumulates a
// real carry cost that was previously invisible to PnL, to the AI context and
// to the position protections.
//
// It is idempotent through a stored watermark: each run only looks at funding
// charged after the last applied entry.
func (t *HyperliquidTrader) SyncFundingPayments(traderID string, st *store.Store) error {
	if st == nil {
		return fmt.Errorf("store is nil")
	}

	watermark, err := st.Trader().GetFundingSyncPoint(traderID)
	if err != nil {
		return fmt.Errorf("failed to read funding watermark: %w", err)
	}
	now := time.Now()
	if watermark <= 0 {
		watermark = now.Add(-fundingHistoryLookback).UnixMilli()
	}

	entries, err := t.exchange.Info().UserFundingHistory(t.ctx, t.walletAddr, watermark, nil)
	if err != nil {
		return fmt.Errorf("failed to fetch user funding history: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}

	applied := 0
	maxTime := watermark
	for _, entry := range entries {
		if entry.Time <= watermark {
			continue // already applied
		}
		usdc := hyperliquid.ParseVenueNumber(entry.Delta.USDC)
		if usdc == 0 {
			continue
		}

		// The venue reports the funding delta as a signed cash flow
		// (negative = paid). Position accounting stores funding as a positive
		// cost, like fees, so the sign is inverted here.
		cost := -usdc
		symbol := market.Normalize(entry.Delta.Coin)
		side := "LONG"
		if hyperliquid.ParseVenueNumber(entry.Delta.Size) < 0 {
			side = "SHORT"
		}

		if err := st.Position().AddFundingFee(traderID, symbol, side, cost); err != nil {
			logger.Infof("  ⚠️ Failed to record funding for %s %s: %v", symbol, side, err)
			continue
		}
		applied++
		if entry.Time > maxTime {
			maxTime = entry.Time
		}
		logger.Infof("  💸 Funding %s %s: %+.4f USDC (rate %s)", symbol, side, usdc, entry.Delta.FundingRate)
	}

	if maxTime > watermark {
		if err := st.Trader().AdvanceFundingSyncPoint(traderID, maxTime); err != nil {
			return fmt.Errorf("failed to advance funding watermark: %w", err)
		}
	}
	if applied > 0 {
		logger.Infof("✅ Applied %d funding payments (watermark → %s)", applied, time.UnixMilli(maxTime).UTC().Format(time.RFC3339))
	}
	return nil
}
