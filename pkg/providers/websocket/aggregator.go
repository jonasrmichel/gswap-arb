package websocket

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/arbitrage"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// PriceAggregator aggregates price updates from multiple WebSocket providers
// and enables real-time arbitrage detection.
type PriceAggregator struct {
	providers    map[string]WSProvider
	providersMu  sync.RWMutex

	// Aggregated price data
	prices   map[string]map[string]*PriceUpdate // pair -> exchange -> update
	pricesMu sync.RWMutex

	// Callbacks
	onPriceUpdate     func(update *PriceUpdate)
	onArbitrage       func(opp *types.ArbitrageOpportunity)
	onChainArbitrage  func(opp *types.ChainArbitrageOpportunity)

	// Configuration
	minSpreadBps    int
	minNetProfitBps int
	staleThreshold  time.Duration
	defaultTradeSize *big.Float
	maxChainHops    int

	// Chain arbitrage detector
	chainDetector *arbitrage.ChainArbitrageDetector

	// Channels
	updates chan *PriceUpdate
	done    chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
}

// AggregatorConfig holds configuration for the price aggregator.
type AggregatorConfig struct {
	MinSpreadBps     int           // Minimum spread to trigger callback
	MinNetProfitBps  int           // Minimum net profit after fees
	StaleThreshold   time.Duration // How long before a price is considered stale
	DefaultTradeSize float64       // Default trade size for calculations
	MaxChainHops     int           // Maximum hops in chain arbitrage (default: 3)
}

// DefaultAggregatorConfig returns default configuration.
func DefaultAggregatorConfig() *AggregatorConfig {
	return &AggregatorConfig{
		MinSpreadBps:     50,
		MinNetProfitBps:  20,
		StaleThreshold:   10 * time.Second,
		DefaultTradeSize: 1000.0,
		MaxChainHops:     3,
	}
}

// NewPriceAggregator creates a new price aggregator.
func NewPriceAggregator(config *AggregatorConfig) *PriceAggregator {
	if config == nil {
		config = DefaultAggregatorConfig()
	}

	if config.MaxChainHops <= 0 {
		config.MaxChainHops = 3
	}

	if config.DefaultTradeSize <= 0 {
		config.DefaultTradeSize = 1000.0
	}

	// Create arbitrage config for chain detector
	arbConfig := &types.ArbitrageConfig{
		MinSpreadBps:    config.MinSpreadBps,
		MinNetProfitBps: config.MinNetProfitBps,
	}

	return &PriceAggregator{
		providers:        make(map[string]WSProvider),
		prices:           make(map[string]map[string]*PriceUpdate),
		minSpreadBps:     config.MinSpreadBps,
		minNetProfitBps:  config.MinNetProfitBps,
		staleThreshold:   config.StaleThreshold,
		defaultTradeSize: big.NewFloat(config.DefaultTradeSize),
		maxChainHops:     config.MaxChainHops,
		chainDetector:    arbitrage.NewChainArbitrageDetector(arbConfig, config.MaxChainHops),
		updates:          make(chan *PriceUpdate, 10000),
		done:             make(chan struct{}),
	}
}

// AddProvider adds a WebSocket provider to the aggregator.
func (a *PriceAggregator) AddProvider(provider WSProvider) {
	a.providersMu.Lock()
	defer a.providersMu.Unlock()
	a.providers[provider.Name()] = provider
}

// RemoveProvider removes a provider from the aggregator.
func (a *PriceAggregator) RemoveProvider(name string) {
	a.providersMu.Lock()
	defer a.providersMu.Unlock()
	delete(a.providers, name)
}

// GetProvider returns a provider by name.
func (a *PriceAggregator) GetProvider(name string) (WSProvider, bool) {
	a.providersMu.RLock()
	defer a.providersMu.RUnlock()
	p, ok := a.providers[name]
	return p, ok
}

// OnPriceUpdate sets the callback for price updates.
func (a *PriceAggregator) OnPriceUpdate(callback func(update *PriceUpdate)) {
	a.onPriceUpdate = callback
}

// OnArbitrage sets the callback for detected arbitrage opportunities.
func (a *PriceAggregator) OnArbitrage(callback func(opp *types.ArbitrageOpportunity)) {
	a.onArbitrage = callback
}

// OnChainArbitrage sets the callback for detected chain arbitrage opportunities.
func (a *PriceAggregator) OnChainArbitrage(callback func(opp *types.ChainArbitrageOpportunity)) {
	a.onChainArbitrage = callback
}

