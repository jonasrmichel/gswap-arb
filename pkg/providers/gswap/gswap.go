// Package gswap provides a price provider for GalaChain/GSwap DEX.
package gswap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

const (
	// GalaChain API endpoints
	compositePoolAPI = "https://gateway-mainnet.galachain.com/api/asset/dexv3-contract/GetCompositePool"
	dexBackendAPI    = "https://dex-backend-prod1.defi.gala.com"

	// Fee tiers (basis points)
	FeeTier005  = 500   // 0.05%
	FeeTier030  = 3000  // 0.30%
	FeeTier100  = 10000 // 1.00%

	// Cache settings
	priceCacheDuration = 30 * time.Second
	quoteCacheDuration = 10 * time.Second
)

// GSwapProvider implements the PriceProvider interface for GalaChain/GSwap.
type GSwapProvider struct {
	client     *http.Client
	tokens     map[string]TokenInfo
	pairs      []types.TradingPair
	fees       *types.FeeStructure

	// Cache
	priceCache   map[string]*cachedQuote
	priceCacheMu sync.RWMutex
}

// TokenInfo holds GalaChain-specific token information.
type TokenInfo struct {
	Symbol       string
	GalaChainMint string // Format: "SYMBOL|Unit|none|none"
	Decimals     int
}

type cachedQuote struct {
	quote     *types.PriceQuote
	timestamp time.Time
}

// CompositePoolRequest is the request body for GetCompositePool API.
type CompositePoolRequest struct {
	Token0ClassKey TokenClassKey `json:"token0ClassKey"`
	Token1ClassKey TokenClassKey `json:"token1ClassKey"`
	Fee            int           `json:"fee"`
}

// TokenClassKey represents a GalaChain token identifier.
type TokenClassKey struct {
	Collection    string `json:"collection"`
	Category      string `json:"category"`
	Type          string `json:"type"`
	AdditionalKey string `json:"additionalKey"`
}

// CompositePoolResponse is the response from GetCompositePool API.
type CompositePoolResponse struct {
	Data *CompositePoolData `json:"Data"`
}

type CompositePoolData struct {
	Pool          *PoolData              `json:"pool"`
	Token0Balance *TokenBalance          `json:"token0Balance"`
	Token1Balance *TokenBalance          `json:"token1Balance"`
	Token0Decimals int                   `json:"token0Decimals"`
	Token1Decimals int                   `json:"token1Decimals"`
}

type PoolData struct {
	Token0             string `json:"token0"`
	Token1             string `json:"token1"`
	Fee                int    `json:"fee"`
	SqrtPrice          string `json:"sqrtPrice"`
	Liquidity          string `json:"liquidity"`
	GrossPoolLiquidity string `json:"grossPoolLiquidity"`
}

type TokenBalance struct {
	Quantity string `json:"quantity"`
}

// NewGSwapProvider creates a new GSwap price provider.
func NewGSwapProvider() *GSwapProvider {
	return &GSwapProvider{
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		tokens:     make(map[string]TokenInfo),
		priceCache: make(map[string]*cachedQuote),
		fees: &types.FeeStructure{
			Exchange:      "gswap",
			MakerFeeBps:   100, // 1% default
			TakerFeeBps:   100, // 1% default
			WithdrawalFee: 1.0, // 1 GALA per hop
			DepositFee:    0,
		},
	}
}

// Name returns the provider name.
func (p *GSwapProvider) Name() string {
	return "gswap"
}

// Type returns the exchange type.
func (p *GSwapProvider) Type() types.ExchangeType {
	return types.ExchangeTypeDEX
}

