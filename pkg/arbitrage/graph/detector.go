package graph

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Detector orchestrates graph-based arbitrage detection for GSwap.
type Detector struct {
	graph      *Graph
	cycleIndex *CycleIndex
	config     *Config
	apiClient  *APIClient

	// Callback for profitable opportunities
	onOpportunity func(*CycleResult)

	// State
	initialized bool
	mu          sync.RWMutex

	// Statistics
	stats DetectorStats
}

// DetectorStats tracks detector performance metrics.
type DetectorStats struct {
	CyclesEnumerated   int
	PriceUpdates       int
	OpportunitiesFound int
	LastUpdateTime     time.Time
	InitDuration       time.Duration
}

// NewDetector creates a new graph-based arbitrage detector.
func NewDetector(config *Config) *Detector {
	if config == nil {
		config = DefaultConfig()
	}

	return &Detector{
		config:    config,
		apiClient: NewAPIClient(config.APIURL),
	}
}

// SetOpportunityCallback sets the callback for when profitable cycles are found.
func (d *Detector) SetOpportunityCallback(cb func(*CycleResult)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onOpportunity = cb
}

// Initialize fetches pools from API, builds the graph, and enumerates all cycles.
func (d *Detector) Initialize(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	startTime := time.Now()

	// Fetch pools from API
	log.Printf("[detector] Fetching pools from %s...", d.config.APIURL)
	pools, err := d.apiClient.FetchPools(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch pools: %w", err)
	}
	log.Printf("[detector] Fetched %d pools", len(pools))

	// Build graph from pools
	log.Printf("[detector] Building graph with default fee %d bps...", d.config.DefaultFeeBps)
	d.graph = BuildGraphFromPools(pools, d.config.DefaultFeeBps)
	log.Printf("[detector] Graph has %d tokens and %d edges", d.graph.TokenCount(), d.graph.EdgeCount())

	// Fetch token prices and update graph
	log.Printf("[detector] Fetching token prices...")
	tokens, err := d.apiClient.FetchTokens(ctx)
	if err != nil {
		log.Printf("[detector] Warning: failed to fetch token prices: %v", err)
		// Continue without prices - they'll be updated later
	} else {
		log.Printf("[detector] Fetched prices for %d tokens", len(tokens))
		UpdateGraphFromTokenPrices(d.graph, tokens)
	}

	// Enumerate all cycles
	log.Printf("[detector] Enumerating cycles up to length %d...", d.config.MaxCycleLength)
	cycles := d.graph.EnumerateAllCycles(d.config.MaxCycleLength)
	log.Printf("[detector] Found %d unique cycles", len(cycles))

	// Build cycle index
	d.cycleIndex = BuildCycleIndex(cycles)

	d.stats.CyclesEnumerated = len(cycles)
	d.stats.InitDuration = time.Since(startTime)
	d.initialized = true

	log.Printf("[detector] Initialization complete in %v", d.stats.InitDuration)

	return nil
}

// HandlePriceUpdate updates an edge and evaluates affected cycles.
// Returns profitable cycles found (if any).
func (d *Detector) HandlePriceUpdate(tokenIn, tokenOut string, rate, liquidity float64) []*CycleResult {
	d.mu.RLock()
	if !d.initialized {
		d.mu.RUnlock()
		return nil
	}
	graph := d.graph
	index := d.cycleIndex
	minProfit := d.config.MinProfitBps
	callback := d.onOpportunity
	d.mu.RUnlock()

	// Update the edge
	from := Token(tokenIn)
	to := Token(tokenOut)

	if err := graph.UpdateEdge(from, to, rate, liquidity); err != nil {
		// Edge doesn't exist - this is fine, not all token pairs have edges
		return nil
	}

	// Get the edge index
	edgeIdx := graph.GetEdgeIndex(from, to)
	if edgeIdx < 0 {
		return nil
	}

	// Evaluate affected cycles
	results := graph.EvaluateAffectedCycles(edgeIdx, index, minProfit)

	// Update stats
	d.mu.Lock()
	d.stats.PriceUpdates++
	d.stats.LastUpdateTime = time.Now()
	if len(results) > 0 {
		d.stats.OpportunitiesFound += len(results)
	}
	d.mu.Unlock()

	// Trigger callbacks for profitable opportunities
	if callback != nil {
		for _, result := range results {
			callback(result)
		}
	}

	return results
}

// HandlePoolQuote handles a quote update for a pool pair.
// It updates both directions of the edge.
func (d *Detector) HandlePoolQuote(pair, tokenIn, tokenOut string, rate, reverseRate, liquidity float64) []*CycleResult {
	var allResults []*CycleResult

	// Update forward direction
	if rate > 0 {
		results := d.HandlePriceUpdate(tokenIn, tokenOut, rate, liquidity)
		allResults = append(allResults, results...)
	}

	// Update reverse direction
	if reverseRate > 0 {
		results := d.HandlePriceUpdate(tokenOut, tokenIn, reverseRate, liquidity)
		allResults = append(allResults, results...)
	}

	return allResults
}

// EvaluateAll evaluates all cycles and returns profitable ones.
func (d *Detector) EvaluateAll() []*CycleResult {
	d.mu.RLock()
	if !d.initialized {
		d.mu.RUnlock()
		return nil
	}
	graph := d.graph
	index := d.cycleIndex
	minProfit := d.config.MinProfitBps
	d.mu.RUnlock()

	return graph.EvaluateAllCycles(index, minProfit)
}

