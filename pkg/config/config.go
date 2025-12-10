// Package config provides configuration management for the arbitrage bot.
package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// Config holds the complete bot configuration.
type Config struct {
	// Bot settings
	UpdateIntervalMs int  `json:"update_interval_ms"`
	Verbose          bool `json:"verbose"`
	DryRun           bool `json:"dry_run"`

	// Arbitrage settings
	Arbitrage ArbitrageSettings `json:"arbitrage"`

	// Cross-chain arbitrage settings
	CrossChainArbitrage CrossChainArbitrageSettings `json:"cross_chain_arbitrage"`

	// Rebalancing settings
	Rebalancing RebalancingSettings `json:"rebalancing"`

	// Exchange settings
	Exchanges []ExchangeSettings `json:"exchanges"`

	// Token settings
	Tokens []TokenSettings `json:"tokens"`

	// Pair settings
	Pairs []PairSettings `json:"pairs"`

	// Slack notification settings
	Slack SlackSettings `json:"slack"`
}

// SlackSettings holds Slack notification configuration.
type SlackSettings struct {
	Enabled  bool   `json:"enabled"`
	APIToken string `json:"api_token,omitempty"`
	Channel  string `json:"channel,omitempty"`
}

// RebalancingSettings holds inventory rebalancing configuration.
type RebalancingSettings struct {
	// Enable/disable features
	Enabled              bool `json:"enabled"`                // Enable inventory monitoring
	AutoBridge           bool `json:"auto_bridge"`            // Enable automatic bridging (vs manual/confirm)
	RequireConfirmation  bool `json:"require_confirmation"`   // Require user confirmation before bridging

	// Timing
	CheckIntervalSeconds     int `json:"check_interval_seconds"`      // How often to check for drift (default: 300 = 5 min)
	BridgeStatusPollSeconds  int `json:"bridge_status_poll_seconds"`  // How often to poll bridge status (default: 60)
	MaxBridgeWaitMinutes     int `json:"max_bridge_wait_minutes"`     // Max time to wait for bridge (default: 30)
	MinTimeBetweenRebalances int `json:"min_time_between_rebalances"` // Minimum seconds between rebalances (default: 600)

	// Thresholds
	DriftThresholdPct     float64 `json:"drift_threshold_pct"`      // Drift % to trigger rebalance (default: 20)
	CriticalDriftPct      float64 `json:"critical_drift_pct"`       // Critical drift level (default: 40)
	MinRebalanceAmountUSD float64 `json:"min_rebalance_amount_usd"` // Minimum USD value to bridge (default: 50)
	MaxRebalanceAmountUSD float64 `json:"max_rebalance_amount_usd"` // Maximum USD value per bridge (default: 1000)

	// Limits
	MaxPendingBridges int `json:"max_pending_bridges"` // Max concurrent bridges (default: 1)

	// Circuit breaker
	CircuitBreakerThreshold  int `json:"circuit_breaker_threshold"`   // Consecutive failures to open (default: 3)
	CircuitBreakerResetMins  int `json:"circuit_breaker_reset_mins"`  // Minutes to wait before retry (default: 30)

	// Target allocations: exchange -> currency -> target percentage (0-100)
	// Example: {"gswap": {"GALA": 60, "USDT": 40}, "binance": {"GALA": 40, "USDT": 60}}
	TargetAllocations map[string]map[string]float64 `json:"target_allocations,omitempty"`

	// Tracked currencies (if empty, track all)
	TrackedCurrencies []string `json:"tracked_currencies,omitempty"`
}

// DefaultRebalancingSettings returns sensible defaults.
func DefaultRebalancingSettings() RebalancingSettings {
	return RebalancingSettings{
		Enabled:                  false, // Disabled by default for safety
		AutoBridge:               false, // Manual confirmation by default
		RequireConfirmation:      true,
		CheckIntervalSeconds:     300,   // 5 minutes
		BridgeStatusPollSeconds:  60,    // 1 minute
		MaxBridgeWaitMinutes:     30,
		MinTimeBetweenRebalances: 600,   // 10 minutes
		DriftThresholdPct:        20.0,
		CriticalDriftPct:         40.0,
		MinRebalanceAmountUSD:    50.0,
		MaxRebalanceAmountUSD:    1000.0,
		MaxPendingBridges:        1,
		CircuitBreakerThreshold:  3,
		CircuitBreakerResetMins:  30,
		TargetAllocations:        make(map[string]map[string]float64),
		TrackedCurrencies:        []string{"GALA", "USDT", "USDC", "GUSDC", "GUSDT"},
	}
}

