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

	// Exchange settings
	Exchanges []ExchangeSettings `json:"exchanges"`

	// Token settings
	Tokens []TokenSettings `json:"tokens"`

	// Pair settings
	Pairs []PairSettings `json:"pairs"`
}

// ArbitrageSettings holds arbitrage-specific configuration.
type ArbitrageSettings struct {
	MinSpreadBps      int     `json:"min_spread_bps"`
	MinNetProfitBps   int     `json:"min_net_profit_bps"`
	MaxPriceImpactBps int     `json:"max_price_impact_bps"`
	DefaultTradeSize  float64 `json:"default_trade_size"`
	QuoteValiditySecs int     `json:"quote_validity_secs"`
}

// ExchangeSettings holds configuration for a single exchange.
type ExchangeSettings struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"` // "dex" or "cex"
	Enabled  bool   `json:"enabled"`
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	Secret   string `json:"secret,omitempty"`
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
			MinSpreadBps:      50,    // 0.5%
			MinNetProfitBps:   20,    // 0.2%
			MaxPriceImpactBps: 500,   // 5%
			DefaultTradeSize:  1000,  // $1000 equivalent
			QuoteValiditySecs: 30,
		},

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
			{Symbol: "BTC", Name: "Bitcoin", Decimals: 8},
			{Symbol: "SOL", Name: "Solana", Decimals: 9},
		},

		Pairs: []PairSettings{
			{Pair: "GALA/USDT", Exchanges: []string{"gswap", "binance", "coinbase", "kraken"}, Enabled: true},
			{Pair: "ETH/USDT", Exchanges: []string{"gswap", "binance", "coinbase", "kraken"}, Enabled: true},
			{Pair: "BTC/USDT", Exchanges: []string{"binance", "coinbase", "kraken"}, Enabled: true},
			{Pair: "SOL/USDT", Exchanges: []string{"binance", "coinbase", "kraken"}, Enabled: true},
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

	// Exchange API keys from environment
	for i := range c.Exchanges {
		envPrefix := strings.ToUpper(c.Exchanges[i].ID)
		if apiKey := os.Getenv(envPrefix + "_API_KEY"); apiKey != "" {
			c.Exchanges[i].APIKey = apiKey
		}
		if secret := os.Getenv(envPrefix + "_SECRET"); secret != "" {
			c.Exchanges[i].Secret = secret
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
