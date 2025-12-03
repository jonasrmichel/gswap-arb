// Package executor provides trade execution implementations.
package executor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

const (
	binanceBaseURL   = "https://api.binance.com"
	binanceUSBaseURL = "https://api.binance.us"
)

// BinanceExecutor implements TradeExecutor for Binance.
type BinanceExecutor struct {
	apiKey    string
	secretKey string
	baseURL   string
	client    *http.Client

	// Cache for exchange info
	exchangeInfo     *binanceExchangeInfo
	exchangeInfoOnce sync.Once

	ready bool
	mu    sync.RWMutex
}

// binanceExchangeInfo holds exchange trading rules.
type binanceExchangeInfo struct {
	Symbols []binanceSymbol `json:"symbols"`
}

type binanceSymbol struct {
	Symbol             string          `json:"symbol"`
	Status             string          `json:"status"`
	BaseAsset          string          `json:"baseAsset"`
	QuoteAsset         string          `json:"quoteAsset"`
	BaseAssetPrecision int             `json:"baseAssetPrecision"`
	QuotePrecision     int             `json:"quotePrecision"`
	Filters            []binanceFilter `json:"filters"`
}

type binanceFilter struct {
	FilterType  string `json:"filterType"`
	MinPrice    string `json:"minPrice,omitempty"`
	MaxPrice    string `json:"maxPrice,omitempty"`
	TickSize    string `json:"tickSize,omitempty"`
	MinQty      string `json:"minQty,omitempty"`
	MaxQty      string `json:"maxQty,omitempty"`
	StepSize    string `json:"stepSize,omitempty"`
	MinNotional string `json:"minNotional,omitempty"`
}

// binanceAccountInfo holds account balance information.
type binanceAccountInfo struct {
	Balances []binanceBalance `json:"balances"`
}

type binanceBalance struct {
	Asset  string `json:"asset"`
	Free   string `json:"free"`
	Locked string `json:"locked"`
}

// binanceOrderResponse holds order response from Binance.
type binanceOrderResponse struct {
	Symbol              string `json:"symbol"`
	OrderID             int64  `json:"orderId"`
	ClientOrderID       string `json:"clientOrderId"`
	TransactTime        int64  `json:"transactTime"`
	Price               string `json:"price"`
	OrigQty             string `json:"origQty"`
	ExecutedQty         string `json:"executedQty"`
	CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
	Status              string `json:"status"`
	Type                string `json:"type"`
	Side                string `json:"side"`
	Fills               []struct {
		Price           string `json:"price"`
		Qty             string `json:"qty"`
		Commission      string `json:"commission"`
		CommissionAsset string `json:"commissionAsset"`
	} `json:"fills"`
}

