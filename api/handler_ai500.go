package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nofx/agent"
)

// handleAI500List serves the AI500 index board for the agent UI panel.
// Data is fetched from the direct nofxos client and served from a 5-minute
// cache, so panel polling never hammers the upstream.
func (s *Server) handleAI500List(c *gin.Context) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a non-negative integer"})
			return
		}
		limit = parsed
	}

	entries, err := agent.AI500Board(limit)
	if err != nil {
		SafeInternalError(c, "Get AI500 list", err)
		return
	}

	// Flat body, matching /api/symbols: the web httpClient wraps the raw
	// response body as `data`, so a nested success/data envelope here would
	// hide the coins from the panel.
	c.JSON(http.StatusOK, gin.H{
		"coins": entries,
		"count": len(entries),
	})
}