// Initialize sets up the provider with default tokens.
func (p *GSwapProvider) Initialize(ctx context.Context) error {
	// Register known GalaChain tokens
	p.tokens = map[string]TokenInfo{
		"GALA": {
			Symbol:        "GALA",
			GalaChainMint: "GALA|Unit|none|none",
			Decimals:      8,
		},
		"GWETH": {
			Symbol:        "GWETH",
			GalaChainMint: "GWETH|Unit|none|none",
			Decimals:      8,
		},
		"GUSDC": {
			Symbol:        "GUSDC",
			GalaChainMint: "GUSDC|Unit|none|none",
			Decimals:      8,
		},
		"GUSDT": {
			Symbol:        "GUSDT",
			GalaChainMint: "GUSDT|Unit|none|none",
			Decimals:      8,
		},
		"GSOL": {
			Symbol:        "GSOL",
			GalaChainMint: "GSOL|Unit|none|none",
			Decimals:      9,
		},
		"GBTC": {
			Symbol:        "GBTC",
			GalaChainMint: "GWBTC|Unit|none|none",
			Decimals:      8,
		},
	}

	// Build trading pairs
	p.pairs = []types.TradingPair{
		{Base: types.Token{Symbol: "GWETH"}, Quote: types.Token{Symbol: "GALA"}, Symbol: "GWETH/GALA", Exchange: "gswap"},
		{Base: types.Token{Symbol: "GUSDC"}, Quote: types.Token{Symbol: "GALA"}, Symbol: "GUSDC/GALA", Exchange: "gswap"},
		{Base: types.Token{Symbol: "GUSDT"}, Quote: types.Token{Symbol: "GALA"}, Symbol: "GUSDT/GALA", Exchange: "gswap"},
		{Base: types.Token{Symbol: "GSOL"}, Quote: types.Token{Symbol: "GALA"}, Symbol: "GSOL/GALA", Exchange: "gswap"},
		{Base: types.Token{Symbol: "GBTC"}, Quote: types.Token{Symbol: "GALA"}, Symbol: "GBTC/GALA", Exchange: "gswap"},
		{Base: types.Token{Symbol: "GWETH"}, Quote: types.Token{Symbol: "GUSDC"}, Symbol: "GWETH/GUSDC", Exchange: "gswap"},
	}

	return nil
}

// GetSupportedPairs returns all supported trading pairs.
func (p *GSwapProvider) GetSupportedPairs(ctx context.Context) ([]types.TradingPair, error) {
	return p.pairs, nil
}

// GetQuote fetches a price quote for a trading pair.
func (p *GSwapProvider) GetQuote(ctx context.Context, pair string) (*types.PriceQuote, error) {
	// Check cache first
	p.priceCacheMu.RLock()
	if cached, ok := p.priceCache[pair]; ok {
		if time.Since(cached.timestamp) < quoteCacheDuration {
			p.priceCacheMu.RUnlock()
			return cached.quote, nil
		}
	}
	p.priceCacheMu.RUnlock()

	// Parse pair (e.g., "GWETH/GALA")
	parts := strings.Split(pair, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid pair format: %s", pair)
	}

	baseSymbol := parts[0]
	quoteSymbol := parts[1]

	// Get token info
	baseToken, ok := p.tokens[baseSymbol]
	if !ok {
		return nil, fmt.Errorf("unknown base token: %s", baseSymbol)
	}
	quoteToken, ok := p.tokens[quoteSymbol]
	if !ok {
		return nil, fmt.Errorf("unknown quote token: %s", quoteSymbol)
	}

	// Fetch pool data from GalaChain
	poolData, err := p.fetchCompositePool(ctx, baseToken.GalaChainMint, quoteToken.GalaChainMint, FeeTier100)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pool data: %w", err)
	}

	// Calculate price from sqrtPrice
	price := p.calculatePriceFromSqrtPrice(poolData.Pool.SqrtPrice, baseToken.Decimals, quoteToken.Decimals)

	quote := &types.PriceQuote{
		Exchange:  p.Name(),
		Pair:      pair,
		Price:     price,
		BidPrice:  price, // DEX has single price
		AskPrice:  price, // DEX has single price
		Timestamp: time.Now(),
		Source:    "composite_pool",
	}

	// Parse liquidity for size info
	if poolData.Pool.Liquidity != "" {
		liquidity := new(big.Float)
		liquidity.SetString(poolData.Pool.Liquidity)
		quote.BidSize = liquidity
		quote.AskSize = liquidity
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

// GetOrderBook fetches the order book (simplified for DEX - uses pool liquidity).
func (p *GSwapProvider) GetOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	quote, err := p.GetQuote(ctx, pair)
	if err != nil {
		return nil, err
	}

	// DEX doesn't have traditional order book, create synthetic one from pool
	orderBook := &types.OrderBook{
		Exchange:  p.Name(),
		Pair:      pair,
		Timestamp: time.Now(),
	}

	// Create single level from pool price/liquidity
	if quote.Price != nil && quote.BidSize != nil {
		orderBook.Bids = []types.OrderBookEntry{{Price: quote.Price, Amount: quote.BidSize}}
		orderBook.Asks = []types.OrderBookEntry{{Price: quote.Price, Amount: quote.AskSize}}
	}

	return orderBook, nil
}

