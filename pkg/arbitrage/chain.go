// Package arbitrage provides chain arbitrage detection across multiple exchanges.
package arbitrage

import (
	"fmt"
	"math/big"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// ChainArbitrageDetector detects arbitrage opportunities across chains of exchanges.
type ChainArbitrageDetector struct {
	config   *types.ArbitrageConfig
	maxHops  int // Maximum number of hops in a chain (default: 3)
}

// NewChainArbitrageDetector creates a new chain arbitrage detector.
func NewChainArbitrageDetector(config *types.ArbitrageConfig, maxHops int) *ChainArbitrageDetector {
	if maxHops <= 0 {
		maxHops = 3 // Default to 3-hop chains
	}
	return &ChainArbitrageDetector{
		config:  config,
		maxHops: maxHops,
	}
}

// ExchangePrice represents a price from a specific exchange.
type ExchangePrice struct {
	Exchange  string
	BidPrice  *big.Float // Price to sell at (what buyers will pay)
	AskPrice  *big.Float // Price to buy at (what sellers want)
	BidSize   *big.Float
	AskSize   *big.Float
	FeeBps    int
	Timestamp time.Time
}

// GenerateAllPaths generates all possible paths through the given exchanges.
// For n exchanges, this generates all permutations of length 2 to maxHops.
func GenerateAllPaths(exchanges []string, maxHops int) []types.ChainPath {
	var paths []types.ChainPath

	// Generate paths of each length from 2 to maxHops
	for length := 2; length <= maxHops && length <= len(exchanges); length++ {
		perms := generatePermutations(exchanges, length)
		for _, perm := range perms {
			paths = append(paths, types.ChainPath{Exchanges: perm})
		}
	}

	return paths
}

// generatePermutations generates all permutations of the given slice with the specified length.
func generatePermutations(elements []string, length int) [][]string {
	if length == 0 {
		return [][]string{{}}
	}
	if length > len(elements) {
		return nil
	}

	var result [][]string

	// For each element, create permutations starting with that element
	for i, elem := range elements {
		// Create remaining elements (excluding current)
		remaining := make([]string, 0, len(elements)-1)
		remaining = append(remaining, elements[:i]...)
		remaining = append(remaining, elements[i+1:]...)

		// Get all permutations of remaining elements with length-1
		subPerms := generatePermutations(remaining, length-1)

		// Prepend current element to each sub-permutation
		for _, subPerm := range subPerms {
			perm := make([]string, 0, length)
			perm = append(perm, elem)
			perm = append(perm, subPerm...)
			result = append(result, perm)
		}
	}

	return result
}

// DetectChainOpportunities finds all profitable chain arbitrage opportunities.
// prices: map of exchange -> ExchangePrice
// pair: the trading pair being analyzed
func (d *ChainArbitrageDetector) DetectChainOpportunities(
	pair string,
	prices map[string]*ExchangePrice,
	tradeSize *big.Float,
) []*types.ChainArbitrageOpportunity {
	if len(prices) < 2 {
		return nil
	}

	// Get list of exchanges with valid prices
	var exchanges []string
	for exchange, price := range prices {
		if price != nil && price.BidPrice != nil && price.AskPrice != nil {
			exchanges = append(exchanges, exchange)
		}
	}

	if len(exchanges) < 2 {
		return nil
	}

	// Generate all possible paths
	paths := GenerateAllPaths(exchanges, d.maxHops)

	var opportunities []*types.ChainArbitrageOpportunity

	// Evaluate each path
	for _, path := range paths {
		opp := d.evaluatePath(pair, path, prices, tradeSize)
		if opp != nil && opp.IsValid {
			opportunities = append(opportunities, opp)
		}
	}

	return opportunities
}

// evaluatePath evaluates a single chain path for arbitrage opportunity.
// The strategy is: buy at first exchange, sell at last exchange.
// For multi-hop: buy at first, transfer through middle exchanges, sell at last.
func (d *ChainArbitrageDetector) evaluatePath(
	pair string,
	path types.ChainPath,
	prices map[string]*ExchangePrice,
	tradeSize *big.Float,
) *types.ChainArbitrageOpportunity {
	if len(path.Exchanges) < 2 {
		return nil
	}

	// For a simple chain arbitrage:
	// - Buy on the first exchange (use ask price)
	// - Sell on the last exchange (use bid price)
	// - Middle exchanges represent transfer hops (add transfer costs)

	firstExchange := path.Exchanges[0]
	lastExchange := path.Exchanges[len(path.Exchanges)-1]

	firstPrice := prices[firstExchange]
	lastPrice := prices[lastExchange]

	if firstPrice == nil || lastPrice == nil {
		return nil
	}

	// Calculate the chain
	// Start with trade size as the amount we want to end up with
	// Work backwards or forwards depending on strategy

	// Forward calculation: start with capital, see what we end up with
	currentAmount := new(big.Float).Set(tradeSize)
	var hops []types.ChainHop
	totalFeeBps := 0

	// Buy on first exchange
	buyPrice := firstPrice.AskPrice
	if buyPrice == nil || buyPrice.Sign() <= 0 {
		return nil
	}

	// Amount of asset we can buy = capital / ask price
	assetAmount := new(big.Float).Quo(currentAmount, buyPrice)

	// Apply trading fee
	buyFeeBps := firstPrice.FeeBps
	if buyFeeBps <= 0 {
		buyFeeBps = 10 // Default 0.1%
	}
	feeMultiplier := new(big.Float).SetFloat64(1.0 - float64(buyFeeBps)/10000.0)
	assetAmount = new(big.Float).Mul(assetAmount, feeMultiplier)
	totalFeeBps += buyFeeBps

	hops = append(hops, types.ChainHop{
		Exchange:  firstExchange,
		Action:    "buy",
		Pair:      pair,
		Price:     buyPrice,
		Size:      assetAmount,
		FeeBps:    buyFeeBps,
		Timestamp: firstPrice.Timestamp,
	})

	// For middle exchanges (if any), we model as transfer/bridge costs
	// In a real scenario, you might need to swap through these exchanges
	// For now, we add a small transfer fee for each hop
	for i := 1; i < len(path.Exchanges)-1; i++ {
		middleExchange := path.Exchanges[i]
		middlePrice := prices[middleExchange]
		if middlePrice == nil {
			return nil
		}

		// Model middle hop as a round-trip (buy then sell at same exchange)
		// This represents the cost of moving through that exchange
		transferFeeBps := 5 // 0.05% transfer cost per hop
		transferMultiplier := new(big.Float).SetFloat64(1.0 - float64(transferFeeBps)/10000.0)
		assetAmount = new(big.Float).Mul(assetAmount, transferMultiplier)
		totalFeeBps += transferFeeBps

		hops = append(hops, types.ChainHop{
			Exchange:  middleExchange,
			Action:    "transfer",
			Pair:      pair,
			Price:     middlePrice.BidPrice, // Reference price
			Size:      assetAmount,
			FeeBps:    transferFeeBps,
			Timestamp: middlePrice.Timestamp,
		})
	}

	// Sell on last exchange
	sellPrice := lastPrice.BidPrice
	if sellPrice == nil || sellPrice.Sign() <= 0 {
		return nil
	}

	// Amount we receive = asset amount * bid price
	endAmount := new(big.Float).Mul(assetAmount, sellPrice)

	// Apply trading fee
	sellFeeBps := lastPrice.FeeBps
	if sellFeeBps <= 0 {
		sellFeeBps = 10 // Default 0.1%
	}
	sellFeeMultiplier := new(big.Float).SetFloat64(1.0 - float64(sellFeeBps)/10000.0)
	endAmount = new(big.Float).Mul(endAmount, sellFeeMultiplier)
	totalFeeBps += sellFeeBps

	hops = append(hops, types.ChainHop{
		Exchange:  lastExchange,
		Action:    "sell",
		Pair:      pair,
		Price:     sellPrice,
		Size:      assetAmount,
		FeeBps:    sellFeeBps,
		Timestamp: lastPrice.Timestamp,
	})

	// Calculate profit
	grossProfit := new(big.Float).Sub(endAmount, tradeSize)

	// Calculate fees in absolute terms
	totalFees := new(big.Float).Mul(tradeSize, new(big.Float).SetFloat64(float64(totalFeeBps)/10000.0))

	// Net profit (already accounted for in endAmount, but calculate for display)
	netProfit := new(big.Float).Set(grossProfit)

	// Calculate spread
	spreadPercent, _ := new(big.Float).Quo(grossProfit, tradeSize).Float64()
	spreadPercent *= 100
	spreadBps := int(spreadPercent * 100)

	netProfitPercent, _ := new(big.Float).Quo(netProfit, tradeSize).Float64()
	netProfitBps := int(netProfitPercent * 10000)

	// Check if profitable
	isValid := netProfitBps >= d.config.MinNetProfitBps

	// Build chain string
	chain := make([]string, len(path.Exchanges))
	copy(chain, path.Exchanges)

	opp := &types.ChainArbitrageOpportunity{
		ID:            fmt.Sprintf("%s-chain-%s-%d", pair, path.String(), time.Now().UnixMilli()),
		Pair:          pair,
		Chain:         chain,
		Hops:          hops,
		StartExchange: firstExchange,
		EndExchange:   lastExchange,
		StartAmount:   tradeSize,
		EndAmount:     endAmount,
		GrossProfit:   grossProfit,
		TotalFees:     totalFees,
		NetProfit:     netProfit,
		NetProfitBps:  netProfitBps,
		SpreadPercent: spreadPercent,
		SpreadBps:     spreadBps,
		HopCount:      len(path.Exchanges),
		DetectedAt:    time.Now(),
		ExpiresAt:     time.Now().Add(5 * time.Second),
		IsValid:       isValid,
	}

	if !isValid {
		opp.InvalidationReasons = append(opp.InvalidationReasons,
			fmt.Sprintf("net profit %d bps below minimum %d bps", netProfitBps, d.config.MinNetProfitBps))
	}

	return opp
}

// FindBestChainOpportunity finds the most profitable chain opportunity.
func (d *ChainArbitrageDetector) FindBestChainOpportunity(
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
		if best == nil || opp.NetProfitBps > best.NetProfitBps {
			best = opp
		}
	}
	return best
}

// FilterByMinProfit filters opportunities by minimum profit threshold.
func FilterByMinProfit(opportunities []*types.ChainArbitrageOpportunity, minProfitBps int) []*types.ChainArbitrageOpportunity {
	var filtered []*types.ChainArbitrageOpportunity
	for _, opp := range opportunities {
		if opp.NetProfitBps >= minProfitBps {
			filtered = append(filtered, opp)
		}
	}
	return filtered
}

// SortByProfit sorts opportunities by net profit in descending order.
func SortByProfit(opportunities []*types.ChainArbitrageOpportunity) {
	// Simple bubble sort (fine for small lists)
	n := len(opportunities)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if opportunities[j].NetProfitBps < opportunities[j+1].NetProfitBps {
				opportunities[j], opportunities[j+1] = opportunities[j+1], opportunities[j]
			}
		}
	}
}
