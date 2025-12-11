<p align="center">
  <img src="logo.svg" alt="GSwap Arbitrage Bot Logo" width="150" height="150">
</p>

<h1 align="center">GSwap Arbitrage Bot</h1>

<p align="center">
  A Golang-based cryptocurrency arbitrage bot that detects and executes trades on price discrepancies between GalaChain/GSwap (DEX) and major centralized exchanges (CEXs).
</p>

## Features

- **Multi-exchange support**: Monitors GSwap (GalaChain DEX), Solana DEXs, and major CEXs including:
  - **GSwap** (GalaChain DEX) - REST polling with 5s intervals
  - **Jupiter** (Solana DEX aggregator) - REST polling with configurable intervals
  - Binance (with Binance.US fallback)
  - Coinbase
  - Kraken
  - OKX
  - Bybit

- **Graph-based DEX arbitrage**: Finds profitable cycles within GSwap using pre-computed cycle enumeration
- **Real-time arbitrage detection**: Scans for price discrepancies across exchanges
- **Trade execution**: Execute arbitrage trades via CCXT (10+ CEX exchanges), GSwap DEX, and Jupiter (Solana)
- **Chain arbitrage**: Multi-hop arbitrage detection across 2-5 exchanges
- **Cross-chain arbitrage**: Detects opportunities spanning GalaChain/Ethereum bridge with volatility-aware risk adjustment
- **Inventory management**: Real-time balance monitoring across exchanges with drift detection
- **Auto-rebalancing**: Automated cross-chain bridging when inventory drifts from targets
- **WebSocket support**: Real-time price feeds for ultra-low latency detection
- **GSwap DEX integration**: Polls GalaChain composite pool API for DEX prices
- **Slack notifications**: Real-time alerts for opportunities, trades, drift alerts, and rebalancing events
- **Configurable thresholds**: Set minimum spread, profit margins, and trade sizes
- **Multiple output formats**: Text, JSON, or CSV
- **Dry-run mode**: Detect opportunities without executing trades (default)
- **Safety features**: Balance checking, rate limiting, circuit breakers, configurable trade limits
- **Bridge CLI**: Manual bridge transfers between GalaChain and Ethereum
- **Rebalance CLI**: Monitor balances and execute guided rebalancing operations

## Project Structure

```
gswap-arb/
├── cmd/
│   ├── bot/
│   │   └── main.go           # REST API-based bot (polling)
│   ├── bot-ws/
│   │   └── main.go           # WebSocket-based bot (real-time)
│   ├── bot-trader/
│   │   └── main.go           # Trading bot with execution
│   ├── bot-gswap/
│   │   └── main.go           # Graph-based GSwap cycle detector
│   ├── bridge/
│   │   └── main.go           # Bridge CLI for cross-chain transfers
│   └── rebalance/
│       └── main.go           # Inventory rebalancing CLI
├── pkg/
│   ├── arbitrage/
│   │   ├── detector.go       # Arbitrage detection logic
│   │   ├── chain.go          # Multi-hop chain arbitrage
│   │   ├── crosschain.go     # Cross-chain arbitrage with bridge support
│   │   ├── volatility.go     # Price volatility model for risk assessment
│   │   └── graph/            # Graph-based cycle detection
│   │       ├── types.go      # Graph data structures
│   │       ├── graph.go      # Graph operations
│   │       ├── cycles.go     # DFS cycle enumeration
│   │       ├── evaluate.go   # Cycle profit evaluation
│   │       ├── api.go        # arb.gala.com API client
│   │       └── detector.go   # Main detector orchestration
│   ├── config/
│   │   └── config.go         # Configuration management
│   ├── executor/
│   │   ├── types.go          # TradeExecutor interface & types
│   │   ├── ccxt_executor.go  # Unified CEX executor via CCXT
│   │   ├── gswap.go          # GSwap DEX executor
│   │   ├── solana.go         # Solana/Jupiter DEX executor
│   │   ├── registry.go       # Executor registry
│   │   └── coordinator.go    # Arbitrage execution coordinator
│   ├── jupiter/
│   │   ├── client.go         # Jupiter API client
│   │   └── types.go          # Jupiter API types & token definitions
│   ├── providers/
│   │   ├── provider.go       # Provider interface & registry
│   │   ├── gswap/
│   │   │   └── gswap.go      # GalaChain/GSwap provider
│   │   ├── solana/
│   │   │   └── jupiter.go    # Solana/Jupiter price provider
│   │   ├── cex/
│   │   │   └── cex.go        # CEX REST API providers
│   │   └── websocket/
│   │       ├── types.go      # WebSocket types & base provider
│   │       ├── aggregator.go # Multi-exchange price aggregator
│   │       ├── gswap_poller.go    # GSwap REST polling provider
│   │       ├── jupiter_poller.go  # Jupiter REST polling provider
│   │       ├── binance.go    # Binance WebSocket
│   │       ├── coinbase.go   # Coinbase WebSocket
│   │       ├── kraken.go     # Kraken WebSocket
│   │       ├── okx.go        # OKX WebSocket
│   │       └── bybit.go      # Bybit WebSocket
│   ├── bridge/
│   │   ├── types.go          # Bridge types and token configuration
│   │   └── bridge.go         # Bridge executor implementation
│   ├── inventory/
│   │   ├── types.go          # Inventory tracking data structures
│   │   ├── manager.go        # Balance monitoring and drift detection
│   │   └── rebalancer.go     # Automated rebalancing logic
│   ├── notifier/
│   │   └── slack.go          # Slack notification integration
│   ├── reporter/
│   │   └── reporter.go       # Output formatting & reporting
│   └── types/
│       └── types.go          # Core data structures
├── .env.example              # Example environment configuration
├── config.example.json       # Example configuration
├── go.mod
└── README.md
```

## Installation

```bash
# Clone the repository
git clone https://github.com/jonasrmichel/gswap-arb.git
cd gswap-arb

# Set up environment variables
cp .env.example .env
# Edit .env with your credentials

# Source environment variables (required before running any commands)
source .env

# Build
go build -o gswap-arb ./cmd/bot

# Or run directly
go run ./cmd/bot
```

## Usage

### REST API Mode (Polling)

The polling mode fetches prices at regular intervals using REST APIs:

```bash
# Run with default settings (15 second intervals)
./gswap-arb

# Run once and exit
./gswap-arb --once

# Use custom configuration file
./gswap-arb --config config.json

# Output in JSON format
./gswap-arb --format json

# Quiet mode (only show opportunities)
./gswap-arb --verbose=false
```

### WebSocket Mode (Real-Time)

The WebSocket mode provides real-time price updates with sub-second latency:

```bash
# Run WebSocket-based bot
go run ./cmd/bot-ws

# With custom config
go run ./cmd/bot-ws --config config.json

# Show all price updates (very verbose)
go run ./cmd/bot-ws --show-updates

# Output as JSON
go run ./cmd/bot-ws --format json
```

### Trading Bot Mode (Execution)

The trading bot combines real-time detection with trade execution:

