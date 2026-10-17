package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nofx/logger"
	"nofx/store"
	"nofx/trader"

	"github.com/gin-gonic/gin"
)

// handleDecisions Decision log list
func (s *Server) handleDecisions(c *gin.Context) {
	_, traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		SafeBadRequest(c, "Invalid trader ID")
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		SafeNotFound(c, "Trader")
		return
	}

	// Get all historical decision records (unlimited)
	records, err := trader.GetStore().Decision().GetLatestRecords(trader.GetID(), 10000)
	if err != nil {
		SafeInternalError(c, "Get decision log", err)
		return
	}

	c.JSON(http.StatusOK, records)
}

// handleLatestDecisions Latest decision logs (newest first, supports limit parameter)
func (s *Server) handleLatestDecisions(c *gin.Context) {
	_, traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		SafeBadRequest(c, "Invalid trader ID")
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		SafeNotFound(c, "Trader")
		return
	}

	// Get limit from query parameter, default to 5
	limit := 5
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
			if limit > 100 {
				limit = 100 // Max 100 to prevent abuse
			}
		}
	}

	records, err := trader.GetStore().Decision().GetLatestRecords(trader.GetID(), limit)
	if err != nil {
		SafeInternalError(c, "Get decision log", err)
		return
	}

	// Reverse array to put newest first (for list display)
	// GetLatestRecords returns oldest to newest (for charts), here we need newest to oldest
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}

	c.JSON(http.StatusOK, records)
}

// handleStatistics Statistics information
func (s *Server) handleStatistics(c *gin.Context) {
	_, traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		SafeBadRequest(c, "Invalid trader ID")
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		SafeNotFound(c, "Trader")
		return
	}

	stats, err := trader.GetStore().Decision().GetStatistics(trader.GetID())
	if err != nil {
		SafeInternalError(c, "Get statistics", err)
		return
	}

	c.JSON(http.StatusOK, stats)
}

// handleCompetition Competition overview (compare all traders)
func (s *Server) handleCompetition(c *gin.Context) {
	userID := c.GetString("user_id")

	// Ensure user's traders are loaded into memory
	err := s.traderManager.LoadUserTradersFromStore(s.store, userID)
	if err != nil {
		logger.Infof("⚠️ Failed to load traders for user %s: %v", userID, err)
	}

	competition, err := s.traderManager.GetCompetitionData()
	if err != nil {
		SafeInternalError(c, "Get competition data", err)
		return
	}

	c.JSON(http.StatusOK, competition)
}

// equityHistoryPoint is one point of a trader's return curve. TotalPnL and
// TotalPnLPct are cash-flow neutral: they compare the point's equity with the
// PnL baseline that was in effect when it was taken (InitialBalance), so a
// deposit or withdrawal never shows up as trading profit.
type equityHistoryPoint struct {
	Timestamp        string  `json:"timestamp"`
	TotalEquity      float64 `json:"total_equity"`      // Account equity (wallet + unrealized)
	AvailableBalance float64 `json:"available_balance"` // Wallet balance
	InitialBalance   float64 `json:"initial_balance"`   // PnL baseline in effect at this point
	TotalPnL         float64 `json:"total_pnl"`         // Total PnL (cash-flow neutral)
	TotalPnLPct      float64 `json:"total_pnl_pct"`     // Total PnL percentage
	PositionCount    int     `json:"position_count"`    // Position count
	MarginUsedPct    float64 `json:"margin_used_pct"`   // Margin used percentage
}

