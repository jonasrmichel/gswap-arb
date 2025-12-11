package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// APIClient handles communication with the arb.gala.com API.
type APIClient struct {
	baseURL    string
	httpClient *http.Client
}

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