```bash
# Build the trading bot
go build -o gswap-trader ./cmd/bot-trader

# Run in dry-run mode (default, recommended for testing)
./gswap-trader

# Run with custom settings
./gswap-trader --max-trade=50 --min-profit=30

# Run with LIVE trading (BE CAREFUL!)
./gswap-trader --dry-run=false --max-trade=10 --min-profit=50
```

**Warning**: Live trading involves real funds. Always test thoroughly in dry-run mode first.

### GSwap Graph Bot (DEX Cycle Detection & Execution)

The graph-based bot focuses solely on finding and executing arbitrage cycles within GalaChain DEX (GSwap). It uses a pre-computed cycle enumeration algorithm for fast detection and can automatically execute profitable cycles.

```bash
# Build the graph bot
go build -o bot-gswap ./cmd/bot-gswap

# Detection only (default)
./bot-gswap -continuous=false

# Continuous monitoring
./bot-gswap

# Enable trading with dry-run (simulates trades)
./bot-gswap -trade -dry-run

# Enable trading with auto-execution (BE CAREFUL!)
./bot-gswap -trade -dry-run=false -auto-execute -exec-min-profit=50

# Custom settings
./bot-gswap -max-cycle=4 -min-profit=20 -poll=10s -trade-size=500
```

#### How Graph-Based Detection Works

1. **Graph Construction**: Fetches all pools from `arb.gala.com/api/pools` and builds a directed graph where:
   - **Nodes** = Tokens (GALA, GUSDT, GWETH, etc.)
   - **Edges** = Trading pairs (bidirectional with different rates)
   - **Filtering**: Excludes test tokens and inactive pools

2. **Cycle Enumeration**: Uses DFS to find all simple cycles up to a maximum length (default 5 hops). For GSwap's ~25 tokens and ~63 pools, this discovers ~2,900 unique cycles in <300ms.

3. **Cycle Indexing**: Builds an index mapping each edge to the cycles containing it, enabling O(k) lookup when prices change.

4. **Direct DEX Quotes**: Instead of deriving rates from USD prices, the bot fetches direct quotes from GalaChain's `QuoteExactAmount` API:
   - Uses the **1% fee tier only** for consistent bidirectional rates
   - Validates rate consistency: A→B × B→A should be ~0.98 for healthy pools
   - Filters out extreme rates (>10x or <0.1x) indicating illiquid pools
   - Invalidates edges that fail to return valid quotes

5. **Profit Evaluation**: For each cycle, calculates profit using the product of exchange rates:
   ```
   profit_ratio = rate₁ × rate₂ × ... × rateₙ
   ```
   Note: DEX quotes already include fees, so no additional fee deduction is needed.
   Cycles with `profit_ratio > 1.0` are profitable.

6. **Continuous Monitoring**: Polls direct DEX quotes every N seconds and re-evaluates affected cycles for opportunities.

7. **Trade Execution** (optional): When enabled, executes profitable cycles by performing sequential swaps through the cycle path.

#### Graph Bot Flags - Detection

| Flag | Default | Description |
|------|---------|-------------|
| `-api` | `https://arb.gala.com/api` | GSwap API URL |
| `-max-cycle` | `5` | Maximum cycle length to detect |
| `-min-profit` | `10` | Minimum profit in bps to report |
| `-fee` | `100` | Default fee in basis points (1% = 100 bps) |
| `-poll` | `5s` | Price polling interval |
| `-continuous` | `true` | Run continuously (vs one-shot) |
| `-show-graph` | `false` | Print graph summary at startup |
| `-show-cycles` | `false` | Print all enumerated cycles |
| `-log` | `bot-gswap.log` | Log file path (empty to disable) |
| `-verbose` | `true` | Enable verbose output |

#### Graph Bot Flags - Trading

| Flag | Default | Description |
|------|---------|-------------|
| `-trade` | `false` | Enable trade execution (requires credentials) |
| `-dry-run` | `true` | Simulate trades without executing |
| `-auto-execute` | `false` | Automatically execute profitable cycles |
| `-trade-size` | `1` | Trade size for cycle execution (kept small due to pool liquidity) |
| `-max-trade-size` | `10` | Maximum trade size (pools are illiquid) |
| `-min-trade-size` | `0.1` | Minimum trade size |
| `-exec-min-profit` | `50` | Minimum profit in bps to execute |
| `-slippage` | `100` | Slippage tolerance in basis points |
| `-sim-amount` | `1` | Amount to use for simulation display |
| `-private-key` | - | GSwap private key (or `GSWAP_PRIVATE_KEY` env) |
| `-wallet` | - | Wallet address (or `GSWAP_WALLET_ADDRESS` env) |

**Note**: The default trade sizes are kept small (1 unit) because many GSwap pools have limited liquidity. The bot fetches direct DEX quotes from the GalaChain `QuoteExactAmount` API to ensure accurate pricing. Testing shows pools can handle up to 10-100 units with slight price impact at larger sizes.

#### Cycle Execution Flow

When trading is enabled, the bot executes cycles as follows:

1. **Balance Check**: Verifies sufficient balance of the starting token
2. **Sequential Swaps**: Executes each swap in the cycle path:
   - Gets quote for current swap
   - Applies slippage protection
   - Submits swap transaction
   - Waits for confirmation
3. **Profit Calculation**: Compares output to input amount
4. **Statistics Tracking**: Records success/failure and profit

**Example execution** for cycle `GALA -> GUSDC -> GWETH -> GALA`:
```
Step 1: Swap 100 GALA -> ~X GUSDC
Step 2: Swap X GUSDC -> ~Y GWETH
Step 3: Swap Y GWETH -> ~Z GALA
Profit: Z - 100 GALA
```

#### Execution Strategies

The bot implements several strategies to mitigate execution risk on multi-hop cycles:

**Strategy #5: Parallel Quote Pre-Validation**

Before executing any swaps, the bot fetches fresh quotes for all edges in the cycle in parallel. This validates that the opportunity still exists with current market prices:

```
1. Fetch quotes for all N edges simultaneously (parallel)
2. Calculate expected profit from fresh quotes
3. Apply profit scaling: longer cycles require higher minimum profit
4. Abort if fresh quotes show opportunity is no longer profitable
```

This prevents executing trades on stale price data and catches opportunities that have been scooped by others.

**Strategy #6: Mid-Cycle Bailout Detection**

During execution, after each successful swap, the bot checks the remaining path profitability:

```
After Step 1/3: Get fresh quotes for steps 2-3, calculate remaining profit
After Step 2/3: Get fresh quote for step 3, calculate remaining profit
```

If remaining profit drops below the bailout threshold (default: -100 bps / -1%), the bot logs an alert. Since DEX trades cannot be easily reversed, execution continues but the alert provides valuable post-mortem data.

**Profit Scaling by Cycle Length**

Longer cycles face more execution risk (more swaps = more time = more price movement). The bot automatically requires higher minimum profit for longer cycles:

