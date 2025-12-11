// Package graph provides graph-based arbitrage detection for GalaChain DEX.
package graph

import (
	"sync"
	"time"
)

// Token represents a node in the arbitrage graph (a tradeable asset).
type Token string

// Edge represents a directed trading path between two tokens.
type Edge struct {
	From      Token   // Source token
	To        Token   // Destination token
	Pair      string  // Original pool pair name (e.g., "GALA/GUSDT")
	Rate      float64 // Conversion rate (amount_out / amount_in)
	LogRate   float64 // -log(rate × (1-fee)) for cycle detection
	FeeBps    int     // Fee in basis points
	Liquidity float64 // Available liquidity
	Timestamp time.Time
}

// Graph holds all tokens and edges for arbitrage detection.
type Graph struct {
	tokens     []Token
	tokenIndex map[Token]int // token -> index for fast lookup
	adj        [][]int       // adjacency list: tokenIdx -> [edgeIdx...]
	edges      []*Edge       // all edges
	edgeIndex  map[string]int // "FROM:TO" -> edge index
	mu         sync.RWMutex
}

// Cycle represents a pre-computed arbitrage cycle.
type Cycle struct {
	ID       int
	Path     []Token // e.g., [GALA, GUSDC, GWETH, GALA]
	EdgeIdxs []int   // indices into Graph.edges for fast lookup
	Length   int     // number of hops (len(Path) - 1)
}

// CycleIndex maps edges to cycles containing them for fast lookup.
type CycleIndex struct {
	cycles  []*Cycle
	byEdge  map[int][]*Cycle   // edgeIdx -> cycles containing this edge
	byToken map[Token][]*Cycle // token -> cycles containing this token
}

// CycleResult is the evaluation of a cycle at current prices.
type CycleResult struct {
	Cycle        *Cycle
	ProfitBps    int       // Profit in basis points
	ProfitRatio  float64   // >1 means profitable
	MinLiquidity float64   // Minimum liquidity along the path
	IsValid      bool      // Whether the cycle is currently profitable
	Timestamp    time.Time // When this result was computed
}

// Pool represents a trading pool from the API.
type Pool struct {
	ID             int    `json:"id"`
	PoolPair       string `json:"poolPair"`
	TokenInSymbol  string `json:"tokenInSymbol"`
	TokenOutSymbol string `json:"tokenOutSymbol"`
	IsActive       int    `json:"isActive"`
}

// TokenInfo represents token information from the API.
type TokenInfo struct {
	ID               int     `json:"id"`
	Symbol           string  `json:"symbol"`
	Name             string  `json:"name"`
	GalaPrice        float64 `json:"galaPrice"`
	CoinGeckoPrice   float64 `json:"coinGeckoPrice"`
	SpreadPercentage float64 `json:"spreadPercentage"`
}

// Config holds configuration for the graph detector.
type Config struct {
	MaxCycleLength int           // Maximum cycle length to enumerate (default: 5)
	MinProfitBps   int           // Minimum profit to report (default: 10)
	DefaultFeeBps  int           // Default fee if not specified (default: 100 = 1%)
	StaleThreshold time.Duration // When to consider prices stale
	PollInterval   time.Duration // How often to poll for prices
	APIURL         string        // Base URL for arb.gala.com API
}

// DefaultConfig returns default configuration.
func DefaultConfig() *Config {
	return &Config{
		MaxCycleLength: 5,
		MinProfitBps:   10,
		DefaultFeeBps:  100, // 1%
		StaleThreshold: 30 * time.Second,
		PollInterval:   5 * time.Second,
		APIURL:         "https://arb.gala.com/api",
	}
}
