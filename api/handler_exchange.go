package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"nofx/config"
	"nofx/crypto"
	"nofx/logger"
	"nofx/store"

	"github.com/gin-gonic/gin"
)

type ExchangeConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"` // "cex" or "dex"
	Enabled   bool   `json:"enabled"`
	APIKey    string `json:"apiKey,omitempty"`
	SecretKey string `json:"secretKey,omitempty"`
	Testnet   bool   `json:"testnet,omitempty"`
}

// SafeExchangeConfig Safe exchange configuration structure (does not contain sensitive information)
type SafeExchangeConfig struct {
	ID                         string `json:"id"`            // UUID
	ExchangeType               string `json:"exchange_type"` // this build only supports "hyperliquid"
	AccountName                string `json:"account_name"`  // User-defined account name
	Name                       string `json:"name"`          // Display name
	Type                       string `json:"type"`          // "cex" or "dex"
	Enabled                    bool   `json:"enabled"`
	HasAPIKey                  bool   `json:"has_api_key"`
	HasSecretKey               bool   `json:"has_secret_key"`
	HasPassphrase              bool   `json:"has_passphrase"`
	Testnet                    bool   `json:"testnet,omitempty"`
	HyperliquidWalletAddr      string `json:"hyperliquidWalletAddr"` // Hyperliquid wallet address (not sensitive)
	HyperliquidBuilderApproved bool   `json:"hyperliquidBuilderApproved"`
}

func safeExchangeConfigFromStore(exchange *store.Exchange) SafeExchangeConfig {
	return SafeExchangeConfig{
		ID:                         exchange.ID,
		ExchangeType:               exchange.ExchangeType,
		AccountName:                exchange.AccountName,
		Name:                       exchange.Name,
		Type:                       exchange.Type,
		Enabled:                    exchange.Enabled,
		HasAPIKey:                  exchange.APIKey != "",
		HasSecretKey:               exchange.SecretKey != "",
		HasPassphrase:              exchange.Passphrase != "",
		Testnet:                    exchange.Testnet,
		HyperliquidWalletAddr:      exchange.HyperliquidWalletAddr,
		HyperliquidBuilderApproved: exchange.HyperliquidBuilderApproved,
	}
}

// ExchangeConfigUpdate is a single exchange account's update payload. It is a
// named type (rather than an inline anonymous struct) so the log-sanitizer in
// utils.go is guaranteed to cover every sensitive field — a drift between the
// two shapes is what let passphrases / private keys reach the logs previously.
type ExchangeConfigUpdate struct {
	Enabled                    bool   `json:"enabled"`
	APIKey                     string `json:"api_key"`
	SecretKey                  string `json:"secret_key"`
	Passphrase                 string `json:"passphrase"` // legacy CEX field, kept for payload compatibility
	Testnet                    bool   `json:"testnet"`
	HyperliquidWalletAddr      string `json:"hyperliquid_wallet_addr"`
	HyperliquidUnifiedAcct     bool   `json:"hyperliquid_unified_account"` // Unified Account mode
	HyperliquidBuilderApproved *bool  `json:"hyperliquid_builder_approved"`
}

type UpdateExchangeConfigRequest struct {
	Exchanges map[string]ExchangeConfigUpdate `json:"exchanges"`
}

// CreateExchangeRequest request structure for creating a new exchange account
type CreateExchangeRequest struct {
	ExchangeType               string `json:"exchange_type" binding:"required"` // this build only supports "hyperliquid"
	AccountName                string `json:"account_name"`                     // User-defined account name
	Enabled                    bool   `json:"enabled"`
	APIKey                     string `json:"api_key"`
	SecretKey                  string `json:"secret_key"`
	Passphrase                 string `json:"passphrase"`
	Testnet                    bool   `json:"testnet"`
	HyperliquidWalletAddr      string `json:"hyperliquid_wallet_addr"`
	HyperliquidUnifiedAcct     bool   `json:"hyperliquid_unified_account"` // Unified Account mode: Spot as Perp collateral
	HyperliquidBuilderApproved bool   `json:"hyperliquid_builder_approved"`
}

// handleGetExchangeConfigs Get exchange configurations
func (s *Server) handleGetExchangeConfigs(c *gin.Context) {
	userID := c.GetString("user_id")
	logger.Infof("🔍 Querying exchange configs for user %s", userID)
	exchanges, err := s.store.Exchange().List(userID)
	if err != nil {
		SafeInternalError(c, "Failed to get exchange configs", err)
		return
	}

	// If no exchanges in database, return empty array (user needs to create accounts)
	if len(exchanges) == 0 {
		logger.Infof("⚠️ No exchanges in database for user %s", userID)
		c.JSON(http.StatusOK, []SafeExchangeConfig{})
		return
	}

	logger.Infof("✅ Found %d exchange configs", len(exchanges))

	// Convert to safe response structure, remove sensitive information
	safeExchanges := make([]SafeExchangeConfig, 0, len(exchanges))
	for _, exchange := range exchanges {
		if !store.IsVisibleExchange(exchange) {
			continue
		}
		safeExchanges = append(safeExchanges, safeExchangeConfigFromStore(exchange))
	}

	c.JSON(http.StatusOK, safeExchanges)
}

