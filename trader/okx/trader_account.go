package okx

import (
	"encoding/json"
	"fmt"
	"nofx/logger"
	"nofx/trader/types"
	"strconv"
	"strings"
	"time"
)

// GetAccountBalance gets the typed account state.
func (t *OKXTrader) GetAccountBalance() (types.AccountBalance, error) {
	// Check cache
	t.balanceCacheMutex.RLock()
	if t.cachedBalance != nil && time.Since(t.balanceCacheTime) < t.cacheDuration {
		cached := t.cachedBalance
		t.balanceCacheMutex.RUnlock()
		logger.Infof("✓ Using cached OKX account balance")
		return types.FromMap(cached), nil
	}
	t.balanceCacheMutex.RUnlock()

	logger.Infof("🔄 Calling OKX API to get account balance...")
	data, err := t.doRequest("GET", okxAccountPath, nil)
	if err != nil {
		return types.AccountBalance{}, fmt.Errorf("failed to get account balance: %w", err)
	}

	balance, err := parseAccountBalance(data)
	if err != nil {
		return types.AccountBalance{}, err
	}

	// Update cache
	t.balanceCacheMutex.Lock()
	t.cachedBalance = balance.ToMap()
	t.balanceCacheTime = time.Now()
	t.balanceCacheMutex.Unlock()

	return balance, nil
}

// GetBalance renders the compatibility map. Prefer GetAccountBalance.
func (t *OKXTrader) GetBalance() (map[string]interface{}, error) {
	balance, err := t.GetAccountBalance()
	if err != nil {
		return nil, err
	}
	return balance.ToMap(), nil
}

// parseAccountBalance maps the raw /api/v5/account/balance payload onto the
// typed account state. Extracted from GetAccountBalance so the mapping is
// testable without HTTP.
func parseAccountBalance(data []byte) (types.AccountBalance, error) {
	var balances []struct {
		TotalEq string `json:"totalEq"`
		AdjEq   string `json:"adjEq"`
		IsoEq   string `json:"isoEq"`
		OrdFroz string `json:"ordFroz"`
		Details []struct {
			Ccy      string `json:"ccy"`
			Eq       string `json:"eq"`
			CashBal  string `json:"cashBal"`
			AvailBal string `json:"availBal"`
			UPL      string `json:"upl"`
		} `json:"details"`
	}

	if err := json.Unmarshal(data, &balances); err != nil {
		return types.AccountBalance{}, fmt.Errorf("failed to parse balance data: %w", err)
	}

	if len(balances) == 0 {
		return types.AccountBalance{}, fmt.Errorf("no balance data received")
	}

	balance := balances[0]

	// Sum unrealized PnL across every currency. OKX defines totalEq = Σ eq and
	// eq = cashBal + upl, so totalEq ALREADY includes the unrealized PnL.
	// Reporting totalEq as the wallet made the trading loop's
	// "wallet + unrealized" fallback count the USDT leg twice, inflating the
	// equity the AI sized against.
	var totalUPL, usdtUPL, usdtAvail float64
	foundUSDT := false
	for _, detail := range balance.Details {
		upl, parseErr := types.ParseFloatField("upl", detail.UPL)
		if parseErr != nil {
			return types.AccountBalance{}, parseErr
		}
		totalUPL += upl

		if detail.Ccy != "USDT" {
			continue
		}
		foundUSDT = true
		avail, parseErr := types.ParseFloatField("availBal", detail.AvailBal)
		if parseErr != nil {
			return types.AccountBalance{}, parseErr
		}
		usdtAvail = avail
		usdtUPL = upl
	}
	if !foundUSDT {
		// A silent zero here used to zero out equity, PnL% and available margin
		// at once, which reads as "no money" rather than "we could not look".
		return types.AccountBalance{}, fmt.Errorf("okx: USDT balance detail not found (currencies seen: %d)", len(balance.Details))
	}

	totalEq, err := types.ParseFloatField("totalEq", balance.TotalEq)
	if err != nil {
		return types.AccountBalance{}, err
	}
	adjEq, err := types.ParseFloatField("adjEq", balance.AdjEq)
	if err != nil {
		return types.AccountBalance{}, err
	}

	logger.Infof("✓ OKX balance: Total equity=%.2f, Wallet=%.2f, Available(USDT)=%.2f, Unrealized PnL=%.2f (USDT leg %.2f, adjEq %.2f)",
		totalEq, totalEq-totalUPL, usdtAvail, totalUPL, usdtUPL, adjEq)

	return types.BalanceFromEquity("okx", totalEq, totalUPL, usdtAvail), nil
}

// SetMarginMode configures the margin mode (cross/isolated) that will be applied
// to all subsequent leverage and order requests for this trader instance.
//
// OKX V5 unified accounts do not expose a per-symbol mode-switch endpoint that
// works reliably — the legacy /api/v5/account/set-isolated-mode endpoint returns
// error 51000 ("Parameter isoMode error") when called on a unified account.
// Instead, OKX applies the mode per-request via the mgnMode field on
// /api/v5/account/set-leverage and via the tdMode field on order placement.
//
// This implementation therefore stores the configured mode locally and injects it
// into each subsequent API request, rather than making an API call here.
// NOTE: unlike Binance/Bybit implementations of this interface, no network call
// is made — the method only updates local state.
func (t *OKXTrader) SetMarginMode(symbol string, isCrossMargin bool) error {
	t.isCrossMargin = isCrossMargin
	mgnMode := t.marginMode()

	// OKX V5 unified account applies cross/isolated per order via tdMode,
	// while leverage uses mgnMode on /account/set-leverage.
	// Persist the configured mode locally so subsequent leverage/order calls use it,
	// instead of calling the legacy isolated-mode endpoint that returns 51000 errors.
	logger.Infof("  ✓ %s margin mode configured as %s (applied via tdMode/mgnMode on subsequent requests)", symbol, mgnMode)
	return nil
}