```
Required Profit = Base Minimum + (Hops - 2) × Scaling Factor

Example with base=20 bps, scaling=20 bps per hop:
- 3-hop cycle: 20 + (3-2) × 20 = 40 bps minimum
- 4-hop cycle: 20 + (4-2) × 20 = 60 bps minimum
- 5-hop cycle: 20 + (5-2) × 20 = 80 bps minimum
```

**Configurable Execution Settings**

| Setting | Default | Description |
|---------|---------|-------------|
| `PreValidateQuotes` | `true` | Fetch fresh quotes before execution |
| `ProfitScalingPerHop` | `20` | Additional bps required per hop |
| `MidCycleBailout` | `true` | Check remaining profit after each swap |
| `BailoutThresholdBps` | `-100` | Bail if remaining path would lose >1% |
| `SwapDeadlineSecs` | `60` | Timeout per swap (faster than default) |
| `QuoteMaxAgeSecs` | `5` | Reject quotes older than this |
| `ValidateLiquidity` | `true` | Check price impact at trade size |
| `MaxPriceImpactBps` | `200` | Max 2% price impact per edge |

**Liquidity Validation via Price Impact**

The bot validates pool liquidity by comparing quotes at 1-unit vs the actual trade size. This detects insufficient liquidity before execution:

```
Price Impact = (1-unit rate - trade size rate) / 1-unit rate × 10000

Example: If GALA→GUSDT returns:
- 1 GALA = 0.0725 GUSDT (1-unit quote)
- 10 GALA = 0.0710 GUSDT per GALA (10-unit quote)
- Price impact = (0.0725 - 0.0710) / 0.0725 × 10000 = 207 bps (2.07%)
- If MaxPriceImpactBps = 200, this edge would be rejected
```

This approach:
- Uses existing DEX API (no separate liquidity endpoint needed)
- Catches real liquidity issues at the actual trade size
- Prevents phantom arbitrage from illiquid pools

#### Example Cycles

```
3-hop: GALA -> GUSDC -> GWETH -> GALA
4-hop: GALA -> GOSMI -> GUSDC -> GWETH -> GALA
5-hop: GUSDC -> GSUSDT -> GUSDT -> GOSMI -> GWBTC -> GUSDC
```

#### Verified Profitable Cycles

The following cycles have been verified profitable using direct DEX quotes:

| Cycle | Reported Profit | Simulated (1 unit) | Notes |
|-------|----------------|-------------------|-------|
| GALA → GOSMI → $GMUSIC → FILM → ETIME → GALA | +64% | 1 → 1.27 GALA (+27%) | Ecosystem token cycle |
| GALA → FILM → ETIME → GALA | +40% | 1 → 1.40 GALA (+40%) | Game token cycle |
| GALA → $GMUSIC → FILM → ETIME → GALA | +38% | 1 → 1.35 GALA (+35%) | Music/Film cycle |

**Note**: Actual simulation results may differ from reported profit due to:
- Price impact at larger trade sizes
- Some edges being invalidated due to failed quotes
- Rate fluctuations between quote fetches

#### Pool Liquidity Considerations

GSwap pools have limited liquidity compared to centralized exchanges. Testing shows:

| Trade Size | Behavior |
|------------|----------|
| 0.1 - 1 unit | Works reliably with minimal price impact |
| 1 - 10 units | Works with slight price impact (~2-3% reduction in profit) |
| 10 - 100 units | May work but with significant price impact |
| 100+ units | Often fails with "Not enough liquidity" errors |

**Recommendations**:
- Start with small trade sizes (1 unit) to verify cycle profitability
- Gradually increase size while monitoring slippage
- Use `-sim-amount` to test different trade sizes in simulation
- The default `-trade-size=1` is conservative but reliable

#### Break-Even Thresholds

With 1% fee per trade:
- **3-hop cycle**: Needs ~3.03% gross spread to break even
- **4-hop cycle**: Needs ~4.06% gross spread to break even
- **5-hop cycle**: Needs ~5.10% gross spread to break even

### Bridge CLI (Cross-Chain Transfers)

The bridge CLI allows bidirectional transfers between GalaChain and Ethereum:

```bash
# First, source your environment variables
source .env

# Build the bridge CLI
go build -o gswap-bridge ./cmd/bridge

# List supported tokens
./gswap-bridge --list

# Check your GalaChain balances
./gswap-bridge --balance

# Bridge 100 GALA from GalaChain to Ethereum
./gswap-bridge --direction to-eth --token GALA --amount 100

# Bridge 50 GUSDC from Ethereum to GalaChain
./gswap-bridge --direction to-gala --token GUSDC --amount 50

# Check bridge transaction status
./gswap-bridge --status <transaction-id>
```

#### Bridge Directions

| Direction | Description | Requirements |
|-----------|-------------|--------------|
| `to-eth` | GalaChain → Ethereum | `GSWAP_PRIVATE_KEY`, `GALACHAIN_BRIDGE_WALLET_ADDRESS` |
| `to-gala` | Ethereum → GalaChain | `GSWAP_PRIVATE_KEY`, `GALACHAIN_BRIDGE_WALLET_ADDRESS`, `ETH_RPC_URL` |
| `to-sol` | GalaChain → Solana | `GSWAP_PRIVATE_KEY`, `GALACHAIN_BRIDGE_WALLET_ADDRESS` |
| `from-sol` | Solana → GalaChain | `SOLANA_PRIVATE_KEY`, `SOLANA_WALLET_ADDRESS`, `GALACHAIN_BRIDGE_WALLET_ADDRESS` |

**Important**: The bridge requires `GALACHAIN_BRIDGE_WALLET_ADDRESS` with non-checksummed address format, which differs from the checksummed `GSWAP_WALLET_ADDRESS` used by the GSwap executor.

**GalaChain → Ethereum (`to-eth`)**:
- Uses the GalaConnect DEX API with EIP-712 signed requests
- Two-step process: RequestTokenBridgeOut → BridgeTokenOut
- Bridge fees paid in GALA

**Ethereum → GalaChain (`to-gala`)**:
- Connects directly to Ethereum via RPC
- Handles ERC-20 approval automatically (approves max on first use)
- For GALA token: uses EIP-2612 permit (no separate approval tx)
- Waits for Ethereum transaction confirmation before returning

#### Supported Bridge Tokens

| Token | Ethereum Address | Decimals | Permit | GalaChain Token Class |
|-------|------------------|----------|--------|----------------------|
| GALA | 0xd1d2Eb1B1e90B638588728b4130137D262C87cae | 8 | Yes | GALA\|Unit\|none\|none |
| GWETH | 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2 | 18 | No | GWETH\|Unit\|none\|none |
| GUSDC | 0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48 | 6 | No | GUSDC\|Unit\|none\|none |
| GUSDT | 0xdAC17F958D2ee523a2206206994597C13D831ec7 | 6 | No | GUSDT\|Unit\|none\|none |
| GWTRX | 0x50327c6c5a14DCaDE707ABad2E27eB517df87AB5 | 6 | No | GWTRX\|Unit\|none\|none |
| GWBTC | 0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599 | 8 | No | GWBTC\|Unit\|none\|none |
| BENE | 0x624d739b88429a4cac97c9282adc226620c025d1 | 18 | No | Token\|Unit\|BENE\|client:5c806869e7fd0e2384461ce9 |