// handleUpdateExchangeConfigs Update exchange configurations (supports both encrypted and plain text based on config)
func (s *Server) handleUpdateExchangeConfigs(c *gin.Context) {
	userID := c.GetString("user_id")
	cfg := config.Get()

	// Read raw request body
	bodyBytes, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
		return
	}

	var req UpdateExchangeConfigRequest

	// Check if transport encryption is enabled
	if !cfg.TransportEncryption {
		// Transport encryption disabled, accept plain JSON
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			logger.Infof("❌ Failed to parse plain JSON request: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
			return
		}
		logger.Infof("📝 Received plain text exchange config (UserID: %s)", userID)
	} else {
		// Transport encryption enabled, require encrypted payload
		var encryptedPayload crypto.EncryptedPayload
		if err := json.Unmarshal(bodyBytes, &encryptedPayload); err != nil {
			logger.Infof("❌ Failed to parse encrypted payload: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format, encrypted transmission required"})
			return
		}

		// Verify encrypted data
		if encryptedPayload.WrappedKey == "" {
			logger.Infof("❌ Detected unencrypted request (UserID: %s)", userID)
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "This endpoint only supports encrypted transmission, please use encrypted client",
				"code":    "ENCRYPTION_REQUIRED",
				"message": "Encrypted transmission is required for security reasons",
			})
			return
		}

		// Decrypt data
		decrypted, err := s.cryptoHandler.cryptoService.DecryptSensitiveData(&encryptedPayload)
		if err != nil {
			logger.Infof("❌ Failed to decrypt exchange config (UserID: %s): %v", userID, err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to decrypt data"})
			return
		}

		// Parse decrypted data
		if err := json.Unmarshal([]byte(decrypted), &req); err != nil {
			logger.Infof("❌ Failed to parse decrypted data: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse decrypted data"})
			return
		}
		logger.Infof("🔓 Decrypted exchange config data (UserID: %s)", userID)
	}

	// Update each exchange's configuration and track traders that need reload
	tradersToReload := make(map[string]bool)
	for exchangeID, exchangeData := range req.Exchanges {
		existing, err := s.store.Exchange().GetByID(userID, exchangeID)
		if err != nil {
			SafeInternalError(c, fmt.Sprintf("Load exchange %s", exchangeID), err)
			return
		}
		effectiveAPIKey := strings.TrimSpace(exchangeData.APIKey)
		if effectiveAPIKey == "" {
			effectiveAPIKey = strings.TrimSpace(string(existing.APIKey))
		}
		effectiveHyperliquidWalletAddr := strings.TrimSpace(exchangeData.HyperliquidWalletAddr)
		if effectiveHyperliquidWalletAddr == "" {
			effectiveHyperliquidWalletAddr = strings.TrimSpace(existing.HyperliquidWalletAddr)
		}
		effectiveHyperliquidBuilderApproved := existing.HyperliquidBuilderApproved
		if exchangeData.HyperliquidBuilderApproved != nil {
			effectiveHyperliquidBuilderApproved = *exchangeData.HyperliquidBuilderApproved
		}

		if missing := store.MissingRequiredExchangeCredentialFields(
			existing.ExchangeType,
			effectiveAPIKey,
			effectiveHyperliquidWalletAddr,
		); len(missing) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":          fmt.Sprintf("Missing required exchange fields: %s", strings.Join(missing, ", ")),
				"missing_fields": missing,
			})
			return
		}

		// Find traders using this exchange BEFORE updating
		traders, _ := s.store.Trader().ListByExchangeID(userID, exchangeID)
		for _, t := range traders {
			tradersToReload[t.ID] = true
		}

		err = s.store.Exchange().Update(userID, exchangeID, true, exchangeData.APIKey, exchangeData.SecretKey, exchangeData.Passphrase, exchangeData.Testnet, effectiveHyperliquidWalletAddr, exchangeData.HyperliquidUnifiedAcct, effectiveHyperliquidBuilderApproved)
		if err != nil {
			SafeInternalError(c, fmt.Sprintf("Update exchange %s", exchangeID), err)
			return
		}
	}

	s.exchangeAccountStateCache.Invalidate(userID)

	// Remove affected traders from memory BEFORE reloading to pick up new config
	for traderID := range tradersToReload {
		logger.Infof("🔄 Removing trader %s from memory to reload with new exchange config", traderID)
		s.traderManager.RemoveTrader(traderID)
	}

	// Reload all traders for this user to make new config take effect immediately
	err = s.traderManager.LoadUserTradersFromStore(s.store, userID)
	if err != nil {
		logger.Infof("⚠️ Failed to reload user traders into memory: %v", err)
		// Don't return error here since exchange config was successfully updated to database
	}

	logger.Infof("✓ Exchange config updated: %+v", SanitizeExchangeConfigForLog(req.Exchanges))
	c.JSON(http.StatusOK, gin.H{"message": "Exchange configuration updated"})
}

