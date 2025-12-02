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
	bybitWSURL = "wss://stream.bybit.com/v5/public/spot"
)

// BybitWSProvider provides real-time price feeds from Bybit.
type BybitWSProvider struct {
	*BaseWSProvider
	conn   *websocket.Conn
	connMu sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	reqID  int
	reqMu  sync.Mutex
}

// NewBybitWSProvider creates a new Bybit WebSocket provider.
func NewBybitWSProvider() *BybitWSProvider {
	return &BybitWSProvider{
		BaseWSProvider: NewBaseWSProvider("bybit", &types.FeeStructure{
			Exchange:    "bybit",
			MakerFeeBps: 10,
			TakerFeeBps: 10,
		}),
	}
}

// Connect establishes the WebSocket connection to Bybit.
func (b *BybitWSProvider) Connect(ctx context.Context) error {
	b.connMu.Lock()
	defer b.connMu.Unlock()

	if b.State() == StateConnected {
		return nil
	}

	b.SetState(StateConnecting)
	b.ctx, b.cancel = context.WithCancel(ctx)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, bybitWSURL, nil)
	if err != nil {
		b.SetState(StateDisconnected)
		return fmt.Errorf("failed to connect to Bybit WebSocket: %w", err)
	}

	b.conn = conn
	b.SetState(StateConnected)
	b.ResetReconnectAttempts()

	// Start message handler
	go b.handleMessages()

	// Start ping handler
	go b.pingHandler()

	return nil
}

// Disconnect closes the WebSocket connection.
func (b *BybitWSProvider) Disconnect() error {
	b.connMu.Lock()
	defer b.connMu.Unlock()

	if b.cancel != nil {
		b.cancel()
	}

	if b.conn != nil {
		err := b.conn.Close()
		b.conn = nil
		b.SetState(StateDisconnected)
		return err
	}

	return nil
}

// Subscribe subscribes to ticker updates for the given pairs.
func (b *BybitWSProvider) Subscribe(pairs []string) error {
	b.connMu.Lock()
	conn := b.conn
	b.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	// Build subscription args
	args := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		symbol := strings.ReplaceAll(pair, "/", "")
		args = append(args, "tickers."+symbol)
		b.AddSubscription(pair)
	}

	msg := bybitSubscribeMsg{
		Op:    "subscribe",
		Args:  args,
		ReqID: fmt.Sprintf("%d", b.nextReqID()),
	}

	return conn.WriteJSON(msg)
}

// Unsubscribe unsubscribes from ticker updates for the given pairs.
func (b *BybitWSProvider) Unsubscribe(pairs []string) error {
	b.connMu.Lock()
	conn := b.conn
	b.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	args := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		symbol := strings.ReplaceAll(pair, "/", "")
		args = append(args, "tickers."+symbol)
		b.RemoveSubscription(pair)
	}

	msg := bybitSubscribeMsg{
		Op:    "unsubscribe",
		Args:  args,
		ReqID: fmt.Sprintf("%d", b.nextReqID()),
	}

	return conn.WriteJSON(msg)
}

type bybitSubscribeMsg struct {
	Op    string   `json:"op"`
	Args  []string `json:"args"`
	ReqID string   `json:"req_id,omitempty"`
}

// handleMessages processes incoming WebSocket messages.
func (b *BybitWSProvider) handleMessages() {
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		b.connMu.Lock()
		conn := b.conn
		b.connMu.Unlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			b.EmitError(fmt.Errorf("read error: %w", err))
			b.reconnect()
			return
		}

		b.processMessage(message)
	}
}

// processMessage parses and processes a WebSocket message.
func (b *BybitWSProvider) processMessage(data []byte) {
	var msg bybitMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	// Handle pong
	if msg.Op == "pong" {
		return
	}

	// Handle subscription response
	if msg.Op == "subscribe" {
		if !msg.Success {
			b.EmitError(fmt.Errorf("bybit subscription failed: %s", msg.RetMsg))
		}
		return
	}

	// Handle ticker data
	if msg.Topic != "" && strings.HasPrefix(msg.Topic, "tickers.") {
		b.handleTicker(&msg)
	}
}

