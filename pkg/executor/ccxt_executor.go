// Package executor provides trade execution implementations.
package executor

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	ccxt "github.com/ccxt/ccxt/go/v4"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// CCXTExecutor implements TradeExecutor using the CCXT library.
// This provides a unified interface for multiple CEX exchanges.
type CCXTExecutor struct {
	exchange     ccxt.IExchange
	exchangeID   string
	exchangeType types.ExchangeType

	ready bool
	mu    sync.RWMutex
}

// SupportedCCXTExchanges lists exchanges supported via CCXT.
var SupportedCCXTExchanges = map[string]bool{
	"binance":  true,
	"kraken":   true,
	"coinbase": true,
	"okx":      true,
	"bybit":    true,
	"kucoin":   true,
	"gate":     true,
	"huobi":    true,
	"bitfinex": true,
	"bitstamp": true,
}

// CCXTConfig holds configuration for creating a CCXT executor.
type CCXTConfig struct {
	ExchangeID string
	APIKey     string
	Secret     string
	Password   string // For exchanges that require passphrase (Coinbase, OKX, KuCoin)
	Sandbox    bool   // Use sandbox/testnet mode
}

// NewCCXTExecutor creates a new CCXT-based trade executor.
func NewCCXTExecutor(config *CCXTConfig) (*CCXTExecutor, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	exchangeID := strings.ToLower(config.ExchangeID)
	if !SupportedCCXTExchanges[exchangeID] {
		return nil, fmt.Errorf("unsupported exchange: %s", exchangeID)
	}

	// Create exchange instance dynamically
	exchange := ccxt.CreateExchange(exchangeID, nil)
	if exchange == nil {
		return nil, fmt.Errorf("failed to create exchange: %s", exchangeID)
	}

	// Set credentials
	exchange.SetApiKey(config.APIKey)
	exchange.SetSecret(config.Secret)
	if config.Password != "" {
		exchange.SetPassword(config.Password)
	}

	// Enable sandbox mode if requested
	if config.Sandbox {
		exchange.SetSandboxMode(true)
	}

	return &CCXTExecutor{
		exchange:     exchange,
		exchangeID:   exchangeID,
		exchangeType: types.ExchangeTypeCEX,
	}, nil
}

// Name returns the exchange name.
func (c *CCXTExecutor) Name() string {
	return c.exchangeID
}

// Type returns the exchange type.
func (c *CCXTExecutor) Type() types.ExchangeType {
	return c.exchangeType
}

// Initialize sets up the executor and validates credentials.
func (c *CCXTExecutor) Initialize(ctx context.Context) error {
	// Load markets
	_, err := c.exchange.LoadMarkets()
	if err != nil {
		return fmt.Errorf("failed to load markets: %w", err)
	}

	// Test authentication by fetching balance
	_, err = c.exchange.FetchBalance()
	if err != nil {
		return fmt.Errorf("failed to authenticate with %s: %w", c.exchangeID, err)
	}

	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()

	return nil
}

// GetBalance retrieves the balance for a specific currency.
func (c *CCXTExecutor) GetBalance(ctx context.Context, currency string) (*Balance, error) {
	balances, err := c.GetBalances(ctx)
	if err != nil {
		return nil, err
	}

	currency = strings.ToUpper(currency)
	balance, ok := balances[currency]
	if !ok {
		// Return zero balance if not found
		return &Balance{
			Exchange:  c.Name(),
			Currency:  currency,
			Free:      big.NewFloat(0),
			Locked:    big.NewFloat(0),
			Total:     big.NewFloat(0),
			UpdatedAt: time.Now(),
		}, nil
	}

	return balance, nil
}

// GetBalances retrieves all balances.
func (c *CCXTExecutor) GetBalances(ctx context.Context) (map[string]*Balance, error) {
	ccxtBalances, err := c.exchange.FetchBalance()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch balance: %w", err)
	}

	balances := make(map[string]*Balance)
	now := time.Now()

	// Use the Balances struct fields
	for currency, bal := range ccxtBalances.Balances {
		free := big.NewFloat(0)
		used := big.NewFloat(0)
		total := big.NewFloat(0)

		if bal.Free != nil {
			free = big.NewFloat(*bal.Free)
		}
		if bal.Used != nil {
			used = big.NewFloat(*bal.Used)
		}
		if bal.Total != nil {
			total = big.NewFloat(*bal.Total)
		}

		// Skip zero balances
		if total.Sign() == 0 {
			continue
		}

		balances[currency] = &Balance{
			Exchange:  c.Name(),
			Currency:  currency,
			Free:      free,
			Locked:    used,
			Total:     total,
			UpdatedAt: now,
		}
	}

	return balances, nil
}

