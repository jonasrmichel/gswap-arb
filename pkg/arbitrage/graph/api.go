package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// APIClient handles communication with the arb.gala.com API and CoinGecko.
type APIClient struct {
	baseURL    string
	httpClient *http.Client

	// Token metadata cache (symbol -> CoinGecko ID)
	tokenCoinGeckoIDs map[string]string
	tokenMu           sync.RWMutex
}

const (
	coinGeckoAPIURL = "https://api.coingecko.com/api/v3"
	gswapQuoteAPI   = "https://gateway-mainnet.galachain.com/api/asset/dexv3-contract/QuoteExactAmount"

	// Fee tiers (basis points)
	feeTier005 = 500   // 0.05%
	feeTier030 = 3000  // 0.30%
	feeTier100 = 10000 // 1.00%
)

// NewAPIClient creates a new API client.
func NewAPIClient(baseURL string) *APIClient {
	if baseURL == "" {
		baseURL = "https://arb.gala.com/api"
	}
	return &APIClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		tokenCoinGeckoIDs: make(map[string]string),
	}
}

// FetchPools fetches all pools from the API.
func (c *APIClient) FetchPools(ctx context.Context) ([]Pool, error) {
	url := c.baseURL + "/pools"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pools: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var pools []Pool
	if err := json.NewDecoder(resp.Body).Decode(&pools); err != nil {
		return nil, fmt.Errorf("failed to decode pools: %w", err)
	}

	return pools, nil
}

// FetchTokens fetches all tokens from the API.
func (c *APIClient) FetchTokens(ctx context.Context) ([]TokenInfo, error) {
	url := c.baseURL + "/tokens"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tokens: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokens []TokenInfo
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return nil, fmt.Errorf("failed to decode tokens: %w", err)
	}

	return tokens, nil
}

// BuildGraphFromPools creates a graph from a list of pools.
// Each pool creates two directed edges (buy and sell directions).
func BuildGraphFromPools(pools []Pool, defaultFeeBps int) *Graph {
	g := NewGraph()

	for _, pool := range pools {
		if pool.IsActive != 1 {
			continue
		}

		// Skip generic "Token" entries (launchpad tokens without specific names)
		if pool.TokenInSymbol == "Token" || pool.TokenOutSymbol == "Token" {
			continue
		}

		// Skip test tokens
		if strings.HasPrefix(pool.TokenInSymbol, "Test") || strings.HasPrefix(pool.TokenOutSymbol, "Test") {
			continue
		}

		tokenIn := Token(pool.TokenInSymbol)
		tokenOut := Token(pool.TokenOutSymbol)

		// Add both directions for the pool
		// Direction 1: tokenIn -> tokenOut (selling tokenIn for tokenOut)
		g.AddEdge(tokenIn, tokenOut, pool.PoolPair, defaultFeeBps)

		// Direction 2: tokenOut -> tokenIn (selling tokenOut for tokenIn)
		g.AddEdge(tokenOut, tokenIn, pool.PoolPair, defaultFeeBps)
	}

	return g
}

// PoolQuote represents a price quote for a pool.
type PoolQuote struct {
	Pair         string
	TokenIn      string
	TokenOut     string
	Rate         float64 // tokenOut per tokenIn
	ReverseRate  float64 // tokenIn per tokenOut
	Liquidity    float64
	Timestamp    time.Time
}

// UpdateGraphWithQuotes updates the graph edges with price quotes.
func UpdateGraphWithQuotes(g *Graph, quotes []PoolQuote) error {
	for _, q := range quotes {
		tokenIn := Token(q.TokenIn)
		tokenOut := Token(q.TokenOut)

		// Update forward direction (tokenIn -> tokenOut)
		if q.Rate > 0 {
			if err := g.UpdateEdge(tokenIn, tokenOut, q.Rate, q.Liquidity); err != nil {
				// Edge might not exist, which is fine
				_ = err
			}
		}

		// Update reverse direction (tokenOut -> tokenIn)
		if q.ReverseRate > 0 {
			if err := g.UpdateEdge(tokenOut, tokenIn, q.ReverseRate, q.Liquidity); err != nil {
				// Edge might not exist, which is fine
				_ = err
			}
		}
	}

	return nil
}

