// Package arbitrage provides cross-chain arbitrage detection with volatility modeling.
package arbitrage

import (
	"math"
	"sync"
	"time"
)

// VolatilityModel tracks price volatility for risk assessment during bridge delays.
type VolatilityModel struct {
	// Price history for each pair
	priceHistory map[string]*PriceHistory
	mu           sync.RWMutex

	// Configuration
	config *VolatilityConfig
}

// VolatilityConfig holds configuration for volatility modeling.
type VolatilityConfig struct {
	// Window size for volatility calculation
	WindowMinutes int `json:"window_minutes"` // Default: 60 (1 hour)

	// Minimum samples required for reliable volatility estimate
	MinSamples int `json:"min_samples"` // Default: 10

	// Default volatility to use when insufficient data
	DefaultVolatilityBps int `json:"default_volatility_bps"` // Default: 200 (2%)

	// Confidence multiplier for risk calculation (e.g., 2.0 for ~95% confidence)
	ConfidenceMultiplier float64 `json:"confidence_multiplier"` // Default: 2.0

	// Bridge time estimates by direction
	BridgeTimeToEthMin   int `json:"bridge_time_to_eth_min"`   // Default: 15
	BridgeTimeToGalaMin  int `json:"bridge_time_to_gala_min"`  // Default: 15
}

// DefaultVolatilityConfig returns sensible defaults.
func DefaultVolatilityConfig() *VolatilityConfig {
	return &VolatilityConfig{
		WindowMinutes:        60,
		MinSamples:           10,
		DefaultVolatilityBps: 200, // 2% default volatility
		ConfidenceMultiplier: 2.0, // ~95% confidence
		BridgeTimeToEthMin:   15,
		BridgeTimeToGalaMin:  15,
	}
}

// PriceHistory tracks historical prices for volatility calculation.
type PriceHistory struct {
	Pair      string
	Prices    []PricePoint
	mu        sync.RWMutex
	maxPoints int
}

// PricePoint represents a single price observation.
type PricePoint struct {
	Price     float64
	Timestamp time.Time
}

// NewVolatilityModel creates a new volatility model.
func NewVolatilityModel(config *VolatilityConfig) *VolatilityModel {
	if config == nil {
		config = DefaultVolatilityConfig()
	}

	return &VolatilityModel{
		priceHistory: make(map[string]*PriceHistory),
		config:       config,
	}
}

// RecordPrice records a price observation for a pair.
func (v *VolatilityModel) RecordPrice(pair string, price float64) {
	v.mu.Lock()
	defer v.mu.Unlock()

	history, ok := v.priceHistory[pair]
	if !ok {
		// Calculate max points based on window and expected frequency
		// Assume prices come in every ~15 seconds
		maxPoints := v.config.WindowMinutes * 4
		if maxPoints < 100 {
			maxPoints = 100
		}

		history = &PriceHistory{
			Pair:      pair,
			Prices:    make([]PricePoint, 0, maxPoints),
			maxPoints: maxPoints,
		}
		v.priceHistory[pair] = history
	}

	history.mu.Lock()
	defer history.mu.Unlock()

	// Add new price point
	history.Prices = append(history.Prices, PricePoint{
		Price:     price,
		Timestamp: time.Now(),
	})

	// Remove old points outside the window
	cutoff := time.Now().Add(-time.Duration(v.config.WindowMinutes) * time.Minute)
	i := 0
	for ; i < len(history.Prices); i++ {
		if history.Prices[i].Timestamp.After(cutoff) {
			break
		}
	}
	if i > 0 {
		history.Prices = history.Prices[i:]
	}

	// Trim to max points if needed
	if len(history.Prices) > history.maxPoints {
		history.Prices = history.Prices[len(history.Prices)-history.maxPoints:]
	}
}

