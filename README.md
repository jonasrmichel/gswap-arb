<p align="center">
  <img src="logo.svg" alt="GSwap Arbitrage Bot Logo" width="150" height="150">
</p>

<h1 align="center">GSwap Arbitrage Bot</h1>

<p align="center">
  A Golang-based cryptocurrency arbitrage bot that detects and executes trades on price discrepancies between GalaChain/GSwap (DEX) and major centralized exchanges (CEXs).
</p>

## Features

- **Multi-exchange support**: Monitors GSwap (GalaChain DEX) and major CEXs including:
  - **GSwap** (GalaChain DEX) - REST polling with 5s intervals
  - Binance (with Binance.US fallback)
  - Coinbase
  - Kraken
  - OKX
  - Bybit

- **Real-time arbitrage detection**: Scans for price discrepancies across exchanges
- **Trade execution**: Execute arbitrage trades via CCXT (10+ CEX exchanges) and GSwap DEX
- **Chain arbitrage**: Multi-hop arbitrage detection across 2-5 exchanges
- **WebSocket support**: Real-time price feeds for ultra-low latency detection
- **GSwap DEX integration**: Polls GalaChain composite pool API for DEX prices
- **Slack notifications**: Real-time alerts for opportunities and trade executions
- **Configurable thresholds**: Set minimum spread, profit margins, and trade sizes
- **Multiple output formats**: Text, JSON, or CSV
- **Dry-run mode**: Detect opportunities without executing trades (default)
- **Safety features**: Balance checking, rate limiting, configurable trade limits

## Project Structure

```
gswap-arb/
├── cmd/
│   ├── bot/
│   │   └── main.go           # REST API-based bot (polling)
│   ├── bot-ws/
│   │   └── main.go           # WebSocket-based bot (real-time)
│   └── bot-trader/
│       └── main.go           # Trading bot with execution
├── pkg/
│   ├── arbitrage/
│   │   ├── detector.go       # Arbitrage detection logic
│   │   └── chain.go          # Multi-hop chain arbitrage
│   ├── config/
│   │   └── config.go         # Configuration management
│   ├── executor/
│   │   ├── types.go          # TradeExecutor interface & types
│   │   ├── ccxt_executor.go  # Unified CEX executor via CCXT
│   │   ├── gswap.go          # GSwap DEX executor
│   │   ├── registry.go       # Executor registry
│   │   └── coordinator.go    # Arbitrage execution coordinator
│   ├── providers/
│   │   ├── provider.go       # Provider interface & registry
│   │   ├── gswap/
│   │   │   └── gswap.go      # GalaChain/GSwap provider
│   │   ├── cex/
│   │   │   └── cex.go        # CEX REST API providers
│   │   └── websocket/
│   │       ├── types.go      # WebSocket types & base provider
│   │       ├── aggregator.go # Multi-exchange price aggregator
│   │       ├── gswap_poller.go # GSwap REST polling provider
│   │       ├── binance.go    # Binance WebSocket
│   │       ├── coinbase.go   # Coinbase WebSocket
│   │       ├── kraken.go     # Kraken WebSocket
│   │       ├── okx.go        # OKX WebSocket
│   │       └── bybit.go      # Bybit WebSocket
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

### Environment Variables

```bash
# Bot settings
BOT_UPDATE_INTERVAL_MS=15000
BOT_VERBOSE=true
BOT_DRY_RUN=true

# Arbitrage settings
ARB_MIN_SPREAD_BPS=50
ARB_MIN_NET_PROFIT_BPS=20
ARB_DEFAULT_TRADE_SIZE=1000

# Exchange API keys (for trading)
BINANCE_API_KEY=your_key
BINANCE_SECRET=your_secret
BINANCE_TRADING_ENABLED=false

KRAKEN_API_KEY=your_key
KRAKEN_SECRET=your_secret
KRAKEN_TRADING_ENABLED=false

# GSwap DEX (for trading)
GSWAP_PRIVATE_KEY=your_private_key
GSWAP_WALLET_ADDRESS=your_wallet_address
GSWAP_TRADING_ENABLED=false

# Slack notifications
SLACK_ENABLED=true
SLACK_API_TOKEN=xoxb-your-bot-token
SLACK_CHANNEL=#your-channel
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
GSWAP_WALLET_ADDRESS=0x...       # Optional (derived from key)
GSWAP_TRADING_ENABLED=true
GSWAP_MAX_TRADE_SIZE=100

# Binance
BINANCE_API_KEY=...
BINANCE_SECRET=...
BINANCE_TRADING_ENABLED=true
BINANCE_MAX_TRADE_SIZE=100
```

## Future Enhancements

- [x] Trade execution (move beyond detection)
- [x] Multi-hop chain arbitrage detection
- [x] CCXT integration for unified CEX support (10+ exchanges)
- [x] Slack notifications for opportunities and trades
- [ ] Historical opportunity tracking and analytics
- [ ] Telegram/Discord notifications
- [ ] Gas/transaction cost estimation for DEX trades
- [ ] GSwap WebSocket support (when available from GalaChain)
- [ ] Additional GalaChain token pairs

## License

MIT License

## Disclaimer

This software is provided for educational and research purposes only. Cryptocurrency trading involves substantial risk of loss. The authors are not responsible for any financial losses incurred through the use of this software. Always do your own research and consider consulting a financial advisor before engaging in trading activities.