// TokenPrices maps token symbols to their prices (in some base unit, e.g., USD).
type TokenPrices map[string]float64

// CalculateRatesFromPrices calculates exchange rates from token prices.
// Rate from A to B = price(A) / price(B)
func CalculateRatesFromPrices(prices TokenPrices) map[string]float64 {
	rates := make(map[string]float64)

	for tokenA, priceA := range prices {
		for tokenB, priceB := range prices {
			if tokenA == tokenB || priceA <= 0 || priceB <= 0 {
				continue
			}
			key := tokenA + ":" + tokenB
			rates[key] = priceA / priceB
		}
	}

	return rates
}

// UpdateGraphFromTokenPrices updates graph edges using token price information.
// This uses the GalaPrice field from TokenInfo to calculate exchange rates.
func UpdateGraphFromTokenPrices(g *Graph, tokens []TokenInfo) {
	// Build price map
	prices := make(TokenPrices)
	for _, t := range tokens {
		if t.GalaPrice > 0 {
			prices[t.Symbol] = t.GalaPrice
		}
	}

	// Update edges
	edges := g.Edges()
	for _, edge := range edges {
		fromPrice, fromOK := prices[string(edge.From)]
		toPrice, toOK := prices[string(edge.To)]

		if fromOK && toOK && fromPrice > 0 && toPrice > 0 {
			// Rate = fromPrice / toPrice
			// e.g., if GALA = 0.007 USD and GWETH = 3200 USD
			// Rate GALA->GWETH = 0.007 / 3200 ≈ 0.0000022 GWETH per GALA
			rate := fromPrice / toPrice
			g.UpdateEdge(edge.From, edge.To, rate, 0) // Liquidity unknown from price API
		}
	}
}

// CacheTokenCoinGeckoIDs caches the CoinGecko IDs from token info for later use.
func (c *APIClient) CacheTokenCoinGeckoIDs(tokens []TokenInfo) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	for _, t := range tokens {
		if t.CoinGeckoID != "" {
			c.tokenCoinGeckoIDs[t.Symbol] = t.CoinGeckoID
		}
	}
}

// GetCoinGeckoIDs returns a list of unique CoinGecko IDs for the cached tokens.
func (c *APIClient) GetCoinGeckoIDs() []string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()

	seen := make(map[string]bool)
	var ids []string
	for _, id := range c.tokenCoinGeckoIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// FetchCoinGeckoPrices fetches live prices from CoinGecko API.
// Returns a map of CoinGecko ID -> USD price.
func (c *APIClient) FetchCoinGeckoPrices(ctx context.Context, ids []string) (map[string]float64, error) {
	if len(ids) == 0 {
		return make(map[string]float64), nil
	}

	// Build comma-separated list of IDs
	idList := strings.Join(ids, ",")
	url := fmt.Sprintf("%s/simple/price?ids=%s&vs_currencies=usd", coinGeckoAPIURL, idList)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch CoinGecko prices: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("CoinGecko rate limit exceeded (429)")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("CoinGecko API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response: {"bitcoin":{"usd":90000},"ethereum":{"usd":3200},...}
	var result map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode CoinGecko response: %w", err)
	}

	prices := make(map[string]float64)
	for id, data := range result {
		if usd, ok := data["usd"]; ok {
			prices[id] = usd
		}
	}

	return prices, nil
}