// Start starts the aggregator and connects all providers.
// It will attempt to connect to all providers but continue even if some fail.
func (a *PriceAggregator) Start(ctx context.Context, pairs []string) error {
	a.ctx, a.cancel = context.WithCancel(ctx)

	a.providersMu.RLock()
	providers := make([]WSProvider, 0, len(a.providers))
	for _, p := range a.providers {
		providers = append(providers, p)
	}
	a.providersMu.RUnlock()

	// Connect all providers (don't fail if some can't connect)
	connectedCount := 0
	for _, provider := range providers {
		if err := provider.Connect(a.ctx); err != nil {
			fmt.Printf("Warning: failed to connect %s: %v\n", provider.Name(), err)
			continue
		}

		// Subscribe to pairs
		if err := provider.Subscribe(pairs); err != nil {
			fmt.Printf("Warning: failed to subscribe %s: %v\n", provider.Name(), err)
			continue
		}

		// Start listening for updates from this provider
		go a.listenToProvider(provider)
		connectedCount++
	}

	if connectedCount == 0 {
		return fmt.Errorf("failed to connect to any providers")
	}

	// Start processing updates
	go a.processUpdates()

	// Start stale price cleanup
	go a.cleanupStalePrices()

	return nil
}

// Stop stops the aggregator and disconnects all providers.
func (a *PriceAggregator) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}

	close(a.done)

	a.providersMu.RLock()
	defer a.providersMu.RUnlock()

	var lastErr error
	for _, provider := range a.providers {
		if err := provider.Disconnect(); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// listenToProvider listens for updates from a single provider.
func (a *PriceAggregator) listenToProvider(provider WSProvider) {
	for {
		select {
		case <-a.ctx.Done():
			return
		case update, ok := <-provider.PriceUpdates():
			if !ok {
				return
			}
			select {
			case a.updates <- update:
			default:
				// Drop if channel is full
			}
		case err, ok := <-provider.Errors():
			if !ok {
				return
			}
			// Log error (could add error callback)
			_ = err
		}
	}
}

// processUpdates processes incoming price updates.
func (a *PriceAggregator) processUpdates() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.done:
			return
		case update := <-a.updates:
			a.handleUpdate(update)
		}
	}
}

// handleUpdate handles a single price update.
func (a *PriceAggregator) handleUpdate(update *PriceUpdate) {
	// Store the update
	a.pricesMu.Lock()
	if a.prices[update.Pair] == nil {
		a.prices[update.Pair] = make(map[string]*PriceUpdate)
	}
	a.prices[update.Pair][update.Exchange] = update
	a.pricesMu.Unlock()

	// Trigger callback
	if a.onPriceUpdate != nil {
		a.onPriceUpdate(update)
	}

	// Check for arbitrage opportunities
	a.checkArbitrage(update.Pair)
}

// checkArbitrage checks for arbitrage opportunities on a pair.
func (a *PriceAggregator) checkArbitrage(pair string) {
	a.pricesMu.RLock()
	pairPrices := a.prices[pair]
	if len(pairPrices) < 2 {
		a.pricesMu.RUnlock()
		return
	}

	// Copy prices to avoid holding lock
	prices := make(map[string]*PriceUpdate)
	for k, v := range pairPrices {
		prices[k] = v
	}
	a.pricesMu.RUnlock()

	// Check simple (direct) arbitrage if callback is set
	if a.onArbitrage != nil {
		a.checkDirectArbitrage(pair, prices)
	}

	// Check chain arbitrage if callback is set
	if a.onChainArbitrage != nil {
		a.checkChainArbitrage(pair, prices)
	}
}

// checkDirectArbitrage checks for simple 2-exchange arbitrage opportunities.
func (a *PriceAggregator) checkDirectArbitrage(pair string, prices map[string]*PriceUpdate) {
	// Find best bid and best ask across exchanges
	var bestBidExchange, bestAskExchange string
	var bestBid, bestAsk *PriceUpdate

	for exchange, update := range prices {
		// Skip stale prices
		if time.Since(update.Timestamp) > a.staleThreshold {
			continue
		}

		if update.BidPrice == nil || update.AskPrice == nil {
			continue
		}

		// Find highest bid (best place to sell)
		if bestBid == nil || update.BidPrice.Cmp(bestBid.BidPrice) > 0 {
			bestBid = update
			bestBidExchange = exchange
		}

		// Find lowest ask (best place to buy)
		if bestAsk == nil || update.AskPrice.Cmp(bestAsk.AskPrice) < 0 {
			bestAsk = update
			bestAskExchange = exchange
		}
	}

	// Check if there's an arbitrage opportunity
	if bestBid == nil || bestAsk == nil || bestBidExchange == bestAskExchange {
		return
	}

	// Calculate spread: (bestBid - bestAsk) / bestAsk
	spread := new(big.Float).Sub(bestBid.BidPrice, bestAsk.AskPrice)
	if spread.Sign() <= 0 {
		return // No profitable spread
	}

	spreadPercent, _ := new(big.Float).Quo(spread, bestAsk.AskPrice).Float64()
	spreadBps := int(spreadPercent * 10000)

	if spreadBps < a.minSpreadBps {
		return
	}

	// Get fees
	a.providersMu.RLock()
	buyFees := a.getProviderFees(bestAskExchange)
	sellFees := a.getProviderFees(bestBidExchange)
	a.providersMu.RUnlock()

	totalFeeBps := buyFees + sellFees
	netProfitBps := spreadBps - totalFeeBps

	if netProfitBps < a.minNetProfitBps {
		return
	}

	// Create opportunity
	opp := &types.ArbitrageOpportunity{
		ID:           fmt.Sprintf("%s-%s-%s-%d", pair, bestAskExchange, bestBidExchange, time.Now().UnixMilli()),
		Pair:         pair,
		Direction:    types.BuyOnAExchangeSellOnB,
		BuyExchange:  bestAskExchange,
		SellExchange: bestBidExchange,
		BuyPrice:     bestAsk.AskPrice,
		SellPrice:    bestBid.BidPrice,
		SpreadAbsolute: spread,
		SpreadPercent:  spreadPercent * 100,
		SpreadBps:      spreadBps,
		NetProfitBps:   netProfitBps,
		BuyLiquidity:   bestAsk.AskSize,
		SellLiquidity:  bestBid.BidSize,
		DetectedAt:     time.Now(),
		ExpiresAt:      time.Now().Add(5 * time.Second), // WebSocket data expires fast
		BuyQuote:       bestAsk.ToQuote(),
		SellQuote:      bestBid.ToQuote(),
		IsValid:        true,
	}

	a.onArbitrage(opp)
}

