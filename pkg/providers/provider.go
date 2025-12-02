// Package providers defines the interface for price providers.
package providers

import (
	"context"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// PriceProvider is the interface that all exchange price providers must implement.
type PriceProvider interface {
	// Name returns the unique identifier for this provider.
	Name() string

	// Type returns whether this is a DEX or CEX.
	Type() types.ExchangeType

	// Initialize sets up the provider with any required configuration.
	Initialize(ctx context.Context) error

	// GetSupportedPairs returns all trading pairs this provider supports.
	GetSupportedPairs(ctx context.Context) ([]types.TradingPair, error)

	// GetQuote fetches the current price quote for a trading pair.
	GetQuote(ctx context.Context, pair string) (*types.PriceQuote, error)

	// GetOrderBook fetches the order book for a trading pair.
	GetOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error)

	// GetFees returns the fee structure for this exchange.
	GetFees() *types.FeeStructure

	// Close cleans up any resources.
	Close() error
}

// ProviderRegistry manages multiple price providers.
type ProviderRegistry struct {
	providers map[string]PriceProvider
}

// NewProviderRegistry creates a new provider registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]PriceProvider),
	}
}

// Register adds a provider to the registry.
func (r *ProviderRegistry) Register(p PriceProvider) {
	r.providers[p.Name()] = p
}

// Get retrieves a provider by name.
func (r *ProviderRegistry) Get(name string) (PriceProvider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// All returns all registered providers.
func (r *ProviderRegistry) All() []PriceProvider {
	result := make([]PriceProvider, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, p)
	}
	return result
}

// InitializeAll initializes all registered providers.
func (r *ProviderRegistry) InitializeAll(ctx context.Context) error {
	for _, p := range r.providers {
		if err := p.Initialize(ctx); err != nil {
			return err
		}
	}
	return nil
}

// CloseAll closes all registered providers.
func (r *ProviderRegistry) CloseAll() error {
	var lastErr error
	for _, p := range r.providers {
		if err := p.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