// ArbitrageSettings holds arbitrage-specific configuration.
type ArbitrageSettings struct {
	MinSpreadBps      int     `json:"min_spread_bps"`
	MinNetProfitBps   int     `json:"min_net_profit_bps"`
	MaxPriceImpactBps int     `json:"max_price_impact_bps"`
	DefaultTradeSize  float64 `json:"default_trade_size"`
	QuoteValiditySecs int     `json:"quote_validity_secs"`
}

// CrossChainArbitrageSettings holds cross-chain arbitrage configuration.
type CrossChainArbitrageSettings struct {
	// Enable/disable
	Enabled bool `json:"enabled"` // Enable cross-chain arbitrage detection

	// Profit thresholds
	MinSpreadPercent         float64 `json:"min_spread_percent"`           // Minimum spread % before considering (default: 3.0)
	MinRiskAdjustedProfitBps int     `json:"min_risk_adjusted_profit_bps"` // Minimum risk-adjusted profit (default: 100)

	// Bridge parameters
	MaxBridgeTimeMinutes int `json:"max_bridge_time_minutes"` // Maximum acceptable bridge time (default: 30)
	BridgeTimeToEthMin   int `json:"bridge_time_to_eth_min"`  // Estimated bridge time to Ethereum (default: 15)
	BridgeTimeToGalaMin  int `json:"bridge_time_to_gala_min"` // Estimated bridge time to GalaChain (default: 15)

	// Volatility settings
	VolatilityWindowMinutes  int     `json:"volatility_window_minutes"`   // Window for volatility calculation (default: 60)
	VolatilityBufferPercent  float64 `json:"volatility_buffer_percent"`   // Additional buffer for volatility risk (default: 2.0)
	DefaultVolatilityBps     int     `json:"default_volatility_bps"`      // Default volatility when insufficient data (default: 200)
	ConfidenceMultiplier     float64 `json:"confidence_multiplier"`       // Multiplier for risk calculation (default: 2.0)

	// Execution settings
	AutoExecute         bool     `json:"auto_execute"`          // Automatically execute cross-chain opportunities
	RequireConfirmation bool     `json:"require_confirmation"`  // Require user confirmation before execution
	AllowedTokens       []string `json:"allowed_tokens"`        // Tokens allowed for cross-chain arb (empty = all bridgeable)
	ExecutionStrategy   string   `json:"execution_strategy"`    // "immediate", "staged", or "hedged"
}

// DefaultCrossChainArbitrageSettings returns sensible defaults.
func DefaultCrossChainArbitrageSettings() CrossChainArbitrageSettings {
	return CrossChainArbitrageSettings{
		Enabled:                  false, // Disabled by default for safety
		MinSpreadPercent:         3.0,   // 3% minimum spread
		MinRiskAdjustedProfitBps: 100,   // 1% minimum risk-adjusted profit
		MaxBridgeTimeMinutes:     30,
		BridgeTimeToEthMin:       15,
		BridgeTimeToGalaMin:      15,
		VolatilityWindowMinutes:  60,
		VolatilityBufferPercent:  2.0,
		DefaultVolatilityBps:     200,
		ConfidenceMultiplier:     2.0,
		AutoExecute:              false,
		RequireConfirmation:      true,
		AllowedTokens:            []string{"GALA", "GUSDT", "GUSDC"},
		ExecutionStrategy:        "staged", // Default to staged execution
	}
}

// ExchangeSettings holds configuration for a single exchange.
type ExchangeSettings struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"` // "dex" or "cex"
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url,omitempty"`

	// API credentials for CEXs
	APIKey     string `json:"api_key,omitempty"`
	Secret     string `json:"secret,omitempty"`
	Passphrase string `json:"passphrase,omitempty"` // Required for some exchanges like Coinbase

	// Wallet credentials for DEXs (GSwap)
	PrivateKey    string `json:"private_key,omitempty"`    // Ethereum-style private key
	WalletAddress string `json:"wallet_address,omitempty"` // Derived or explicit wallet address

	// Trading settings per exchange
	TradingEnabled bool    `json:"trading_enabled"`          // Allow actual trades (not just detection)
	MaxTradeSize   float64 `json:"max_trade_size,omitempty"` // Max single trade size
}

// TokenSettings holds configuration for a token.
type TokenSettings struct {
	Symbol        string `json:"symbol"`
	Name          string `json:"name,omitempty"`
	Decimals      int    `json:"decimals"`
	GalaChainMint string `json:"galachain_mint,omitempty"`
	ContractAddr  string `json:"contract_addr,omitempty"`
}

