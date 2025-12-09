# Bridge Integration for Arbitrage Trading

This document outlines the design for integrating cross-chain bridging capabilities into the arbitrage trading system.

## Current Architecture

The arbitrage bot currently:
1. Detects price differences between exchanges (GSwap DEX + CEXs like Binance, Kraken)
2. Executes simultaneous buy/sell orders on different venues
3. Assumes assets are **pre-positioned** on each exchange

**Key limitation**: The bot can only exploit opportunities where funds already exist on both sides. Cross-chain arbitrage (e.g., buy cheap GALA on Binance, sell high on GSwap) requires manual bridging.

## Problem Statement

After executing arbitrage trades, balances drift:
- Buy side accumulates quote currency debt (e.g., spent USDT)
- Sell side accumulates base currency surplus (e.g., excess GALA)

Without rebalancing, the bot eventually runs out of capital on one side and cannot continue trading.

## Integration Approaches

### Approach 1: Post-Trade Rebalancing (Recommended)

After executing arbitrage trades, monitor balance drift and bridge assets to rebalance inventory.

**Integration points in `pkg/executor/coordinator.go`:**

1. Track balance drift per exchange after each trade
2. When drift exceeds threshold (e.g., 20% of position), trigger bridge
3. Bridge surplus assets back to depleted exchange
4. Continue trading with rebalanced inventory

**Implementation sketch:**

```go
type BalanceManager struct {
    bridgeExecutor *bridge.BridgeExecutor
    driftThreshold float64  // e.g., 0.20 (20%)
    targetBalances map[string]map[string]*big.Float  // exchange -> token -> amount
}

func (bm *BalanceManager) CheckAndRebalance(ctx context.Context) error {
    for token, exchanges := range bm.calculateDrift() {
        if drift > bm.driftThreshold {
            // Bridge from surplus exchange to deficit exchange
            bm.bridgeExecutor.Bridge(ctx, &bridge.BridgeRequest{
                Token:     token,
                Amount:    surplusAmount,
                Direction: determineDirection(fromExchange, toExchange),
            })
        }
    }
    return nil
}
```

**Pros:**
- Low risk - bridges happen outside trading loop
- Doesn't block opportunity capture
- Simple to implement and test

**Cons:**
- Bridge delays (10-30 min) mean inventory can be depleted before rebalance completes

### Approach 2: Opportunistic Cross-Chain Arbitrage

Detect arbitrage opportunities that **include** the bridge cost and time:

```
ChainHop with Bridge:
  [Binance] --buy GALA--> [Bridge 15min] --transfer--> [GSwap] --sell GALA-->
```

**Modifications to `pkg/arbitrage/chain.go`:**

```go
type ChainHop struct {
    Exchange    string
    Action      string           // "buy", "sell", "transfer", "bridge"
    BridgeCost  *big.Float       // Bridge fees (gas + protocol)
    BridgeTime  time.Duration    // Expected delay
    // ...existing fields
}

func (cd *ChainDetector) evaluateChainWithBridge(chain []string) *ChainArbitrageOpportunity {
    // Factor in:
    // - Bridge fees (~$5-20 depending on gas)
    // - Price volatility risk during 15-30 min bridge time
    // - Opportunity cost of locked capital
}
```

**Profitability threshold must account for:**
- Bridge gas fees (variable, query from bridge API)
- Price movement risk during bridge delay
- Minimum spread of ~5-10% to be worthwhile (vs. 0.5% for same-chain arb)

**Pros:**
- Captures larger spreads that exist cross-chain
- Can exploit inefficiencies others can't

**Cons:**
- High risk due to price volatility during bridge time
- Capital locked during bridge

### Approach 3: Inventory Pre-Positioning

Proactively bridge assets based on predicted opportunity frequency:

```go
// pkg/inventory/manager.go (new package)

type InventoryManager struct {
    targetAllocation map[string]map[string]float64  // exchange -> token -> percentage
    bridgeExecutor   *bridge.BridgeExecutor
    opportunityStats *OpportunityStats              // Track where opportunities occur
}

func (im *InventoryManager) OptimizePositions(ctx context.Context) {
    // Analyze recent opportunities:
    // - If 70% of GALA opportunities are "buy GSwap, sell Binance"
    // - Ensure more GALA inventory on GSwap, more USDT on Binance

    for exchange, tokens := range im.calculateOptimalAllocation() {
        currentBalance := im.getBalance(exchange, token)
        targetBalance := im.targetAllocation[exchange][token]

        if needsRebalance(currentBalance, targetBalance) {
            im.bridgeExecutor.Bridge(ctx, ...)
        }
    }
}
```

**Pros:**
- Optimizes for observed market patterns
- Reduces reactive bridging

