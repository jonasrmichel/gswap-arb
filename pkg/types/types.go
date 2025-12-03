// Package types defines core data structures for the arbitrage bot.
package types

import (
	"math/big"
	"time"
)

// ExchangeType represents the type of exchange (DEX or CEX).
type ExchangeType string

const (
	ExchangeTypeDEX ExchangeType = "DEX"
	ExchangeTypeCEX ExchangeType = "CEX"
)

// Exchange represents a trading venue.
type Exchange struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Type     ExchangeType `json:"type"`
	Enabled  bool         `json:"enabled"`
	BaseURL  string       `json:"base_url,omitempty"`
	APIKey   string       `json:"-"` // Not serialized
	Secret   string       `json:"-"` // Not serialized
}

// Token represents a cryptocurrency token.
type Token struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name,omitempty"`
	Decimals int    `json:"decimals"`

	// Chain-specific identifiers
	GalaChainMint string `json:"galachain_mint,omitempty"` // e.g., "GALA|Unit|none|none"
	ContractAddr  string `json:"contract_addr,omitempty"`  // EVM contract address
}

// TradingPair represents a tradable pair.
type TradingPair struct {
	Base     Token  `json:"base"`
	Quote    Token  `json:"quote"`
	Symbol   string `json:"symbol"` // e.g., "GALA/USDT"
	Exchange string `json:"exchange"`
}

// PriceQuote represents a price quote from an exchange.
type PriceQuote struct {
	Exchange  string    `json:"exchange"`
	Pair      string    `json:"pair"`      // e.g., "GALA/USDT"
	Price     *big.Float `json:"price"`     // Best price
	BidPrice  *big.Float `json:"bid_price"` // Best bid (buy price)
	AskPrice  *big.Float `json:"ask_price"` // Best ask (sell price)
	BidSize   *big.Float `json:"bid_size"`  // Size at best bid
	AskSize   *big.Float `json:"ask_size"`  // Size at best ask
	Volume24h *big.Float `json:"volume_24h,omitempty"`
	Timestamp time.Time  `json:"timestamp"`
	Source    string     `json:"source"` // "ticker", "orderbook", etc.
}

// OrderBookEntry represents a single order book level.
type OrderBookEntry struct {
	Price  *big.Float `json:"price"`
	Amount *big.Float `json:"amount"`
}

// OrderBook represents the order book for a trading pair.
type OrderBook struct {
	Exchange  string           `json:"exchange"`
	Pair      string           `json:"pair"`
	Bids      []OrderBookEntry `json:"bids"` // Buy orders (descending by price)
	Asks      []OrderBookEntry `json:"asks"` // Sell orders (ascending by price)
	Timestamp time.Time        `json:"timestamp"`
}

// ArbitrageDirection indicates the direction of an arbitrage trade.
type ArbitrageDirection string

const (
	// BuyOnAExchangeSellOnB means buy on exchange A (lower price), sell on exchange B (higher price)
	BuyOnAExchangeSellOnB ArbitrageDirection = "BUY_A_SELL_B"
	// BuyOnBExchangeSellOnA means buy on exchange B (lower price), sell on exchange A (higher price)
	BuyOnBExchangeSellOnA ArbitrageDirection = "BUY_B_SELL_A"
)

// ArbitrageOpportunity represents a detected arbitrage opportunity.
type ArbitrageOpportunity struct {
	ID        string             `json:"id"`
	Pair      string             `json:"pair"`
	Direction ArbitrageDirection `json:"direction"`

	// Exchange details
	BuyExchange  string `json:"buy_exchange"`
	SellExchange string `json:"sell_exchange"`

	// Price information
	BuyPrice  *big.Float `json:"buy_price"`
	SellPrice *big.Float `json:"sell_price"`

	// Spread calculation
	SpreadAbsolute *big.Float `json:"spread_absolute"` // Sell - Buy price
	SpreadPercent  float64    `json:"spread_percent"`  // (Sell - Buy) / Buy * 100
	SpreadBps      int        `json:"spread_bps"`      // Basis points (spread_percent * 100)

	// Estimated profit
	TradeSize      *big.Float `json:"trade_size"`       // Amount to trade
	GrossProfit    *big.Float `json:"gross_profit"`     // SpreadAbsolute * TradeSize
	EstimatedFees  *big.Float `json:"estimated_fees"`   // Total fees (trading + withdrawal)
	NetProfit      *big.Float `json:"net_profit"`       // GrossProfit - EstimatedFees
	NetProfitBps   int        `json:"net_profit_bps"`   // Net profit in basis points

	// Risk metrics
	BuyLiquidity  *big.Float `json:"buy_liquidity"`  // Available liquidity on buy side
	SellLiquidity *big.Float `json:"sell_liquidity"` // Available liquidity on sell side
	PriceImpactBps int       `json:"price_impact_bps"` // Estimated price impact

	// Timestamps
	DetectedAt time.Time `json:"detected_at"`
	ExpiresAt  time.Time `json:"expires_at"` // Quotes typically valid for ~30s

	// Quote details for reference
	BuyQuote  *PriceQuote `json:"buy_quote,omitempty"`
	SellQuote *PriceQuote `json:"sell_quote,omitempty"`

	// Validity
	IsValid            bool     `json:"is_valid"`
	InvalidationReasons []string `json:"invalidation_reasons,omitempty"`
}

