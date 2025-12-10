// Package solana provides a price provider for Solana DEXs via Jupiter aggregator.
package solana

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/jupiter"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

const (
	// Cache settings
	quoteCacheDuration = 10 * time.Second
)

// JupiterProvider implements the PriceProvider interface for Solana via Jupiter.
type JupiterProvider struct {
	client     *jupiter.Client
	tokens     map[string]jupiter.TokenInfo
	pairs      []types.TradingPair
	fees       *types.FeeStructure

	// Cache
	priceCache   map[string]*cachedQuote
	priceCacheMu sync.RWMutex
}

type cachedQuote struct {
	quote     *types.PriceQuote
	timestamp time.Time
}

// JupiterProviderConfig contains configuration for the Jupiter provider.
type JupiterProviderConfig struct {
	BaseURL    string // Jupiter API base URL
	APIKey     string // Optional API key for Ultra API
	Tokens     map[string]jupiter.TokenInfo // Custom token configurations
}

// NewJupiterProvider creates a new Jupiter price provider.
func NewJupiterProvider(config *JupiterProviderConfig) *JupiterProvider {
	if config == nil {
		config = &JupiterProviderConfig{}
	}

	clientConfig := &jupiter.ClientConfig{
		BaseURL: config.BaseURL,
		APIKey:  config.APIKey,
	}

	tokens := config.Tokens
	if tokens == nil {
		tokens = jupiter.DefaultTokens()
	}

	return &JupiterProvider{
		client:     jupiter.NewClient(clientConfig),
		tokens:     tokens,
		priceCache: make(map[string]*cachedQuote),
		fees: &types.FeeStructure{
			Exchange:      "jupiter",
			MakerFeeBps:   0,   // Jupiter has no platform fee
			TakerFeeBps:   30,  // ~0.3% average DEX fee (varies by pool)
			WithdrawalFee: 0,
			DepositFee:    0,
		},
	}
}

// Name returns the provider name.
func (p *JupiterProvider) Name() string {
	return "jupiter"
}

// Type returns the exchange type.
func (p *JupiterProvider) Type() types.ExchangeType {
	return types.ExchangeTypeDEX
}

// Initialize sets up the provider with default pairs.
func (p *JupiterProvider) Initialize(ctx context.Context) error {
	// Build trading pairs from tokens
	// Default pairs: token/SOL and token/USDC for each token
	p.pairs = []types.TradingPair{
		// SOL pairs
		{Base: types.Token{Symbol: "SOL"}, Quote: types.Token{Symbol: "USDC"}, Symbol: "SOL/USDC", Exchange: "jupiter"},
		{Base: types.Token{Symbol: "SOL"}, Quote: types.Token{Symbol: "USDT"}, Symbol: "SOL/USDT", Exchange: "jupiter"},

		// GALA pairs (if available on Solana)
		{Base: types.Token{Symbol: "GALA"}, Quote: types.Token{Symbol: "SOL"}, Symbol: "GALA/SOL", Exchange: "jupiter"},
		{Base: types.Token{Symbol: "GALA"}, Quote: types.Token{Symbol: "USDC"}, Symbol: "GALA/USDC", Exchange: "jupiter"},

		// Memecoin pairs
		{Base: types.Token{Symbol: "BONK"}, Quote: types.Token{Symbol: "SOL"}, Symbol: "BONK/SOL", Exchange: "jupiter"},
		{Base: types.Token{Symbol: "WIF"}, Quote: types.Token{Symbol: "SOL"}, Symbol: "WIF/SOL", Exchange: "jupiter"},
		{Base: types.Token{Symbol: "POPCAT"}, Quote: types.Token{Symbol: "SOL"}, Symbol: "POPCAT/SOL", Exchange: "jupiter"},
		{Base: types.Token{Symbol: "FARTCOIN"}, Quote: types.Token{Symbol: "SOL"}, Symbol: "FARTCOIN/SOL", Exchange: "jupiter"},
	}

	return nil
}

// GetSupportedPairs returns all supported trading pairs.
func (p *JupiterProvider) GetSupportedPairs(ctx context.Context) ([]types.TradingPair, error) {
	return p.pairs, nil
}