**Note**: BENE uses the memecoin token class format on GalaChain, which differs from standard Gala-created tokens.

### Inventory Management System

The bot includes a comprehensive inventory management system to track balances, detect drift, and rebalance assets across exchanges. This is essential for sustained arbitrage trading, as trades naturally cause inventory to drift from optimal allocations.

#### Components

| Component | Description | Phase |
|-----------|-------------|-------|
| **Balance Monitoring** | Real-time tracking of balances across all connected exchanges | Phase 1 |
| **Drift Detection** | Calculates deviation from target allocations with configurable thresholds | Phase 1 |
| **Slack Alerts** | Notifications when drift exceeds warning or critical thresholds | Phase 1 |
| **Rebalance CLI** | Interactive tool for guided rebalancing with confirmation prompts | Phase 2 |
| **Recommendations** | Prioritized suggestions for which assets to bridge and where | Phase 2 |
| **Auto-Rebalancer** | Background process that automatically executes bridge operations | Phase 3 |
| **Circuit Breaker** | Safety mechanism that pauses auto-rebalancing after failures | Phase 3 |

#### Why Inventory Management Matters

After executing arbitrage trades, balances naturally drift:
- **Buy side** accumulates quote currency debt (e.g., spent USDT)
- **Sell side** accumulates base currency surplus (e.g., excess GALA)

Without rebalancing, the bot eventually runs out of capital on one side and cannot continue trading. The inventory management system solves this by:
1. Monitoring balance drift in real-time
2. Alerting when manual intervention is needed
3. Optionally auto-rebalancing via the GalaChain/Ethereum bridge

### Rebalance CLI (Phase 2)

The rebalance CLI helps monitor exchange balances and execute cross-chain rebalancing:

```bash
# First, source your environment variables
source .env

# Build the rebalance CLI
go build -o gswap-rebalance ./cmd/rebalance

# Check current balances and drift status
./gswap-rebalance --check

# Generate rebalance recommendations
./gswap-rebalance --recommend

# Execute a specific rebalance (interactive confirmation)
./gswap-rebalance --execute --token GALA --from gswap --to binance --amount 1000

# Execute and wait for bridge completion
./gswap-rebalance --execute --token GALA --from gswap --to binance --amount 1000 --wait

# Set custom drift threshold (default: 20%)
./gswap-rebalance --check --drift-threshold 15
```

#### Operation Modes

| Mode | Description |
|------|-------------|
| `--check` | Display current balances across all exchanges and calculate drift from target allocations |
| `--recommend` | Analyze drift and generate prioritized rebalance recommendations |
| `--execute` | Execute a specific rebalance operation with interactive confirmation |

#### Drift Detection

The rebalance CLI calculates "drift" as the deviation from target balance allocations:

- **Default Target**: Equal distribution across all exchanges (e.g., 50/50 for two exchanges)
- **Drift Threshold**: Default 20% - balances outside this range trigger warnings
- **Critical Drift**: Default 40% - severe imbalance requiring immediate attention

Example output:
```
================================================================================
BALANCE DRIFT STATUS
================================================================================

Currency: GALA
  Target Allocation: gswap=50.0%, binance=50.0%
  Current Allocation: gswap=65.0%, binance=35.0%

  Drift by Exchange:
    gswap: +15.0% (surplus)
    binance: -15.0% (deficit)

  Max Drift: 15.0%
  Status: ⚠️  WARNING - Needs Rebalancing
```

#### Execute Mode

When using `--execute`, the CLI provides an interactive flow:

1. Validates token, source, and destination
2. Displays current balances on both exchanges
3. Shows expected balance after rebalance
4. Prompts for confirmation before executing
5. Optionally waits for bridge completion with `--wait`

### Auto-Rebalancing (Phase 3)

The trading bot can be configured to automatically rebalance inventory when drift exceeds thresholds:

```bash
# Enable auto-rebalancing with the trading bot
./gswap-trader --auto-rebalance

# With Ethereum RPC for bidirectional bridging
./gswap-trader --auto-rebalance --eth-rpc https://eth.llamarpc.com

# Or set via environment variable
export ETH_RPC_URL=https://eth.llamarpc.com
./gswap-trader --auto-rebalance
```

#### How Auto-Rebalancing Works

1. **Drift Monitoring**: Every 5 minutes, the bot collects balance snapshots and calculates drift from target allocations
2. **Recommendation Generation**: When drift exceeds the threshold (default 20%), generates rebalance recommendations
3. **Bridge Execution**: If `require_confirmation=false`, automatically executes the bridge operation
4. **Status Tracking**: Polls bridge status until completion or timeout (default 30 minutes)
5. **Circuit Breaker**: After 3 consecutive failures, pauses auto-rebalancing for 30 minutes

#### Safety Features

| Feature | Description |
|---------|-------------|
| **Circuit Breaker** | Automatically pauses after consecutive failures to prevent runaway issues |
| **Confirmation Mode** | Default requires explicit confirmation before bridging |
| **Single Bridge Limit** | Only one bridge operation at a time |
| **Minimum Interval** | At least 10 minutes between rebalance operations |
| **Slack Notifications** | Real-time alerts for bridge start, completion, failures, and circuit breaker state |

#### Configuration

Auto-rebalancing can be configured via environment variables or config file:

```bash
# Environment variables
export REBALANCING_ENABLED=true
export REBALANCING_AUTO_BRIDGE=true
export REBALANCING_REQUIRE_CONFIRMATION=false  # Set to true for manual approval
export REBALANCING_DRIFT_THRESHOLD_PCT=20
export REBALANCING_CRITICAL_DRIFT_PCT=40
export REBALANCING_CHECK_INTERVAL_SECONDS=300
```

Or in `config.json`:

```json
{
  "rebalancing": {
    "enabled": true,
    "auto_bridge": true,
    "require_confirmation": false,
    "check_interval_seconds": 300,
    "drift_threshold_pct": 20,
    "critical_drift_pct": 40,
    "min_rebalance_amount_usd": 50,
    "max_rebalance_amount_usd": 1000,
    "target_allocations": {
      "gswap": {"GALA": 60, "USDT": 40},
      "binance": {"GALA": 40, "USDT": 60}
    }
  }
}
```

### Cross-Chain Arbitrage Detection (Phase 4)

The trading bot can detect arbitrage opportunities that span the GalaChain/Ethereum bridge, accounting for bridge costs and price volatility risk during the bridge delay.

```bash
# Enable cross-chain arbitrage detection
./gswap-trader --cross-chain

# With custom minimum spread threshold
./gswap-trader --cross-chain --cross-chain-min-spread 3.5
```

#### How Cross-Chain Arbitrage Works

Cross-chain arbitrage opportunities arise when:
1. A token is trading at different prices across different chains (GalaChain, Ethereum, Solana)
2. The price spread is large enough to cover bridge costs and volatility risk

