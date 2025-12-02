// Package arbitrage provides arbitrage opportunity detection and calculation.
package arbitrage

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/providers"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// Detector detects arbitrage opportunities across multiple exchanges.
type Detector struct {
	registry *providers.ProviderRegistry
	config   *types.ArbitrageConfig

	// Tracked pairs (common pairs across exchanges)
	trackedPairs map[string][]string // pair -> []exchangeNames
	mu           sync.RWMutex
}

// NewDetector creates a new arbitrage detector.
func NewDetector(registry *providers.ProviderRegistry, config *types.ArbitrageConfig) *Detector {
	if config == nil {
		config = DefaultConfig()
	}
	return &Detector{
		registry:     registry,
		config:       config,
		trackedPairs: make(map[string][]string),
	}
}

// DefaultConfig returns default arbitrage configuration.
func DefaultConfig() *types.ArbitrageConfig {
	return &types.ArbitrageConfig{
		MinSpreadBps:      50,  // 0.5% minimum spread
		MinNetProfitBps:   20,  // 0.2% minimum net profit after fees
		MaxPriceImpactBps: 500, // 5% max price impact
		DefaultTradeSize:  big.NewFloat(1000),
		QuoteValiditySecs: 30,
	}
}

// TrackPair adds a pair to track across specified exchanges.
func (d *Detector) TrackPair(pair string, exchanges []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trackedPairs[pair] = exchanges
}

// GetTrackedPairs returns all tracked pairs.
func (d *Detector) GetTrackedPairs() map[string][]string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make(map[string][]string)
	for k, v := range d.trackedPairs {
		result[k] = append([]string{}, v...)
	}
	return result
}

// DetectOpportunities scans all tracked pairs for arbitrage opportunities.
func (d *Detector) DetectOpportunities(ctx context.Context) ([]*types.ArbitrageOpportunity, error) {
	d.mu.RLock()
	pairs := make(map[string][]string)
	for k, v := range d.trackedPairs {
		pairs[k] = v
	}
	d.mu.RUnlock()

	var allOpportunities []*types.ArbitrageOpportunity
	var mu sync.Mutex
	var wg sync.WaitGroup

	for pair, exchanges := range pairs {
		wg.Add(1)
		go func(pair string, exchanges []string) {
			defer wg.Done()

			opps, err := d.detectForPair(ctx, pair, exchanges)
			if err != nil {
				return // Log error in production
			}

			mu.Lock()
			allOpportunities = append(allOpportunities, opps...)
			mu.Unlock()
		}(pair, exchanges)
	}

	wg.Wait()

	// Sort by spread (highest first)
	sort.Slice(allOpportunities, func(i, j int) bool {
		return allOpportunities[i].SpreadBps > allOpportunities[j].SpreadBps
	})

	return allOpportunities, nil
}

// detectForPair detects arbitrage opportunities for a single pair.
func (d *Detector) detectForPair(ctx context.Context, pair string, exchanges []string) ([]*types.ArbitrageOpportunity, error) {
	// Fetch quotes from all exchanges concurrently
	quotes := make(map[string]*types.PriceQuote)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, exchangeName := range exchanges {
		provider, ok := d.registry.Get(exchangeName)
		if !ok {
			continue
		}

		wg.Add(1)
		go func(name string, p providers.PriceProvider) {
			defer wg.Done()

			quote, err := p.GetQuote(ctx, pair)
			if err != nil {
				return // Log in production
			}

			mu.Lock()
			quotes[name] = quote
			mu.Unlock()
		}(exchangeName, provider)
	}

	wg.Wait()

	if len(quotes) < 2 {
		return nil, nil // Need at least 2 exchanges
	}

	// Compare all pairs of exchanges
	var opportunities []*types.ArbitrageOpportunity

	exchangeList := make([]string, 0, len(quotes))
	for name := range quotes {
		exchangeList = append(exchangeList, name)
	}

	for i := 0; i < len(exchangeList); i++ {
		for j := i + 1; j < len(exchangeList); j++ {
			exA := exchangeList[i]
			exB := exchangeList[j]
			quoteA := quotes[exA]
			quoteB := quotes[exB]

			opp := d.calculateOpportunity(pair, exA, exB, quoteA, quoteB)
			if opp != nil && opp.IsValid {
				opportunities = append(opportunities, opp)
			}
		}
	}

	return opportunities, nil
}