// PairSettings holds configuration for a trading pair to track.
type PairSettings struct {
	Pair      string   `json:"pair"`      // e.g., "GALA/USDT"
	Exchanges []string `json:"exchanges"` // Exchange IDs to track
	Enabled   bool     `json:"enabled"`
	TradeSize float64  `json:"trade_size,omitempty"` // Override default trade size
}

// DefaultConfig returns a default configuration.
func DefaultConfig() *Config {
	return &Config{
		UpdateIntervalMs: 15000, // 15 seconds
		Verbose:          true,
		DryRun:           true,

		Arbitrage: ArbitrageSettings{
			MinSpreadBps:      50,   // 0.5%
			MinNetProfitBps:   20,   // 0.2%
			MaxPriceImpactBps: 500,  // 5%
			DefaultTradeSize:  1000, // $1000 equivalent
			QuoteValiditySecs: 30,
		},

		Rebalancing: DefaultRebalancingSettings(),

		CrossChainArbitrage: DefaultCrossChainArbitrageSettings(),

		Exchanges: []ExchangeSettings{
			{ID: "gswap", Name: "GSwap", Type: "dex", Enabled: true},
			{ID: "binance", Name: "Binance", Type: "cex", Enabled: true},
			{ID: "coinbase", Name: "Coinbase", Type: "cex", Enabled: true},
			{ID: "kraken", Name: "Kraken", Type: "cex", Enabled: true},
			{ID: "okx", Name: "OKX", Type: "cex", Enabled: false},
			{ID: "bybit", Name: "Bybit", Type: "cex", Enabled: false},
			{ID: "kucoin", Name: "KuCoin", Type: "cex", Enabled: false},
			{ID: "gate", Name: "Gate.io", Type: "cex", Enabled: false},
		},

		Tokens: []TokenSettings{
			{Symbol: "GALA", Name: "Gala", Decimals: 8, GalaChainMint: "GALA|Unit|none|none"},
			{Symbol: "ETH", Name: "Ethereum", Decimals: 18},
			{Symbol: "USDT", Name: "Tether", Decimals: 6},
			{Symbol: "USDC", Name: "USD Coin", Decimals: 6},
			{Symbol: "USD", Name: "US Dollar", Decimals: 2},
			{Symbol: "USDUC", Name: "USD Coin (GalaChain)", Decimals: 6, GalaChainMint: "GUSDUC|Unit|none|none"},
			{Symbol: "BTC", Name: "Bitcoin", Decimals: 8},
			{Symbol: "SOL", Name: "Solana", Decimals: 9},
			{Symbol: "TRX", Name: "TRON", Decimals: 6},
			{Symbol: "MEW", Name: "Mew", Decimals: 8, GalaChainMint: "GMEW|Unit|none|none"},
		},

		Pairs: []PairSettings{
			{Pair: "GALA/USDT", Exchanges: []string{"gswap", "binance", "coinbase", "kraken"}, Enabled: true},
			{Pair: "ETH/USDT", Exchanges: []string{"gswap", "binance", "coinbase", "kraken"}, Enabled: true},
			{Pair: "BTC/USDT", Exchanges: []string{"binance", "coinbase", "kraken"}, Enabled: true},
			{Pair: "SOL/USDT", Exchanges: []string{"binance", "coinbase", "kraken"}, Enabled: true},
			{Pair: "USDUC/USDT", Exchanges: []string{"kraken"}, Enabled: true},
			{Pair: "USDUC/USDC", Exchanges: []string{"kraken"}, Enabled: true},
			{Pair: "TRX/USDT", Exchanges: []string{"binance"}, Enabled: true},
			{Pair: "MEW/GALA", Exchanges: []string{"gswap"}, Enabled: true},
		},
	}
}

// LoadFromFile loads configuration from a JSON file.
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Override with environment variables
	cfg.applyEnvOverrides()

	return cfg, nil
}

// LoadFromEnv loads configuration from environment variables.
func LoadFromEnv() *Config {
	cfg := DefaultConfig()
	cfg.applyEnvOverrides()
	return cfg
}