**Example flows**:
- **GalaChain → Ethereum**: Buy GALA on GSwap (cheaper) → Bridge to Ethereum → Sell on Binance (higher price)
- **GalaChain → Solana**: Buy GSOL on GSwap → Bridge to Solana → Sell on Jupiter
- **Solana → GalaChain**: Buy GALA on Jupiter → Bridge to GalaChain → Sell on GSwap

**Note**: Bridge execution is disabled by default (`CROSS_CHAIN_ARB_BRIDGE_ENABLED=false`). When disabled, the bot detects and reports opportunities but does not execute bridge transactions.

#### Risk-Adjusted Profit Calculation

The detector calculates risk-adjusted profit by accounting for:

| Factor | Description | Typical Range |
|--------|-------------|---------------|
| **Gross Spread** | Raw price difference between exchanges | 0-10% |
| **Bridge Cost** | Gas fees, bridge fees, and slippage | 40-75 bps |
| **Volatility Risk** | Price movement risk during bridge time | 50-200 bps |
| **Risk-Adjusted Profit** | Gross spread - bridge cost - volatility risk | Min 100 bps |

The volatility risk scales with the square root of bridge time, using historical price volatility data.

#### Configuration

Cross-chain arbitrage can be configured via environment variables:

```bash
# Enable cross-chain arbitrage detection (GalaChain <-> Ethereum <-> Solana)
export CROSS_CHAIN_ARB_ENABLED=true

# Enable bridge execution for cross-chain arbitrage
# When false (default), only detects and reports opportunities without executing bridges
export CROSS_CHAIN_ARB_BRIDGE_ENABLED=false

# Minimum spread required (percentage)
export CROSS_CHAIN_MIN_SPREAD_PCT=3.0

# Minimum risk-adjusted profit (basis points)
export CROSS_CHAIN_MIN_RISK_ADJ_PROFIT_BPS=100

# Bridge time estimates (minutes)
export CROSS_CHAIN_BRIDGE_TIME_TO_ETH=15
export CROSS_CHAIN_BRIDGE_TIME_TO_GALA=15

# Volatility model settings
export CROSS_CHAIN_VOLATILITY_WINDOW_MIN=60
export CROSS_CHAIN_DEFAULT_VOLATILITY_BPS=200
export CROSS_CHAIN_CONFIDENCE_MULTIPLIER=2.0

# Allowed tokens for cross-chain arb (includes Solana-bridgeable tokens)
export CROSS_CHAIN_ALLOWED_TOKENS=GALA,GUSDT,GUSDC,GSOL,GMEW,GTRUMP

# Execution strategy: staged, immediate, or hedged
export CROSS_CHAIN_EXECUTION_STRATEGY=staged
```

Or in `config.json`:

```json
{
  "cross_chain_arbitrage": {
    "enabled": true,
    "bridge_enabled": false,
    "min_spread_percent": 3.0,
    "min_risk_adjusted_profit_bps": 100,
    "max_bridge_time_minutes": 30,
    "bridge_time_to_eth_min": 15,
    "bridge_time_to_gala_min": 15,
    "bridge_time_to_solana_min": 10,
    "bridge_time_from_solana_min": 10,
    "volatility_window_minutes": 60,
    "default_volatility_bps": 200,
    "confidence_multiplier": 2.0,
    "allowed_tokens": ["GALA", "GUSDT", "GUSDC", "GSOL", "GMEW", "GTRUMP"],
    "execution_strategy": "staged"
  }
}
```

#### Bridge Cost Estimates

| Token | To Ethereum | To GalaChain | To Solana | From Solana |
|-------|-------------|--------------|-----------|-------------|
| GALA | 40 bps | 65 bps | 50 bps | 55 bps |
| GWETH | 75 bps | 100 bps | - | - |
| GUSDC/GUSDT | 50 bps | 75 bps | - | - |
| GSOL | - | - | 40 bps | 45 bps |
| GMEW/GTRUMP | - | - | 45 bps | 50 bps |

#### Slack Notifications

Cross-chain opportunities trigger Slack notifications with:
- Buy/sell exchanges and direction
- Gross spread percentage
- Bridge cost and volatility risk breakdown
- Risk-adjusted profit
- Recommendation status (profitable or not after risk adjustment)

### Solana/Jupiter DEX Integration

The bot supports arbitrage detection and trade execution on Solana via the Jupiter aggregator. Jupiter routes trades through multiple Solana DEXs (Raydium, Orca, Phoenix, etc.) to find the best prices.

#### Enabling Solana Support

```bash
# Enable Solana price feeds
export SOLANA_ENABLED=true

# Solana RPC endpoint (public or private)
export SOLANA_RPC_URL=https://api.mainnet-beta.solana.com

# For trading (optional)
export SOLANA_PRIVATE_KEY=<base58_encoded_private_key>
export SOLANA_WALLET_ADDRESS=<base58_encoded_public_key>
export SOLANA_TRADING_ENABLED=true
export SOLANA_MAX_TRADE_SIZE=100
export SOLANA_DEFAULT_SLIPPAGE_BPS=50
```

#### Jupiter API Configuration

```bash
# Jupiter Lite API (free, rate limited)
export JUPITER_API_BASE=https://lite-api.jup.ag/swap/v1

# Jupiter Ultra API (requires API key for higher limits)
export JUPITER_API_BASE=https://api.jup.ag/swap/v1
export JUPITER_API_KEY=your_api_key

# Polling interval (seconds)
export SOLANA_POLL_INTERVAL_SECONDS=5
```

#### Supported Solana Pairs

The following pairs are supported by default:

| Pair | Description |
|------|-------------|
| SOL/USDC | Native SOL to USDC |
| SOL/USDT | Native SOL to USDT |
| GALA/SOL | GALA (wormhole) to SOL |
| GALA/USDC | GALA (wormhole) to USDC |
| BONK/SOL | BONK memecoin to SOL |
| WIF/SOL | WIF memecoin to SOL |
| POPCAT/SOL | POPCAT memecoin to SOL |
| FARTCOIN/SOL | FARTCOIN memecoin to SOL |

#### Token Addresses (Mainnet)

| Token | Mint Address | Decimals |
|-------|--------------|----------|
| SOL | `So11111111111111111111111111111111111111112` | 9 |
| USDC | `EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v` | 6 |
| USDT | `Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB` | 6 |
| GALA | `GALAxveLUPZLARuXA5WyJQ5ThEyc5T49xF1dN3BJGALA` | 8 |
| BONK | `DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263` | 5 |
| WIF | `EKpQGSJtjMFqKZ9KQanSqYXRcF8fBopzLHYxdM65zcjm` | 6 |
| POPCAT | `7GCihgDB8fe6KNjn2MYtkzZcRjQy3t9GHdC8uHYmW2hr` | 9 |
| FARTCOIN | `9BB6NFEcjBCtnNLFko2FqVQBq8HHM13kCyYcdQbgpump` | 6 |

#### How Jupiter Integration Works

1. **Price Discovery**: The bot polls Jupiter's quote API to get real-time swap prices
2. **Route Optimization**: Jupiter automatically finds the best route across multiple DEXs
3. **Arbitrage Detection**: Prices are compared with CEXs and other DEXs for opportunities
4. **Trade Execution** (if enabled): Uses Jupiter's swap API to build and submit transactions

