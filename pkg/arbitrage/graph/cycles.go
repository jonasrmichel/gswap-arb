package graph

// EnumerateAllCycles finds all simple cycles up to maxLength using DFS.
// A cycle is a path that starts and ends at the same token without repeating
// any intermediate token.
func (g *Graph) EnumerateAllCycles(maxLength int) []*Cycle {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if maxLength < 3 {
		maxLength = 3 // Minimum meaningful cycle
	}

	var allCycles []*Cycle
	cycleID := 0

	// Start DFS from each token
	for startIdx := range g.tokens {
		startToken := g.tokens[startIdx]

		// Track visited tokens for this DFS
		visited := make([]bool, len(g.tokens))
		path := make([]Token, 0, maxLength+1)
		edgePath := make([]int, 0, maxLength)

		cycles := g.dfs(startIdx, startIdx, visited, path, edgePath, maxLength, &cycleID)
		allCycles = append(allCycles, cycles...)

		// Clear the start token to avoid finding the same cycle multiple times
		// We only want cycles that start with the lexicographically smallest token
		_ = startToken
	}

	// Deduplicate cycles (same cycle can be found starting from different tokens)
	return deduplicateCycles(allCycles)
}

// dfs performs depth-first search to find cycles.
func (g *Graph) dfs(startIdx, currentIdx int, visited []bool, path []Token, edgePath []int, maxLen int, cycleID *int) []*Cycle {
	var cycles []*Cycle

	currentToken := g.tokens[currentIdx]
	path = append(path, currentToken)
	visited[currentIdx] = true

	// Explore neighbors
	for _, edgeIdx := range g.adj[currentIdx] {
		edge := g.edges[edgeIdx]
		nextIdx := g.tokenIndex[edge.To]

		// Check if we've completed a cycle back to start
		if nextIdx == startIdx && len(path) >= 2 {
			// Found a cycle! Create it.
			cyclePath := make([]Token, len(path)+1)
			copy(cyclePath, path)
			cyclePath[len(path)] = g.tokens[startIdx]

			cycleEdges := make([]int, len(edgePath)+1)
			copy(cycleEdges, edgePath)
			cycleEdges[len(edgePath)] = edgeIdx

			cycle := &Cycle{
				ID:       *cycleID,
				Path:     cyclePath,
				EdgeIdxs: cycleEdges,
				Length:   len(cyclePath) - 1,
			}
			*cycleID++
			cycles = append(cycles, cycle)
			continue
		}

		// Continue DFS if not visited and within max length
		if !visited[nextIdx] && len(path) < maxLen {
			newEdgePath := append(edgePath, edgeIdx)
			foundCycles := g.dfs(startIdx, nextIdx, visited, path, newEdgePath, maxLen, cycleID)
			cycles = append(cycles, foundCycles...)
		}
	}

	visited[currentIdx] = false
	return cycles
}

// deduplicateCycles removes duplicate cycles.
// Two cycles are considered the same if they contain the same edges in the same order
// (regardless of starting point).
func deduplicateCycles(cycles []*Cycle) []*Cycle {
	seen := make(map[string]bool)
	var result []*Cycle

	for _, c := range cycles {
		key := canonicalCycleKey(c)
		if !seen[key] {
			seen[key] = true
			result = append(result, c)
		}
	}

	return result
}

// canonicalCycleKey creates a canonical string key for a cycle.
// This ensures the same cycle starting from different points gets the same key.
func canonicalCycleKey(c *Cycle) string {
	if len(c.Path) < 2 {
		return ""
	}

	// Find the lexicographically smallest rotation
	tokens := c.Path[:len(c.Path)-1] // Exclude the repeated start token
	n := len(tokens)

	// Find index of smallest token
	minIdx := 0
	for i := 1; i < n; i++ {
		if tokens[i] < tokens[minIdx] {
			minIdx = i
		}
	}

	// Build key starting from smallest token
	key := ""
	for i := 0; i < n; i++ {
		idx := (minIdx + i) % n
		if i > 0 {
			key += "->"
		}
		key += string(tokens[idx])
	}

	return key
}

// BuildCycleIndex creates an index mapping edges to cycles containing them.
func BuildCycleIndex(cycles []*Cycle) *CycleIndex {
	index := &CycleIndex{
		cycles:  cycles,
		byEdge:  make(map[int][]*Cycle),
		byToken: make(map[Token][]*Cycle),
	}

	for _, c := range cycles {
		// Index by edge
		for _, edgeIdx := range c.EdgeIdxs {
			index.byEdge[edgeIdx] = append(index.byEdge[edgeIdx], c)
		}

		// Index by token (excluding the repeated end token)
		for i := 0; i < len(c.Path)-1; i++ {
			token := c.Path[i]
			// Check if already added for this cycle
			found := false
			for _, existing := range index.byToken[token] {
				if existing.ID == c.ID {
					found = true
					break
				}
			}
			if !found {
				index.byToken[token] = append(index.byToken[token], c)
			}
		}
	}

	return index
}

// CyclesByEdge returns all cycles containing the given edge index.
func (idx *CycleIndex) CyclesByEdge(edgeIdx int) []*Cycle {
	return idx.byEdge[edgeIdx]
}

// CyclesByToken returns all cycles containing the given token.
func (idx *CycleIndex) CyclesByToken(token Token) []*Cycle {
	return idx.byToken[token]
}

// AllCycles returns all cycles in the index.
func (idx *CycleIndex) AllCycles() []*Cycle {
	return idx.cycles
}

// CycleCount returns the total number of cycles.
func (idx *CycleIndex) CycleCount() int {
	return len(idx.cycles)
}