// PlaceMarketOrder places a market order.
func (c *CCXTExecutor) PlaceMarketOrder(ctx context.Context, pair string, side OrderSide, amount *big.Float) (*Order, error) {
	symbol := c.normalizeSymbol(pair)
	amountFloat, _ := amount.Float64()

	var ccxtOrder ccxt.Order
	var err error

	if side == OrderSideBuy {
		ccxtOrder, err = c.exchange.CreateMarketBuyOrder(symbol, amountFloat)
	} else {
		ccxtOrder, err = c.exchange.CreateMarketSellOrder(symbol, amountFloat)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to place market order: %w", err)
	}

	return c.parseOrder(&ccxtOrder, pair, side, OrderTypeMarket), nil
}

// PlaceLimitOrder places a limit order.
func (c *CCXTExecutor) PlaceLimitOrder(ctx context.Context, pair string, side OrderSide, amount, price *big.Float) (*Order, error) {
	symbol := c.normalizeSymbol(pair)
	amountFloat, _ := amount.Float64()
	priceFloat, _ := price.Float64()

	var ccxtOrder ccxt.Order
	var err error

	if side == OrderSideBuy {
		ccxtOrder, err = c.exchange.CreateLimitBuyOrder(symbol, amountFloat, priceFloat)
	} else {
		ccxtOrder, err = c.exchange.CreateLimitSellOrder(symbol, amountFloat, priceFloat)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to place limit order: %w", err)
	}

	return c.parseOrder(&ccxtOrder, pair, side, OrderTypeLimit), nil
}

// GetOrder retrieves an order by ID.
func (c *CCXTExecutor) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	// Parse orderID which may contain symbol: "symbol:id"
	symbol := ""
	id := orderID
	if parts := strings.SplitN(orderID, ":", 2); len(parts) == 2 {
		symbol = parts[0]
		id = parts[1]
	}

	ccxtOrder, err := c.exchange.FetchOrder(id, ccxt.WithFetchOrderSymbol(symbol))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch order: %w", err)
	}

	return c.parseOrder(&ccxtOrder, symbol, "", ""), nil
}

// CancelOrder cancels an open order.
func (c *CCXTExecutor) CancelOrder(ctx context.Context, orderID string) error {
	// Parse orderID which may contain symbol: "symbol:id"
	symbol := ""
	id := orderID
	if parts := strings.SplitN(orderID, ":", 2); len(parts) == 2 {
		symbol = parts[0]
		id = parts[1]
	}

	_, err := c.exchange.CancelOrder(id, ccxt.WithCancelOrderSymbol(symbol))
	if err != nil {
		return fmt.Errorf("failed to cancel order: %w", err)
	}

	return nil
}

// GetOpenOrders retrieves all open orders for a pair.
func (c *CCXTExecutor) GetOpenOrders(ctx context.Context, pair string) ([]*Order, error) {
	symbol := c.normalizeSymbol(pair)

	ccxtOrders, err := c.exchange.FetchOpenOrders(ccxt.WithFetchOpenOrdersSymbol(symbol))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch open orders: %w", err)
	}

	orders := make([]*Order, 0, len(ccxtOrders))
	for i := range ccxtOrders {
		orders = append(orders, c.parseOrder(&ccxtOrders[i], pair, "", ""))
	}

	return orders, nil
}

// IsReady returns true if the executor is ready to trade.
func (c *CCXTExecutor) IsReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ready
}

// Close cleans up resources.
func (c *CCXTExecutor) Close() error {
	c.mu.Lock()
	c.ready = false
	c.mu.Unlock()
	return nil
}

// Helper methods

// normalizeSymbol ensures the symbol is in the format expected by CCXT.
func (c *CCXTExecutor) normalizeSymbol(pair string) string {
	// CCXT uses "BTC/USDT" format
	if !strings.Contains(pair, "/") {
		// Try to insert slash for common quote currencies
		quotes := []string{"USDT", "USDC", "USD", "EUR", "BTC", "ETH"}
		for _, q := range quotes {
			if strings.HasSuffix(pair, q) {
				base := strings.TrimSuffix(pair, q)
				return base + "/" + q
			}
		}
	}
	return pair
}

