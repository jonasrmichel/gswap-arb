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
	quotingAPI       = "https://gateway-mainnet.galachain.com/api/asset/dexv3-contract/QuoteExactAmount"
	dexBackendAPI    = "https://dex-backend-prod1.defi.gala.com"

	// Fee tiers (basis points)
	FeeTier005 = 500   // 0.05%
	FeeTier030 = 3000  // 0.30%
	FeeTier100 = 10000 // 1.00%

	// Cache settings
	priceCacheDuration = 30 * time.Second
	quoteCacheDuration = 10 * time.Second
)

// GSwapProvider implements the PriceProvider interface for GalaChain/GSwap.
type GSwapProvider struct {
	client *http.Client
	tokens map[string]TokenInfo
	pairs  []types.TradingPair
	fees   *types.FeeStructure

	// Cache
	priceCache   map[string]*cachedQuote
	priceCacheMu sync.RWMutex
}

// TokenInfo holds GalaChain-specific token information.
type TokenInfo struct {
	Symbol        string
	GalaChainMint string // Format: "SYMBOL|Unit|none|none"
	Decimals      int
}

type cachedQuote struct {
	quote     *types.PriceQuote
	timestamp time.Time
}

// QuoteExactAmountRequest is the request body for QuoteExactAmount API.
type QuoteExactAmountRequest struct {
	Token0     TokenClassKey `json:"token0"`
	Token1     TokenClassKey `json:"token1"`
	ZeroForOne bool          `json:"zeroForOne"`
	Fee        int           `json:"fee"`
	Amount     string        `json:"amount"`
}

