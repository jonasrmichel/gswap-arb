package graph

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// NewGraph creates a new empty graph.
func NewGraph() *Graph {
	return &Graph{
		tokens:     make([]Token, 0),
		tokenIndex: make(map[Token]int),
		adj:        make([][]int, 0),
		edges:      make([]*Edge, 0),
		edgeIndex:  make(map[string]int),
	}
}

// AddToken adds a token to the graph if it doesn't exist.
// Returns the token's index.
func (g *Graph) AddToken(token Token) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	if idx, exists := g.tokenIndex[token]; exists {
		return idx
	}

	idx := len(g.tokens)
	g.tokens = append(g.tokens, token)
	g.tokenIndex[token] = idx
	g.adj = append(g.adj, make([]int, 0))
	return idx
}

// AddEdge adds a directed edge between two tokens.
// If the edge already exists, it updates the existing edge.
func (g *Graph) AddEdge(from, to Token, pair string, feeBps int) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Ensure tokens exist
	fromIdx, fromExists := g.tokenIndex[from]
	if !fromExists {
		fromIdx = len(g.tokens)
		g.tokens = append(g.tokens, from)
		g.tokenIndex[from] = fromIdx
		g.adj = append(g.adj, make([]int, 0))
	}

	toIdx, toExists := g.tokenIndex[to]
	if !toExists {
		toIdx = len(g.tokens)
		g.tokens = append(g.tokens, to)
		g.tokenIndex[to] = toIdx
		g.adj = append(g.adj, make([]int, 0))
	}

	// Check if edge already exists
	edgeKey := edgeKeyString(from, to)
	if edgeIdx, exists := g.edgeIndex[edgeKey]; exists {
		// Update existing edge
		g.edges[edgeIdx].Pair = pair
		g.edges[edgeIdx].FeeBps = feeBps
		return edgeIdx
	}

	// Create new edge
	edge := &Edge{
		From:      from,
		To:        to,
		Pair:      pair,
		FeeBps:    feeBps,
		Rate:      0,
		LogRate:   0,
		Liquidity: 0,
		Timestamp: time.Time{},
	}

	edgeIdx := len(g.edges)
	g.edges = append(g.edges, edge)
	g.edgeIndex[edgeKey] = edgeIdx
	g.adj[fromIdx] = append(g.adj[fromIdx], edgeIdx)

	return edgeIdx
}

// UpdateEdge updates the rate and liquidity for an existing edge.
func (g *Graph) UpdateEdge(from, to Token, rate, liquidity float64) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	edgeKey := edgeKeyString(from, to)
	edgeIdx, exists := g.edgeIndex[edgeKey]
	if !exists {
		return fmt.Errorf("edge %s -> %s does not exist", from, to)
	}

	edge := g.edges[edgeIdx]
	edge.Rate = rate
	edge.Liquidity = liquidity
	edge.Timestamp = time.Now()

	// Calculate log rate for cycle detection
	// LogRate = -log(rate × (1 - fee))
	// Negative cycle (sum of LogRates < 0) means profitable
	if rate > 0 {
		feeMultiplier := 1.0 - float64(edge.FeeBps)/10000.0
		edge.LogRate = -math.Log(rate * feeMultiplier)
	} else {
		edge.LogRate = math.Inf(1) // Invalid rate
	}

	return nil
}

// UpdateEdgeByIndex updates an edge by its index.
func (g *Graph) UpdateEdgeByIndex(edgeIdx int, rate, liquidity float64) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if edgeIdx < 0 || edgeIdx >= len(g.edges) {
		return fmt.Errorf("edge index %d out of range", edgeIdx)
	}

	edge := g.edges[edgeIdx]
	edge.Rate = rate
	edge.Liquidity = liquidity
	edge.Timestamp = time.Now()

	if rate > 0 {
		feeMultiplier := 1.0 - float64(edge.FeeBps)/10000.0
		edge.LogRate = -math.Log(rate * feeMultiplier)
	} else {
		edge.LogRate = math.Inf(1)
	}

	return nil
}

// GetEdge returns the edge between two tokens, or nil if it doesn't exist.
func (g *Graph) GetEdge(from, to Token) *Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()

	edgeKey := edgeKeyString(from, to)
	if edgeIdx, exists := g.edgeIndex[edgeKey]; exists {
		return g.edges[edgeIdx]
	}
	return nil
}

// GetEdgeIndex returns the index of an edge, or -1 if it doesn't exist.
func (g *Graph) GetEdgeIndex(from, to Token) int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	edgeKey := edgeKeyString(from, to)
	if edgeIdx, exists := g.edgeIndex[edgeKey]; exists {
		return edgeIdx
	}
	return -1
}

// GetEdgeByIndex returns an edge by its index.
func (g *Graph) GetEdgeByIndex(idx int) *Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if idx < 0 || idx >= len(g.edges) {
		return nil
	}
	return g.edges[idx]
}

// Tokens returns all tokens in the graph.
func (g *Graph) Tokens() []Token {
	g.mu.RLock()
	defer g.mu.RUnlock()

	result := make([]Token, len(g.tokens))
	copy(result, g.tokens)
	return result
}

// TokenCount returns the number of tokens in the graph.
func (g *Graph) TokenCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.tokens)
}

// EdgeCount returns the number of edges in the graph.
func (g *Graph) EdgeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.edges)
}

// Edges returns all edges in the graph.
func (g *Graph) Edges() []*Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()

	result := make([]*Edge, len(g.edges))
	copy(result, g.edges)
	return result
}

// Neighbors returns the indices of edges leaving the given token.
func (g *Graph) Neighbors(token Token) []int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	idx, exists := g.tokenIndex[token]
	if !exists {
		return nil
	}

	result := make([]int, len(g.adj[idx]))
	copy(result, g.adj[idx])
	return result
}

// NeighborsByIndex returns the indices of edges leaving the token at the given index.
func (g *Graph) NeighborsByIndex(tokenIdx int) []int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if tokenIdx < 0 || tokenIdx >= len(g.adj) {
		return nil
	}

	result := make([]int, len(g.adj[tokenIdx]))
	copy(result, g.adj[tokenIdx])
	return result
}

// TokenByIndex returns the token at the given index.
func (g *Graph) TokenByIndex(idx int) Token {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if idx < 0 || idx >= len(g.tokens) {
		return ""
	}
	return g.tokens[idx]
}

// TokenIndex returns the index of a token, or -1 if not found.
func (g *Graph) TokenIndex(token Token) int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if idx, exists := g.tokenIndex[token]; exists {
		return idx
	}
	return -1
}

// String returns a string representation of the graph.
func (g *Graph) String() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Graph: %d tokens, %d edges\n", len(g.tokens), len(g.edges)))

	sb.WriteString("Tokens: ")
	for i, t := range g.tokens {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(string(t))
	}
	sb.WriteString("\n")

	sb.WriteString("Edges:\n")
	for _, e := range g.edges {
		sb.WriteString(fmt.Sprintf("  %s -> %s (pair=%s, rate=%.6f, fee=%dbps)\n",
			e.From, e.To, e.Pair, e.Rate, e.FeeBps))
	}

	return sb.String()
}

// edgeKeyString creates a unique key for an edge.
func edgeKeyString(from, to Token) string {
	return string(from) + ":" + string(to)
}
