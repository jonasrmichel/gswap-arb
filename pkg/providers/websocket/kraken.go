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
	krakenWSURL = "wss://ws.kraken.com"
)

// KrakenWSProvider provides real-time price feeds from Kraken.
type KrakenWSProvider struct {
	*BaseWSProvider
	conn   *websocket.Conn
	connMu sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	reqID  int
	reqMu  sync.Mutex
}

// NewKrakenWSProvider creates a new Kraken WebSocket provider.
func NewKrakenWSProvider() *KrakenWSProvider {
	return &KrakenWSProvider{
		BaseWSProvider: NewBaseWSProvider("kraken", &types.FeeStructure{
			Exchange:    "kraken",
			MakerFeeBps: 16,
			TakerFeeBps: 26,
		}),
	}
}

// Connect establishes the WebSocket connection to Kraken.
func (k *KrakenWSProvider) Connect(ctx context.Context) error {
	k.connMu.Lock()
	defer k.connMu.Unlock()

	if k.State() == StateConnected {
		return nil
	}

	k.SetState(StateConnecting)
	k.ctx, k.cancel = context.WithCancel(ctx)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, krakenWSURL, nil)
	if err != nil {
		k.SetState(StateDisconnected)
		return fmt.Errorf("failed to connect to Kraken WebSocket: %w", err)
	}

	k.conn = conn
	k.SetState(StateConnected)
	k.ResetReconnectAttempts()

	// Start message handler
	go k.handleMessages()

	// Start ping handler
	go k.pingHandler()

	return nil
}

// Disconnect closes the WebSocket connection.
func (k *KrakenWSProvider) Disconnect() error {
	k.connMu.Lock()
	defer k.connMu.Unlock()

	if k.cancel != nil {
		k.cancel()
	}

	if k.conn != nil {
		err := k.conn.Close()
		k.conn = nil
		k.SetState(StateDisconnected)
		return err
	}

	return nil
}

// Subscribe subscribes to ticker updates for the given pairs.
func (k *KrakenWSProvider) Subscribe(pairs []string) error {
	k.connMu.Lock()
	conn := k.conn
	k.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	// Convert pairs to Kraken format
	krakenPairs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		krakenPair := convertToKrakenPair(pair)
		krakenPairs = append(krakenPairs, krakenPair)
		k.AddSubscription(pair)
	}

	// Send subscription message
	msg := krakenSubscribeMsg{
		Event: "subscribe",
		Pair:  krakenPairs,
		Subscription: krakenSubscription{
			Name: "ticker",
		},
		ReqID: k.nextReqID(),
	}

	return conn.WriteJSON(msg)
}

// Unsubscribe unsubscribes from ticker updates for the given pairs.
func (k *KrakenWSProvider) Unsubscribe(pairs []string) error {
	k.connMu.Lock()
	conn := k.conn
	k.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	krakenPairs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		krakenPair := convertToKrakenPair(pair)
		krakenPairs = append(krakenPairs, krakenPair)
		k.RemoveSubscription(pair)
	}

	msg := krakenSubscribeMsg{
		Event: "unsubscribe",
		Pair:  krakenPairs,
		Subscription: krakenSubscription{
			Name: "ticker",
		},
		ReqID: k.nextReqID(),
	}

	return conn.WriteJSON(msg)
}

type krakenSubscribeMsg struct {
	Event        string             `json:"event"`
	Pair         []string           `json:"pair"`
	Subscription krakenSubscription `json:"subscription"`
	ReqID        int                `json:"reqid,omitempty"`
}

type krakenSubscription struct {
	Name string `json:"name"`
}

// handleMessages processes incoming WebSocket messages.
func (k *KrakenWSProvider) handleMessages() {
	for {
		select {
		case <-k.ctx.Done():
			return
		default:
		}

		k.connMu.Lock()
		conn := k.conn
		k.connMu.Unlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			k.EmitError(fmt.Errorf("read error: %w", err))
			k.reconnect()
			return
		}

		k.processMessage(message)
	}
}

// processMessage parses and processes a WebSocket message.
func (k *KrakenWSProvider) processMessage(data []byte) {
	// Try to parse as event message
	var event struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(data, &event); err == nil && event.Event != "" {
		// Handle system messages
		switch event.Event {
		case "systemStatus", "subscriptionStatus", "heartbeat":
			return // Ignore these events
		case "error":
			var errMsg struct {
				ErrorMessage string `json:"errorMessage"`
			}
			json.Unmarshal(data, &errMsg)
			k.EmitError(fmt.Errorf("kraken error: %s", errMsg.ErrorMessage))
			return
		}
	}

	// Try to parse as ticker data (array format)
	var tickerData []interface{}
	if err := json.Unmarshal(data, &tickerData); err == nil && len(tickerData) >= 4 {
		k.handleTickerArray(tickerData)
	}
}