// checkChainArbitrage checks for multi-hop chain arbitrage opportunities.
func (a *PriceAggregator) checkChainArbitrage(pair string, prices map[string]*PriceUpdate) {
	// Convert PriceUpdate map to ExchangePrice map for the chain detector
	exchangePrices := make(map[string]*arbitrage.ExchangePrice)

	a.providersMu.RLock()
	for exchange, update := range prices {
		// Skip stale prices
		if time.Since(update.Timestamp) > a.staleThreshold {
			continue
		}

		if update.BidPrice == nil || update.AskPrice == nil {
			continue
		}

		exchangePrices[exchange] = &arbitrage.ExchangePrice{
			Exchange:  exchange,
			BidPrice:  update.BidPrice,
			AskPrice:  update.AskPrice,
			BidSize:   update.BidSize,
			AskSize:   update.AskSize,
			FeeBps:    a.getProviderFees(exchange),
			Timestamp: update.Timestamp,
		}
	}
	a.providersMu.RUnlock()

	// Detect chain opportunities
	opportunities := a.chainDetector.DetectChainOpportunities(pair, exchangePrices, a.defaultTradeSize)

	// Emit all valid opportunities
	for _, opp := range opportunities {
		if opp.IsValid {
			a.onChainArbitrage(opp)
		}
	}
}

// getProviderFees gets the taker fee in basis points for a provider.
func (a *PriceAggregator) getProviderFees(name string) int {
	if provider, ok := a.providers[name]; ok {
		fees := provider.GetFees()
		if fees != nil {
			return fees.TakerFeeBps
		}
	}
	return 10 // Default 0.1%
}

// cleanupStalePrices removes stale price data periodically.
func (a *PriceAggregator) cleanupStalePrices() {
	ticker := time.NewTicker(a.staleThreshold)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.done:
			return
		case <-ticker.C:
			a.pricesMu.Lock()
			now := time.Now()
			for pair, exchanges := range a.prices {
				for exchange, update := range exchanges {
					if now.Sub(update.Timestamp) > a.staleThreshold*2 {
						delete(exchanges, exchange)
					}
				}
				if len(exchanges) == 0 {
					delete(a.prices, pair)
				}
			}
			a.pricesMu.Unlock()
		}
	}
}

// GetLatestPrices returns the latest prices for all pairs.
func (a *PriceAggregator) GetLatestPrices() map[string]map[string]*PriceUpdate {
	a.pricesMu.RLock()
	defer a.pricesMu.RUnlock()

	result := make(map[string]map[string]*PriceUpdate)
	for pair, exchanges := range a.prices {
		result[pair] = make(map[string]*PriceUpdate)
		for exchange, update := range exchanges {
			result[pair][exchange] = update
		}
	}
	return result
}

// GetLatestPrice returns the latest price for a specific pair and exchange.
func (a *PriceAggregator) GetLatestPrice(pair, exchange string) *PriceUpdate {
	a.pricesMu.RLock()
	defer a.pricesMu.RUnlock()

	if exchanges, ok := a.prices[pair]; ok {
		return exchanges[exchange]
	}
	return nil
}

// GetPairPrices returns all prices for a specific pair.
func (a *PriceAggregator) GetPairPrices(pair string) map[string]*PriceUpdate {
	a.pricesMu.RLock()
	defer a.pricesMu.RUnlock()

	result := make(map[string]*PriceUpdate)
	if exchanges, ok := a.prices[pair]; ok {
		for exchange, update := range exchanges {
			result[exchange] = update
		}
	}
	return result
}

// GetConnectionStatus returns the connection status of all providers.
func (a *PriceAggregator) GetConnectionStatus() map[string]ConnectionState {
	a.providersMu.RLock()
	defer a.providersMu.RUnlock()

	status := make(map[string]ConnectionState)
	for name, provider := range a.providers {
		status[name] = provider.State()
	}
	return status
}

// IsAllConnected returns true if all providers are connected.
func (a *PriceAggregator) IsAllConnected() bool {
	a.providersMu.RLock()
	defer a.providersMu.RUnlock()

	for _, provider := range a.providers {
		if provider.State() != StateConnected {
			return false
		}
	}
	return true
}
