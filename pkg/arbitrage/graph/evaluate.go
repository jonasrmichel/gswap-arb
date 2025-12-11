package graph

import (
	"math"
	"sort"
	"time"
)

// EvaluateCycle computes the profit for a cycle at current edge rates.
// Returns a CycleResult with profit information.
func (g *Graph) EvaluateCycle(c *Cycle) *CycleResult {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.evaluateCycleUnlocked(c)
}

// evaluateCycleUnlocked evaluates a cycle without acquiring the lock.
// Caller must hold at least a read lock.
func (g *Graph) evaluateCycleUnlocked(c *Cycle) *CycleResult {
	result := &CycleResult{
		Cycle:        c,
		ProfitBps:    0,
		ProfitRatio:  0,
		MinLiquidity: math.MaxFloat64,
		IsValid:      false,
		Timestamp:    time.Now(),
	}

	if len(c.EdgeIdxs) == 0 {
		return result
	}

	// Sum the log rates for the cycle
	// If sum < 0, the cycle is profitable
	logSum := 0.0
	for _, edgeIdx := range c.EdgeIdxs {
		if edgeIdx < 0 || edgeIdx >= len(g.edges) {
			return result
		}

		edge := g.edges[edgeIdx]
		if edge.Rate <= 0 {
			return result // Invalid rate
		}

		logSum += edge.LogRate

		if edge.Liquidity < result.MinLiquidity {
			result.MinLiquidity = edge.Liquidity
		}
	}

	// Convert log sum to profit ratio
	// profit_ratio = exp(-log_sum)
	// If log_sum < 0, profit_ratio > 1 (profitable)
	result.ProfitRatio = math.Exp(-logSum)

	// Calculate profit in basis points
	// profit_bps = (profit_ratio - 1) * 10000
	if result.ProfitRatio > 1.0 {
		result.ProfitBps = int((result.ProfitRatio - 1.0) * 10000)
		result.IsValid = true
	} else {
		result.ProfitBps = int((result.ProfitRatio - 1.0) * 10000) // Will be negative
		result.IsValid = false
	}

	return result
}

// EvaluateAffectedCycles evaluates all cycles containing the given edge.
// Returns only cycles meeting the minimum profit threshold, sorted by profit.
func (g *Graph) EvaluateAffectedCycles(edgeIdx int, index *CycleIndex, minProfitBps int) []*CycleResult {
	cycles := index.CyclesByEdge(edgeIdx)
	if len(cycles) == 0 {
		return nil
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	var results []*CycleResult
	for _, c := range cycles {
		result := g.evaluateCycleUnlocked(c)
		if result.IsValid && result.ProfitBps >= minProfitBps {
			results = append(results, result)
		}
	}

	// Sort by profit descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].ProfitBps > results[j].ProfitBps
	})

	return results
}

// EvaluateAllCycles evaluates all cycles in the index.
// Returns only cycles meeting the minimum profit threshold, sorted by profit.
func (g *Graph) EvaluateAllCycles(index *CycleIndex, minProfitBps int) []*CycleResult {
	cycles := index.AllCycles()
	if len(cycles) == 0 {
		return nil
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	var results []*CycleResult
	for _, c := range cycles {
		result := g.evaluateCycleUnlocked(c)
		if result.IsValid && result.ProfitBps >= minProfitBps {
			results = append(results, result)
		}
	}

	// Sort by profit descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].ProfitBps > results[j].ProfitBps
	})

	return results
}

// SimulateTrade simulates executing a cycle with a given input amount.
// Returns the output amount after all trades and fees.
func (g *Graph) SimulateTrade(c *Cycle, inputAmount float64) (outputAmount float64, fees float64) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	amount := inputAmount
	totalFees := 0.0

	for _, edgeIdx := range c.EdgeIdxs {
		if edgeIdx < 0 || edgeIdx >= len(g.edges) {
			return 0, 0
		}

		edge := g.edges[edgeIdx]
		if edge.Rate <= 0 {
			return 0, 0
		}

		// Apply the trade: output = input * rate
		preTradeAmount := amount
		amount = amount * edge.Rate

		// Apply fee
		feeAmount := amount * float64(edge.FeeBps) / 10000.0
		amount -= feeAmount
		totalFees += feeAmount

		_ = preTradeAmount
	}

	return amount, totalFees
}

// FormatCycleResult returns a human-readable string for a cycle result.
func FormatCycleResult(r *CycleResult) string {
	if r == nil || r.Cycle == nil {
		return "<nil>"
	}

	path := ""
	for i, t := range r.Cycle.Path {
		if i > 0 {
			path += " -> "
		}
		path += string(t)
	}

	profitStr := ""
	if r.ProfitBps >= 0 {
		profitStr = "+" + formatBps(r.ProfitBps)
	} else {
		profitStr = formatBps(r.ProfitBps)
	}

	return path + " [" + profitStr + ", liq=" + formatFloat(r.MinLiquidity) + "]"
}

func formatBps(bps int) string {
	return formatFloat(float64(bps)/100.0) + "%"
}

func formatFloat(f float64) string {
	if f == math.MaxFloat64 {
		return "∞"
	}
	if f >= 1000000 {
		return formatFloatWithPrecision(f/1000000, 2) + "M"
	}
	if f >= 1000 {
		return formatFloatWithPrecision(f/1000, 2) + "K"
	}
	return formatFloatWithPrecision(f, 2)
}

func formatFloatWithPrecision(f float64, precision int) string {
	format := "%." + string(rune('0'+precision)) + "f"
	return sprintf(format, f)
}

// sprintf is a simple formatter to avoid importing fmt in hot paths
func sprintf(format string, f float64) string {
	// Simple implementation for common cases
	switch format {
	case "%.2f":
		i := int(f * 100)
		whole := i / 100
		frac := i % 100
		if frac < 0 {
			frac = -frac
		}
		sign := ""
		if f < 0 && whole == 0 {
			sign = "-"
		}
		if frac < 10 {
			return sign + itoa(whole) + ".0" + itoa(frac)
		}
		return sign + itoa(whole) + "." + itoa(frac)
	default:
		return itoa(int(f))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	sign := ""
	if i < 0 {
		sign = "-"
		i = -i
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return sign + s
}