// calculateOpportunity calculates arbitrage opportunity between two exchanges.
func (d *Detector) calculateOpportunity(pair, exA, exB string, quoteA, quoteB *types.PriceQuote) *types.ArbitrageOpportunity {
	// Determine buy/sell exchanges based on prices
	// Buy where ask is lower, sell where bid is higher
	var buyEx, sellEx string
	var buyQuote, sellQuote *types.PriceQuote

	// Compare ask prices (buy side) and bid prices (sell side)
	askA := quoteA.AskPrice
	askB := quoteB.AskPrice
	bidA := quoteA.BidPrice
	bidB := quoteB.BidPrice

	// Check both directions
	// Direction 1: Buy on A (at askA), sell on B (at bidB)
	spreadAB := new(big.Float).Sub(bidB, askA)

	// Direction 2: Buy on B (at askB), sell on A (at bidA)
	spreadBA := new(big.Float).Sub(bidA, askB)

	// Choose the more profitable direction
	cmp := spreadAB.Cmp(spreadBA)
	if cmp >= 0 {
		buyEx = exA
		sellEx = exB
		buyQuote = quoteA
		sellQuote = quoteB
	} else {
		buyEx = exB
		sellEx = exA
		buyQuote = quoteB
		sellQuote = quoteA
		spreadAB = spreadBA
	}

	// Calculate spread percentage
	spreadPercent, _ := new(big.Float).Quo(spreadAB, buyQuote.AskPrice).Float64()
	spreadPercent *= 100
	spreadBps := int(spreadPercent * 100)

	// Check minimum spread threshold
	if spreadBps < d.config.MinSpreadBps {
		return nil
	}

	// Calculate fees
	buyFees := d.getProviderFees(buyEx)
	sellFees := d.getProviderFees(sellEx)
	totalFeeBps := buyFees.TakerFeeBps + sellFees.TakerFeeBps

	// Calculate net profit
	netProfitBps := spreadBps - totalFeeBps

	// Calculate estimated profit in quote currency
	tradeSize := d.config.DefaultTradeSize
	grossProfit := new(big.Float).Mul(spreadAB, tradeSize)

	// Estimate total fees in quote currency
	feeRate := new(big.Float).SetFloat64(float64(totalFeeBps) / 10000)
	tradeCost := new(big.Float).Mul(buyQuote.AskPrice, tradeSize)
	estimatedFees := new(big.Float).Mul(tradeCost, feeRate)

	netProfit := new(big.Float).Sub(grossProfit, estimatedFees)

	// Determine direction
	var direction types.ArbitrageDirection
	if buyEx == exA {
		direction = types.BuyOnAExchangeSellOnB
	} else {
		direction = types.BuyOnBExchangeSellOnA
	}

	// Build opportunity
	opp := &types.ArbitrageOpportunity{
		ID:             fmt.Sprintf("%s-%s-%s-%d", pair, buyEx, sellEx, time.Now().UnixMilli()),
		Pair:           pair,
		Direction:      direction,
		BuyExchange:    buyEx,
		SellExchange:   sellEx,
		BuyPrice:       buyQuote.AskPrice,
		SellPrice:      sellQuote.BidPrice,
		SpreadAbsolute: spreadAB,
		SpreadPercent:  spreadPercent,
		SpreadBps:      spreadBps,
		TradeSize:      tradeSize,
		GrossProfit:    grossProfit,
		EstimatedFees:  estimatedFees,
		NetProfit:      netProfit,
		NetProfitBps:   netProfitBps,
		BuyLiquidity:   buyQuote.AskSize,
		SellLiquidity:  sellQuote.BidSize,
		DetectedAt:     time.Now(),
		ExpiresAt:      time.Now().Add(time.Duration(d.config.QuoteValiditySecs) * time.Second),
		BuyQuote:       buyQuote,
		SellQuote:      sellQuote,
		IsValid:        true,
	}

	// Validate opportunity
	d.validateOpportunity(opp)

	return opp
}

// validateOpportunity checks if an opportunity meets all criteria.
func (d *Detector) validateOpportunity(opp *types.ArbitrageOpportunity) {
	opp.InvalidationReasons = nil

	// Check minimum net profit
	if opp.NetProfitBps < d.config.MinNetProfitBps {
		opp.IsValid = false
		opp.InvalidationReasons = append(opp.InvalidationReasons,
			fmt.Sprintf("net profit %d bps below minimum %d bps", opp.NetProfitBps, d.config.MinNetProfitBps))
	}

	// Check liquidity
	if opp.BuyLiquidity != nil && opp.TradeSize != nil {
		if opp.BuyLiquidity.Cmp(opp.TradeSize) < 0 {
			opp.IsValid = false
			opp.InvalidationReasons = append(opp.InvalidationReasons,
				"insufficient buy-side liquidity")
		}
	}

	if opp.SellLiquidity != nil && opp.TradeSize != nil {
		if opp.SellLiquidity.Cmp(opp.TradeSize) < 0 {
			opp.IsValid = false
			opp.InvalidationReasons = append(opp.InvalidationReasons,
				"insufficient sell-side liquidity")
		}
	}

	// Check price impact
	if opp.PriceImpactBps > d.config.MaxPriceImpactBps {
		opp.IsValid = false
		opp.InvalidationReasons = append(opp.InvalidationReasons,
			fmt.Sprintf("price impact %d bps exceeds maximum %d bps", opp.PriceImpactBps, d.config.MaxPriceImpactBps))
	}
}

// getProviderFees retrieves fee structure for an exchange.
func (d *Detector) getProviderFees(exchangeName string) *types.FeeStructure {
	provider, ok := d.registry.Get(exchangeName)
	if !ok {
		// Return default fees if provider not found
		return &types.FeeStructure{
			MakerFeeBps: 10,
			TakerFeeBps: 10,
		}
	}
	return provider.GetFees()
}

// FilterProfitableOpportunities filters opportunities that meet minimum profit criteria.
func FilterProfitableOpportunities(opportunities []*types.ArbitrageOpportunity, minNetProfitBps int) []*types.ArbitrageOpportunity {
	var result []*types.ArbitrageOpportunity
	for _, opp := range opportunities {
		if opp.IsValid && opp.NetProfitBps >= minNetProfitBps {
			result = append(result, opp)
		}
	}
	return result
}

// CalculateOptimalTradeSize calculates optimal trade size based on liquidity.
func CalculateOptimalTradeSize(opp *types.ArbitrageOpportunity) *big.Float {
	// Use minimum of available liquidity on both sides
	if opp.BuyLiquidity == nil || opp.SellLiquidity == nil {
		return opp.TradeSize
	}

	minLiquidity := opp.BuyLiquidity
	if opp.SellLiquidity.Cmp(minLiquidity) < 0 {
		minLiquidity = opp.SellLiquidity
	}

	// Use 80% of available liquidity to account for slippage
	optimalSize := new(big.Float).Mul(minLiquidity, big.NewFloat(0.8))

	// Cap at configured trade size
	if opp.TradeSize != nil && optimalSize.Cmp(opp.TradeSize) > 0 {
		return opp.TradeSize
	}

	return optimalSize
}