// GetVolatility returns the annualized volatility for a pair in basis points.
func (v *VolatilityModel) GetVolatility(pair string) int {
	v.mu.RLock()
	history, ok := v.priceHistory[pair]
	v.mu.RUnlock()

	if !ok || history == nil {
		return v.config.DefaultVolatilityBps
	}

	history.mu.RLock()
	defer history.mu.RUnlock()

	if len(history.Prices) < v.config.MinSamples {
		return v.config.DefaultVolatilityBps
	}

	// Calculate returns
	returns := make([]float64, len(history.Prices)-1)
	for i := 1; i < len(history.Prices); i++ {
		if history.Prices[i-1].Price > 0 {
			returns[i-1] = (history.Prices[i].Price - history.Prices[i-1].Price) / history.Prices[i-1].Price
		}
	}

	if len(returns) == 0 {
		return v.config.DefaultVolatilityBps
	}

	// Calculate standard deviation of returns
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		diff := r - mean
		variance += diff * diff
	}
	variance /= float64(len(returns))

	stdDev := math.Sqrt(variance)

	// Convert to basis points
	volatilityBps := int(stdDev * 10000)

	// Apply minimum floor
	if volatilityBps < 10 {
		volatilityBps = 10
	}

	return volatilityBps
}

// EstimateBridgeRisk estimates the price risk during bridge time in basis points.
// This accounts for potential price movement during the bridge delay.
func (v *VolatilityModel) EstimateBridgeRisk(pair string, bridgeTimeMinutes int) int {
	// Get current volatility (per observation interval, roughly)
	volatilityBps := v.GetVolatility(pair)

	// Scale volatility by time
	// Volatility scales with sqrt(time)
	// Assume our observed volatility is per ~15 seconds
	observationIntervalMin := 0.25 // 15 seconds
	timeScaleFactor := math.Sqrt(float64(bridgeTimeMinutes) / observationIntervalMin)

	// Calculate scaled volatility
	scaledVolatility := float64(volatilityBps) * timeScaleFactor

	// Apply confidence multiplier for risk buffer
	riskBps := int(scaledVolatility * v.config.ConfidenceMultiplier)

	// Cap at reasonable maximum
	if riskBps > 2000 { // 20% max risk
		riskBps = 2000
	}

	return riskBps
}

// GetBridgeCost returns the estimated bridge cost in basis points.
// This includes gas fees, bridge fees, and any slippage.
func (v *VolatilityModel) GetBridgeCost(token string, direction string) int {
	// Bridge costs vary by token and direction
	// These are estimates based on typical GalaConnect bridge costs

	// Base cost for the bridge transaction (gas + fees)
	baseCostBps := 50 // 0.5% base cost

	// Token-specific adjustments
	switch token {
	case "GALA":
		// GALA uses permit, slightly cheaper
		baseCostBps = 40
	case "GWETH", "ETH":
		// ETH transactions have higher gas
		baseCostBps = 75
	case "GUSDC", "GUSDT", "USDC", "USDT":
		// Stablecoins are standard cost
		baseCostBps = 50
	}

	// Direction adjustment (Ethereum gas can vary)
	if direction == "to_ethereum" {
		// No additional cost - just GalaChain transaction
	} else if direction == "to_galachain" {
		// Ethereum gas cost
		baseCostBps += 25 // Additional ETH gas cost
	}

	return baseCostBps
}

// GetBridgeTime returns the estimated bridge time in minutes.
func (v *VolatilityModel) GetBridgeTime(direction string) int {
	if direction == "to_ethereum" {
		return v.config.BridgeTimeToEthMin
	}
	return v.config.BridgeTimeToGalaMin
}

// CalculateRiskAdjustedProfit calculates profit minus volatility risk.
func (v *VolatilityModel) CalculateRiskAdjustedProfit(
	pair string,
	grossProfitBps int,
	bridgeCostBps int,
	bridgeTimeMinutes int,
) (riskAdjustedProfitBps int, volatilityRiskBps int) {
	volatilityRiskBps = v.EstimateBridgeRisk(pair, bridgeTimeMinutes)

	// Risk-adjusted profit = gross profit - bridge cost - volatility risk
	riskAdjustedProfitBps = grossProfitBps - bridgeCostBps - volatilityRiskBps

	return riskAdjustedProfitBps, volatilityRiskBps
}