**Cons:**
- Requires historical data analysis
- Market patterns may shift

## Implementation Plan

### Phase 1: Balance Monitoring (Immediate Value)

Add visibility into balance drift without automated action.

**Tasks:**
1. Add balance tracking to `ArbitrageCoordinator`
2. Log balance drift warnings when inventory becomes skewed
3. Alert via Slack when manual bridging is recommended
4. Display current vs. target allocation in bot status

**New/Modified Files:**
- `pkg/executor/coordinator.go` - Add drift tracking
- `pkg/notifications/slack.go` - Add rebalance alerts

### Phase 2: Semi-Automated Rebalancing

Add CLI tooling for guided rebalancing decisions.

**Tasks:**
1. Add `--rebalance` CLI command to bot-trader
2. Detect when balances drift beyond threshold
3. Prompt user to confirm bridge operation
4. Execute bridge and wait for completion
5. Resume trading after rebalance

**New/Modified Files:**
- `cmd/bot-trader/main.go` - Add rebalance command
- `pkg/inventory/manager.go` - Balance analysis logic

### Phase 3: Automated Rebalancing

Full automation with safeguards (requires careful testing).

**Tasks:**
1. Background rebalancing goroutine in bot-trader
2. Configurable thresholds and limits
3. Bridge queuing (don't start new bridge while one pending)
4. Status tracking and notifications
5. Circuit breaker for repeated failures

**New/Modified Files:**
- `pkg/inventory/rebalancer.go` - Automated rebalancing logic
- `pkg/config/config.go` - Rebalancing configuration

### Phase 4: Cross-Chain Arbitrage Detection

Detect and execute arbitrage that spans the bridge.

**Tasks:**
1. Add bridge costs to `ChainHop` calculations
2. Model price volatility risk during bridge time
3. Only execute when `spread >> bridge_cost + volatility_risk`
4. Implement bridge-aware opportunity scoring
5. Add execution path that includes bridge step

**New/Modified Files:**
- `pkg/arbitrage/chain.go` - Add bridge hop type
- `pkg/arbitrage/volatility.go` - Price risk modeling

## New Package Structure

```
pkg/
├── inventory/
│   ├── manager.go      # Balance tracking across exchanges
│   ├── rebalancer.go   # Rebalancing decision logic
│   └── types.go        # Inventory data structures
├── arbitrage/
│   ├── chain.go        # Modified: add bridge hop support
│   └── volatility.go   # NEW: price volatility modeling
└── bridge/
    └── (existing)      # Bridge execution (already implemented)
```

## Configuration

```json
{
  "rebalancing": {
    "enabled": true,
    "auto_bridge": false,
    "drift_threshold_percent": 20,
    "min_bridge_amount_usd": 100,
    "max_pending_bridges": 1,
    "check_interval_seconds": 300,
    "target_allocations": {
      "gswap": {
        "GALA": 0.6,
        "USDT": 0.4
      },
      "binance": {
        "GALA": 0.4,
        "USDT": 0.6
      }
    }
  },
  "bridge": {
    "max_wait_time_minutes": 30,
    "gas_price_limit_gwei": 50,
    "retry_attempts": 2
  },
  "cross_chain_arbitrage": {
    "enabled": false,
    "min_spread_percent": 5.0,
    "max_bridge_time_minutes": 30,
    "volatility_buffer_percent": 2.0
  }
}
```

## Risk Considerations

| Risk | Mitigation |
|------|------------|
| Bridge delays (10-30 min) | Don't rely on bridged funds for active trading; queue rebalances |
| Failed bridges | Retry logic, alerting, manual intervention fallback |
| Gas cost spikes | Gas price limits, defer non-urgent bridges |
| Inventory lock during bridge | Maintain reserve on each side, limit bridge size |
| Price movement during bridge | Only bridge during low volatility; avoid cross-chain arb in volatile markets |
| Exchange rate risk | Bridge stablecoins when possible; hedge with futures |

## Success Metrics

- **Inventory utilization**: % of capital actively available for trading
- **Rebalance frequency**: How often bridges are needed
- **Rebalance cost**: Total fees spent on bridging
- **Opportunity capture rate**: % of detected opportunities that could be executed
- **Cross-chain profit** (Phase 4): Net profit from bridge-inclusive arbitrage

## Recommendation

Start with **Phase 1-2** (monitoring + semi-automated rebalancing). This provides immediate value with low risk:

1. Visibility into when rebalancing is needed
2. User-confirmed bridge operations
3. Foundation for future automation

Reserve **Phase 3-4** (full automation, cross-chain arb) for after the system has been battle-tested with manual oversight. The 10-30 minute bridge delays and price volatility make fully automated cross-chain arbitrage high-risk without extensive backtesting.