// FetchLivePrices fetches live prices from the GSwap DEX (on-chain) via QuoteExactAmount API.
// It queries each pool to get the current sqrtPrice and derives token prices in USD.
// Returns a map of token symbol -> USD price.
func (c *APIClient) FetchLivePrices(ctx context.Context) (TokenPrices, error) {
	prices := make(TokenPrices)

	// First, get pool list from arb.gala.com
	pools, err := c.FetchPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pools: %w", err)
	}

	// We need a reference price for GUSDC (assume 1 USD)
	prices["GUSDC"] = 1.0
	prices["GUSDT"] = 1.0
	prices["GSUSDT"] = 1.0

	// Build a set of unique tokens and their pool relationships
	tokenPools := make(map[string][]Pool)
	for _, pool := range pools {
		if pool.IsActive != 1 {
			continue
		}
		if pool.TokenInSymbol == "Token" || pool.TokenOutSymbol == "Token" {
			continue
		}
		tokenPools[pool.TokenInSymbol] = append(tokenPools[pool.TokenInSymbol], pool)
		tokenPools[pool.TokenOutSymbol] = append(tokenPools[pool.TokenOutSymbol], pool)
	}

	// Fetch prices for key pairs to establish USD values
	// Priority: get prices relative to stables first
	keyPairs := []struct {
		base, quote string
	}{
		{"GALA", "GUSDC"},
		{"GWETH", "GUSDC"},
		{"GWBTC", "GWETH"},
		{"GSOL", "GUSDC"},
		{"GWXRP", "GUSDC"},
		{"GWTRX", "GUSDC"},
		{"GMEW", "GALA"},
		{"GUFD", "GALA"},
		{"GOSMI", "GUSDC"},
		{"$GMUSIC", "GOSMI"},
		{"FILM", "GWETH"},
	}

	for _, pair := range keyPairs {
		rate, err := c.FetchGSwapPoolPrice(ctx, pair.base, pair.quote)
		if err != nil {
			log.Printf("[api] Warning: failed to fetch %s/%s price: %v", pair.base, pair.quote, err)
			continue
		}

		// Calculate USD price based on quote token price
		quotePrice, hasQuote := prices[pair.quote]
		if hasQuote && rate > 0 {
			prices[pair.base] = rate * quotePrice
		}
	}

	log.Printf("[api] Fetched %d token prices from GSwap DEX", len(prices))

	return prices, nil
}

// gswapQuoteRequest is the request body for QuoteExactAmount.
type gswapQuoteRequest struct {
	Token0     gswapTokenKey `json:"token0"`
	Token1     gswapTokenKey `json:"token1"`
	ZeroForOne bool          `json:"zeroForOne"`
	Fee        int           `json:"fee"`
	Amount     string        `json:"amount"`
}

// gswapTokenKey represents a GalaChain token identifier.
type gswapTokenKey struct {
	Collection    string `json:"collection"`
	Category      string `json:"category"`
	Type          string `json:"type"`
	AdditionalKey string `json:"additionalKey"`
}

// gswapQuoteResponse is the response from QuoteExactAmount.
type gswapQuoteResponse struct {
	Status int `json:"Status"`
	Data   *struct {
		Amount0          string `json:"amount0"`
		Amount1          string `json:"amount1"`
		CurrentSqrtPrice string `json:"currentSqrtPrice"`
		NewSqrtPrice     string `json:"newSqrtPrice"`
	} `json:"Data"`
}

// FetchGSwapPoolPrice fetches the current price for a token pair from GSwap DEX.
// Returns the rate: how much of quote token you get per 1 base token.
func (c *APIClient) FetchGSwapPoolPrice(ctx context.Context, baseSymbol, quoteSymbol string) (float64, error) {
	// Build token keys
	tkBase := gswapTokenKey{Collection: baseSymbol, Category: "Unit", Type: "none", AdditionalKey: "none"}
	tkQuote := gswapTokenKey{Collection: quoteSymbol, Category: "Unit", Type: "none", AdditionalKey: "none"}

	// Ensure correct ordering (GalaChain requires token0 < token1 lexicographically by collection)
	token0, token1 := tkBase, tkQuote
	baseIsToken0 := true
	if strings.Compare(baseSymbol, quoteSymbol) > 0 {
		token0, token1 = tkQuote, tkBase
		baseIsToken0 = false
	}

	// The GSwap API uses human-readable units, so amount=1 means 1 token
	amountIn := "1"

	// Try different fee tiers
	feeTiers := []int{feeTier100, feeTier030, feeTier005}
	var bestPrice float64

	for _, fee := range feeTiers {
		req := gswapQuoteRequest{
			Token0:     token0,
			Token1:     token1,
			ZeroForOne: baseIsToken0, // if base is token0, we sell token0 (zeroForOne=true)
			Fee:        fee,
			Amount:     amountIn,
		}

		price, err := c.callGSwapQuoteWithAmounts(ctx, req, baseIsToken0)
		if err != nil {
			continue
		}

		if price > bestPrice {
			bestPrice = price
		}
	}

	if bestPrice == 0 {
		return 0, fmt.Errorf("no price available for %s/%s", baseSymbol, quoteSymbol)
	}

	return bestPrice, nil
}