// GetFees returns the fee structure.
func (p *GSwapProvider) GetFees() *types.FeeStructure {
	return p.fees
}

// Close cleans up resources.
func (p *GSwapProvider) Close() error {
	return nil
}

// fetchCompositePool retrieves pool data from GalaChain API.
func (p *GSwapProvider) fetchCompositePool(ctx context.Context, token0Mint, token1Mint string, fee int) (*CompositePoolData, error) {
	// Parse token mints into TokenClassKey format
	token0Key := parseTokenMint(token0Mint)
	token1Key := parseTokenMint(token1Mint)

	// Ensure correct ordering (GalaChain requires token0 < token1 lexicographically)
	if compareTokenKeys(token0Key, token1Key) > 0 {
		token0Key, token1Key = token1Key, token0Key
	}

	reqBody := CompositePoolRequest{
		Token0ClassKey: token0Key,
		Token1ClassKey: token1Key,
		Fee:            fee,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", compositePoolAPI, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var poolResp CompositePoolResponse
	if err := json.NewDecoder(resp.Body).Decode(&poolResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if poolResp.Data == nil {
		return nil, fmt.Errorf("no pool data returned")
	}

	return poolResp.Data, nil
}

// calculatePriceFromSqrtPrice converts sqrtPriceX96 to actual price.
// sqrtPriceX96 = sqrt(price) * 2^96
func (p *GSwapProvider) calculatePriceFromSqrtPrice(sqrtPriceStr string, baseDecimals, quoteDecimals int) *big.Float {
	sqrtPrice := new(big.Float)
	sqrtPrice.SetString(sqrtPriceStr)

	// Price = (sqrtPrice / 2^96)^2
	// First divide by 2^96
	q96 := new(big.Float).SetInt(new(big.Int).Lsh(big.NewInt(1), 96))
	ratio := new(big.Float).Quo(sqrtPrice, q96)

	// Square it
	price := new(big.Float).Mul(ratio, ratio)

	// Adjust for decimals: price * 10^(baseDecimals - quoteDecimals)
	decimalAdj := baseDecimals - quoteDecimals
	if decimalAdj != 0 {
		adjFactor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(decimalAdj))), nil))
		if decimalAdj > 0 {
			price = new(big.Float).Mul(price, adjFactor)
		} else {
			price = new(big.Float).Quo(price, adjFactor)
		}
	}

	return price
}

// parseTokenMint parses a GalaChain mint string into TokenClassKey.
func parseTokenMint(mint string) TokenClassKey {
	parts := strings.Split(mint, "|")
	if len(parts) != 4 {
		return TokenClassKey{Collection: mint, Category: "Unit", Type: "none", AdditionalKey: "none"}
	}
	return TokenClassKey{
		Collection:    parts[0],
		Category:      parts[1],
		Type:          parts[2],
		AdditionalKey: parts[3],
	}
}

// compareTokenKeys compares two token keys lexicographically.
func compareTokenKeys(a, b TokenClassKey) int {
	if a.Collection != b.Collection {
		return strings.Compare(a.Collection, b.Collection)
	}
	if a.Category != b.Category {
		return strings.Compare(a.Category, b.Category)
	}
	if a.Type != b.Type {
		return strings.Compare(a.Type, b.Type)
	}
	return strings.Compare(a.AdditionalKey, b.AdditionalKey)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// AddToken adds a custom token to the provider.
func (p *GSwapProvider) AddToken(symbol, galaChainMint string, decimals int) {
	p.tokens[symbol] = TokenInfo{
		Symbol:        symbol,
		GalaChainMint: galaChainMint,
		Decimals:      decimals,
	}
}

// AddPair adds a trading pair to the provider.
func (p *GSwapProvider) AddPair(base, quote, symbol string) {
	p.pairs = append(p.pairs, types.TradingPair{
		Base:     types.Token{Symbol: base},
		Quote:    types.Token{Symbol: quote},
		Symbol:   symbol,
		Exchange: p.Name(),
	})
}