// parseOrder converts a CCXT order to our Order type.
func (c *CCXTExecutor) parseOrder(ccxtOrder *ccxt.Order, pair string, side OrderSide, orderType OrderType) *Order {
	if ccxtOrder == nil {
		return nil
	}

	// Use provided values or parse from CCXT order
	if pair == "" && ccxtOrder.Symbol != nil {
		pair = *ccxtOrder.Symbol
	}

	if side == "" && ccxtOrder.Side != nil {
		if *ccxtOrder.Side == "buy" {
			side = OrderSideBuy
		} else if *ccxtOrder.Side == "sell" {
			side = OrderSideSell
		}
	}

	if orderType == "" && ccxtOrder.Type != nil {
		if *ccxtOrder.Type == "market" {
			orderType = OrderTypeMarket
		} else if *ccxtOrder.Type == "limit" {
			orderType = OrderTypeLimit
		}
	}

	// Map status
	var status OrderStatus
	if ccxtOrder.Status != nil {
		status = mapCCXTStatus(*ccxtOrder.Status)
	}

	// Convert numeric fields safely
	amount := big.NewFloat(0)
	filled := big.NewFloat(0)
	remaining := big.NewFloat(0)
	price := big.NewFloat(0)
	avgPrice := big.NewFloat(0)
	fee := big.NewFloat(0)

	if ccxtOrder.Amount != nil {
		amount = big.NewFloat(*ccxtOrder.Amount)
	}
	if ccxtOrder.Filled != nil {
		filled = big.NewFloat(*ccxtOrder.Filled)
	}
	if ccxtOrder.Remaining != nil {
		remaining = big.NewFloat(*ccxtOrder.Remaining)
	}
	if ccxtOrder.Price != nil {
		price = big.NewFloat(*ccxtOrder.Price)
	}
	if ccxtOrder.Average != nil {
		avgPrice = big.NewFloat(*ccxtOrder.Average)
	}
	if ccxtOrder.Fee.Cost != nil {
		fee = big.NewFloat(*ccxtOrder.Fee.Cost)
	}

	// Parse timestamps
	createdAt := time.Now()
	if ccxtOrder.Timestamp != nil {
		createdAt = time.UnixMilli(*ccxtOrder.Timestamp)
	}

	// Get order ID
	orderID := ""
	if ccxtOrder.Id != nil {
		orderID = fmt.Sprintf("%s:%s", pair, *ccxtOrder.Id)
	}

	return &Order{
		ID:              orderID,
		Exchange:        c.Name(),
		Pair:            pair,
		Side:            side,
		Type:            orderType,
		Status:          status,
		Price:           price,
		Amount:          amount,
		FilledAmount:    filled,
		RemainingAmount: remaining,
		AveragePrice:    avgPrice,
		Fee:             fee,
		FeeCurrency:     "", // CCXT Fee struct doesn't include currency
		CreatedAt:       createdAt,
		UpdatedAt:       time.Now(),
	}
}

// mapCCXTStatus maps CCXT order status to our OrderStatus.
func mapCCXTStatus(status string) OrderStatus {
	switch strings.ToLower(status) {
	case "open":
		return OrderStatusOpen
	case "closed":
		return OrderStatusFilled
	case "canceled", "cancelled":
		return OrderStatusCanceled
	case "expired":
		return OrderStatusExpired
	case "rejected":
		return OrderStatusRejected
	default:
		return OrderStatusNew
	}
}

// GetExchange returns the underlying CCXT exchange instance.
// This can be used for advanced operations not covered by TradeExecutor.
func (c *CCXTExecutor) GetExchange() ccxt.IExchange {
	return c.exchange
}

// FetchTicker fetches the current ticker for a symbol.
func (c *CCXTExecutor) FetchTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
	symbol := c.normalizeSymbol(pair)

	ticker, err := c.exchange.FetchTicker(symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ticker: %w", err)
	}

	// Safely convert ticker fields
	lastPrice := big.NewFloat(0)
	bidPrice := big.NewFloat(0)
	askPrice := big.NewFloat(0)
	bidSize := big.NewFloat(0)
	askSize := big.NewFloat(0)
	volume := big.NewFloat(0)
	var timestamp time.Time

	if ticker.Last != nil {
		lastPrice = big.NewFloat(*ticker.Last)
	}
	if ticker.Bid != nil {
		bidPrice = big.NewFloat(*ticker.Bid)
	}
	if ticker.Ask != nil {
		askPrice = big.NewFloat(*ticker.Ask)
	}
	if ticker.BidVolume != nil {
		bidSize = big.NewFloat(*ticker.BidVolume)
	}
	if ticker.AskVolume != nil {
		askSize = big.NewFloat(*ticker.AskVolume)
	}
	if ticker.BaseVolume != nil {
		volume = big.NewFloat(*ticker.BaseVolume)
	}
	if ticker.Timestamp != nil {
		timestamp = time.UnixMilli(*ticker.Timestamp)
	} else {
		timestamp = time.Now()
	}

	return &types.PriceQuote{
		Exchange:  c.Name(),
		Pair:      pair,
		Price:     lastPrice,
		BidPrice:  bidPrice,
		AskPrice:  askPrice,
		BidSize:   bidSize,
		AskSize:   askSize,
		Volume24h: volume,
		Timestamp: timestamp,
		Source:    "ccxt",
	}, nil
}
