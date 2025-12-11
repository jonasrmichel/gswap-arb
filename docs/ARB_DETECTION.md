# Graph-Based Arbitrage Detection for GalaChain DEX

## Overview

Replace the current combinatorial arbitrage detection with a graph-based path discovery system for **GalaChain DEX (GSwap) only**. The goal is to find profitable trading cycles within GSwap's token pairs.

## Data Source

**API**: `https://arb.gala.com/api/`
- `/tokens` - List all tokens with prices
- `/pools` - List all active trading pools

## GSwap Token Universe (~20 tokens, 50+ pools)

**Core Tokens**:
| Token | Description |
|-------|-------------|
| GALA | Native GalaChain token (hub) |
| GUSDT | Wrapped Tether |
| GUSDC | Wrapped USD Coin |
| GSUSDT | Solana-bridged USDT |
| GWETH | Wrapped Ethereum |
| GWBTC | Wrapped Bitcoin |
| GSOL | Wrapped Solana |
| GWXRP | Wrapped XRP |
| GWTRX | Wrapped TRON |
| GUSDUC | GalaChain stablecoin |

**Ecosystem Tokens**:
| Token | Description |
|-------|-------------|
| GOSMI | Osmi token |
| $GMUSIC | Gala Music token |
| FILM | Gala Film token |
| GMEW | Cat memecoin |
| GUFD | Unicorn Fart Dust |
| GNC | GNC token |
| GFINANCE | Gala Finance |
| GSHRAP | Shrapnel token |
| Materium | Game currency |
| SILK | Spider Tanks currency |

**Key Trading Pools** (28+ active, excluding launchpad tokens):
```
GALA/GWETH    GALA/GUSDC    GALA/GUSDT    GALA/GSOL
GALA/GWBTC    GALA/GMEW     GALA/GUSDUC   GALA/GUFD
GALA/GOSMI    GALA/GNC      GALA/GFINANCE GALA/GSHRAP
GALA/GWTRX    GWBTC/GWETH   GWETH/GWTRX   GWETH/GUSDC
GSOL/GSUSDT   GSOL/GUSDC    GUSDC/GWXRP   GSUSDT/GUSDC
GSUSDT/GUSDT  GOSMI/GUSDT   GOSMI/GUSDC   GOSMI/GWBTC
$GMUSIC/GOSMI $GMUSIC/FILM  FILM/GWETH    Materium/SILK
```

## Graph Model

**Scale**: ~20 nodes, ~56 directed edges (28 pairs × 2 directions)

**Nodes**: Tokens on GSwap
- GALA is the hub (connected to most tokens)
- Stablecoins form a cluster (GUSDT, GUSDC, GSUSDT, GUSDUC)
- GOSMI is another hub (connected to GALA, GUSDT, GUSDC, GWBTC, $GMUSIC, FILM)

**Edges**: Trading pairs (bidirectional with different rates)
- Each pair creates 2 directed edges (buy vs sell direction)
- Edge weight: `-log(rate × (1 - fee))` for cycle detection

**Graph Structure** (simplified):
```
                    GWBTC ←――――→ GWETH ←――――→ GWTRX
                      ↑           ↑
                      |           |
    GSOL ←→ GSUSDT ←→ GUSDC ←――→ GALA ←――――→ [many tokens]
              ↓         ↓         ↓
            GUSDT ←―――――+―――――→ GOSMI ←→ $GMUSIC ←→ FILM
                                  ↓
                                GWBTC
```

**Arbitrage Cycle Examples**:
```
3-hop: GALA → GUSDC → GWETH → GALA
4-hop: GALA → GOSMI → GUSDC → GWETH → GALA
4-hop: GUSDC → GSUSDT → GUSDT → GOSMI → GUSDC
```

## Algorithm Selection

With ~20 nodes and ~56 edges, the graph is medium-sized. Key considerations:

| Approach | Complexity | Pros | Cons |
|----------|------------|------|------|
| **Pre-computed cycles** | O(k) per update | Fastest lookup | Must enumerate all cycles once |
| **Bellman-Ford** | O(V×E) = O(1120) | Finds ANY negative cycle | Doesn't find ALL cycles |
| **DFS enumeration** | O(cycles) | Simple | May be slow for many cycles |

