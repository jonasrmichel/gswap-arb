package websocket

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/providers/gswap"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// GSwapPollerProvider wraps the GSwap REST provider to integrate with the WebSocket aggregator.
// Since GSwap doesn't have a WebSocket API, this provider polls at regular intervals.
type GSwapPollerProvider struct {
	*BaseWSProvider
	gswapProvider *gswap.GSwapProvider
	pollInterval  time.Duration
	pairs         []string
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
}

// NewGSwapPollerProvider creates a new GSwap polling provider.
func NewGSwapPollerProvider(pollInterval time.Duration) *GSwapPollerProvider {
	if pollInterval == 0 {
		pollInterval = 5 * time.Second // Default 5 second polling
	}

	return &GSwapPollerProvider{
		BaseWSProvider: NewBaseWSProvider("gswap", &types.FeeStructure{
			Exchange:    "gswap",
			MakerFeeBps: 100, // 1%
			TakerFeeBps: 100, // 1%
		}),
		gswapProvider: gswap.NewGSwapProvider(),
		pollInterval:  pollInterval,
	}
}

// Connect initializes the GSwap provider (no actual connection needed for REST).
func (g *GSwapPollerProvider) Connect(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.State() == StateConnected {
		return nil
	}

	g.SetState(StateConnecting)
	g.ctx, g.cancel = context.WithCancel(ctx)

	// Initialize the underlying GSwap provider
	if err := g.gswapProvider.Initialize(ctx); err != nil {
		g.SetState(StateDisconnected)
		return fmt.Errorf("failed to initialize GSwap provider: %w", err)
	}

	g.SetState(StateConnected)
	return nil
}

// Disconnect stops polling.
func (g *GSwapPollerProvider) Disconnect() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cancel != nil {
		g.cancel()
	}

	g.SetState(StateDisconnected)
	return nil
}

// Subscribe starts polling for the given pairs.
func (g *GSwapPollerProvider) Subscribe(pairs []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Map CEX pair names to GSwap pair names
	gswapPairs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		gswapPair := mapToGSwapPair(pair)
		if gswapPair != "" {
			gswapPairs = append(gswapPairs, gswapPair)
			g.AddSubscription(pair) // Store original pair name
		}
	}

	g.pairs = gswapPairs

	// Start polling goroutine
	go g.pollLoop()

	return nil
}

// Unsubscribe stops polling for the given pairs.
func (g *GSwapPollerProvider) Unsubscribe(pairs []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, pair := range pairs {
		g.RemoveSubscription(pair)
	}

	return nil
}

// pollLoop continuously polls GSwap for prices.
func (g *GSwapPollerProvider) pollLoop() {
	ticker := time.NewTicker(g.pollInterval)
	defer ticker.Stop()

	// Poll immediately on start
	g.pollAllPairs()

	for {
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
			g.pollAllPairs()
		}
	}
}

// pollAllPairs fetches prices for all subscribed pairs.
func (g *GSwapPollerProvider) pollAllPairs() {
	g.mu.Lock()
	pairs := make([]string, len(g.pairs))
	copy(pairs, g.pairs)
	g.mu.Unlock()

	for _, gswapPair := range pairs {
		quote, err := g.gswapProvider.GetQuote(g.ctx, gswapPair)
		if err != nil {
			g.EmitError(fmt.Errorf("failed to get GSwap quote for %s: %w", gswapPair, err))
			continue
		}

		// Convert to PriceUpdate and emit
		// Map GSwap pair back to standard pair name for comparison
		standardPair := mapFromGSwapPair(gswapPair)

		update := &PriceUpdate{
			Exchange:  g.Name(),
			Pair:      standardPair,
			BidPrice:  quote.BidPrice,
			AskPrice:  quote.AskPrice,
			BidSize:   quote.BidSize,
			AskSize:   quote.AskSize,
			LastPrice: quote.Price,
			Timestamp: time.Now(),
		}

		g.EmitPriceUpdate(update)
	}
}

// mapToGSwapPair maps a standard CEX pair to a GSwap pair.
// GSwap uses wrapped tokens (GUSDT, GUSDC, etc.)
func mapToGSwapPair(pair string) string {
	// Map common CEX pairs to GSwap equivalents
	pairMappings := map[string]string{
		// GALA pairs
		"GALA/USDT":  "GALA/GUSDT",
		"GALA/USDC":  "GALA/GUSDC",
		"USDUC/GALA": "GUSDUC/GALA",
		"ETH/GALA":   "GWETH/GALA",
		"BTC/GALA":   "GBTC/GALA",
		"SOL/GALA":   "GSOL/GALA",

		// MEW on GalaChain
		"MEW/GALA": "GMEW/GALA",

		// Direct GSwap pairs
		"GWETH/GALA":  "GWETH/GALA",
		"GUSDT/GALA":  "GUSDT/GALA",
		"GUSDC/GALA":  "GUSDC/GALA",
		"GUSDUC/GALA": "GUSDUC/GALA",
		"GBTC/GALA":   "GBTC/GALA",
		"GSOL/GALA":   "GSOL/GALA",
		"GMEW/GALA":   "GMEW/GALA",
	}

	if mapped, ok := pairMappings[pair]; ok {
		return mapped
	}

	return "" // Not supported on GSwap
}

// mapFromGSwapPair maps a GSwap pair back to the standard comparison pair.
func mapFromGSwapPair(gswapPair string) string {
	// For arbitrage comparison, we need to use a common pair format
	// Keep base/quote in the same order we requested.
	pairMappings := map[string]string{
		"GALA/GUSDT":  "GALA/USDT",
		"GALA/GUSDC":  "GALA/USDC",
		"GUSDUC/GALA": "USDUC/GALA",
		"GWETH/GALA":  "ETH/GALA",
		"GBTC/GALA":   "BTC/GALA",
		"GSOL/GALA":   "SOL/GALA",
		"GMEW/GALA":   "MEW/GALA",
	}

	if mapped, ok := pairMappings[gswapPair]; ok {
		return mapped
	}

	return gswapPair
}

// GetSupportedPairs returns pairs that can be mapped between CEX and GSwap.
func GetGSwapSupportedPairs() []string {
	return []string{
		"GALA/USDT",
		"GALA/USDC",
		"USDUC/GALA",
		"MEW/GALA",
	}
}