// equityHistoryForTrader builds the return curve of one trader. Access control
// belongs to the caller: the public competition route requires the trader to
// have opted in, the owner route requires ownership.
func (s *Server) equityHistoryForTrader(traderID string, limit int) ([]equityHistoryPoint, error) {
	snapshots, err := s.store.Equity().GetLatest(traderID, limit)
	if err != nil {
		return nil, err
	}
	if len(snapshots) == 0 {
		return []equityHistoryPoint{}, nil
	}

	baselines, err := s.snapshotBaselines(traderID, snapshots)
	if err != nil {
		return nil, err
	}

	history := make([]equityHistoryPoint, 0, len(snapshots))
	for i, snap := range snapshots {
		baseline := baselines[i]
		totalPnL, totalPnLPct := trader.PnLAgainstBaseline(snap.TotalEquity, baseline)

		history = append(history, equityHistoryPoint{
			Timestamp:        snap.Timestamp.Format("2006-01-02 15:04:05"),
			TotalEquity:      snap.TotalEquity,
			AvailableBalance: snap.Balance,
			InitialBalance:   baseline,
			TotalPnL:         totalPnL,
			TotalPnLPct:      totalPnLPct,
			PositionCount:    snap.PositionCount,
			MarginUsedPct:    snap.MarginUsedPct,
		})
	}
	return history, nil
}

// handleEquityHistory returns equity history for a trader. This endpoint is
// PUBLIC (used by the competition leaderboard), so it cannot use the
// authenticated getTraderFromQuery helper. Instead, it validates that the
// requested trader has explicitly opted into the public competition via
// show_in_competition=true. Traders without that flag are not exposed.
func (s *Server) handleEquityHistory(c *gin.Context) {
	traderID := c.Query("trader_id")
	if traderID == "" {
		SafeBadRequest(c, "trader_id is required")
		return
	}
	trader, err := s.store.Trader().GetByID(traderID)
	if err != nil || trader == nil {
		SafeNotFound(c, "Trader")
		return
	}
	if !trader.ShowInCompetition {
		// Do not leak that a private trader exists; report not found.
		SafeNotFound(c, "Trader")
		return
	}

	// Every 3 minutes per cycle: 10000 records = about 20 days of data
	history, err := s.equityHistoryForTrader(traderID, 10000)
	if err != nil {
		SafeInternalError(c, "Get historical data", err)
		return
	}
	c.JSON(http.StatusOK, history)
}

// handleMyEquityHistory returns the return curve of one of the caller's own
// traders, whether or not it is published to the competition. The public route
// deliberately answers 404 for private traders, which the dashboard used to
// surface as "API not found" on its own equity chart.
func (s *Server) handleMyEquityHistory(c *gin.Context) {
	_, traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		SafeBadRequest(c, "Invalid trader ID")
		return
	}
	trader, err := s.store.Trader().GetByID(traderID)
	if err != nil || trader == nil {
		SafeNotFound(c, "Trader")
		return
	}

	history, err := s.equityHistoryForTrader(traderID, 10000)
	if err != nil {
		SafeInternalError(c, "Get historical data", err)
		return
	}
	c.JSON(http.StatusOK, history)
}

// handlePublicTraderList Get public trader list (no authentication required)
func (s *Server) handlePublicTraderList(c *gin.Context) {
	// Get trader information from all users
	competition, err := s.traderManager.GetCompetitionData()
	if err != nil {
		SafeInternalError(c, "Get trader list", err)
		return
	}

	// Get traders array
	tradersData, exists := competition["traders"]
	if !exists {
		c.JSON(http.StatusOK, []map[string]interface{}{})
		return
	}

	traders, ok := tradersData.([]map[string]interface{})
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Trader data format error",
		})
		return
	}

	// Return trader basic information, filter sensitive information
	result := make([]map[string]interface{}, 0, len(traders))
	for _, trader := range traders {
		result = append(result, map[string]interface{}{
			"trader_id":       trader["trader_id"],
			"trader_name":     trader["trader_name"],
			"ai_model":        trader["ai_model"],
			"exchange":        trader["exchange"],
			"is_running":      trader["is_running"],
			"total_equity":    trader["total_equity"],
			"total_pnl":       trader["total_pnl"],
			"total_pnl_pct":   trader["total_pnl_pct"],
			"position_count":  trader["position_count"],
			"margin_used_pct": trader["margin_used_pct"],
		})
	}

	c.JSON(http.StatusOK, result)
}

// handlePublicCompetition Get public competition data (no authentication required)
func (s *Server) handlePublicCompetition(c *gin.Context) {
	competition, err := s.traderManager.GetCompetitionData()
	if err != nil {
		SafeInternalError(c, "Get competition data", err)
		return
	}

	c.JSON(http.StatusOK, competition)
}