**Recommendation: Hybrid approach**

1. **At startup**: Enumerate all cycles up to length 5 using DFS (~500-2000 cycles)
2. **On price update**: Re-evaluate only affected cycles in O(k) where k = cycles containing changed edge
3. **Fallback**: If graph topology changes (new pool), re-enumerate

```
Startup
   │
   ▼
┌─────────────────────┐
│ Enumerate all cycles│  DFS, max depth 5
│ Cache cycle list    │  ~500-2000 cycles total
│ Index by edge       │  cycles_by_edge[pair] = [cycle1, cycle2, ...]
└─────────────────────┘

Per Price Update
   │
   ▼
┌─────────────────────┐
│ 1. Update Edge(s)   │  O(1)
└─────────────────────┘
   │
   ▼
┌─────────────────────┐
│ 2. Get affected     │  O(1) lookup
│    cycles from index│
└─────────────────────┘
   │
   ▼
┌─────────────────────┐
│ 3. Evaluate each    │  O(k × cycle_len)
│    affected cycle   │  k = ~20-50 cycles per edge
└─────────────────────┘
   │
   ▼
  Return profitable cycles (sorted by profit)
```

## Data Structures

```go
// Token represents a node in the graph
type Token string  // "GALA", "GUSDT", "GWETH", etc.

// Edge represents a directed trading path between two tokens
type Edge struct {
    From      Token
    To        Token
    Pair      string     // "GALA/GUSDT" (original pool name)
    Rate      float64    // Conversion rate (amount_out / amount_in)
    LogRate   float64    // -log(rate × (1-fee)) for cycle detection
    FeeBps    int        // Fee in basis points
    Liquidity float64    // Available liquidity
    Timestamp time.Time
}

// Graph holds all tokens and edges
type Graph struct {
    tokens     []Token
    tokenIndex map[Token]int          // token -> index for fast lookup
    adj        [][]int                // adjacency list: tokenIdx -> [edgeIdx...]
    edges      []*Edge                // all edges
    edgeIndex  map[string]int         // "FROM:TO" -> edge index
}

// Cycle represents a pre-computed arbitrage cycle
type Cycle struct {
    ID         int
    Path       []Token      // e.g., [GALA, GUSDC, GWETH, GALA]
    EdgeIdxs   []int        // indices into Graph.edges for fast lookup
    Length     int          // number of hops
}

// CycleIndex maps edges to cycles containing them
type CycleIndex struct {
    cycles       []*Cycle
    byEdge       map[int][]*Cycle  // edgeIdx -> cycles containing this edge
    byToken      map[Token][]*Cycle // token -> cycles containing this token
}

// CycleResult is the evaluation of a cycle at current prices
type CycleResult struct {
    Cycle        *Cycle
    ProfitBps    int
    ProfitRatio  float64    // >1 means profitable
    MinLiquidity float64
    IsValid      bool
    Timestamp    time.Time
}
```

## Implementation Plan

### Phase 1: Core Graph & Types
**File**: `pkg/arbitrage/graph/types.go`
- Token, Edge, Graph, Cycle, CycleIndex, CycleResult types

**File**: `pkg/arbitrage/graph/graph.go`
```go
func NewGraph() *Graph
func (g *Graph) AddToken(token Token)
func (g *Graph) AddEdge(from, to Token, pair string, feeBps int)
func (g *Graph) UpdateEdge(from, to Token, rate, liquidity float64)
func (g *Graph) GetEdge(from, to Token) *Edge
```

### Phase 2: Cycle Enumeration
**File**: `pkg/arbitrage/graph/cycles.go`
```go
// EnumerateAllCycles finds all simple cycles up to maxLength using DFS
func (g *Graph) EnumerateAllCycles(maxLength int) []*Cycle

// BuildCycleIndex creates index mapping edges to cycles
func BuildCycleIndex(cycles []*Cycle) *CycleIndex

// Internal DFS
func (g *Graph) findCyclesFromNode(start Token, maxLen int) []*Cycle
```

