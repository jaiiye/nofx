package mcp

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"nofx/logger"
	"nofx/security"
)

// Config client configuration (centralized management of all configurations)
type Config struct {
	// Provider configuration
	Provider string
	APIKey   string
	BaseURL  string
	Model    string

	// Behavior configuration
	MaxTokens   int
	MaxContext  int // Model's max context window in tokens (0 = no limit)
	Temperature float64
	UseFullURL  bool

	// ExtraBody holds raw request-body overrides merged verbatim into every
	// chat-completions body. It exists for the provider knobs nofx has no field
	// for — chiefly a thinking model's reasoning switch (`thinking: false`,
	// `reasoning_effort: "none"`). A model that spends the whole completion
	// budget on its reasoning returns an empty content, which fails the decision
	// cycle; there is otherwise no way to turn that off. Set via
	// AI_EXTRA_BODY_JSON, e.g. {"thinking": false}.
	ExtraBody map[string]any

	// Retry configuration
	MaxRetries      int
	RetryWaitBase   time.Duration
	RetryableErrors []string

	// Timeout configuration
	Timeout time.Duration

	// Dependency injection
	Logger     Logger
	HTTPClient *http.Client
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		// Default values
		// 8192, not 2000: a reasoning model spends the completion budget on its
		// thinking first, and 2000 was routinely exhausted before any answer — the
		// model then returned an empty content and the cycle produced nothing.
		MaxTokens:       getEnvInt("AI_MAX_TOKENS", 8192),
		Temperature:     MCPClientTemperature,
		ExtraBody:       getEnvJSON("AI_EXTRA_BODY_JSON"),
		MaxRetries:      MaxRetryTimes,
		RetryWaitBase:   2 * time.Second,
		Timeout:         DefaultTimeout,
		RetryableErrors: retryableErrors,

		// Default dependencies (use global logger)
		Logger:     logger.NewMCPLogger(),
		HTTPClient: security.SafeHTTPClient(DefaultTimeout),
	}
}

// getEnvInt reads integer from environment variable, returns default value if failed
func getEnvInt(key string, defaultValue int) int {
	if val := os.Getenv(key); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultValue
}

// getEnvString reads string from environment variable, returns default value if empty
func getEnvString(key string, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}

// getEnvJSON parses a JSON object from an environment variable. A malformed
// value is dropped with a warning instead of failing startup: extra body
// overrides are opt-in provider knobs, and losing them must not take the
// process down.
func getEnvJSON(key string) map[string]any {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(val), &parsed); err != nil {
		logger.Warnf("⚠️  %s is not a valid JSON object, ignoring it: %v", key, err)
		return nil
	}
	return parsed
}