// handleTopTraders Get top 5 trader data (no authentication required, for performance comparison)
func (s *Server) handleTopTraders(c *gin.Context) {
	topTraders, err := s.traderManager.GetTopTradersData()
	if err != nil {
		SafeInternalError(c, "Get top traders data", err)
		return
	}

	c.JSON(http.StatusOK, topTraders)
}

// handleEquityHistoryBatch Batch get return rate historical data for multiple traders (no authentication required, for performance comparison)
// Supports optional 'hours' parameter to filter data by time range (e.g., hours=24 for last 24 hours)
func (s *Server) handleEquityHistoryBatch(c *gin.Context) {
	var requestBody struct {
		TraderIDs []string `json:"trader_ids"`
		Hours     int      `json:"hours"` // Optional: filter by last N hours (0 = all data)
	}

	// Try to parse POST request JSON body
	if err := c.ShouldBindJSON(&requestBody); err != nil {
		// If JSON parse fails, try to get from query parameters (compatible with GET request)
		traderIDsParam := c.Query("trader_ids")
		if traderIDsParam == "" {
			// If no trader_ids specified, return historical data for top 5
			topTraders, err := s.traderManager.GetTopTradersData()
			if err != nil {
				SafeInternalError(c, "Get top traders", err)
				return
			}

			traders, ok := topTraders["traders"].([]map[string]interface{})
			if !ok {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Trader data format error"})
				return
			}

			// Extract trader IDs
			traderIDs := make([]string, 0, len(traders))
			for _, trader := range traders {
				if traderID, ok := trader["trader_id"].(string); ok {
					traderIDs = append(traderIDs, traderID)
				}
			}

			// Parse hours parameter from query
			hoursParam := c.Query("hours")
			hours := 0
			if hoursParam != "" {
				fmt.Sscanf(hoursParam, "%d", &hours)
			}

			result := s.getEquityHistoryForTraders(traderIDs, hours)
			c.JSON(http.StatusOK, result)
			return
		}

		// Parse comma-separated trader IDs
		requestBody.TraderIDs = strings.Split(traderIDsParam, ",")
		for i := range requestBody.TraderIDs {
			requestBody.TraderIDs[i] = strings.TrimSpace(requestBody.TraderIDs[i])
		}

		// Parse hours parameter from query
		hoursParam := c.Query("hours")
		if hoursParam != "" {
			fmt.Sscanf(hoursParam, "%d", &requestBody.Hours)
		}
	}

	// Limit to maximum 20 traders to prevent oversized requests
	if len(requestBody.TraderIDs) > 20 {
		requestBody.TraderIDs = requestBody.TraderIDs[:20]
	}

	result := s.getEquityHistoryForTraders(requestBody.TraderIDs, requestBody.Hours)
	c.JSON(http.StatusOK, result)
}