type bybitMessage struct {
	Op      string          `json:"op"`
	Success bool            `json:"success"`
	RetMsg  string          `json:"ret_msg"`
	Topic   string          `json:"topic"`
	Type    string          `json:"type"`
	Data    bybitTickerData `json:"data"`
	Ts      int64           `json:"ts"`
}

type bybitTickerData struct {
	Symbol        string `json:"symbol"`
	LastPrice     string `json:"lastPrice"`
	Bid1Price     string `json:"bid1Price"`
	Bid1Size      string `json:"bid1Size"`
	Ask1Price     string `json:"ask1Price"`
	Ask1Size      string `json:"ask1Size"`
	HighPrice24h  string `json:"highPrice24h"`
	LowPrice24h   string `json:"lowPrice24h"`
	Turnover24h   string `json:"turnover24h"`
	Volume24h     string `json:"volume24h"`
}

// handleTicker processes a ticker update.
func (b *BybitWSProvider) handleTicker(msg *bybitMessage) {
	// Extract symbol from topic (e.g., "tickers.BTCUSDT" -> "BTCUSDT")
	symbol := strings.TrimPrefix(msg.Topic, "tickers.")
	pair := convertBybitSymbolToPair(symbol)

	bidPrice := new(big.Float)
	bidPrice.SetString(msg.Data.Bid1Price)

	askPrice := new(big.Float)
	askPrice.SetString(msg.Data.Ask1Price)

	bidSize := new(big.Float)
	bidSize.SetString(msg.Data.Bid1Size)

	askSize := new(big.Float)
	askSize.SetString(msg.Data.Ask1Size)

	lastPrice := new(big.Float)
	lastPrice.SetString(msg.Data.LastPrice)

	volume := new(big.Float)
	volume.SetString(msg.Data.Volume24h)

	timestamp := time.Now()
	if msg.Ts > 0 {
		timestamp = time.UnixMilli(msg.Ts)
	}

	update := &PriceUpdate{
		Exchange:  b.Name(),
		Pair:      pair,
		BidPrice:  bidPrice,
		AskPrice:  askPrice,
		BidSize:   bidSize,
		AskSize:   askSize,
		LastPrice: lastPrice,
		Volume24h: volume,
		Timestamp: timestamp,
	}

	b.EmitPriceUpdate(update)
}

// pingHandler sends periodic pings to keep the connection alive.
func (b *BybitWSProvider) pingHandler() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.connMu.Lock()
			conn := b.conn
			b.connMu.Unlock()

			if conn != nil {
				msg := map[string]interface{}{
					"op":     "ping",
					"req_id": fmt.Sprintf("%d", b.nextReqID()),
				}
				if err := conn.WriteJSON(msg); err != nil {
					b.EmitError(fmt.Errorf("ping failed: %w", err))
				}
			}
		}
	}
}

// reconnect attempts to reconnect to the WebSocket.
func (b *BybitWSProvider) reconnect() {
	b.SetState(StateReconnecting)

	pairs := b.GetSubscribedPairs()

	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		delay := b.CalculateReconnectDelay()
		time.Sleep(delay)

		if err := b.Connect(b.ctx); err != nil {
			b.EmitError(fmt.Errorf("reconnect failed: %w", err))
			continue
		}

		if len(pairs) > 0 {
			if err := b.Subscribe(pairs); err != nil {
				b.EmitError(fmt.Errorf("resubscribe failed: %w", err))
			}
		}

		return
	}
}

// nextReqID returns the next request ID.
func (b *BybitWSProvider) nextReqID() int {
	b.reqMu.Lock()
	defer b.reqMu.Unlock()
	b.reqID++
	return b.reqID
}

// convertBybitSymbolToPair converts a Bybit symbol to standard pair format.
func convertBybitSymbolToPair(symbol string) string {
	quotes := []string{"USDT", "USDC", "BTC", "ETH"}

	for _, quote := range quotes {
		if strings.HasSuffix(symbol, quote) {
			base := strings.TrimSuffix(symbol, quote)
			return base + "/" + quote
		}
	}

	return symbol
}