#### Trade Execution Flow (Solana)

1. Get fresh quote from Jupiter with slippage protection
2. Build swap transaction via Jupiter API
3. Sign transaction with wallet keypair
4. Submit via Solana RPC
5. Confirm transaction status

**Note**: Full transaction signing requires ed25519 keypair handling. The current implementation uses Jupiter's API for transaction building.

### Slack Notifications

The bot sends real-time Slack notifications for various events. Enable by setting `SLACK_ENABLED=true` and providing `SLACK_API_TOKEN` and `SLACK_CHANNEL`.

#### Notification Types

| Category | Event | Description |
|----------|-------|-------------|
| **Arbitrage** | Opportunity Detected | Price discrepancy found between exchanges |
| **Arbitrage** | Trade Executed | Buy/sell orders completed (or simulated in dry-run) |
| **Chain Arbitrage** | Multi-hop Opportunity | Profitable path across 3+ exchanges |
| **Cross-Chain** | Bridge Opportunity | Profitable spread spanning GalaChain/Ethereum bridge |
| **Cross-Chain** | Execution Started | Cross-chain trade initiated (staged execution) |
| **Inventory** | Drift Alert | Balance allocation drifted beyond threshold |
| **Inventory** | Critical Drift | Severe imbalance requiring immediate attention |
| **Rebalancing** | Recommendation | Suggested bridge operation to restore balance |
| **Rebalancing** | Bridge Started | Auto-rebalance bridge initiated |
| **Rebalancing** | Bridge Completed | Bridge operation finished successfully |
| **Rebalancing** | Bridge Failed | Bridge operation failed with error details |
| **Safety** | Circuit Breaker Open | Auto-rebalancing paused after consecutive failures |
| **Safety** | Circuit Breaker Closed | Auto-rebalancing resumed after cooldown |

#### Example Slack Messages

**Arbitrage Opportunity:**
```
🔔 ARBITRAGE OPPORTUNITY
Pair: GALA/USDT
Buy: binance @ 0.02345
Sell: gswap @ 0.02380
Spread: 1.49% (149 bps)
Net Profit: 139 bps
```

**Drift Alert:**
```
⚠️ INVENTORY DRIFT ALERT
Currency: GALA
Max Drift: 25.0%
gswap: +25.0% (surplus)
binance: -25.0% (deficit)
Action: Rebalancing recommended
```

**Auto-Rebalance Started:**
```
🔄 AUTO-REBALANCE STARTED
Currency: GALA
Amount: 1000.0000
From: gswap → To: binance
Transaction: 0x1234...abcd
```

### Command Line Options

#### REST API Bot (`./cmd/bot`)

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | - | Path to JSON configuration file |
| `--format` | `text` | Output format: `text`, `json`, `csv` |
| `--verbose` | `true` | Enable verbose output |
| `--dry-run` | `true` | Dry run mode (detection only) |
| `--once` | `false` | Run single detection cycle and exit |

#### WebSocket Bot (`./cmd/bot-ws`)

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | - | Path to JSON configuration file |
| `--format` | `text` | Output format: `text`, `json`, `csv` |
| `--verbose` | `true` | Enable verbose output |
| `--show-updates` | `false` | Show all price updates (very verbose) |

#### Trading Bot (`./cmd/bot-trader`)

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | - | Path to JSON configuration file |
| `--dry-run` | `true` | Dry run mode (no real trades) |
| `--max-trade` | `10` | Maximum trade size in quote currency |
| `--min-profit` | `20` | Minimum profit in basis points |
| `--format` | `text` | Output format: `text`, `json`, `csv` |
| `--verbose` | `true` | Enable verbose output |
| `--inventory` | `true` | Enable inventory monitoring |
| `--drift-threshold` | `20` | Drift threshold percentage for alerts |
| `--auto-rebalance` | `false` | Enable automatic rebalancing |
| `--eth-rpc` | - | Ethereum RPC URL for bridging (or use `ETH_RPC_URL` env) |
| `--cross-chain` | `false` | Enable cross-chain arbitrage detection |
| `--cross-chain-min-spread` | `3.0` | Minimum spread % for cross-chain arbitrage |

#### GSwap Graph Bot (`./cmd/bot-gswap`)

**Detection Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-api` | `https://arb.gala.com/api` | GSwap API URL |
| `-max-cycle` | `5` | Maximum cycle length to detect |
| `-min-profit` | `10` | Minimum profit in bps to report |
| `-fee` | `100` | Default fee in basis points (1% = 100 bps) |
| `-poll` | `5s` | Price polling interval |
| `-continuous` | `true` | Run continuously (vs one-shot) |
| `-show-graph` | `false` | Print graph summary at startup |
| `-show-cycles` | `false` | Print all enumerated cycles |
| `-log` | `bot-gswap.log` | Log file path (empty to disable) |
| `-verbose` | `true` | Enable verbose output |

**Trading Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-trade` | `false` | Enable trade execution (requires credentials) |
| `-dry-run` | `true` | Simulate trades without executing |
| `-auto-execute` | `false` | Automatically execute profitable cycles |
| `-trade-size` | `1` | Trade size for cycle execution (kept small due to pool liquidity) |
| `-max-trade-size` | `10` | Maximum trade size (pools are illiquid) |
| `-min-trade-size` | `0.1` | Minimum trade size |
| `-exec-min-profit` | `50` | Minimum profit in bps to execute |
| `-slippage` | `100` | Slippage tolerance in basis points |
| `-sim-amount` | `1` | Amount to use for simulation display |
| `-private-key` | - | GSwap private key (or `GSWAP_PRIVATE_KEY` env) |
| `-wallet` | - | Wallet address (or `GSWAP_WALLET_ADDRESS` env) |

#### Bridge CLI (`./cmd/bridge`)

| Flag | Default | Description |
|------|---------|-------------|
| `--direction` | - | Bridge direction: `to-eth` or `to-gala` |
| `--token` | - | Token to bridge (GALA, GWETH, GUSDC, etc.) |
| `--amount` | - | Amount to bridge |
| `--to` | - | Destination address (optional) |
| `--balance` | `false` | Show GalaChain balances |
| `--status` | - | Check bridge transaction status |
| `--list` | `false` | List supported tokens |
| `--private-key` | - | Wallet private key (or use `GSWAP_PRIVATE_KEY` env var) |
| `--eth-rpc` | - | Ethereum RPC URL (or use `ETH_RPC_URL` env var) - required for `to-gala` |

#### Rebalance CLI (`./cmd/rebalance`)

| Flag | Default | Description |
|------|---------|-------------|
| `--check` | `false` | Check current balances and drift status |
| `--recommend` | `false` | Generate rebalance recommendations |
| `--execute` | `false` | Execute a rebalance operation |
| `--token` | - | Token to rebalance (required for execute) |
| `--from` | - | Source exchange (required for execute) |
| `--to` | - | Destination exchange (required for execute) |
| `--amount` | - | Amount to transfer (required for execute) |
| `--wait` | `false` | Wait for bridge completion |
| `--max-wait` | `10m` | Maximum time to wait for bridge |
| `--drift-threshold` | `20` | Drift threshold percentage |
| `--private-key` | - | Wallet private key (or use `GSWAP_PRIVATE_KEY` env var) |
| `--eth-rpc` | - | Ethereum RPC URL (or use `ETH_RPC_URL` env var) |

### Environment Variables

Copy `.env.example` to `.env`, fill in your credentials, then source it:

```bash
cp .env.example .env
# Edit .env with your credentials
source .env
```

The `.env` file uses `export` statements so variables are available to all commands after sourcing:

```bash
# Bot settings
export BOT_UPDATE_INTERVAL_MS=15000
export BOT_VERBOSE=true
export BOT_DRY_RUN=true