// getEquityHistoryForTraders Get historical data for multiple traders
// Query directly from database, not dependent on trader in memory (so historical data can be retrieved after restart)
// Also appends current real-time data point to ensure chart matches leaderboard
// hours: filter by last N hours (0 = use default limit of 500 records)
func (s *Server) getEquityHistoryForTraders(traderIDs []string, hours int) map[string]interface{} {
	result := make(map[string]interface{})
	histories := make(map[string]interface{})
	errors := make(map[string]string)

	// Use a single consistent timestamp for all real-time data points
	now := time.Now()

	// This endpoint is PUBLIC (competition leaderboard). Like the single-trader
	// variant, it must only expose traders that opted into the public
	// competition via show_in_competition=true — otherwise private trader
	// equity curves leak to anyone holding their IDs. Private/unknown IDs are
	// skipped silently so their existence is not revealed either.
	visible := make([]string, 0, len(traderIDs))
	for _, traderID := range traderIDs {
		if traderID == "" {
			continue
		}
		// Get trader from database (use GetByID which doesn't require userID)
		trader, err := s.store.Trader().GetByID(traderID)
		if err != nil || trader == nil || !trader.ShowInCompetition {
			continue
		}
		visible = append(visible, traderID)
	}

	for _, traderID := range visible {
		if traderID == "" {
			continue
		}

		// Get equity historical data from new equity table
		var snapshots []*store.EquitySnapshot
		var err error

		if hours > 0 {
			// Filter by time range
			startTime := now.Add(-time.Duration(hours) * time.Hour)
			snapshots, err = s.store.Equity().GetByTimeRange(traderID, startTime, now)
		} else {
			// Default: get latest 500 records
			snapshots, err = s.store.Equity().GetLatest(traderID, 500)
		}
		if err != nil {
			logger.Errorf("[API] Failed to get equity history for %s: %v", traderID, err)
			errors[traderID] = "Failed to get historical data"
			continue
		}

		baselines, err := s.snapshotBaselines(traderID, snapshots)
		if err != nil {
			logger.Errorf("[API] Failed to reconstruct PnL baselines for %s: %v", traderID, err)
			errors[traderID] = "Failed to reconstruct PnL baselines"
			continue
		}

		// Build return rate historical data with PnL percentage
		history := make([]map[string]interface{}, 0, len(snapshots)+1)
		var lastSnapshotTime time.Time
		for i, snap := range snapshots {
			baseline := baselines[i]
			totalPnL, pnlPct := trader.PnLAgainstBaseline(snap.TotalEquity, baseline)

			history = append(history, map[string]interface{}{
				"timestamp":       snap.Timestamp,
				"total_equity":    snap.TotalEquity,
				"total_pnl":       totalPnL,
				"total_pnl_pct":   pnlPct,
				"balance":         snap.Balance,
				"initial_balance": baseline,
			})
			if snap.Timestamp.After(lastSnapshotTime) {
				lastSnapshotTime = snap.Timestamp
			}
		}

		// Append current real-time data point to ensure chart matches leaderboard
		// This ensures the latest point is always current, not from a potentially stale snapshot
		if liveTrader, err := s.traderManager.GetTrader(traderID); err == nil {
			if accountInfo, err := liveTrader.GetAccountInfo(); err == nil {
				// Only append if it's been more than 30 seconds since last snapshot
				if now.Sub(lastSnapshotTime) > 30*time.Second {
					totalEquity := 0.0
					if v, ok := accountInfo["total_equity"].(float64); ok {
						totalEquity = v
					}
					walletBalance := 0.0
					if v, ok := accountInfo["wallet_balance"].(float64); ok {
						walletBalance = v
					}
					// The live account reports its current (cash-flow neutral)
					// baseline, which is the right one for the appended point —
					// and the point is measured with the same rule as every
					// snapshot above.
					baseline, _ := accountInfo["initial_balance"].(float64)
					totalPnL, pnlPct := trader.PnLAgainstBaseline(totalEquity, baseline)

					history = append(history, map[string]interface{}{
						"timestamp":       now,
						"total_equity":    totalEquity,
						"total_pnl":       totalPnL,
						"total_pnl_pct":   pnlPct,
						"balance":         walletBalance,
						"initial_balance": baseline,
					})
				}
			}
		}

		histories[traderID] = history
	}

	result["histories"] = histories
	result["count"] = len(histories)
	if len(errors) > 0 {
		result["errors"] = errors
	}

	return result
}

// handleGetPublicTraderConfig Get public trader configuration information (no authentication required, does not include sensitive information)
func (s *Server) handleGetPublicTraderConfig(c *gin.Context) {
	traderID := c.Param("id")
	if traderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Trader ID cannot be empty"})
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Trader does not exist"})
		return
	}

	// Get trader status information
	status := trader.GetStatus()

	// Only return public configuration information, not including sensitive data like API keys
	result := map[string]interface{}{
		"trader_id":   trader.GetID(),
		"trader_name": trader.GetName(),
		"ai_model":    trader.GetAIModel(),
		"exchange":    trader.GetExchange(),
		"is_running":  status["is_running"],
		"ai_provider": status["ai_provider"],
		"start_time":  status["start_time"],
	}

	c.JSON(http.StatusOK, result)
}