// CompositePoolRequest is the request body for GetCompositePool API.
type CompositePoolRequest struct {
	Token0 TokenClassKey `json:"token0"`
	Token1 TokenClassKey `json:"token1"`
	Fee    int           `json:"fee"`
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

// QuoteResponse is the response from QuoteExactAmount.
type QuoteResponse struct {
	Status int        `json:"Status"`
	Data   *QuoteData `json:"Data"`
}

// QuoteData contains the swap amounts and price.
type QuoteData struct {
	Amount0          string `json:"amount0"`
	Amount1          string `json:"amount1"`
	CurrentSqrtPrice string `json:"currentSqrtPrice"`
	NewSqrtPrice     string `json:"newSqrtPrice,omitempty"`
}

type quoteResult struct {
	amount0   *big.Float
	amount1   *big.Float
	sqrtPrice *big.Float
}

type CompositePoolData struct {
	Pool           *PoolData     `json:"pool"`
	Token0Balance  *TokenBalance `json:"token0Balance"`
	Token1Balance  *TokenBalance `json:"token1Balance"`
	Token0Decimals int           `json:"token0Decimals"`
	Token1Decimals int           `json:"token1Decimals"`
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
			Decimals:      18,
		},
		"GUSDC": {
			Symbol:        "GUSDC",
			GalaChainMint: "GUSDC|Unit|none|none",
			Decimals:      6,
		},
		"GUSDT": {
			Symbol:        "GUSDT",
			GalaChainMint: "GUSDT|Unit|none|none",
			Decimals:      6,
		},
		"GSOL": {
			Symbol:        "GSOL",
			GalaChainMint: "GSOL|Unit|none|none",
			Decimals:      9,
		},
		"GWBTC": {
			Symbol:        "GWBTC",
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
		{Base: types.Token{Symbol: "GWBTC"}, Quote: types.Token{Symbol: "GALA"}, Symbol: "GWBTC/GALA", Exchange: "gswap"},
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
	// Translate common CEX-style pair names (e.g., GALA/USDC) to GSwap-native wrapped pairs.
	pair = mapCEXPairToGSwap(pair)

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

	// Use quoting API to get amountOut for 1 unit of base
	price, err := p.fetchQuotePrice(ctx, baseToken, quoteToken)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch quote: %w", err)
	}

	quote := &types.PriceQuote{
		Exchange:  p.Name(),
		Pair:      pair,
		Price:     price,
		BidPrice:  price, // DEX has single price
		AskPrice:  price, // DEX has single price
		Timestamp: time.Now(),
		Source:    "quote_api",
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

// fetchQuotePrice calls QuoteExactAmount for 1 unit of base to derive a price in quote.
func (p *GSwapProvider) fetchQuotePrice(ctx context.Context, baseToken, quoteToken TokenInfo) (*big.Float, error) {
	// Build token keys and normalize ordering (API expects token0/token1 sorted)
	tkBase := parseTokenMint(baseToken.GalaChainMint)
	tkQuote := parseTokenMint(quoteToken.GalaChainMint)

	token0 := tkBase
	token1 := tkQuote
	tokenInIsToken0 := true // we sell base
	if compareTokenKeys(tkBase, tkQuote) > 0 {
		// swap to maintain lex order
		token0 = tkQuote
		token1 = tkBase
		tokenInIsToken0 = false
	}

	req := QuoteExactAmountRequest{
		Token0:     token0,
		Token1:     token1,
		ZeroForOne: tokenInIsToken0, // sell tokenIn
		Fee:        FeeTier100,
		Amount:     "1", // small amount - we rely on sqrt price for spot
	}

	// Try fee tiers and pick best out
	fees := []int{FeeTier005, FeeTier030, FeeTier100}
	var best *big.Float
	for _, f := range fees {
		req.Fee = f
		quoteRes, err := p.callQuoteExactAmount(ctx, req)
		if err != nil {
			continue
		}
		price := derivePriceFromQuote(quoteRes, tokenInIsToken0)
		if price == nil || price.Sign() <= 0 {
			continue
		}
		if best == nil || price.Cmp(best) > 0 {
			best = price
		}
	}

	if best == nil {
		return nil, fmt.Errorf("quote API returned no price")
	}

	return best, nil
}

// callQuoteExactAmount makes the HTTP request and returns parsed quote data.
func (p *GSwapProvider) callQuoteExactAmount(ctx context.Context, reqBody QuoteExactAmountRequest) (*quoteResult, error) {
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal quote request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", quotingAPI, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create quote request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("quote API request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read quote response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quote API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var qr QuoteResponse
	if err := json.Unmarshal(bodyBytes, &qr); err != nil {
		return nil, fmt.Errorf("failed to decode quote response: %w", err)
	}

	if qr.Data == nil {
		return nil, fmt.Errorf("quote response missing data")
	}

	result := &quoteResult{}

	if amt0 := strings.TrimPrefix(qr.Data.Amount0, "-"); amt0 != "" {
		if val, ok := new(big.Float).SetString(amt0); ok {
			result.amount0 = val
		}
	}
	if amt1 := strings.TrimPrefix(qr.Data.Amount1, "-"); amt1 != "" {
		if val, ok := new(big.Float).SetString(amt1); ok {
			result.amount1 = val
		}
	}
	if qr.Data.CurrentSqrtPrice != "" {
		if val, ok := new(big.Float).SetString(qr.Data.CurrentSqrtPrice); ok {
			result.sqrtPrice = val
		}
	}

	if result.amount0 == nil && result.amount1 == nil && result.sqrtPrice == nil {
		return nil, fmt.Errorf("quote response missing amounts")
	}

	return result, nil
}

// derivePriceFromQuote converts raw quote data into a price for the base token (pair first element).
func derivePriceFromQuote(q *quoteResult, baseIsToken0 bool) *big.Float {
	if q == nil {
		return nil
	}

	// Prefer spot price from sqrt price if present.
	if q.sqrtPrice != nil && q.sqrtPrice.Sign() > 0 {
		price := new(big.Float).Mul(q.sqrtPrice, q.sqrtPrice)
		if !baseIsToken0 {
			price = new(big.Float).Quo(big.NewFloat(1), price)
		}
		return price
	}

	// Fallback to ratio of in/out amounts.
	if q.amount0 != nil && q.amount1 != nil && q.amount0.Sign() > 0 && q.amount1.Sign() > 0 {
		if baseIsToken0 {
			return new(big.Float).Quo(q.amount1, q.amount0)
		}
		return new(big.Float).Quo(q.amount0, q.amount1)
	}

	return nil
}

// mapCEXPairToGSwap converts typical CEX pair names into the wrapped-token pairs
// expected by GSwap. If no mapping exists, the original pair is returned.
func mapCEXPairToGSwap(pair string) string {
	pair = strings.ToUpper(pair)

	mappings := map[string]string{
		// Keep base = GALA, quote = stable
		"GALA/USDT": "GALA/GUSDT",
		"GALA/USDC": "GALA/GUSDC",
		"ETH/GALA":  "GWETH/GALA",
		"BTC/GALA":  "GWBTC/GALA",
		"SOL/GALA":  "GSOL/GALA",

		// Wrapped ETH vs USDC
		"ETH/USDC":  "GWETH/GUSDC",
		"WETH/USDC": "GWETH/GUSDC",
	}

	if mapped, ok := mappings[pair]; ok {
		return mapped
	}

	return pair
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
		Token0: token0Key,
		Token1: token1Key,
		Fee:    fee,
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
	if _, ok := sqrtPrice.SetString(sqrtPriceStr); !ok {
		return nil
	}

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