// ShouldExecuteCrossChain determines if a cross-chain opportunity is worth executing.
func (v *VolatilityModel) ShouldExecuteCrossChain(
	pair string,
	grossProfitBps int,
	bridgeCostBps int,
	bridgeTimeMinutes int,
	minProfitBps int,
) (shouldExecute bool, reason string) {
	riskAdjustedProfit, volatilityRisk := v.CalculateRiskAdjustedProfit(
		pair, grossProfitBps, bridgeCostBps, bridgeTimeMinutes,
	)

	// Check if risk-adjusted profit meets minimum
	if riskAdjustedProfit < minProfitBps {
		return false, "risk-adjusted profit below minimum"
	}

	// Check if volatility risk is not too high relative to profit
	// We want profit to be at least 2x the volatility risk
	if volatilityRisk > grossProfitBps/2 {
		return false, "volatility risk too high relative to profit"
	}

	return true, ""
}

// GetStats returns volatility statistics for all tracked pairs.
func (v *VolatilityModel) GetStats() map[string]VolatilityStats {
	v.mu.RLock()
	defer v.mu.RUnlock()

	stats := make(map[string]VolatilityStats)
	for pair, history := range v.priceHistory {
		history.mu.RLock()
		sampleCount := len(history.Prices)
		var lastPrice float64
		var lastUpdate time.Time
		if sampleCount > 0 {
			lastPrice = history.Prices[sampleCount-1].Price
			lastUpdate = history.Prices[sampleCount-1].Timestamp
		}
		history.mu.RUnlock()

		stats[pair] = VolatilityStats{
			Pair:          pair,
			VolatilityBps: v.GetVolatility(pair),
			SampleCount:   sampleCount,
			LastPrice:     lastPrice,
			LastUpdate:    lastUpdate,
		}
	}

	return stats
}

// VolatilityStats holds volatility statistics for a pair.
type VolatilityStats struct {
	Pair          string    `json:"pair"`
	VolatilityBps int       `json:"volatility_bps"`
	SampleCount   int       `json:"sample_count"`
	LastPrice     float64   `json:"last_price"`
	LastUpdate    time.Time `json:"last_update"`
}

// ExchangeCategory categorizes exchanges for bridging purposes.
type ExchangeCategory string

const (
	ExchangeCategoryGalaChain ExchangeCategory = "galachain" // GSwap, GalaChain DEX
	ExchangeCategoryEthereum  ExchangeCategory = "ethereum"  // CEXs, Ethereum DEXs
)

// GetExchangeCategory returns the category for an exchange.
func GetExchangeCategory(exchange string) ExchangeCategory {
	switch exchange {
	case "gswap":
		return ExchangeCategoryGalaChain
	default:
		// All CEXs operate on Ethereum side (withdrawals/deposits)
		return ExchangeCategoryEthereum
	}
}

// NeedsBridge returns true if moving between two exchanges requires a bridge.
func NeedsBridge(fromExchange, toExchange string) bool {
	return GetExchangeCategory(fromExchange) != GetExchangeCategory(toExchange)
}

// GetBridgeDirection returns the bridge direction needed between exchanges.
func GetBridgeDirection(fromExchange, toExchange string) string {
	fromCat := GetExchangeCategory(fromExchange)
	toCat := GetExchangeCategory(toExchange)

	if fromCat == ExchangeCategoryGalaChain && toCat == ExchangeCategoryEthereum {
		return "to_ethereum"
	} else if fromCat == ExchangeCategoryEthereum && toCat == ExchangeCategoryGalaChain {
		return "to_galachain"
	}
	return "" // No bridge needed
}