// SetLeverage sets leverage
func (t *OKXTrader) SetLeverage(symbol string, leverage int) error {
	instId := t.convertSymbol(symbol)
	marginMode := t.marginMode()

	// Set leverage for both long and short
	for _, posSide := range []string{"long", "short"} {
		body := map[string]interface{}{
			"instId":  instId,
			"lever":   strconv.Itoa(leverage),
			"mgnMode": marginMode,
			"posSide": posSide,
		}

		_, err := t.doRequest("POST", okxLeveragePath, body)
		if err != nil {
			// Ignore if already at target leverage
			if strings.Contains(err.Error(), "same") {
				continue
			}
			logger.Infof("  ⚠️ Failed to set %s %s leverage: %v", symbol, posSide, err)
		}
	}

	logger.Infof("  ✓ %s leverage set to %dx (%s)", symbol, leverage, marginMode)
	return nil
}

// GetMarketPrice gets market price
func (t *OKXTrader) GetMarketPrice(symbol string) (float64, error) {
	instId := t.convertSymbol(symbol)
	path := fmt.Sprintf("%s?instId=%s", okxTickerPath, instId)

	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get price: %w", err)
	}

	var tickers []struct {
		Last string `json:"last"`
	}

	if err := json.Unmarshal(data, &tickers); err != nil {
		return 0, err
	}

	if len(tickers) == 0 {
		return 0, fmt.Errorf("no price data received")
	}

	price, err := strconv.ParseFloat(tickers[0].Last, 64)
	if err != nil {
		return 0, err
	}

	return price, nil
}

// GetClosedPnL retrieves closed position PnL records from OKX
// OKX API: /api/v5/account/positions-history
func (t *OKXTrader) GetClosedPnL(startTime time.Time, limit int) ([]types.ClosedPnLRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}

	// Build query path with parameters
	path := fmt.Sprintf("/api/v5/account/positions-history?instType=SWAP&limit=%d", limit)
	if !startTime.IsZero() {
		path += fmt.Sprintf("&after=%d", startTime.UnixMilli())
	}

	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get positions history: %w", err)
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID        string `json:"instId"`        // Instrument ID (e.g., "BTC-USDT-SWAP")
			Direction     string `json:"direction"`     // Position direction: "long" or "short"
			OpenAvgPx     string `json:"openAvgPx"`     // Average open price
			CloseAvgPx    string `json:"closeAvgPx"`    // Average close price
			CloseTotalPos string `json:"closeTotalPos"` // Closed position quantity
			RealizedPnl   string `json:"realizedPnl"`   // Realized PnL
			Fee           string `json:"fee"`           // Total fee
			FundingFee    string `json:"fundingFee"`    // Funding fee
			Lever         string `json:"lever"`         // Leverage
			CTime         string `json:"cTime"`         // Position open time
			UTime         string `json:"uTime"`         // Position close time
			Type          string `json:"type"`          // Close type: 1=close position, 2=partial close, 3=liquidation, 4=partial liquidation
			PosId         string `json:"posId"`         // Position ID
		} `json:"data"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.Code != "0" {
		return nil, fmt.Errorf("OKX API error: %s - %s", resp.Code, resp.Msg)
	}

	records := make([]types.ClosedPnLRecord, 0, len(resp.Data))

	for _, pos := range resp.Data {
		record := types.ClosedPnLRecord{}

		// Convert instrument ID to standard format (BTC-USDT-SWAP -> BTCUSDT)
		parts := strings.Split(pos.InstID, "-")
		if len(parts) >= 2 {
			record.Symbol = parts[0] + parts[1]
		} else {
			record.Symbol = pos.InstID
		}

		// Side
		record.Side = pos.Direction // OKX already returns "long" or "short"

		// Prices
		record.EntryPrice, _ = strconv.ParseFloat(pos.OpenAvgPx, 64)
		record.ExitPrice, _ = strconv.ParseFloat(pos.CloseAvgPx, 64)

		// Quantity
		record.Quantity, _ = strconv.ParseFloat(pos.CloseTotalPos, 64)

		// PnL
		record.RealizedPnL, _ = strconv.ParseFloat(pos.RealizedPnl, 64)

		// Fee
		fee, _ := strconv.ParseFloat(pos.Fee, 64)
		fundingFee, _ := strconv.ParseFloat(pos.FundingFee, 64)
		// Fee is negative in OKX (outflow), so negate to a positive cost.
		// NOTE: funding is folded in here for this legacy record path. This
		// is the ONLY place funding may live inside Fee — callers that use
		// PositionStore.AddFundingFee must not also consume this, or funding
		// gets counted twice in net PnL.
		record.Fee = -fee + fundingFee

		// Leverage
		lev, _ := strconv.ParseFloat(pos.Lever, 64)
		record.Leverage = int(lev)

		// Times
		cTime, _ := strconv.ParseInt(pos.CTime, 10, 64)
		uTime, _ := strconv.ParseInt(pos.UTime, 10, 64)
		record.EntryTime = time.UnixMilli(cTime).UTC()
		record.ExitTime = time.UnixMilli(uTime).UTC()

		// Close type
		switch pos.Type {
		case "1", "2":
			record.CloseType = "unknown" // Could be manual or AI, need to cross-reference
		case "3", "4":
			record.CloseType = "liquidation"
		default:
			record.CloseType = "unknown"
		}

		// Exchange ID
		record.ExchangeID = pos.PosId

		records = append(records, record)
	}

	return records, nil
}
