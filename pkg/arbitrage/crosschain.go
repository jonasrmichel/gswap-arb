// Package arbitrage provides cross-chain arbitrage detection.
package arbitrage

import (
	"fmt"
	"math/big"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// CrossChainArbitrageDetector detects arbitrage opportunities that span the GalaChain/Ethereum bridge.
type CrossChainArbitrageDetector struct {
	config          *CrossChainConfig
	volatilityModel *VolatilityModel
}

// CrossChainConfig holds configuration for cross-chain arbitrage detection.
type CrossChainConfig struct {
	// Thresholds
	MinSpreadPercent         float64 `json:"min_spread_percent"`
	MinRiskAdjustedProfitBps int     `json:"min_risk_adjusted_profit_bps"`

	// Bridge settings
	MaxBridgeTimeMinutes int `json:"max_bridge_time_minutes"`
	BridgeTimeToEthMin   int `json:"bridge_time_to_eth_min"`
	BridgeTimeToGalaMin  int `json:"bridge_time_to_gala_min"`

	// Execution
	ExecutionStrategy string   `json:"execution_strategy"`
	AllowedTokens     []string `json:"allowed_tokens"`
}

// DefaultCrossChainConfig returns sensible defaults.
func DefaultCrossChainConfig() *CrossChainConfig {
	return &CrossChainConfig{
		MinSpreadPercent:         3.0,
		MinRiskAdjustedProfitBps: 100,
		MaxBridgeTimeMinutes:     30,
		BridgeTimeToEthMin:       15,
		BridgeTimeToGalaMin:      15,
		ExecutionStrategy:        "staged",
		AllowedTokens:            []string{"GALA", "GUSDT", "GUSDC"},
	}
}

// NewCrossChainArbitrageDetector creates a new cross-chain detector.
func NewCrossChainArbitrageDetector(config *CrossChainConfig, volatilityModel *VolatilityModel) *CrossChainArbitrageDetector {
	if config == nil {
		config = DefaultCrossChainConfig()
	}

	if volatilityModel == nil {
		volatilityModel = NewVolatilityModel(nil)
	}

	return &CrossChainArbitrageDetector{
		config:          config,
		volatilityModel: volatilityModel,
	}
}

// GetVolatilityModel returns the volatility model for external updates.
func (d *CrossChainArbitrageDetector) GetVolatilityModel() *VolatilityModel {
	return d.volatilityModel
}

// DetectCrossChainOpportunities finds profitable cross-chain arbitrage opportunities.
// This looks for price differences between GSwap (GalaChain) and CEXs (Ethereum side)
// that are large enough to cover bridge costs and volatility risk.
func (d *CrossChainArbitrageDetector) DetectCrossChainOpportunities(
	pair string,
	prices map[string]*ExchangePrice,
	tradeSize *big.Float,
) []*types.ChainArbitrageOpportunity {
	if len(prices) < 2 {
		return nil
	}

	// Separate exchanges by chain category
	var galaChainExchanges, ethereumExchanges []string
	for exchange := range prices {
		if GetExchangeCategory(exchange) == ExchangeCategoryGalaChain {
			galaChainExchanges = append(galaChainExchanges, exchange)
		} else {
			ethereumExchanges = append(ethereumExchanges, exchange)
		}
	}

	// Need at least one exchange on each side
	if len(galaChainExchanges) == 0 || len(ethereumExchanges) == 0 {
		return nil
	}

	var opportunities []*types.ChainArbitrageOpportunity

	// Check all cross-chain pairs (GalaChain <-> Ethereum)
	for _, galaEx := range galaChainExchanges {
		for _, ethEx := range ethereumExchanges {
			galaPrice := prices[galaEx]
			ethPrice := prices[ethEx]

			if galaPrice == nil || ethPrice == nil {
				continue
			}

			// Direction 1: Buy on GalaChain, bridge to Ethereum, sell on CEX
			opp1 := d.evaluateCrossChainPath(pair, galaEx, ethEx, galaPrice, ethPrice, tradeSize, "to_ethereum")
			if opp1 != nil && opp1.IsValid {
				opportunities = append(opportunities, opp1)
			}

			// Direction 2: Buy on CEX, bridge to GalaChain, sell on GSwap
			opp2 := d.evaluateCrossChainPath(pair, ethEx, galaEx, ethPrice, galaPrice, tradeSize, "to_galachain")
			if opp2 != nil && opp2.IsValid {
				opportunities = append(opportunities, opp2)
			}
		}
	}

	return opportunities
}

// evaluateCrossChainPath evaluates a single cross-chain arbitrage path.
func (d *CrossChainArbitrageDetector) evaluateCrossChainPath(
	pair string,
	buyExchange string,
	sellExchange string,
	buyPrice *ExchangePrice,
	sellPrice *ExchangePrice,
	tradeSize *big.Float,
	bridgeDirection string,
) *types.ChainArbitrageOpportunity {
	// Validate prices
	if buyPrice.AskPrice == nil || buyPrice.AskPrice.Sign() <= 0 {
		return nil
	}
	if sellPrice.BidPrice == nil || sellPrice.BidPrice.Sign() <= 0 {
		return nil
	}

	// Calculate gross spread
	spread := new(big.Float).Sub(sellPrice.BidPrice, buyPrice.AskPrice)
	spreadPercent, _ := new(big.Float).Quo(spread, buyPrice.AskPrice).Float64()
	spreadPercent *= 100

	// Quick check: skip if spread is below minimum
	if spreadPercent < d.config.MinSpreadPercent {
		return nil
	}

	// Get token from pair (e.g., "GALA" from "GALA/USDT")
	token := extractBaseToken(pair)
	if !d.isTokenAllowed(token) {
		return nil
	}

	// Calculate amounts through the path
	currentAmount := new(big.Float).Set(tradeSize)
	var hops []types.ChainHop
	totalFeeBps := 0

	// Step 1: Buy on first exchange
	buyFeeBps := buyPrice.FeeBps
	if buyFeeBps <= 0 {
		buyFeeBps = 10
	}

	assetAmount := new(big.Float).Quo(currentAmount, buyPrice.AskPrice)
	feeMultiplier := new(big.Float).SetFloat64(1.0 - float64(buyFeeBps)/10000.0)
	assetAmount = new(big.Float).Mul(assetAmount, feeMultiplier)
	totalFeeBps += buyFeeBps

	hops = append(hops, types.ChainHop{
		Exchange:  buyExchange,
		Action:    "buy",
		Pair:      pair,
		Price:     buyPrice.AskPrice,
		Size:      assetAmount,
		FeeBps:    buyFeeBps,
		Timestamp: buyPrice.Timestamp,
	})

	// Step 2: Bridge
	bridgeTimeMin := d.config.BridgeTimeToEthMin
	if bridgeDirection == "to_galachain" {
		bridgeTimeMin = d.config.BridgeTimeToGalaMin
	}

	bridgeCostBps := d.volatilityModel.GetBridgeCost(token, bridgeDirection)
	volatilityRiskBps := d.volatilityModel.EstimateBridgeRisk(pair, bridgeTimeMin)

	// Apply bridge cost
	bridgeFeeMultiplier := new(big.Float).SetFloat64(1.0 - float64(bridgeCostBps)/10000.0)
	assetAmount = new(big.Float).Mul(assetAmount, bridgeFeeMultiplier)
	totalFeeBps += bridgeCostBps

	hops = append(hops, types.ChainHop{
		Exchange:          "bridge",
		Action:            "bridge",
		Pair:              pair,
		Price:             buyPrice.AskPrice, // Reference price at bridge time
		Size:              assetAmount,
		FeeBps:            bridgeCostBps,
		Timestamp:         time.Now(),
		BridgeDirection:   bridgeDirection,
		BridgeToken:       token,
		BridgeCostBps:     bridgeCostBps,
		BridgeTimeMinutes: bridgeTimeMin,
		VolatilityRiskBps: volatilityRiskBps,
	})

	// Step 3: Sell on second exchange
	sellFeeBps := sellPrice.FeeBps
	if sellFeeBps <= 0 {
		sellFeeBps = 10
	}

	endAmount := new(big.Float).Mul(assetAmount, sellPrice.BidPrice)
	sellFeeMultiplier := new(big.Float).SetFloat64(1.0 - float64(sellFeeBps)/10000.0)
	endAmount = new(big.Float).Mul(endAmount, sellFeeMultiplier)
	totalFeeBps += sellFeeBps

	hops = append(hops, types.ChainHop{
		Exchange:  sellExchange,
		Action:    "sell",
		Pair:      pair,
		Price:     sellPrice.BidPrice,
		Size:      assetAmount,
		FeeBps:    sellFeeBps,
		Timestamp: sellPrice.Timestamp,
	})

	// Calculate profits
	grossProfit := new(big.Float).Sub(endAmount, tradeSize)
	grossProfitBps := int(spreadPercent * 100)

	// Calculate risk-adjusted profit
	riskAdjustedProfitBps, _ := d.volatilityModel.CalculateRiskAdjustedProfit(
		pair, grossProfitBps, bridgeCostBps, bridgeTimeMin,
	)

	netProfitPercent, _ := new(big.Float).Quo(grossProfit, tradeSize).Float64()
	netProfitBps := int(netProfitPercent * 10000)

	totalFees := new(big.Float).Mul(tradeSize, new(big.Float).SetFloat64(float64(totalFeeBps)/10000.0))

	// Determine validity
	isValid := riskAdjustedProfitBps >= d.config.MinRiskAdjustedProfitBps
	var invalidReasons []string

	if !isValid {
		invalidReasons = append(invalidReasons,
			fmt.Sprintf("risk-adjusted profit %d bps below minimum %d bps",
				riskAdjustedProfitBps, d.config.MinRiskAdjustedProfitBps))
	}

	if bridgeTimeMin > d.config.MaxBridgeTimeMinutes {
		isValid = false
		invalidReasons = append(invalidReasons,
			fmt.Sprintf("bridge time %d min exceeds maximum %d min",
				bridgeTimeMin, d.config.MaxBridgeTimeMinutes))
	}

	// Build opportunity
	opp := &types.ChainArbitrageOpportunity{
		ID:                  fmt.Sprintf("%s-crosschain-%s-%s-%d", pair, buyExchange, sellExchange, time.Now().UnixMilli()),
		Pair:                pair,
		Chain:               []string{buyExchange, "bridge", sellExchange},
		Hops:                hops,
		StartExchange:       buyExchange,
		EndExchange:         sellExchange,
		StartAmount:         tradeSize,
		EndAmount:           endAmount,
		GrossProfit:         grossProfit,
		TotalFees:           totalFees,
		NetProfit:           grossProfit,
		NetProfitBps:        netProfitBps,
		SpreadPercent:       spreadPercent,
		SpreadBps:           int(spreadPercent * 100),
		HopCount:            3,
		IsCrossChain:        true,
		BridgeCount:         1,
		TotalBridgeCostBps:  bridgeCostBps,
		TotalBridgeTimeMin:  bridgeTimeMin,
		VolatilityRiskBps:   volatilityRiskBps,
		RiskAdjustedProfit:  riskAdjustedProfitBps,
		ExecutionStrategy:   d.config.ExecutionStrategy,
		DetectedAt:          time.Now(),
		ExpiresAt:           time.Now().Add(30 * time.Second), // Cross-chain ops need longer validity
		IsValid:             isValid,
		InvalidationReasons: invalidReasons,
	}

	return opp
}

// isTokenAllowed checks if the token is in the allowed list.
func (d *CrossChainArbitrageDetector) isTokenAllowed(token string) bool {
	if len(d.config.AllowedTokens) == 0 {
		return true
	}

	for _, allowed := range d.config.AllowedTokens {
		if allowed == token || allowed == mapTokenToBridgeToken(token) {
			return true
		}
	}
	return false
}

// extractBaseToken extracts the base token from a pair (e.g., "GALA" from "GALA/USDT").
func extractBaseToken(pair string) string {
	for i, c := range pair {
		if c == '/' || c == '-' {
			return pair[:i]
		}
	}
	return pair
}

// mapTokenToBridgeToken maps exchange tokens to bridge token symbols.
func mapTokenToBridgeToken(token string) string {
	switch token {
	case "GALA":
		return "GALA"
	case "ETH", "WETH":
		return "GWETH"
	case "USDT":
		return "GUSDT"
	case "USDC":
		return "GUSDC"
	case "BTC", "WBTC":
		return "GWBTC"
	default:
		return token
	}
}

// FindBestCrossChainOpportunity finds the most profitable cross-chain opportunity.
func (d *CrossChainArbitrageDetector) FindBestCrossChainOpportunity(
	opportunities []*types.ChainArbitrageOpportunity,
) *types.ChainArbitrageOpportunity {
	if len(opportunities) == 0 {
		return nil
	}

	var best *types.ChainArbitrageOpportunity
	for _, opp := range opportunities {
		if !opp.IsValid {
			continue
		}
		// For cross-chain, prioritize risk-adjusted profit
		if best == nil || opp.RiskAdjustedProfit > best.RiskAdjustedProfit {
			best = opp
		}
	}
	return best
}

// CrossChainOpportunityReport represents a summary for display.
type CrossChainOpportunityReport struct {
	Pair              string
	BuyExchange       string
	SellExchange      string
	BridgeDirection   string
	SpreadPercent     float64
	GrossProfitBps    int
	BridgeCostBps     int
	VolatilityRiskBps int
	RiskAdjustedBps   int
	BridgeTimeMin     int
	ExecutionStrategy string
	IsRecommended     bool
	Reason            string
}

// FormatOpportunityReport generates a human-readable report for an opportunity.
func FormatOpportunityReport(opp *types.ChainArbitrageOpportunity) *CrossChainOpportunityReport {
	if opp == nil {
		return nil
	}

	direction := ""
	for _, hop := range opp.Hops {
		if hop.IsBridge() {
			direction = hop.BridgeDirection
			break
		}
	}

	report := &CrossChainOpportunityReport{
		Pair:              opp.Pair,
		BuyExchange:       opp.StartExchange,
		SellExchange:      opp.EndExchange,
		BridgeDirection:   direction,
		SpreadPercent:     opp.SpreadPercent,
		GrossProfitBps:    opp.SpreadBps,
		BridgeCostBps:     opp.TotalBridgeCostBps,
		VolatilityRiskBps: opp.VolatilityRiskBps,
		RiskAdjustedBps:   opp.RiskAdjustedProfit,
		BridgeTimeMin:     opp.TotalBridgeTimeMin,
		ExecutionStrategy: opp.ExecutionStrategy,
		IsRecommended:     opp.IsValid,
	}

	if opp.IsValid {
		report.Reason = "Profitable after risk adjustment"
	} else if len(opp.InvalidationReasons) > 0 {
		report.Reason = opp.InvalidationReasons[0]
	}

	return report
}

// FormatReportString generates a string representation of the opportunity report.
func (r *CrossChainOpportunityReport) FormatReportString() string {
	status := "❌ NOT RECOMMENDED"
	if r.IsRecommended {
		status = "✅ RECOMMENDED"
	}

	return fmt.Sprintf(`
Cross-Chain Arbitrage Opportunity
==================================
Pair: %s
Direction: Buy %s → Bridge (%s) → Sell %s

Spread: %.2f%% (%d bps)
Bridge Cost: %d bps
Volatility Risk: %d bps (based on %d min bridge time)
Risk-Adjusted Profit: %d bps

Execution Strategy: %s
Status: %s
Reason: %s
`,
		r.Pair,
		r.BuyExchange, r.BridgeDirection, r.SellExchange,
		r.SpreadPercent, r.GrossProfitBps,
		r.BridgeCostBps,
		r.VolatilityRiskBps, r.BridgeTimeMin,
		r.RiskAdjustedBps,
		r.ExecutionStrategy,
		status,
		r.Reason,
	)
}