// ArbitrageConfig holds configuration for arbitrage detection.
type ArbitrageConfig struct {
	MinSpreadBps      int        `json:"min_spread_bps"`      // Minimum spread to report (default: 50 = 0.5%)
	MinNetProfitBps   int        `json:"min_net_profit_bps"`  // Minimum net profit after fees
	MaxPriceImpactBps int        `json:"max_price_impact_bps"` // Max acceptable price impact
	DefaultTradeSize  *big.Float `json:"default_trade_size"`  // Default trade size for calculations
	QuoteValiditySecs int        `json:"quote_validity_secs"` // How long quotes are valid
}

// FeeStructure represents trading fees for an exchange.
type FeeStructure struct {
	Exchange      string  `json:"exchange"`
	MakerFeeBps   int     `json:"maker_fee_bps"`   // Maker fee in basis points
	TakerFeeBps   int     `json:"taker_fee_bps"`   // Taker fee in basis points
	WithdrawalFee float64 `json:"withdrawal_fee"`  // Flat withdrawal fee
	DepositFee    float64 `json:"deposit_fee"`     // Flat deposit fee (usually 0)
}

// BotStats holds runtime statistics.
type BotStats struct {
	StartTime               time.Time `json:"start_time"`
	TotalCycles             int64     `json:"total_cycles"`
	OpportunitiesFound      int64     `json:"opportunities_found"`
	ProfitableOpportunities int64     `json:"profitable_opportunities"`
	ChainOpportunitiesFound int64     `json:"chain_opportunities_found"`
	LastCycleTime           time.Time `json:"last_cycle_time"`
	LastCycleDurationMs     int64     `json:"last_cycle_duration_ms"`
	Errors                  int64     `json:"errors"`
}

// ChainHop represents a single hop in a chain arbitrage path.
type ChainHop struct {
	Exchange  string     `json:"exchange"`
	Action    string     `json:"action"` // "buy" or "sell"
	Pair      string     `json:"pair"`
	Price     *big.Float `json:"price"`
	Size      *big.Float `json:"size,omitempty"`
	FeeBps    int        `json:"fee_bps"`
	Timestamp time.Time  `json:"timestamp"`
}

// ChainArbitrageOpportunity represents a multi-hop arbitrage opportunity.
type ChainArbitrageOpportunity struct {
	ID   string `json:"id"`
	Pair string `json:"pair"` // The trading pair being arbitraged

	// Chain of exchanges: e.g., ["binance", "gswap", "coinbase"]
	Chain []string `json:"chain"`

	// Individual hops in the chain
	Hops []ChainHop `json:"hops"`

	// Starting and ending state
	StartExchange string     `json:"start_exchange"`
	EndExchange   string     `json:"end_exchange"`
	StartAmount   *big.Float `json:"start_amount"` // Initial capital
	EndAmount     *big.Float `json:"end_amount"`   // Final amount after all hops

	// Profit metrics
	GrossProfit    *big.Float `json:"gross_profit"`     // EndAmount - StartAmount (before fees)
	TotalFees      *big.Float `json:"total_fees"`       // Sum of all fees across hops
	NetProfit      *big.Float `json:"net_profit"`       // EndAmount - StartAmount - TotalFees
	NetProfitBps   int        `json:"net_profit_bps"`   // Net profit in basis points
	SpreadPercent  float64    `json:"spread_percent"`   // Total spread as percentage
	SpreadBps      int        `json:"spread_bps"`       // Total spread in basis points

	// Chain length
	HopCount int `json:"hop_count"` // Number of exchanges in the chain

	// Timestamps
	DetectedAt time.Time `json:"detected_at"`
	ExpiresAt  time.Time `json:"expires_at"`

	// Validity
	IsValid             bool     `json:"is_valid"`
	InvalidationReasons []string `json:"invalidation_reasons,omitempty"`
}

// ChainPath represents a path through exchanges for arbitrage calculation.
type ChainPath struct {
	Exchanges []string // Ordered list of exchanges in the path
}

// String returns a string representation of the chain path.
func (p ChainPath) String() string {
	result := ""
	for i, ex := range p.Exchanges {
		if i > 0 {
			result += " -> "
		}
		result += ex
	}
	return result
}
