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
	binanceWSURL       = "wss://stream.binance.com:9443/ws"
	binanceUSWSURL     = "wss://stream.binance.us:9443/ws"
	binanceCombinedURL = "wss://stream.binance.com:9443/stream"
)

// BinanceWSProvider provides real-time price feeds from Binance.
type BinanceWSProvider struct {
	*BaseWSProvider
	conn      *websocket.Conn
	connMu    sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	msgID     int
	msgIDMu   sync.Mutex
	useUS     bool // Use Binance.US endpoint
}

// NewBinanceWSProvider creates a new Binance WebSocket provider.
func NewBinanceWSProvider() *BinanceWSProvider {
	return &BinanceWSProvider{
		BaseWSProvider: NewBaseWSProvider("binance", &types.FeeStructure{
			Exchange:    "binance",
			MakerFeeBps: 10,
			TakerFeeBps: 10,
		}),
	}
}

// Connect establishes the WebSocket connection to Binance.
// It tries the global endpoint first, then falls back to Binance.US if that fails.
func (b *BinanceWSProvider) Connect(ctx context.Context) error {
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

	// Try global Binance first (unless we already know to use US)
	wsURL := binanceWSURL
	if b.useUS {
		wsURL = binanceUSWSURL
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		// If global failed and we haven't tried US yet, try Binance.US
		if !b.useUS {
			conn, _, err = dialer.DialContext(ctx, binanceUSWSURL, nil)
			if err == nil {
				b.useUS = true // Remember to use US endpoint for reconnects
			}
		}
	}

	if err != nil {
		b.SetState(StateDisconnected)
		return fmt.Errorf("failed to connect to Binance WebSocket: %w", err)
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
func (b *BinanceWSProvider) Disconnect() error {
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
func (b *BinanceWSProvider) Subscribe(pairs []string) error {
	b.connMu.Lock()
	conn := b.conn
	b.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	// Convert pairs to Binance stream format
	streams := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		symbol := strings.ToLower(strings.ReplaceAll(pair, "/", ""))
		streams = append(streams, symbol+"@bookTicker") // Best bid/ask
		b.AddSubscription(pair)
	}

	// Send subscription message
	msg := map[string]interface{}{
		"method": "SUBSCRIBE",
		"params": streams,
		"id":     b.nextMsgID(),
	}

	return conn.WriteJSON(msg)
}

// Unsubscribe unsubscribes from ticker updates for the given pairs.
func (b *BinanceWSProvider) Unsubscribe(pairs []string) error {
	b.connMu.Lock()
	conn := b.conn
	b.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	streams := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		symbol := strings.ToLower(strings.ReplaceAll(pair, "/", ""))
		streams = append(streams, symbol+"@bookTicker")
		b.RemoveSubscription(pair)
	}

	msg := map[string]interface{}{
		"method": "UNSUBSCRIBE",
		"params": streams,
		"id":     b.nextMsgID(),
	}

	return conn.WriteJSON(msg)
}

// handleMessages processes incoming WebSocket messages.
func (b *BinanceWSProvider) handleMessages() {
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
func (b *BinanceWSProvider) processMessage(data []byte) {
	// Check if it's a bookTicker update
	var ticker binanceBookTicker
	if err := json.Unmarshal(data, &ticker); err == nil && ticker.Symbol != "" {
		b.handleBookTicker(&ticker)
		return
	}

	// Check if it's a combined stream message
	var combined binanceCombinedStream
	if err := json.Unmarshal(data, &combined); err == nil && combined.Stream != "" {
		if strings.HasSuffix(combined.Stream, "@bookTicker") {
			var ticker binanceBookTicker
			if err := json.Unmarshal(combined.Data, &ticker); err == nil {
				b.handleBookTicker(&ticker)
			}
		}
	}
}

// binanceBookTicker represents Binance book ticker data.
type binanceBookTicker struct {
	UpdateID int64  `json:"u"`
	Symbol   string `json:"s"`
	BidPrice string `json:"b"`
	BidQty   string `json:"B"`
	AskPrice string `json:"a"`
	AskQty   string `json:"A"`
}

type binanceCombinedStream struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

// handleBookTicker processes a book ticker update.
func (b *BinanceWSProvider) handleBookTicker(ticker *binanceBookTicker) {
	pair := convertBinanceSymbolToPair(ticker.Symbol)

	bidPrice := new(big.Float)
	bidPrice.SetString(ticker.BidPrice)

	askPrice := new(big.Float)
	askPrice.SetString(ticker.AskPrice)

	bidSize := new(big.Float)
	bidSize.SetString(ticker.BidQty)

	askSize := new(big.Float)
	askSize.SetString(ticker.AskQty)

	update := &PriceUpdate{
		Exchange:   b.Name(),
		Pair:       pair,
		BidPrice:   bidPrice,
		AskPrice:   askPrice,
		BidSize:    bidSize,
		AskSize:    askSize,
		Timestamp:  time.Now(),
		SequenceID: ticker.UpdateID,
	}

	b.EmitPriceUpdate(update)
}

// pingHandler sends periodic pings to keep the connection alive.
func (b *BinanceWSProvider) pingHandler() {
	ticker := time.NewTicker(30 * time.Second)
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
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					b.EmitError(fmt.Errorf("ping failed: %w", err))
				}
			}
		}
	}
}

// reconnect attempts to reconnect to the WebSocket.
func (b *BinanceWSProvider) reconnect() {
	b.SetState(StateReconnecting)

	// Get pairs to resubscribe
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

		// Resubscribe to pairs
		if len(pairs) > 0 {
			if err := b.Subscribe(pairs); err != nil {
				b.EmitError(fmt.Errorf("resubscribe failed: %w", err))
			}
		}

		return
	}
}

// nextMsgID returns the next message ID.
func (b *BinanceWSProvider) nextMsgID() int {
	b.msgIDMu.Lock()
	defer b.msgIDMu.Unlock()
	b.msgID++
	return b.msgID
}

// convertBinanceSymbolToPair converts a Binance symbol to a standard pair format.
func convertBinanceSymbolToPair(symbol string) string {
	// Common quote currencies
	quotes := []string{"USDT", "USDC", "BUSD", "BTC", "ETH", "BNB"}

	for _, quote := range quotes {
		if strings.HasSuffix(symbol, quote) {
			base := strings.TrimSuffix(symbol, quote)
			return base + "/" + quote
		}
	}

	return symbol
}