// handleTickerArray processes a Kraken ticker array message.
// Format: [channelID, tickerData, "ticker", "pair"]
func (k *KrakenWSProvider) handleTickerArray(data []interface{}) {
	if len(data) < 4 {
		return
	}

	// Check if this is a ticker message
	channelName, ok := data[2].(string)
	if !ok || channelName != "ticker" {
		return
	}

	pairName, ok := data[3].(string)
	if !ok {
		return
	}

	tickerData, ok := data[1].(map[string]interface{})
	if !ok {
		return
	}

	// Parse ticker data
	// a = ask [price, wholeLotVolume, lotVolume]
	// b = bid [price, wholeLotVolume, lotVolume]
	// c = last trade closed [price, lotVolume]
	// v = volume [today, last24hours]

	bidPrice := new(big.Float)
	askPrice := new(big.Float)
	bidSize := new(big.Float)
	askSize := new(big.Float)
	lastPrice := new(big.Float)
	volume := new(big.Float)

	if bid, ok := tickerData["b"].([]interface{}); ok && len(bid) >= 2 {
		if price, ok := bid[0].(string); ok {
			bidPrice.SetString(price)
		}
		if size, ok := bid[2].(string); ok {
			bidSize.SetString(size)
		}
	}

	if ask, ok := tickerData["a"].([]interface{}); ok && len(ask) >= 2 {
		if price, ok := ask[0].(string); ok {
			askPrice.SetString(price)
		}
		if size, ok := ask[2].(string); ok {
			askSize.SetString(size)
		}
	}

	if last, ok := tickerData["c"].([]interface{}); ok && len(last) >= 1 {
		if price, ok := last[0].(string); ok {
			lastPrice.SetString(price)
		}
	}

	if vol, ok := tickerData["v"].([]interface{}); ok && len(vol) >= 2 {
		if v24h, ok := vol[1].(string); ok {
			volume.SetString(v24h)
		}
	}

	// Convert Kraken pair to standard format
	pair := convertFromKrakenPair(pairName)

	update := &PriceUpdate{
		Exchange:  k.Name(),
		Pair:      pair,
		BidPrice:  bidPrice,
		AskPrice:  askPrice,
		BidSize:   bidSize,
		AskSize:   askSize,
		LastPrice: lastPrice,
		Volume24h: volume,
		Timestamp: time.Now(),
	}

	k.EmitPriceUpdate(update)
}

// pingHandler sends periodic pings to keep the connection alive.
func (k *KrakenWSProvider) pingHandler() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-k.ctx.Done():
			return
		case <-ticker.C:
			k.connMu.Lock()
			conn := k.conn
			k.connMu.Unlock()

			if conn != nil {
				msg := map[string]interface{}{
					"event": "ping",
					"reqid": k.nextReqID(),
				}
				if err := conn.WriteJSON(msg); err != nil {
					k.EmitError(fmt.Errorf("ping failed: %w", err))
				}
			}
		}
	}
}

// reconnect attempts to reconnect to the WebSocket.
func (k *KrakenWSProvider) reconnect() {
	k.SetState(StateReconnecting)

	pairs := k.GetSubscribedPairs()

	for {
		select {
		case <-k.ctx.Done():
			return
		default:
		}

		delay := k.CalculateReconnectDelay()
		time.Sleep(delay)

		if err := k.Connect(k.ctx); err != nil {
			k.EmitError(fmt.Errorf("reconnect failed: %w", err))
			continue
		}

		if len(pairs) > 0 {
			if err := k.Subscribe(pairs); err != nil {
				k.EmitError(fmt.Errorf("resubscribe failed: %w", err))
			}
		}

		return
	}
}

// nextReqID returns the next request ID.
func (k *KrakenWSProvider) nextReqID() int {
	k.reqMu.Lock()
	defer k.reqMu.Unlock()
	k.reqID++
	return k.reqID
}

// convertToKrakenPair converts a standard pair to Kraken format.
func convertToKrakenPair(pair string) string {
	// BTC/USD -> XBT/USD
	pair = strings.ReplaceAll(pair, "BTC", "XBT")
	return pair
}

// convertFromKrakenPair converts a Kraken pair to standard format.
func convertFromKrakenPair(pair string) string {
	// XBT/USD -> BTC/USD
	pair = strings.ReplaceAll(pair, "XBT", "BTC")
	// Remove any X or Z prefixes Kraken uses
	pair = strings.TrimPrefix(pair, "X")
	pair = strings.TrimPrefix(pair, "Z")
	return pair
}