// binanceError represents an API error response.
type binanceError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// NewBinanceExecutor creates a new Binance trade executor.
func NewBinanceExecutor(apiKey, secretKey string, useUS bool) *BinanceExecutor {
	baseURL := binanceBaseURL
	if useUS {
		baseURL = binanceUSBaseURL
	}

	return &BinanceExecutor{
		apiKey:    apiKey,
		secretKey: secretKey,
		baseURL:   baseURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Name returns the exchange name.
func (b *BinanceExecutor) Name() string {
	return "binance"
}

// Type returns the exchange type.
func (b *BinanceExecutor) Type() types.ExchangeType {
	return types.ExchangeTypeCEX
}

// Initialize sets up the executor and validates credentials.
func (b *BinanceExecutor) Initialize(ctx context.Context) error {
	if b.apiKey == "" || b.secretKey == "" {
		return fmt.Errorf("binance API key and secret are required")
	}

	// Test authentication by fetching account info
	_, err := b.GetBalances(ctx)
	if err != nil {
		return fmt.Errorf("failed to authenticate with Binance: %w", err)
	}

	// Load exchange info for trading rules
	if err := b.loadExchangeInfo(ctx); err != nil {
		return fmt.Errorf("failed to load exchange info: %w", err)
	}

	b.mu.Lock()
	b.ready = true
	b.mu.Unlock()

	return nil
}

// loadExchangeInfo loads trading rules from Binance.
func (b *BinanceExecutor) loadExchangeInfo(ctx context.Context) error {
	var loadErr error
	b.exchangeInfoOnce.Do(func() {
		resp, err := b.doPublicRequest(ctx, "GET", "/api/v3/exchangeInfo", nil)
		if err != nil {
			loadErr = err
			return
		}
		defer resp.Body.Close()

		var info binanceExchangeInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			loadErr = fmt.Errorf("failed to decode exchange info: %w", err)
			return
		}
		b.exchangeInfo = &info
	})
	return loadErr
}

// GetBalance retrieves the balance for a specific currency.
func (b *BinanceExecutor) GetBalance(ctx context.Context, currency string) (*Balance, error) {
	balances, err := b.GetBalances(ctx)
	if err != nil {
		return nil, err
	}

	balance, ok := balances[strings.ToUpper(currency)]
	if !ok {
		// Return zero balance if not found
		return &Balance{
			Exchange:  b.Name(),
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
func (b *BinanceExecutor) GetBalances(ctx context.Context) (map[string]*Balance, error) {
	params := url.Values{}
	params.Set("timestamp", fmt.Sprintf("%d", time.Now().UnixMilli()))

	resp, err := b.doSignedRequest(ctx, "GET", "/api/v3/account", params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr binanceError
		if err := json.Unmarshal(body, &apiErr); err == nil {
			return nil, fmt.Errorf("binance API error %d: %s", apiErr.Code, apiErr.Msg)
		}
		return nil, fmt.Errorf("binance API error: %s", string(body))
	}

	var accountInfo binanceAccountInfo
	if err := json.Unmarshal(body, &accountInfo); err != nil {
		return nil, fmt.Errorf("failed to decode account info: %w", err)
	}

	balances := make(map[string]*Balance)
	now := time.Now()

	for _, bal := range accountInfo.Balances {
		free, _ := new(big.Float).SetString(bal.Free)
		locked, _ := new(big.Float).SetString(bal.Locked)

		if free == nil {
			free = big.NewFloat(0)
		}
		if locked == nil {
			locked = big.NewFloat(0)
		}

		total := new(big.Float).Add(free, locked)

		// Skip zero balances
		if total.Sign() == 0 {
			continue
		}

		balances[bal.Asset] = &Balance{
			Exchange:  b.Name(),
			Currency:  bal.Asset,
			Free:      free,
			Locked:    locked,
			Total:     total,
			UpdatedAt: now,
		}
	}

	return balances, nil
}

// PlaceMarketOrder places a market order.
func (b *BinanceExecutor) PlaceMarketOrder(ctx context.Context, pair string, side OrderSide, amount *big.Float) (*Order, error) {
	symbol := b.pairToSymbol(pair)

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", strings.ToUpper(string(side)))
	params.Set("type", "MARKET")
	params.Set("quantity", b.formatQuantity(symbol, amount))
	params.Set("timestamp", fmt.Sprintf("%d", time.Now().UnixMilli()))

	resp, err := b.doSignedRequest(ctx, "POST", "/api/v3/order", params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr binanceError
		if err := json.Unmarshal(body, &apiErr); err == nil {
			return nil, fmt.Errorf("binance order error %d: %s", apiErr.Code, apiErr.Msg)
		}
		return nil, fmt.Errorf("binance order error: %s", string(body))
	}

	var orderResp binanceOrderResponse
	if err := json.Unmarshal(body, &orderResp); err != nil {
		return nil, fmt.Errorf("failed to decode order response: %w", err)
	}

	return b.parseOrderResponse(&orderResp, pair), nil
}

// PlaceLimitOrder places a limit order.
func (b *BinanceExecutor) PlaceLimitOrder(ctx context.Context, pair string, side OrderSide, amount, price *big.Float) (*Order, error) {
	symbol := b.pairToSymbol(pair)

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", strings.ToUpper(string(side)))
	params.Set("type", "LIMIT")
	params.Set("timeInForce", "GTC") // Good Till Canceled
	params.Set("quantity", b.formatQuantity(symbol, amount))
	params.Set("price", b.formatPrice(symbol, price))
	params.Set("timestamp", fmt.Sprintf("%d", time.Now().UnixMilli()))

	resp, err := b.doSignedRequest(ctx, "POST", "/api/v3/order", params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr binanceError
		if err := json.Unmarshal(body, &apiErr); err == nil {
			return nil, fmt.Errorf("binance order error %d: %s", apiErr.Code, apiErr.Msg)
		}
		return nil, fmt.Errorf("binance order error: %s", string(body))
	}

	var orderResp binanceOrderResponse
	if err := json.Unmarshal(body, &orderResp); err != nil {
		return nil, fmt.Errorf("failed to decode order response: %w", err)
	}

	return b.parseOrderResponse(&orderResp, pair), nil
}

// GetOrder retrieves an order by ID.
func (b *BinanceExecutor) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	// OrderID format: "symbol:orderId"
	parts := strings.SplitN(orderID, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid order ID format, expected 'symbol:orderId'")
	}

	symbol := parts[0]
	binanceOrderID := parts[1]

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", binanceOrderID)
	params.Set("timestamp", fmt.Sprintf("%d", time.Now().UnixMilli()))

	resp, err := b.doSignedRequest(ctx, "GET", "/api/v3/order", params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr binanceError
		if err := json.Unmarshal(body, &apiErr); err == nil {
			return nil, fmt.Errorf("binance API error %d: %s", apiErr.Code, apiErr.Msg)
		}
		return nil, fmt.Errorf("binance API error: %s", string(body))
	}

	var orderResp binanceOrderResponse
	if err := json.Unmarshal(body, &orderResp); err != nil {
		return nil, fmt.Errorf("failed to decode order response: %w", err)
	}

	return b.parseOrderResponse(&orderResp, b.symbolToPair(symbol)), nil
}

// CancelOrder cancels an open order.
func (b *BinanceExecutor) CancelOrder(ctx context.Context, orderID string) error {
	// OrderID format: "symbol:orderId"
	parts := strings.SplitN(orderID, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid order ID format, expected 'symbol:orderId'")
	}

	symbol := parts[0]
	binanceOrderID := parts[1]

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", binanceOrderID)
	params.Set("timestamp", fmt.Sprintf("%d", time.Now().UnixMilli()))

	resp, err := b.doSignedRequest(ctx, "DELETE", "/api/v3/order", params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr binanceError
		if err := json.Unmarshal(body, &apiErr); err == nil {
			return fmt.Errorf("binance API error %d: %s", apiErr.Code, apiErr.Msg)
		}
		return fmt.Errorf("binance API error: %s", string(body))
	}

	return nil
}

// GetOpenOrders retrieves all open orders for a pair.
func (b *BinanceExecutor) GetOpenOrders(ctx context.Context, pair string) ([]*Order, error) {
	symbol := b.pairToSymbol(pair)

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("timestamp", fmt.Sprintf("%d", time.Now().UnixMilli()))

	resp, err := b.doSignedRequest(ctx, "GET", "/api/v3/openOrders", params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr binanceError
		if err := json.Unmarshal(body, &apiErr); err == nil {
			return nil, fmt.Errorf("binance API error %d: %s", apiErr.Code, apiErr.Msg)
		}
		return nil, fmt.Errorf("binance API error: %s", string(body))
	}

	var ordersResp []binanceOrderResponse
	if err := json.Unmarshal(body, &ordersResp); err != nil {
		return nil, fmt.Errorf("failed to decode orders response: %w", err)
	}

	orders := make([]*Order, 0, len(ordersResp))
	for _, or := range ordersResp {
		orders = append(orders, b.parseOrderResponse(&or, pair))
	}

	return orders, nil
}

// IsReady returns true if the executor is ready to trade.
func (b *BinanceExecutor) IsReady() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ready
}

// Close cleans up resources.
func (b *BinanceExecutor) Close() error {
	b.mu.Lock()
	b.ready = false
	b.mu.Unlock()
	return nil
}

// Helper methods

// doPublicRequest performs an unauthenticated request.
func (b *BinanceExecutor) doPublicRequest(ctx context.Context, method, path string, params url.Values) (*http.Response, error) {
	reqURL := b.baseURL + path
	if params != nil && len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	return b.client.Do(req)
}

// doSignedRequest performs an authenticated request with HMAC signature.
func (b *BinanceExecutor) doSignedRequest(ctx context.Context, method, path string, params url.Values) (*http.Response, error) {
	// Add signature
	queryString := params.Encode()
	signature := b.sign(queryString)
	params.Set("signature", signature)

	reqURL := b.baseURL + path + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-MBX-APIKEY", b.apiKey)

	return b.client.Do(req)
}

// sign creates an HMAC SHA256 signature.
func (b *BinanceExecutor) sign(data string) string {
	h := hmac.New(sha256.New, []byte(b.secretKey))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// pairToSymbol converts "GALA/USDT" to "GALAUSDT".
func (b *BinanceExecutor) pairToSymbol(pair string) string {
	return strings.ReplaceAll(strings.ToUpper(pair), "/", "")
}

// symbolToPair converts "GALAUSDT" to "GALA/USDT" (best effort).
func (b *BinanceExecutor) symbolToPair(symbol string) string {
	// Common quote assets
	quotes := []string{"USDT", "USDC", "BUSD", "BTC", "ETH", "BNB"}
	for _, quote := range quotes {
		if strings.HasSuffix(symbol, quote) {
			base := strings.TrimSuffix(symbol, quote)
			return base + "/" + quote
		}
	}
	return symbol
}

// formatQuantity formats quantity according to symbol's step size.
func (b *BinanceExecutor) formatQuantity(symbol string, amount *big.Float) string {
	precision := 8 // Default precision
	if b.exchangeInfo != nil {
		for _, s := range b.exchangeInfo.Symbols {
			if s.Symbol == symbol {
				for _, f := range s.Filters {
					if f.FilterType == "LOT_SIZE" && f.StepSize != "" {
						precision = b.getPrecisionFromStep(f.StepSize)
						break
					}
				}
				break
			}
		}
	}
	return amount.Text('f', precision)
}

// formatPrice formats price according to symbol's tick size.
func (b *BinanceExecutor) formatPrice(symbol string, price *big.Float) string {
	precision := 8 // Default precision
	if b.exchangeInfo != nil {
		for _, s := range b.exchangeInfo.Symbols {
			if s.Symbol == symbol {
				for _, f := range s.Filters {
					if f.FilterType == "PRICE_FILTER" && f.TickSize != "" {
						precision = b.getPrecisionFromStep(f.TickSize)
						break
					}
				}
				break
			}
		}
	}
	return price.Text('f', precision)
}

// getPrecisionFromStep calculates decimal precision from step size.
func (b *BinanceExecutor) getPrecisionFromStep(step string) int {
	if step == "" {
		return 8
	}
	f, err := strconv.ParseFloat(step, 64)
	if err != nil || f == 0 {
		return 8
	}
	precision := 0
	for f < 1 {
		f *= 10
		precision++
	}
	return precision
}

// parseOrderResponse converts Binance order response to our Order type.
func (b *BinanceExecutor) parseOrderResponse(resp *binanceOrderResponse, pair string) *Order {
	price, _ := new(big.Float).SetString(resp.Price)
	origQty, _ := new(big.Float).SetString(resp.OrigQty)
	execQty, _ := new(big.Float).SetString(resp.ExecutedQty)
	cummQuoteQty, _ := new(big.Float).SetString(resp.CummulativeQuoteQty)

	// Calculate remaining
	remaining := new(big.Float).Sub(origQty, execQty)
	if remaining.Sign() < 0 {
		remaining = big.NewFloat(0)
	}

	// Calculate average price
	var avgPrice *big.Float
	if execQty != nil && execQty.Sign() > 0 && cummQuoteQty != nil {
		avgPrice = new(big.Float).Quo(cummQuoteQty, execQty)
	}

	// Calculate fees from fills
	var totalFee *big.Float
	var feeCurrency string
	if len(resp.Fills) > 0 {
		totalFee = big.NewFloat(0)
		for _, fill := range resp.Fills {
			commission, _ := new(big.Float).SetString(fill.Commission)
			if commission != nil {
				totalFee = new(big.Float).Add(totalFee, commission)
			}
			feeCurrency = fill.CommissionAsset
		}
	}

	// Map status
	status := b.mapOrderStatus(resp.Status)

	// Map side
	var side OrderSide
	if strings.ToLower(resp.Side) == "buy" {
		side = OrderSideBuy
	} else {
		side = OrderSideSell
	}

	// Map type
	var orderType OrderType
	if strings.ToLower(resp.Type) == "market" {
		orderType = OrderTypeMarket
	} else {
		orderType = OrderTypeLimit
	}

	return &Order{
		ID:              fmt.Sprintf("%s:%d", resp.Symbol, resp.OrderID),
		Exchange:        b.Name(),
		Pair:            pair,
		Side:            side,
		Type:            orderType,
		Status:          status,
		Price:           price,
		Amount:          origQty,
		FilledAmount:    execQty,
		RemainingAmount: remaining,
		AveragePrice:    avgPrice,
		Fee:             totalFee,
		FeeCurrency:     feeCurrency,
		CreatedAt:       time.UnixMilli(resp.TransactTime),
		UpdatedAt:       time.Now(),
	}
}

// mapOrderStatus maps Binance status to our OrderStatus.
func (b *BinanceExecutor) mapOrderStatus(status string) OrderStatus {
	switch strings.ToUpper(status) {
	case "NEW":
		return OrderStatusNew
	case "PARTIALLY_FILLED":
		return OrderStatusPartial
	case "FILLED":
		return OrderStatusFilled
	case "CANCELED":
		return OrderStatusCanceled
	case "REJECTED":
		return OrderStatusRejected
	case "EXPIRED":
		return OrderStatusExpired
	default:
		return OrderStatusNew
	}
}
