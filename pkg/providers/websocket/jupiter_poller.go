package websocket

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/providers/solana"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// JupiterPollerProvider wraps the Jupiter REST provider to integrate with the WebSocket aggregator.
// Since Jupiter doesn't have a WebSocket API, this provider polls at regular intervals.
type JupiterPollerProvider struct {
	*BaseWSProvider
	jupiterProvider *solana.JupiterProvider
	pollInterval    time.Duration
	pairs           []string
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
}

// JupiterPollerConfig contains configuration for the Jupiter poller.
type JupiterPollerConfig struct {
	PollInterval time.Duration
	BaseURL      string // Jupiter API base URL
	APIKey       string // Optional API key
}

// NewJupiterPollerProvider creates a new Jupiter polling provider.
func NewJupiterPollerProvider(config *JupiterPollerConfig) *JupiterPollerProvider {
	if config == nil {
		config = &JupiterPollerConfig{}
	}

	pollInterval := config.PollInterval
	if pollInterval == 0 {
		pollInterval = 5 * time.Second // Default 5 second polling
	}

	jupiterConfig := &solana.JupiterProviderConfig{
		BaseURL: config.BaseURL,
		APIKey:  config.APIKey,
	}

	return &JupiterPollerProvider{
		BaseWSProvider: NewBaseWSProvider("jupiter", &types.FeeStructure{
			Exchange:    "jupiter",
			MakerFeeBps: 0,  // Jupiter has no platform fee
			TakerFeeBps: 30, // ~0.3% average DEX fee
		}),
		jupiterProvider: solana.NewJupiterProvider(jupiterConfig),
		pollInterval:    pollInterval,
	}
}

// Connect initializes the Jupiter provider.
func (j *JupiterPollerProvider) Connect(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.State() == StateConnected {
		return nil
	}

	j.SetState(StateConnecting)
	j.ctx, j.cancel = context.WithCancel(ctx)

	// Initialize the underlying Jupiter provider
	if err := j.jupiterProvider.Initialize(ctx); err != nil {
		j.SetState(StateDisconnected)
		return fmt.Errorf("failed to initialize Jupiter provider: %w", err)
	}

	j.SetState(StateConnected)
	return nil
}

// Disconnect stops polling.
func (j *JupiterPollerProvider) Disconnect() error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.cancel != nil {
		j.cancel()
	}

	j.SetState(StateDisconnected)
	return nil
}

// Subscribe starts polling for the given pairs.
func (j *JupiterPollerProvider) Subscribe(pairs []string) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	// Filter pairs that are supported on Jupiter
	jupiterPairs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		if isJupiterSupportedPair(pair) {
			jupiterPairs = append(jupiterPairs, pair)
			j.AddSubscription(pair)
		}
	}

	j.pairs = jupiterPairs

	// Start polling goroutine
	go j.pollLoop()

	return nil
}

// Unsubscribe stops polling for the given pairs.
func (j *JupiterPollerProvider) Unsubscribe(pairs []string) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	for _, pair := range pairs {
		j.RemoveSubscription(pair)
	}

	return nil
}

// pollLoop continuously polls Jupiter for prices.
func (j *JupiterPollerProvider) pollLoop() {
	ticker := time.NewTicker(j.pollInterval)
	defer ticker.Stop()

	// Poll immediately on start
	j.pollAllPairs()

	for {
		select {
		case <-j.ctx.Done():
			return
		case <-ticker.C:
			j.pollAllPairs()
		}
	}
}

// pollAllPairs fetches prices for all subscribed pairs.
func (j *JupiterPollerProvider) pollAllPairs() {
	j.mu.Lock()
	pairs := make([]string, len(j.pairs))
	copy(pairs, j.pairs)
	j.mu.Unlock()

	for _, pair := range pairs {
		quote, err := j.jupiterProvider.GetQuote(j.ctx, pair)
		if err != nil {
			j.EmitError(fmt.Errorf("failed to get Jupiter quote for %s: %w", pair, err))
			continue
		}

		// Convert to PriceUpdate and emit
		update := &PriceUpdate{
			Exchange:  j.Name(),
			Pair:      pair,
			BidPrice:  quote.BidPrice,
			AskPrice:  quote.AskPrice,
			BidSize:   quote.BidSize,
			AskSize:   quote.AskSize,
			LastPrice: quote.Price,
			Timestamp: time.Now(),
		}

		j.EmitPriceUpdate(update)
	}
}

// isJupiterSupportedPair checks if a pair can be traded on Jupiter.
func isJupiterSupportedPair(pair string) bool {
	supportedPairs := map[string]bool{
		// SOL pairs
		"SOL/USDC": true,
		"SOL/USDT": true,

		// GALA pairs (if available on Solana)
		"GALA/SOL":  true,
		"GALA/USDC": true,

		// Memecoin pairs
		"BONK/SOL":     true,
		"WIF/SOL":      true,
		"POPCAT/SOL":   true,
		"FARTCOIN/SOL": true,
	}

	return supportedPairs[pair]
}

// GetJupiterSupportedPairs returns the list of pairs supported on Jupiter.
func GetJupiterSupportedPairs() []string {
	return []string{
		"SOL/USDC",
		"SOL/USDT",
		"GALA/SOL",
		"GALA/USDC",
		"BONK/SOL",
		"WIF/SOL",
		"POPCAT/SOL",
		"FARTCOIN/SOL",
	}
}

// AddPair adds a custom pair to poll.
func (j *JupiterPollerProvider) AddPair(pair string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	// Add to Jupiter provider
	j.jupiterProvider.AddPair(pair, pair, pair)

	// Add to polling list
	j.pairs = append(j.pairs, pair)
	j.AddSubscription(pair)
}

// AddToken adds a custom token to the provider.
func (j *JupiterPollerProvider) AddToken(symbol, mint string, decimals int) {
	j.jupiterProvider.AddToken(symbol, mint, decimals)
}
