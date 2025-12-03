// Package executor provides trade execution interfaces and implementations.
package executor

import (
	"context"
	"math/big"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// OrderSide represents buy or sell.
type OrderSide string

const (
	OrderSideBuy  OrderSide = "buy"
	OrderSideSell OrderSide = "sell"
)

// OrderType represents the type of order.
type OrderType string

const (
	OrderTypeMarket OrderType = "market"
	OrderTypeLimit  OrderType = "limit"
)

// OrderStatus represents the status of an order.
type OrderStatus string

const (
	OrderStatusNew       OrderStatus = "new"
	OrderStatusOpen      OrderStatus = "open"
	OrderStatusFilled    OrderStatus = "filled"
	OrderStatusPartial   OrderStatus = "partial"
	OrderStatusCanceled  OrderStatus = "canceled"
	OrderStatusRejected  OrderStatus = "rejected"
	OrderStatusExpired   OrderStatus = "expired"
	OrderStatusFailed    OrderStatus = "failed"
)

// Order represents a trade order.
type Order struct {
	ID            string      `json:"id"`
	Exchange      string      `json:"exchange"`
	Pair          string      `json:"pair"`
	Side          OrderSide   `json:"side"`
	Type          OrderType   `json:"type"`
	Status        OrderStatus `json:"status"`
	Price         *big.Float  `json:"price"`
	Amount        *big.Float  `json:"amount"`          // Requested amount
	FilledAmount  *big.Float  `json:"filled_amount"`   // Actually filled
	RemainingAmount *big.Float `json:"remaining_amount"`
	AveragePrice  *big.Float  `json:"average_price"`   // Average fill price
	Fee           *big.Float  `json:"fee"`
	FeeCurrency   string      `json:"fee_currency"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	TransactionID string      `json:"transaction_id,omitempty"` // For DEX trades
}

// Balance represents a token balance on an exchange.
type Balance struct {
	Exchange  string     `json:"exchange"`
	Currency  string     `json:"currency"`
	Free      *big.Float `json:"free"`      // Available for trading
	Locked    *big.Float `json:"locked"`    // In open orders
	Total     *big.Float `json:"total"`     // Free + Locked
	UpdatedAt time.Time  `json:"updated_at"`
}

// TradeExecutor defines the interface for executing trades on an exchange.
type TradeExecutor interface {
	// Name returns the exchange name.
	Name() string

	// Type returns whether this is a CEX or DEX.
	Type() types.ExchangeType

	// Initialize sets up the executor with credentials.
	Initialize(ctx context.Context) error

	// GetBalance retrieves the balance for a specific currency.
	GetBalance(ctx context.Context, currency string) (*Balance, error)

	// GetBalances retrieves all balances.
	GetBalances(ctx context.Context) (map[string]*Balance, error)

	// PlaceMarketOrder places a market order (immediate execution at best price).
	PlaceMarketOrder(ctx context.Context, pair string, side OrderSide, amount *big.Float) (*Order, error)

	// PlaceLimitOrder places a limit order (execution at specified price or better).
	PlaceLimitOrder(ctx context.Context, pair string, side OrderSide, amount, price *big.Float) (*Order, error)

	// GetOrder retrieves an order by ID.
	GetOrder(ctx context.Context, orderID string) (*Order, error)

	// CancelOrder cancels an open order.
	CancelOrder(ctx context.Context, orderID string) error

	// GetOpenOrders retrieves all open orders for a pair.
	GetOpenOrders(ctx context.Context, pair string) ([]*Order, error)

	// IsReady returns true if the executor is properly configured and ready to trade.
	IsReady() bool

	// Close cleans up resources.
	Close() error
}

// ExecutorRegistry manages multiple trade executors.
type ExecutorRegistry struct {
	executors map[string]TradeExecutor
}

// NewExecutorRegistry creates a new executor registry.
func NewExecutorRegistry() *ExecutorRegistry {
	return &ExecutorRegistry{
		executors: make(map[string]TradeExecutor),
	}
}

// Register adds an executor to the registry.
func (r *ExecutorRegistry) Register(executor TradeExecutor) {
	r.executors[executor.Name()] = executor
}

// Get retrieves an executor by name.
func (r *ExecutorRegistry) Get(name string) (TradeExecutor, bool) {
	executor, ok := r.executors[name]
	return executor, ok
}

// GetAll returns all registered executors.
func (r *ExecutorRegistry) GetAll() map[string]TradeExecutor {
	return r.executors
}

// GetReady returns all executors that are ready to trade.
func (r *ExecutorRegistry) GetReady() []TradeExecutor {
	var ready []TradeExecutor
	for _, executor := range r.executors {
		if executor.IsReady() {
			ready = append(ready, executor)
		}
	}
	return ready
}

// Close closes all executors.
func (r *ExecutorRegistry) Close() error {
	var lastErr error
	for _, executor := range r.executors {
		if err := executor.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// TradeRequest represents a request to execute a trade.
type TradeRequest struct {
	Exchange      string      `json:"exchange"`
	Pair          string      `json:"pair"`
	Side          OrderSide   `json:"side"`
	Type          OrderType   `json:"type"`
	Amount        *big.Float  `json:"amount"`
	Price         *big.Float  `json:"price,omitempty"`      // For limit orders
	MinAmount     *big.Float  `json:"min_amount,omitempty"` // Minimum acceptable fill
	MaxSlippageBps int        `json:"max_slippage_bps"`     // Max slippage in basis points
	Deadline      time.Time   `json:"deadline"`             // Cancel if not filled by this time
}

// TradeResult represents the result of a trade execution.
type TradeResult struct {
	Request       *TradeRequest `json:"request"`
	Order         *Order        `json:"order"`
	Success       bool          `json:"success"`
	Error         string        `json:"error,omitempty"`
	ExecutionTime time.Duration `json:"execution_time"`
}

// ArbitrageExecution represents a complete arbitrage execution (buy + sell).
type ArbitrageExecution struct {
	ID            string                      `json:"id"`
	Opportunity   *types.ArbitrageOpportunity `json:"opportunity"`
	BuyTrade      *TradeResult                `json:"buy_trade"`
	SellTrade     *TradeResult                `json:"sell_trade"`
	GrossProfit   *big.Float                  `json:"gross_profit"`
	TotalFees     *big.Float                  `json:"total_fees"`
	NetProfit     *big.Float                  `json:"net_profit"`
	Success       bool                        `json:"success"`
	Error         string                      `json:"error,omitempty"`
	StartedAt     time.Time                   `json:"started_at"`
	CompletedAt   time.Time                   `json:"completed_at"`
	DryRun        bool                        `json:"dry_run"`
}