# Arbitrage settings
export ARB_MIN_SPREAD_BPS=50
export ARB_MIN_NET_PROFIT_BPS=20
export ARB_DEFAULT_TRADE_SIZE=1000

# Exchange API keys (for trading)
export BINANCE_API_KEY=your_key
export BINANCE_SECRET=your_secret
export BINANCE_TRADING_ENABLED=false

export KRAKEN_API_KEY=your_key
export KRAKEN_SECRET=your_secret
export KRAKEN_TRADING_ENABLED=false

# GSwap DEX (for trading)
export GSWAP_PRIVATE_KEY=your_private_key
# GSwap executor requires EIP-55 checksummed address
export GSWAP_WALLET_ADDRESS=your_checksummed_wallet_address
export GSWAP_TRADING_ENABLED=false

# Bridge wallet address (uses different casing than GSwap executor)
# GalaChain bridge API requires non-checksummed address format
export GALACHAIN_BRIDGE_WALLET_ADDRESS=your_non_checksummed_wallet_address

# Ethereum RPC (for bridging Ethereum → GalaChain)
export ETH_RPC_URL=https://eth.llamarpc.com

# Slack notifications
export SLACK_ENABLED=true
export SLACK_API_TOKEN=xoxb-your-bot-token
export SLACK_CHANNEL=#your-channel
```

## Configuration

Copy `config.example.json` to `config.json` and customize:

```json
{
  "update_interval_ms": 15000,
  "verbose": true,
  "dry_run": true,
  "arbitrage": {
    "min_spread_bps": 50,
    "min_net_profit_bps": 20,
    "max_price_impact_bps": 500,
    "default_trade_size": 1000,
    "quote_validity_secs": 30
  },
  "exchanges": [...],
  "pairs": [...]
}
```

### Configuration Fields

| Field | Description |
|-------|-------------|
| `update_interval_ms` | Milliseconds between detection cycles |
| `min_spread_bps` | Minimum spread in basis points to report |
| `min_net_profit_bps` | Minimum profit after fees in basis points |
| `max_price_impact_bps` | Maximum acceptable price impact |
| `default_trade_size` | Default trade size in quote currency |
| `quote_validity_secs` | How long quotes are considered valid |

## Example Output

### Text Format

```
================================================================================
ARBITRAGE OPPORTUNITIES DETECTED: 2
Time: 2025-01-15T10:30:45Z
================================================================================

--- Opportunity #1 ---
Pair:         GALA/USDT
Buy on:       binance @ 0.02345000
Sell on:      gswap @ 0.02380000

Spread:       0.00035000 (1.49% / 149 bps)
Gross Profit: 35.00000000
Est. Fees:    2.34500000
Net Profit:   32.65500000 (139 bps)

Trade Size:   1000.00000000
Valid Until:  2025-01-15T10:31:15Z
```

### JSON Format

```json
{
  "timestamp": "2025-01-15T10:30:45Z",
  "count": 2,
  "opportunities": [
    {
      "id": "GALA/USDT-binance-gswap-1736934645000",
      "pair": "GALA/USDT",
      "buy_exchange": "binance",
      "sell_exchange": "gswap",
      "buy_price": "0.02345000",
      "sell_price": "0.02380000",
      "spread_percent": 1.49,
      "spread_bps": 149,
      "net_profit_bps": 139,
      "net_profit": "32.65500000",
      "trade_size": "1000.00000000"
    }
  ]
}
```

## Architecture

### Price Providers

The bot uses a modular provider architecture:

- **GSwap Provider**: Fetches prices from GalaChain DEX via the composite pool API
- **CEX Providers**: Fetch prices from centralized exchanges via their REST APIs

Each provider implements the `PriceProvider` interface:

```go
type PriceProvider interface {
    Name() string
    Type() ExchangeType
    Initialize(ctx context.Context) error
    GetQuote(ctx context.Context, pair string) (*PriceQuote, error)
    GetOrderBook(ctx context.Context, pair string, depth int) (*OrderBook, error)
    GetFees() *FeeStructure
    Close() error
}
```

### Arbitrage Detection

1. **Quote Collection**: Fetches prices from all configured exchanges in parallel
2. **Spread Calculation**: Compares bid/ask prices to find profitable spreads
3. **Fee Estimation**: Accounts for trading fees on both exchanges
4. **Validation**: Checks liquidity, price impact, and profit thresholds
5. **Reporting**: Outputs valid opportunities in the configured format

## Extending the Bot

### Adding a New CEX

1. Add the exchange configuration to `cex/cex.go`:

```go
var NewExchangeConfig = ExchangeConfig{
    ID:      "newexchange",
    Name:    "New Exchange",
    BaseURL: "https://api.newexchange.com",
}
```

2. Implement the ticker and order book fetching methods:

```go
func (p *CEXProvider) fetchNewExchangeTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
    // Implementation
}

func (p *CEXProvider) fetchNewExchangeOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
    // Implementation
}
```

### Adding GalaChain Tokens

The GSwap provider can be extended with additional tokens:

```go
gswapProvider.AddToken("NEWTOKEN", "NEWTOKEN|Unit|none|none", 8)
gswapProvider.AddPair("NEWTOKEN", "GALA", "NEWTOKEN/GALA")
```

## WebSocket Architecture

The WebSocket implementation provides real-time price feeds with automatic reconnection:

### Supported Exchanges

| Exchange | Connection Type | Endpoint | Features |
|----------|----------------|----------|----------|
| **GSwap** | REST Polling (5s) | GalaChain API | Composite pool prices, DEX liquidity |
| **Jupiter** | REST Polling (configurable) | Jupiter API | Solana DEX aggregator, best route prices |
| Binance | WebSocket | `wss://stream.binance.com:9443/ws` | Book ticker (best bid/ask), US fallback |
| Coinbase | WebSocket | `wss://ws-feed.exchange.coinbase.com` | Full ticker with 24h stats |
| Kraken | WebSocket | `wss://ws.kraken.com` | Ticker with volume |
| OKX | WebSocket | `wss://ws.okx.com:8443/ws/v5/public` | Ticker with 24h stats |
| Bybit | WebSocket | `wss://stream.bybit.com/v5/public/spot` | Ticker with best bid/ask |

### GSwap DEX Integration

