package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	nofxiagent "nofx/agent"
	"nofx/api"
	"nofx/auth"
	"nofx/config"
	"nofx/crypto"
	"nofx/kernel"
	"nofx/logger"
	"nofx/manager"
	"nofx/market"
	_ "nofx/mcp/payment"
	_ "nofx/mcp/provider"
	"nofx/store"
	"nofx/telegram"
	"nofx/telemetry"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func main() {
	// Local admin subcommands (account recovery) run directly against the
	// database and never start the HTTP server. Recovery therefore requires
	// shell/file access to the host instead of a network request, which keeps
	// it safe even when NOFX is exposed to the public internet. See cli.go.
	if runCLISubcommand(os.Args[1:]) {
		return
	}

	// Load .env environment variables
	_ = godotenv.Load()

	// Initialize logger
	logger.Init(nil)

	logger.Info("╔════════════════════════════════════════════════════════════╗")
	logger.Info("║           🚀 NOFX - AI-Powered Trading System              ║")
	logger.Info("╚════════════════════════════════════════════════════════════╝")

	// Initialize global configuration (loaded from .env).
	// MustInit refuses to start under an insecure config (e.g. missing or default JWT_SECRET).
	config.MustInit()
	cfg := config.Get()
	logger.Info("✅ Configuration loaded")

	// Initialize encryption service BEFORE database (so EncryptedString can decrypt on read)
	logger.Info("🔐 Initializing encryption service...")
	cryptoService, err := crypto.NewCryptoService()
	if err != nil {
		logger.Fatalf("❌ Failed to initialize encryption service: %v", err)
	}
	crypto.SetGlobalCryptoService(cryptoService)
	logger.Info("✅ Encryption service initialized successfully")

	// Initialize database from configuration
	// For backward compatibility: command line arg overrides config (SQLite only)
	if len(os.Args) > 1 {
		cfg.DBPath = os.Args[1]
	}
	// Ensure data directory exists (for SQLite)
	if cfg.DBType == "sqlite" {
		if dir := filepath.Dir(cfg.DBPath); dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				logger.Errorf("Failed to create data directory: %v", err)
			}
		}
	}

	logger.Infof("📋 Initializing database (%s)...", cfg.DBType)
	dbType := store.DBTypeSQLite
	if cfg.DBType == "postgres" {
		dbType = store.DBTypePostgres
	}
	st, err := store.NewWithConfig(store.DBConfig{
		Type:     dbType,
		Path:     cfg.DBPath,
		Host:     cfg.DBHost,
		Port:     cfg.DBPort,
		User:     cfg.DBUser,
		Password: cfg.DBPassword,
		DBName:   cfg.DBName,
		SSLMode:  cfg.DBSSLMode,
	})
	if err != nil {
		logger.Fatalf("❌ Failed to initialize database: %v", err)
	}
	defer st.Close()

	// Initialize installation ID for experience improvement (anonymous statistics)
	initInstallationID(st)

	// Set JWT secret
	auth.SetJWTSecret(cfg.JWTSecret)
	logger.Info("🔑 JWT secret configured")

	// WebSocket market monitor is NO LONGER USED
	// All K-line data now comes from CoinAnk API instead of Binance WebSocket cache
	// Commented out to reduce unnecessary connections:
	// go market.NewWSMonitor(150).Start(nil)
	// logger.Info("📊 WebSocket market monitor started")
	// time.Sleep(500 * time.Millisecond)
	logger.Info("📊 Using CoinAnk API for all market data (WebSocket cache disabled)")

	// Create TraderManager
	traderManager := manager.NewTraderManager()

	// Load all traders from database to memory (may auto-start traders with IsRunning=true)
	if err := traderManager.LoadTradersFromStore(st); err != nil {
		logger.Fatalf("❌ Failed to load traders: %v", err)
	}

	// Display loaded trader information
	traders, err := st.Trader().List("default")
	if err != nil {
		logger.Fatalf("❌ Failed to get trader list: %v", err)
	}

	logger.Info("🤖 AI Trader Configurations in Database:")
	if len(traders) == 0 {
		logger.Info("  (No trader configurations, please create via Web interface)")
	} else {
		for _, t := range traders {
			status := "❌ Stopped"
			if t.IsRunning {
				status = "✅ Running"
			}
			idShort := t.ID
			if len(idShort) > 8 {
				idShort = idShort[:8]
			}
			logger.Infof("  • %s [%s] %s - AI Model: %s, Exchange: %s",
				t.Name, idShort, status, t.AIModelID, t.ExchangeID)
		}
	}

	// Start API server
	server := api.NewServer(traderManager, st, cryptoService, cfg.APIServerPort)

	// Create hot-reload channel for Telegram bot; wire it to the API server
	// so that POST /api/telegram can trigger a bot restart when the token changes.
	telegramReloadCh := make(chan struct{}, 1)
	server.SetTelegramReloadCh(telegramReloadCh)

	// Start the NOFXi web agent on top of the current dev branch services.
	nofxiAgent := nofxiagent.New(traderManager, st, nil, slog.Default())
	agentWeb := nofxiagent.NewWebHandler(nofxiAgent, slog.Default())
	server.RegisterAgentHandler(agentWeb)
	nofxiAgent.Start()
	defer nofxiAgent.Stop()

	// Local market universe: one Bybit tickers poll feeds the screener, the OI
	// ranking and the price ranking. No vendor key is involved.
	universe := market.StartUniverse(context.Background())
	defer universe.Stop()

	// Stream Hyperliquid public trades to feed the positioning taker-flow ratio.
	// Best-effort: without it the prompt simply omits the flow clause.
	hlFlowWatcher := market.StartHLFlow(context.Background())
	market.SetHLFlowWatcher(hlFlowWatcher)
	defer hlFlowWatcher.Stop()

	// Index holder cost/liquidation structure from public venue positions. The
	// trades feed above is what discovers the addresses, so this is best-effort in
	// the same way: with nothing collected the structure block is omitted, and
	// nothing here can block a trade.
	positionIndex := kernel.StartPositionStructure(context.Background(), st)
	defer positionIndex.Stop()

	// Cross-venue OI/funding, and the public liquidation streams. Both are
	// best-effort: an unreachable venue simply contributes no reading, and the
	// prompt omits the section instead of showing a zero.
	marketMetrics := kernel.StartMarketMetrics(context.Background(), st)
	defer marketMetrics.Stop()

	liquidationFlow := kernel.StartLiquidationFlow(context.Background(), st)
	defer liquidationFlow.Stop()

	go func() {
		// A closed server is the normal shutdown path, not a startup failure.
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("❌ Failed to start API server: %v", err)
		}
	}()

	// Start Telegram bot (if a token is configured). It shares the NOFXi agent
	// with the web UI — only the transport layer lives in the telegram package.
	go telegram.Start(cfg, st, telegramReloadCh, nofxiAgent)

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("✅ System started successfully, waiting for trading commands...")
	logger.Info("📌 Tip: Use Ctrl+C to stop the system")

	<-quit
	logger.Info("📴 Shutdown signal received, closing system...")

	if err := server.Shutdown(); err != nil {
		logger.Warnf("⚠️ HTTP server shutdown error: %v", err)
	}
	logger.Info("✅ HTTP server stopped")

	// nofxiAgent.Stop() is handled by defer above

	// Stop all traders
	traderManager.StopAll()
	logger.Info("✅ System shut down safely")
}

// initInstallationID initializes the anonymous installation ID for experience improvement
// This ID is persisted in database and used for anonymous usage statistics
func initInstallationID(st *store.Store) {
	const key = "installation_id"

	// Try to load from database
	installationID, err := st.GetSystemConfig(key)
	if err != nil {
		logger.Warnf("⚠️ Failed to load installation ID: %v", err)
	}

	// Generate new ID if not exists
	if installationID == "" {
		installationID = uuid.New().String()
		if err := st.SetSystemConfig(key, installationID); err != nil {
			logger.Warnf("⚠️ Failed to save installation ID: %v", err)
		}
		logger.Infof("📊 Generated new installation ID: %s", installationID[:8]+"...")
	}

	// Set installation ID in experience module
	telemetry.SetInstallationID(installationID)
}