// callGSwapQuoteWithAmounts makes the HTTP request and calculates price from the returned amounts.
// This is more reliable than using sqrtPrice for most cases.
func (c *APIClient) callGSwapQuoteWithAmounts(ctx context.Context, req gswapQuoteRequest, baseIsToken0 bool) (float64, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", gswapQuoteAPI, bytes.NewReader(jsonBody))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var qr gswapQuoteResponse
	if err := json.Unmarshal(body, &qr); err != nil {
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	if qr.Data == nil {
		return 0, fmt.Errorf("no price data in response")
	}

	// Parse amounts (strip negative signs)
	amt0Str := strings.TrimPrefix(qr.Data.Amount0, "-")
	amt1Str := strings.TrimPrefix(qr.Data.Amount1, "-")

	amt0, err := parseFloat(amt0Str)
	if err != nil || amt0 == 0 {
		return 0, fmt.Errorf("invalid amount0: %s", qr.Data.Amount0)
	}

	amt1, err := parseFloat(amt1Str)
	if err != nil || amt1 == 0 {
		return 0, fmt.Errorf("invalid amount1: %s", qr.Data.Amount1)
	}

	// Calculate price based on which token is base
	// If baseIsToken0: we sent amount0 and received amount1, so price = amount1/amount0
	// If !baseIsToken0: we sent amount1 and received amount0, so price = amount0/amount1
	var price float64
	if baseIsToken0 {
		price = amt1 / amt0
	} else {
		price = amt0 / amt1
	}

	return price, nil
}

// parseFloat parses a string to float64.
func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}

// EdgeRate represents a direct rate quote for a trading edge.
type EdgeRate struct {
	From      string
	To        string
	Rate      float64 // How much "To" you get per 1 "From"
	FeeBps    int     // Fee tier that provided this rate
	Timestamp time.Time
	Error     error
}

// FetchDirectEdgeRates fetches direct DEX quotes for all edges in the graph.
// This queries the actual pool for each edge, ensuring accurate rates.
// Returns a map of "From:To" -> EdgeRate
func (c *APIClient) FetchDirectEdgeRates(ctx context.Context, edges []*Edge, maxConcurrent int) map[string]*EdgeRate {
	if maxConcurrent <= 0 {
		maxConcurrent = 10
	}

	results := make(map[string]*EdgeRate)
	var mu sync.Mutex

	// Use a semaphore to limit concurrency
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for _, edge := range edges {
		wg.Add(1)
		go func(e *Edge) {
			defer wg.Done()

			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			key := string(e.From) + ":" + string(e.To)
			result := &EdgeRate{
				From:      string(e.From),
				To:        string(e.To),
				Timestamp: time.Now(),
			}

			// Fetch direct quote from DEX
			rate, feeBps, err := c.FetchDirectPoolRate(ctx, string(e.From), string(e.To))
			if err != nil {
				result.Error = err
			} else {
				result.Rate = rate
				result.FeeBps = feeBps
			}

			mu.Lock()
			results[key] = result
			mu.Unlock()
		}(edge)
	}

	wg.Wait()
	return results
}