// handleCreateExchange Create a new exchange account
func (s *Server) handleCreateExchange(c *gin.Context) {
	userID := c.GetString("user_id")
	cfg := config.Get()

	// Read raw request body
	bodyBytes, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
		return
	}

	var req CreateExchangeRequest

	// Check if transport encryption is enabled
	if !cfg.TransportEncryption {
		// Transport encryption disabled, accept plain JSON
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			logger.Infof("❌ Failed to parse plain JSON request: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
			return
		}
	} else {
		// Transport encryption enabled, require encrypted payload
		var encryptedPayload crypto.EncryptedPayload
		if err := json.Unmarshal(bodyBytes, &encryptedPayload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format, encrypted transmission required"})
			return
		}

		if encryptedPayload.WrappedKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "This endpoint only supports encrypted transmission",
				"code":    "ENCRYPTION_REQUIRED",
				"message": "Encrypted transmission is required for security reasons",
			})
			return
		}

		decrypted, err := s.cryptoHandler.cryptoService.DecryptSensitiveData(&encryptedPayload)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to decrypt data"})
			return
		}

		if err := json.Unmarshal([]byte(decrypted), &req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse decrypted data"})
			return
		}
	}

	// Validate exchange type (this build only supports Hyperliquid)
	validTypes := map[string]bool{
		"hyperliquid": true,
	}
	if !validTypes[req.ExchangeType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid exchange type: %s (this build only supports hyperliquid)", req.ExchangeType)})
		return
	}
	if missing := store.MissingRequiredExchangeCredentialFields(
		req.ExchangeType,
		req.APIKey,
		req.HyperliquidWalletAddr,
	); len(missing) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":          fmt.Sprintf("Missing required exchange fields: %s", strings.Join(missing, ", ")),
			"missing_fields": missing,
		})
		return
	}

	// Exchange configs only persist once complete; persisted configs are always enabled.
	id, err := s.store.Exchange().Create(
		userID, req.ExchangeType, req.AccountName, true,
		req.APIKey, req.SecretKey, req.Passphrase, req.Testnet,
		req.HyperliquidWalletAddr, req.HyperliquidUnifiedAcct, req.HyperliquidBuilderApproved,
	)
	if err != nil {
		logger.Infof("❌ Failed to create exchange account: %v", err)
		SafeInternalError(c, "Failed to create exchange account", err)
		return
	}

	s.exchangeAccountStateCache.Invalidate(userID)

	logger.Infof("✓ Created exchange account: type=%s, name=%s, id=%s", req.ExchangeType, req.AccountName, id)
	c.JSON(http.StatusOK, gin.H{
		"message": "Exchange account created",
		"id":      id,
	})
}

// handleDeleteExchange Delete an exchange account
func (s *Server) handleDeleteExchange(c *gin.Context) {
	userID := c.GetString("user_id")
	exchangeID := c.Param("id")

	if exchangeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Exchange ID is required"})
		return
	}

	// Check if any traders are using this exchange
	traders, err := s.store.Trader().List(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check traders"})
		return
	}

	for _, trader := range traders {
		if trader.ExchangeID == exchangeID {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":       "Cannot delete exchange account that is in use by traders",
				"trader_id":   trader.ID,
				"trader_name": trader.Name,
			})
			return
		}
	}

	// Delete exchange account
	err = s.store.Exchange().Delete(userID, exchangeID)
	if err != nil {
		logger.Infof("❌ Failed to delete exchange account: %v", err)
		SafeInternalError(c, "Failed to delete exchange account", err)
		return
	}

	s.exchangeAccountStateCache.Invalidate(userID)

	logger.Infof("✓ Deleted exchange account: id=%s", exchangeID)
	c.JSON(http.StatusOK, gin.H{"message": "Exchange account deleted"})
}

// handleGetSupportedExchanges Get list of exchanges supported by the system
func (s *Server) handleGetSupportedExchanges(c *gin.Context) {
	// Return static list of supported exchange types.
	// Note: ID is empty for supported exchanges (they are templates, not actual accounts).
	// This build only ships the Hyperliquid trader implementation.
	supportedExchanges := []SafeExchangeConfig{
		{ExchangeType: "hyperliquid", Name: "Hyperliquid", Type: "dex"},
	}

	c.JSON(http.StatusOK, supportedExchanges)
}