// RefreshPrices fetches current prices from the API and updates all edges.
// Note: This does NOT increment PriceUpdates counter. Use RefreshPricesIncremental instead.
func (d *Detector) RefreshPrices(ctx context.Context) error {
	d.mu.RLock()
	if !d.initialized {
		d.mu.RUnlock()
		return fmt.Errorf("detector not initialized")
	}
	graph := d.graph
	d.mu.RUnlock()

	tokens, err := d.apiClient.FetchTokens(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch tokens: %w", err)
	}

	UpdateGraphFromTokenPrices(graph, tokens)
	return nil
}

// RefreshPricesIncremental fetches current prices from CoinGecko (with arb.gala.com fallback)
// and uses HandlePriceUpdate for incremental updates. This properly increments the PriceUpdates
// counter and only re-evaluates cycles affected by changed edges.
// Returns all profitable cycles found across all updated edges.
func (d *Detector) RefreshPricesIncremental(ctx context.Context) ([]*CycleResult, error) {
	d.mu.RLock()
	if !d.initialized {
		d.mu.RUnlock()
		return nil, fmt.Errorf("detector not initialized")
	}
	graph := d.graph
	d.mu.RUnlock()

	// Fetch live prices from CoinGecko (with fallback to arb.gala.com)
	prices, err := d.apiClient.FetchLivePrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch live prices: %w", err)
	}

	// Track all profitable results
	var allResults []*CycleResult
	seen := make(map[int]bool) // Track seen cycle IDs to avoid duplicates

	// Update each edge and collect results
	edges := graph.Edges()
	for _, edge := range edges {
		fromPrice, fromOK := prices[string(edge.From)]
		toPrice, toOK := prices[string(edge.To)]

		if fromOK && toOK && fromPrice > 0 && toPrice > 0 {
			// Calculate rate: fromPrice / toPrice
			rate := fromPrice / toPrice

			// Only update if rate has changed meaningfully (> 0.0001% to filter float noise)
			// This uses relative difference to handle rates of different magnitudes
			if rateChanged(edge.Rate, rate, 1e-6) {
				// Use HandlePriceUpdate which increments stats and evaluates affected cycles
				results := d.HandlePriceUpdate(string(edge.From), string(edge.To), rate, 0)
				for _, r := range results {
					if !seen[r.Cycle.ID] {
						seen[r.Cycle.ID] = true
						allResults = append(allResults, r)
					}
				}
			}
		}
	}

	return allResults, nil
}

// rateChanged returns true if the rate has changed by more than the relative tolerance.
func rateChanged(oldRate, newRate, tolerance float64) bool {
	if oldRate == 0 {
		return newRate != 0
	}
	relDiff := (newRate - oldRate) / oldRate
	if relDiff < 0 {
		relDiff = -relDiff
	}
	return relDiff > tolerance
}

// Graph returns the underlying graph (for inspection).
func (d *Detector) Graph() *Graph {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.graph
}

// CycleIndex returns the cycle index (for inspection).
func (d *Detector) CycleIndex() *CycleIndex {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cycleIndex
}

// Stats returns current detector statistics.
func (d *Detector) Stats() DetectorStats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.stats
}

// IsInitialized returns whether the detector has been initialized.
func (d *Detector) IsInitialized() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.initialized
}

// SimulateCycleTrade simulates executing a cycle with a given input amount.
func (d *Detector) SimulateCycleTrade(cycleID int, inputAmount float64) (outputAmount, profit, fees float64, err error) {
	d.mu.RLock()
	if !d.initialized {
		d.mu.RUnlock()
		return 0, 0, 0, fmt.Errorf("detector not initialized")
	}
	graph := d.graph
	index := d.cycleIndex
	d.mu.RUnlock()

	// Find the cycle
	cycles := index.AllCycles()
	var cycle *Cycle
	for _, c := range cycles {
		if c.ID == cycleID {
			cycle = c
			break
		}
	}

	if cycle == nil {
		return 0, 0, 0, fmt.Errorf("cycle %d not found", cycleID)
	}

	output, fees := graph.SimulateTrade(cycle, inputAmount)
	profit = output - inputAmount

	return output, profit, fees, nil
}

// GetCycleDetails returns details about a specific cycle.
func (d *Detector) GetCycleDetails(cycleID int) (*Cycle, *CycleResult, error) {
	d.mu.RLock()
	if !d.initialized {
		d.mu.RUnlock()
		return nil, nil, fmt.Errorf("detector not initialized")
	}
	graph := d.graph
	index := d.cycleIndex
	d.mu.RUnlock()

	// Find the cycle
	cycles := index.AllCycles()
	var cycle *Cycle
	for _, c := range cycles {
		if c.ID == cycleID {
			cycle = c
			break
		}
	}

	if cycle == nil {
		return nil, nil, fmt.Errorf("cycle %d not found", cycleID)
	}

	result := graph.EvaluateCycle(cycle)
	return cycle, result, nil
}

// PrintGraphSummary logs a summary of the current graph state.
func (d *Detector) PrintGraphSummary() {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if !d.initialized {
		log.Printf("[detector] Not initialized")
		return
	}

	log.Printf("[detector] Graph Summary:")
	log.Printf("  Tokens: %d", d.graph.TokenCount())
	log.Printf("  Edges: %d", d.graph.EdgeCount())
	log.Printf("  Cycles: %d", d.cycleIndex.CycleCount())
	log.Printf("  Price updates: %d", d.stats.PriceUpdates)
	log.Printf("  Opportunities found: %d", d.stats.OpportunitiesFound)

	// List tokens
	tokens := d.graph.Tokens()
	log.Printf("  Token list: %v", tokens)
}