// FetchDirectPoolRate fetches the direct exchange rate for selling fromSymbol to get toSymbol.
// Returns: rate (how much toSymbol per 1 fromSymbol), feeBps (fee tier used), error
//
// IMPORTANT: Only uses the 1% fee tier (feeTier100) because:
// 1. Different fee tiers have different pools with vastly different rates
// 2. Using "best rate" across tiers breaks bidirectional consistency checks
// 3. 1% is the most common fee tier and likely has the most liquidity
func (c *APIClient) FetchDirectPoolRate(ctx context.Context, fromSymbol, toSymbol string) (float64, int, error) {
	tkFrom := gswapTokenKey{Collection: fromSymbol, Category: "Unit", Type: "none", AdditionalKey: "none"}
	tkTo := gswapTokenKey{Collection: toSymbol, Category: "Unit", Type: "none", AdditionalKey: "none"}

	// Order tokens (GalaChain requires token0 < token1 lexicographically)
	token0, token1 := tkFrom, tkTo
	fromIsToken0 := true
	if strings.Compare(fromSymbol, toSymbol) > 0 {
		token0, token1 = tkTo, tkFrom
		fromIsToken0 = false
	}

	// Only use 1% fee tier for consistency
	// Different fee tiers have completely different pools and rates
	req := gswapQuoteRequest{
		Token0:     token0,
		Token1:     token1,
		ZeroForOne: fromIsToken0,
		Fee:        feeTier100, // 1% fee tier only
		Amount:     "1",
	}

	rate, err := c.callGSwapQuoteWithAmounts(ctx, req, fromIsToken0)
	if err != nil {
		return 0, 0, fmt.Errorf("no valid pool for %s -> %s: %w", fromSymbol, toSymbol, err)
	}

	return rate, feeTier100, nil
}

// FetchDirectEdgeRatesForGraph is a convenience method that fetches rates for all edges in a graph.
// It validates:
// 1. Bidirectional consistency - if A->B * B->A is far from 1, both edges are marked invalid
// 2. Extreme rates - rates > 100 or < 0.01 indicate illiquid pools that won't work for practical trades
func (c *APIClient) FetchDirectEdgeRatesForGraph(ctx context.Context, g *Graph) (map[string]*EdgeRate, error) {
	edges := g.Edges()
	if len(edges) == 0 {
		return make(map[string]*EdgeRate), nil
	}

	log.Printf("[api] Fetching direct DEX quotes for %d edges...", len(edges))
	start := time.Now()

	rates := c.FetchDirectEdgeRates(ctx, edges, 20) // 20 concurrent requests

	invalidatedConsistency := 0
	invalidatedExtreme := 0

	// First pass: filter out extreme rates
	// Rates > 10 or < 0.1 indicate pools with significant price disparity
	// These pools may quote for 1 unit but often fail for practical trade amounts
	// due to insufficient liquidity. Conservative filtering is better than
	// reporting phantom arbitrage opportunities.
	for _, rate := range rates {
		if rate.Error != nil {
			continue
		}

		if rate.Rate > 10 || rate.Rate < 0.1 {
			rate.Error = fmt.Errorf("extreme rate %g for %s->%s (likely illiquid)",
				rate.Rate, rate.From, rate.To)
			invalidatedExtreme++
		}
	}

	// Second pass: validate bidirectional consistency
	// For each pair A->B and B->A, check that A->B * B->A is close to 1
	// If not, mark both as invalid (broken/illiquid pool)
	for _, rate := range rates {
		if rate.Error != nil {
			continue
		}

		// Find reverse edge
		reverseKey := rate.To + ":" + rate.From
		reverseRate, hasReverse := rates[reverseKey]
		if !hasReverse || reverseRate.Error != nil {
			continue
		}

		// Check consistency: A->B * B->A should be ~1 for liquid pools
		// With 1% fee per direction, expect ~0.98 for healthy pools
		// Allow range 0.90 to 1.02 to account for fees and slippage
		product := rate.Rate * reverseRate.Rate
		if product < 0.90 || product > 1.02 {
			rate.Error = fmt.Errorf("bidirectional inconsistency: %s->%s * %s->%s = %g (expected ~1)",
				rate.From, rate.To, reverseRate.From, reverseRate.To, product)
			reverseRate.Error = fmt.Errorf("bidirectional inconsistency: %s->%s * %s->%s = %g (expected ~1)",
				reverseRate.From, reverseRate.To, rate.From, rate.To, product)
			invalidatedConsistency += 2
		}
	}

	// Count successes and failures
	successes := 0
	failures := 0
	for _, r := range rates {
		if r.Error != nil {
			failures++
		} else {
			successes++
		}
	}

	log.Printf("[api] Fetched %d/%d edge rates in %v (%d failed, %d extreme, %d inconsistent)",
		successes, len(edges), time.Since(start).Round(time.Millisecond),
		failures-invalidatedExtreme-invalidatedConsistency, invalidatedExtreme, invalidatedConsistency)

	return rates, nil
}