### Phase 3: Cycle Evaluation
**File**: `pkg/arbitrage/graph/evaluate.go`
```go
// EvaluateCycle computes profit for a cycle at current edge rates
func (g *Graph) EvaluateCycle(c *Cycle) *CycleResult

// EvaluateAffectedCycles evaluates all cycles containing the given edge
func (g *Graph) EvaluateAffectedCycles(edgeIdx int, index *CycleIndex, minProfitBps int) []*CycleResult
```

### Phase 4: GSwap Detector
**File**: `pkg/arbitrage/graph/detector.go`
```go
type GSwapGraphDetector struct {
    graph       *Graph
    cycleIndex  *CycleIndex
    config      *Config

    // Callbacks
    onOpportunity func(*CycleResult)
}

func NewGSwapGraphDetector(config *Config) *GSwapGraphDetector

// Initialize fetches pools from API, builds graph, enumerates cycles
func (d *GSwapGraphDetector) Initialize(ctx context.Context) error

// HandlePriceUpdate updates edge and evaluates affected cycles
func (d *GSwapGraphDetector) HandlePriceUpdate(pair string, rate, liquidity float64)
```

### Phase 5: API Client
**File**: `pkg/arbitrage/graph/api.go`
```go
// FetchPools fetches all pools from arb.gala.com/api/pools
func FetchPools(ctx context.Context) ([]Pool, error)

// FetchTokens fetches all tokens from arb.gala.com/api/tokens
func FetchTokens(ctx context.Context) ([]TokenInfo, error)
```

### Phase 6: Integration
**File**: `pkg/providers/websocket/gswap_poller.go`
- Modify to call `GSwapGraphDetector.HandlePriceUpdate()` on each quote

**File**: `cmd/bot-trader/main.go`
- Initialize GSwapGraphDetector at startup
- Wire up callbacks for profitable cycles

## File Structure

```
pkg/arbitrage/graph/
├── types.go           # Type definitions
├── graph.go           # Graph structure & operations
├── cycles.go          # Cycle enumeration (DFS)
├── evaluate.go        # Cycle profit evaluation
├── detector.go        # Main detector orchestration
└── api.go             # arb.gala.com API client
```

## Critical Files to Modify

| File | Action |
|------|--------|
| `pkg/arbitrage/graph/*.go` | NEW - Graph implementation |
| `pkg/providers/websocket/gswap_poller.go` | Call detector on price updates |
| `cmd/bot-trader/main.go` | Initialize and wire up detector |

## Example Cycles

**3-hop cycles** (~10-20):
```
GALA → GUSDC → GWETH → GALA
GALA → GOSMI → GUSDC → GALA
GOSMI → GUSDC → GWBTC → GOSMI
```

**4-hop cycles** (~50-100):
```
GALA → GUSDC → GWETH → GWBTC → GALA
GALA → GOSMI → GUSDT → GUSDC → GALA
GSOL → GSUSDT → GUSDC → GALA → GSOL
```

## Profit Calculation

For cycle `A → B → C → A` with edges e1, e2, e3:
```
profit_ratio = e1.Rate × e2.Rate × e3.Rate × (1-e1.Fee) × (1-e2.Fee) × (1-e3.Fee)

if profit_ratio > 1.0:
    profit_bps = (profit_ratio - 1.0) × 10000
```

Using `-log()` transformation for efficiency:
```
log_sum = e1.LogRate + e2.LogRate + e3.LogRate
if log_sum < 0:  // negative sum = profitable
    profit_ratio = exp(-log_sum)
```

**Break-even thresholds** (assuming 1% fee per trade):
- 3-hop: needs ~3.03% gross spread
- 4-hop: needs ~4.06% gross spread
- 5-hop: needs ~5.10% gross spread

## Performance Targets

| Operation | Target |
|-----------|--------|
| Cycle enumeration (startup) | <1 second |
| Edge update | <1 μs |
| Evaluate affected cycles | <100 μs |
| Total per price update | <200 μs |

## Testing Strategy

1. Unit tests for graph operations (add/update edges)
2. Unit tests for cycle enumeration (verify finds all cycles)
3. Unit tests for profit calculation (known scenarios)
4. Benchmark: cycle enumeration time for full graph
5. Benchmark: evaluation time per price update
6. Integration test with live GSwap API