// applyEnvOverrides applies environment variable overrides.
func (c *Config) applyEnvOverrides() {
	// Bot settings
	if v := os.Getenv("BOT_UPDATE_INTERVAL_MS"); v != "" {
		if val, err := parseInt(v); err == nil {
			c.UpdateIntervalMs = val
		}
	}
	if v := os.Getenv("BOT_VERBOSE"); v != "" {
		c.Verbose = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("BOT_DRY_RUN"); v != "" {
		c.DryRun = strings.ToLower(v) == "true"
	}

	// Arbitrage settings
	if v := os.Getenv("ARB_MIN_SPREAD_BPS"); v != "" {
		if val, err := parseInt(v); err == nil {
			c.Arbitrage.MinSpreadBps = val
		}
	}
	if v := os.Getenv("ARB_MIN_NET_PROFIT_BPS"); v != "" {
		if val, err := parseInt(v); err == nil {
			c.Arbitrage.MinNetProfitBps = val
		}
	}
	if v := os.Getenv("ARB_DEFAULT_TRADE_SIZE"); v != "" {
		if val, err := parseFloat(v); err == nil {
			c.Arbitrage.DefaultTradeSize = val
		}
	}

	// Slack settings
	if v := os.Getenv("SLACK_ENABLED"); v != "" {
		c.Slack.Enabled = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("SLACK_API_TOKEN"); v != "" {
		c.Slack.APIToken = v
	}
	if v := os.Getenv("SLACK_CHANNEL"); v != "" {
		c.Slack.Channel = v
	}

	// Cross-chain arbitrage settings
	if v := os.Getenv("CROSS_CHAIN_ARB_ENABLED"); v != "" {
		c.CrossChainArbitrage.Enabled = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("CROSS_CHAIN_ARB_MIN_SPREAD_PERCENT"); v != "" {
		if val, err := parseFloat(v); err == nil {
			c.CrossChainArbitrage.MinSpreadPercent = val
		}
	}
	if v := os.Getenv("CROSS_CHAIN_ARB_MIN_PROFIT_BPS"); v != "" {
		if val, err := parseInt(v); err == nil {
			c.CrossChainArbitrage.MinRiskAdjustedProfitBps = val
		}
	}
	if v := os.Getenv("CROSS_CHAIN_ARB_AUTO_EXECUTE"); v != "" {
		c.CrossChainArbitrage.AutoExecute = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("CROSS_CHAIN_ARB_EXECUTION_STRATEGY"); v != "" {
		c.CrossChainArbitrage.ExecutionStrategy = v
	}

	// Rebalancing settings
	if v := os.Getenv("REBALANCING_ENABLED"); v != "" {
		c.Rebalancing.Enabled = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("REBALANCING_AUTO_BRIDGE"); v != "" {
		c.Rebalancing.AutoBridge = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("REBALANCING_REQUIRE_CONFIRMATION"); v != "" {
		c.Rebalancing.RequireConfirmation = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("REBALANCING_CHECK_INTERVAL_SECONDS"); v != "" {
		if val, err := parseInt(v); err == nil {
			c.Rebalancing.CheckIntervalSeconds = val
		}
	}
	if v := os.Getenv("REBALANCING_DRIFT_THRESHOLD_PCT"); v != "" {
		if val, err := parseFloat(v); err == nil {
			c.Rebalancing.DriftThresholdPct = val
		}
	}
	if v := os.Getenv("REBALANCING_CRITICAL_DRIFT_PCT"); v != "" {
		if val, err := parseFloat(v); err == nil {
			c.Rebalancing.CriticalDriftPct = val
		}
	}
	if v := os.Getenv("REBALANCING_MIN_AMOUNT_USD"); v != "" {
		if val, err := parseFloat(v); err == nil {
			c.Rebalancing.MinRebalanceAmountUSD = val
		}
	}
	if v := os.Getenv("REBALANCING_MAX_AMOUNT_USD"); v != "" {
		if val, err := parseFloat(v); err == nil {
			c.Rebalancing.MaxRebalanceAmountUSD = val
		}
	}

	// Exchange API keys from environment
	for i := range c.Exchanges {
		envPrefix := strings.ToUpper(c.Exchanges[i].ID)
		if apiKey := os.Getenv(envPrefix + "_API_KEY"); apiKey != "" {
			c.Exchanges[i].APIKey = apiKey
		}
		if secret := os.Getenv(envPrefix + "_SECRET"); secret != "" {
			c.Exchanges[i].Secret = secret
		}
		if passphrase := os.Getenv(envPrefix + "_PASSPHRASE"); passphrase != "" {
			c.Exchanges[i].Passphrase = passphrase
		}
		if privateKey := os.Getenv(envPrefix + "_PRIVATE_KEY"); privateKey != "" {
			c.Exchanges[i].PrivateKey = privateKey
		}
		if walletAddr := os.Getenv(envPrefix + "_WALLET_ADDRESS"); walletAddr != "" {
			c.Exchanges[i].WalletAddress = walletAddr
		}
		if tradingEnabled := os.Getenv(envPrefix + "_TRADING_ENABLED"); tradingEnabled != "" {
			c.Exchanges[i].TradingEnabled = strings.ToLower(tradingEnabled) == "true"
		}
		if maxTradeSize := os.Getenv(envPrefix + "_MAX_TRADE_SIZE"); maxTradeSize != "" {
			if val, err := parseFloat(maxTradeSize); err == nil {
				c.Exchanges[i].MaxTradeSize = val
			}
		}
	}
}

// SaveToFile saves the configuration to a JSON file.
func (c *Config) SaveToFile(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// ToArbitrageConfig converts to types.ArbitrageConfig.
func (c *Config) ToArbitrageConfig() *types.ArbitrageConfig {
	return &types.ArbitrageConfig{
		MinSpreadBps:      c.Arbitrage.MinSpreadBps,
		MinNetProfitBps:   c.Arbitrage.MinNetProfitBps,
		MaxPriceImpactBps: c.Arbitrage.MaxPriceImpactBps,
		DefaultTradeSize:  big.NewFloat(c.Arbitrage.DefaultTradeSize),
		QuoteValiditySecs: c.Arbitrage.QuoteValiditySecs,
	}
}

// ToInventoryConfig converts rebalancing settings to inventory.InventoryConfig.
func (c *Config) ToInventoryConfig() map[string]interface{} {
	return map[string]interface{}{
		"drift_threshold_pct":      c.Rebalancing.DriftThresholdPct,
		"critical_drift_pct":       c.Rebalancing.CriticalDriftPct,
		"min_rebalance_amount_usd": c.Rebalancing.MinRebalanceAmountUSD,
		"max_rebalance_amount_usd": c.Rebalancing.MaxRebalanceAmountUSD,
		"check_interval_seconds":   c.Rebalancing.CheckIntervalSeconds,
		"alert_on_drift":           true,
		"target_allocations":       c.Rebalancing.TargetAllocations,
		"tracked_currencies":       c.Rebalancing.TrackedCurrencies,
	}
}

// ToRebalancerConfig converts rebalancing settings to inventory.RebalancerConfig.
func (c *Config) ToRebalancerConfig() map[string]interface{} {
	return map[string]interface{}{
		"enabled":                     c.Rebalancing.Enabled && c.Rebalancing.AutoBridge,
		"check_interval_seconds":      c.Rebalancing.CheckIntervalSeconds,
		"bridge_status_poll_seconds":  c.Rebalancing.BridgeStatusPollSeconds,
		"max_bridge_wait_minutes":     c.Rebalancing.MaxBridgeWaitMinutes,
		"min_time_between_rebalances": time.Duration(c.Rebalancing.MinTimeBetweenRebalances) * time.Second,
		"max_pending_bridges":         c.Rebalancing.MaxPendingBridges,
		"circuit_breaker_threshold":   c.Rebalancing.CircuitBreakerThreshold,
		"circuit_breaker_reset_time":  time.Duration(c.Rebalancing.CircuitBreakerResetMins) * time.Minute,
		"require_confirmation":        c.Rebalancing.RequireConfirmation,
	}
}

// GetEnabledExchanges returns only enabled exchanges.
func (c *Config) GetEnabledExchanges() []ExchangeSettings {
	var enabled []ExchangeSettings
	for _, ex := range c.Exchanges {
		if ex.Enabled {
			enabled = append(enabled, ex)
		}
	}
	return enabled
}

// GetEnabledPairs returns only enabled pairs.
func (c *Config) GetEnabledPairs() []PairSettings {
	var enabled []PairSettings
	for _, p := range c.Pairs {
		if p.Enabled {
			enabled = append(enabled, p)
		}
	}
	return enabled
}

// GetUpdateInterval returns the update interval as a duration.
func (c *Config) GetUpdateInterval() time.Duration {
	return time.Duration(c.UpdateIntervalMs) * time.Millisecond
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.UpdateIntervalMs < 1000 {
		return fmt.Errorf("update_interval_ms must be at least 1000 (1 second)")
	}

	if c.Arbitrage.MinSpreadBps < 0 {
		return fmt.Errorf("min_spread_bps cannot be negative")
	}

	if len(c.GetEnabledExchanges()) < 2 {
		return fmt.Errorf("at least 2 exchanges must be enabled for arbitrage detection")
	}

	if len(c.GetEnabledPairs()) == 0 {
		return fmt.Errorf("at least 1 pair must be enabled")
	}

	return nil
}

// Helper functions
func parseInt(s string) (int, error) {
	var v int
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}

func parseFloat(s string) (float64, error) {
	var v float64
	_, err := fmt.Sscanf(s, "%f", &v)
	return v, err
}
