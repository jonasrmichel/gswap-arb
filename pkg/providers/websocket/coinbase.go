package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

const (
	coinbaseWSURL = "wss://ws-feed.exchange.coinbase.com"
)

// CoinbaseWSProvider provides real-time price feeds from Coinbase.
type CoinbaseWSProvider struct {
	*BaseWSProvider
	conn   *websocket.Conn
	connMu sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

// NewCoinbaseWSProvider creates a new Coinbase WebSocket provider.
func NewCoinbaseWSProvider() *CoinbaseWSProvider {
	return &CoinbaseWSProvider{
		BaseWSProvider: NewBaseWSProvider("coinbase", &types.FeeStructure{
			Exchange:    "coinbase",
			MakerFeeBps: 40,
			TakerFeeBps: 60,
		}),
	}
}

// Connect establishes the WebSocket connection to Coinbase.
func (c *CoinbaseWSProvider) Connect(ctx context.Context) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.State() == StateConnected {
		return nil
	}

	c.SetState(StateConnecting)
	c.ctx, c.cancel = context.WithCancel(ctx)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, coinbaseWSURL, nil)
	if err != nil {
		c.SetState(StateDisconnected)
		return fmt.Errorf("failed to connect to Coinbase WebSocket: %w", err)
	}

	c.conn = conn
	c.SetState(StateConnected)
	c.ResetReconnectAttempts()

	// Start message handler
	go c.handleMessages()

	return nil
}

// Disconnect closes the WebSocket connection.
func (c *CoinbaseWSProvider) Disconnect() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.cancel != nil {
		c.cancel()
	}

	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		c.SetState(StateDisconnected)
		return err
	}

	return nil
}

// Subscribe subscribes to ticker updates for the given pairs.
func (c *CoinbaseWSProvider) Subscribe(pairs []string) error {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	// Convert pairs to Coinbase product IDs
	productIDs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		productID := strings.ReplaceAll(pair, "/", "-")
		productIDs = append(productIDs, productID)
		c.AddSubscription(pair)
	}

	// Send subscription message
	msg := coinbaseSubscribeMsg{
		Type:       "subscribe",
		ProductIDs: productIDs,
		Channels: []interface{}{
			"ticker",
			map[string]interface{}{
				"name":        "ticker",
				"product_ids": productIDs,
			},
		},
	}

	return conn.WriteJSON(msg)
}

// Unsubscribe unsubscribes from ticker updates for the given pairs.
func (c *CoinbaseWSProvider) Unsubscribe(pairs []string) error {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	productIDs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		productID := strings.ReplaceAll(pair, "/", "-")
		productIDs = append(productIDs, productID)
		c.RemoveSubscription(pair)
	}

	msg := coinbaseSubscribeMsg{
		Type:       "unsubscribe",
		ProductIDs: productIDs,
		Channels:   []interface{}{"ticker"},
	}

	return conn.WriteJSON(msg)
}

type coinbaseSubscribeMsg struct {
	Type       string        `json:"type"`
	ProductIDs []string      `json:"product_ids"`
	Channels   []interface{} `json:"channels"`
}

// handleMessages processes incoming WebSocket messages.
func (c *CoinbaseWSProvider) handleMessages() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		c.connMu.Lock()
		conn := c.conn
		c.connMu.Unlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			c.EmitError(fmt.Errorf("read error: %w", err))
			c.reconnect()
			return
		}

		c.processMessage(message)
	}
}

// processMessage parses and processes a WebSocket message.
func (c *CoinbaseWSProvider) processMessage(data []byte) {
	var baseMsg struct {
		Type string `json:"type"`
	}

	if err := json.Unmarshal(data, &baseMsg); err != nil {
		return
	}

	switch baseMsg.Type {
	case "ticker":
		var ticker coinbaseTicker
		if err := json.Unmarshal(data, &ticker); err == nil {
			c.handleTicker(&ticker)
		}
	case "error":
		var errMsg struct {
			Message string `json:"message"`
			Reason  string `json:"reason"`
		}
		if err := json.Unmarshal(data, &errMsg); err == nil {
			c.EmitError(fmt.Errorf("coinbase error: %s - %s", errMsg.Message, errMsg.Reason))
		}
	}
}

// coinbaseTicker represents Coinbase ticker data.
type coinbaseTicker struct {
	Type      string `json:"type"`
	Sequence  int64  `json:"sequence"`
	ProductID string `json:"product_id"`
	Price     string `json:"price"`
	Open24h   string `json:"open_24h"`
	Volume24h string `json:"volume_24h"`
	Low24h    string `json:"low_24h"`
	High24h   string `json:"high_24h"`
	BestBid   string `json:"best_bid"`
	BestAsk   string `json:"best_ask"`
	BestBidSize string `json:"best_bid_size"`
	BestAskSize string `json:"best_ask_size"`
	Side      string `json:"side"`
	Time      string `json:"time"`
	TradeID   int64  `json:"trade_id"`
	LastSize  string `json:"last_size"`
}

// handleTicker processes a ticker update.
func (c *CoinbaseWSProvider) handleTicker(ticker *coinbaseTicker) {
	pair := strings.ReplaceAll(ticker.ProductID, "-", "/")

	bidPrice := new(big.Float)
	bidPrice.SetString(ticker.BestBid)

	askPrice := new(big.Float)
	askPrice.SetString(ticker.BestAsk)

	bidSize := new(big.Float)
	bidSize.SetString(ticker.BestBidSize)

	askSize := new(big.Float)
	askSize.SetString(ticker.BestAskSize)

	lastPrice := new(big.Float)
	lastPrice.SetString(ticker.Price)

	volume := new(big.Float)
	volume.SetString(ticker.Volume24h)

	timestamp := time.Now()
	if ticker.Time != "" {
		if t, err := time.Parse(time.RFC3339Nano, ticker.Time); err == nil {
			timestamp = t
		}
	}

	update := &PriceUpdate{
		Exchange:   c.Name(),
		Pair:       pair,
		BidPrice:   bidPrice,
		AskPrice:   askPrice,
		BidSize:    bidSize,
		AskSize:    askSize,
		LastPrice:  lastPrice,
		Volume24h:  volume,
		Timestamp:  timestamp,
		SequenceID: ticker.Sequence,
	}

	c.EmitPriceUpdate(update)
}

// reconnect attempts to reconnect to the WebSocket.
func (c *CoinbaseWSProvider) reconnect() {
	c.SetState(StateReconnecting)

	pairs := c.GetSubscribedPairs()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		delay := c.CalculateReconnectDelay()
		time.Sleep(delay)

		if err := c.Connect(c.ctx); err != nil {
			c.EmitError(fmt.Errorf("reconnect failed: %w", err))
			continue
		}

		if len(pairs) > 0 {
			if err := c.Subscribe(pairs); err != nil {
				c.EmitError(fmt.Errorf("resubscribe failed: %w", err))
			}
		}

		return
	}
}