// GetQuote fetches a price quote for a trading pair.
func (p *JupiterProvider) GetQuote(ctx context.Context, pair string) (*types.PriceQuote, error) {
	// Check cache first
	p.priceCacheMu.RLock()
	if cached, ok := p.priceCache[pair]; ok {
		if time.Since(cached.timestamp) < quoteCacheDuration {
			p.priceCacheMu.RUnlock()
			return cached.quote, nil
		}
	}
	p.priceCacheMu.RUnlock()

	// Parse pair (e.g., "SOL/USDC")
	parts := strings.Split(pair, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid pair format: %s", pair)
	}

	baseSymbol := strings.ToUpper(parts[0])
	quoteSymbol := strings.ToUpper(parts[1])

	// Get token info
	baseToken, ok := p.tokens[baseSymbol]
	if !ok {
		return nil, fmt.Errorf("unknown base token: %s", baseSymbol)
	}
	quoteToken, ok := p.tokens[quoteSymbol]
	if !ok {
		return nil, fmt.Errorf("unknown quote token: %s", quoteSymbol)
	}

	// Fetch quote from Jupiter (1 unit of base token)
	amount := pow10(baseToken.Decimals)
	jupiterQuote, err := p.client.GetQuote(ctx, &jupiter.QuoteParams{
		InputMint:   baseToken.Mint,
		OutputMint:  quoteToken.Mint,
		Amount:      strconv.FormatInt(amount, 10),
		SlippageBps: 50,
		SwapMode:    jupiter.SwapModeExactIn,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Jupiter quote: %w", err)
	}

	// Parse output amount
	outAmount, err := strconv.ParseInt(jupiterQuote.OutAmount, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse output amount: %w", err)
	}

	// Calculate price (quote tokens per base token)
	price := new(big.Float).SetFloat64(float64(outAmount) / float64(pow10(quoteToken.Decimals)))

	// Parse price impact
	var priceImpact float64
	if jupiterQuote.PriceImpactPct != "" {
		priceImpact, _ = strconv.ParseFloat(jupiterQuote.PriceImpactPct, 64)
	}

	quote := &types.PriceQuote{
		Exchange:    p.Name(),
		Pair:        pair,
		Price:       price,
		BidPrice:    price, // DEX has single price
		AskPrice:    price,
		Timestamp:   time.Now(),
		Source:      "jupiter_quote",
		PriceImpact: priceImpact,
	}

	// Cache the quote
	p.priceCacheMu.Lock()
	p.priceCache[pair] = &cachedQuote{
		quote:     quote,
		timestamp: time.Now(),
	}
	p.priceCacheMu.Unlock()

	return quote, nil
}

// GetOrderBook returns a synthetic order book from the pool.
func (p *JupiterProvider) GetOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	quote, err := p.GetQuote(ctx, pair)
	if err != nil {
		return nil, err
	}

	// DEX doesn't have traditional order book, create synthetic one
	orderBook := &types.OrderBook{
		Exchange:  p.Name(),
		Pair:      pair,
		Timestamp: time.Now(),
	}

	// Create single level from pool price
	if quote.Price != nil {
		orderBook.Bids = []types.OrderBookEntry{{Price: quote.Price, Amount: quote.BidSize}}
		orderBook.Asks = []types.OrderBookEntry{{Price: quote.Price, Amount: quote.AskSize}}
	}

	return orderBook, nil
}

// GetFees returns the fee structure.
func (p *JupiterProvider) GetFees() *types.FeeStructure {
	return p.fees
}

// Close cleans up resources.
func (p *JupiterProvider) Close() error {
	return nil
}

// AddToken adds a custom token to the provider.
func (p *JupiterProvider) AddToken(symbol, mint string, decimals int) {
	p.tokens[symbol] = jupiter.TokenInfo{
		Symbol:   symbol,
		Mint:     mint,
		Decimals: decimals,
	}
}

// AddPair adds a trading pair to the provider.
func (p *JupiterProvider) AddPair(base, quote, symbol string) {
	p.pairs = append(p.pairs, types.TradingPair{
		Base:     types.Token{Symbol: base},
		Quote:    types.Token{Symbol: quote},
		Symbol:   symbol,
		Exchange: p.Name(),
	})
}

// GetToken returns token info for a symbol.
func (p *JupiterProvider) GetToken(symbol string) (jupiter.TokenInfo, bool) {
	token, ok := p.tokens[strings.ToUpper(symbol)]
	return token, ok
}

// GetClient returns the underlying Jupiter client.
func (p *JupiterProvider) GetClient() *jupiter.Client {
	return p.client
}

// pow10 returns 10^n as int64.
func pow10(n int) int64 {
	result := int64(1)
	for i := 0; i < n; i++ {
		result *= 10
	}
	return result
}