GSwap (GalaChain DEX) doesn't provide a WebSocket API, so the bot uses a polling wrapper that:

- Fetches prices from the GalaChain composite pool API every 5 seconds
- Maps CEX pair names to GSwap equivalents (e.g., `GALA/USDT` ↔ `GUSDT/GALA`)
- Integrates seamlessly with the price aggregator for arbitrage detection
- Supports wrapped GalaChain tokens: GUSDT, GUSDC, GWETH, GBTC, GSOL

### Price Aggregator

The `PriceAggregator` combines feeds from all connected exchanges:

```go
// Create aggregator
aggregator := websocket.NewPriceAggregator(&websocket.AggregatorConfig{
    MinSpreadBps:    50,           // 0.5% minimum spread
    MinNetProfitBps: 20,           // 0.2% minimum profit
    StaleThreshold:  10 * time.Second,
})

// Add providers
aggregator.AddProvider(websocket.NewGSwapPollerProvider(5 * time.Second)) // GSwap DEX
aggregator.AddProvider(websocket.NewBinanceWSProvider())
aggregator.AddProvider(websocket.NewCoinbaseWSProvider())

// Set callbacks
aggregator.OnPriceUpdate(func(update *websocket.PriceUpdate) {
    fmt.Printf("%s %s: %s\n", update.Exchange, update.Pair, update.BidPrice)
})

aggregator.OnArbitrage(func(opp *types.ArbitrageOpportunity) {
    fmt.Printf("Arbitrage! Buy %s @ %s, Sell %s @ %s\n",
        opp.BuyExchange, opp.BuyPrice, opp.SellExchange, opp.SellPrice)
})

// Start
aggregator.Start(ctx, []string{"BTC/USDT", "ETH/USDT"})
```

### Features

- **Automatic Reconnection**: Exponential backoff reconnection on disconnect
- **Price Caching**: Latest prices cached for each pair/exchange
- **Stale Detection**: Automatically discards prices older than threshold
- **Parallel Processing**: Non-blocking price update handling

## Trade Execution

The bot now supports actual trade execution on supported exchanges. **Use with caution!**

### Setup

1. Copy `.env.example` to `.env` and configure your credentials:

```bash
cp .env.example .env
# Edit .env with your API keys and private keys
```

2. Enable trading for specific exchanges:

```bash
# In .env:
BINANCE_API_KEY=your_key
BINANCE_SECRET=your_secret
BINANCE_TRADING_ENABLED=true
BINANCE_MAX_TRADE_SIZE=100

GSWAP_PRIVATE_KEY=your_private_key
GSWAP_TRADING_ENABLED=true
GSWAP_MAX_TRADE_SIZE=100
```

### Safety Features

- **Dry Run Mode**: Enabled by default - detects opportunities without executing
- **Balance Checking**: Verifies sufficient balance before each trade
- **Rate Limiting**: Minimum 5 seconds between trade attempts
- **Maximum Trade Size**: Configurable per-exchange limits
- **Minimum Profit Threshold**: Only executes trades above minimum profit

### Supported Executors

| Exchange | Type | Features |
|----------|------|----------|
| **GSwap** | DEX | Swap execution via GalaChain API, balance checking |
| **Jupiter** | DEX | Swap execution via Jupiter API (Solana), balance checking |
| **Binance** | CEX | Via CCXT - Market/limit orders, balance checking |
| **Kraken** | CEX | Via CCXT - Market/limit orders, balance checking |
| **Coinbase** | CEX | Via CCXT - Market/limit orders, balance checking |
| **OKX** | CEX | Via CCXT - Market/limit orders, balance checking |
| **Bybit** | CEX | Via CCXT - Market/limit orders, balance checking |
| **KuCoin** | CEX | Via CCXT - Market/limit orders, balance checking |
| **Gate** | CEX | Via CCXT - Market/limit orders, balance checking |
| **Huobi** | CEX | Via CCXT - Market/limit orders, balance checking |
| **Bitfinex** | CEX | Via CCXT - Market/limit orders, balance checking |
| **Bitstamp** | CEX | Via CCXT - Market/limit orders, balance checking |

All CEX integrations use [CCXT](https://github.com/ccxt/ccxt) for unified exchange access.

### Trade Execution Flow

1. **Opportunity Detection**: WebSocket aggregator detects arbitrage opportunity
2. **Validation**: Checks profit threshold, expiry, and validity
3. **Balance Check**: Verifies sufficient balance on both exchanges
4. **Execution**: Places buy order, then sell order
5. **Profit Calculation**: Calculates actual profit from filled orders

### Environment Variables for Trading

```bash
# GSwap (DEX)
GSWAP_PRIVATE_KEY=0x...          # Ethereum-compatible private key
GSWAP_WALLET_ADDRESS=0x...       # EIP-55 checksummed address (required for GSwap executor)
GSWAP_TRADING_ENABLED=true
GSWAP_MAX_TRADE_SIZE=100

# Bridge wallet address (different casing than GSwap executor)
# GalaChain bridge API requires non-checksummed address format
GALACHAIN_BRIDGE_WALLET_ADDRESS=0x...  # Non-checksummed address for bridge operations

# Binance
BINANCE_API_KEY=...
BINANCE_SECRET=...
BINANCE_TRADING_ENABLED=true
BINANCE_MAX_TRADE_SIZE=100
```

**Important**: The GSwap executor and bridge require different address formats:
- `GSWAP_WALLET_ADDRESS`: EIP-55 checksummed (e.g., `0x0e5137178b1737A73e521A4e76327d184EddB275`)
- `GALACHAIN_BRIDGE_WALLET_ADDRESS`: Non-checksummed (e.g., `0x0E5137178b1737a73E521a4E76327D184EddB275`)

## Future Enhancements

- [x] Trade execution (move beyond detection)
- [x] Multi-hop chain arbitrage detection
- [x] CCXT integration for unified CEX support (10+ exchanges)
- [x] Slack notifications for opportunities and trades
- [x] Bridge CLI for GalaChain ↔ Ethereum transfers
- [x] Inventory monitoring and drift detection
- [x] Semi-automated rebalancing CLI
- [x] Automated rebalancing based on drift thresholds
- [x] Circuit breaker for rebalancing failures
- [x] Cross-chain arbitrage detection with volatility-aware risk adjustment
- [x] Solana DEX support via Jupiter aggregator
- [x] Graph-based DEX cycle detection for GSwap
- [x] Trade execution for graph-detected cycles
- [ ] Cross-chain arbitrage execution (Phase 5)
- [ ] Historical opportunity tracking and analytics
- [ ] Telegram/Discord notifications
- [ ] Gas/transaction cost estimation for DEX trades
- [ ] GSwap WebSocket support (when available from GalaChain)
- [ ] Additional GalaChain token pairs
- [ ] Solana-GalaChain bridge arbitrage (when bridge available)

## License

MIT License

## Disclaimer

This software is provided for educational and research purposes only. Cryptocurrency trading involves substantial risk of loss. The authors are not responsible for any financial losses incurred through the use of this software. Always do your own research and consider consulting a financial advisor before engaging in trading activities.
