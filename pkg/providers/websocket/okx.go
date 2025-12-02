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
	okxWSURL = "wss://ws.okx.com:8443/ws/v5/public"
)

// OKXWSProvider provides real-time price feeds from OKX.
type OKXWSProvider struct {
	*BaseWSProvider
	conn   *websocket.Conn
	connMu sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

// NewOKXWSProvider creates a new OKX WebSocket provider.
func NewOKXWSProvider() *OKXWSProvider {
	return &OKXWSProvider{
		BaseWSProvider: NewBaseWSProvider("okx", &types.FeeStructure{
			Exchange:    "okx",
			MakerFeeBps: 8,
			TakerFeeBps: 10,
		}),
	}
}

// Connect establishes the WebSocket connection to OKX.
func (o *OKXWSProvider) Connect(ctx context.Context) error {
	o.connMu.Lock()
	defer o.connMu.Unlock()

	if o.State() == StateConnected {
		return nil
	}

	o.SetState(StateConnecting)
	o.ctx, o.cancel = context.WithCancel(ctx)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, okxWSURL, nil)
	if err != nil {
		o.SetState(StateDisconnected)
		return fmt.Errorf("failed to connect to OKX WebSocket: %w", err)
	}

	o.conn = conn
	o.SetState(StateConnected)
	o.ResetReconnectAttempts()

	// Start message handler
	go o.handleMessages()

	// Start ping handler
	go o.pingHandler()

	return nil
}

// Disconnect closes the WebSocket connection.
func (o *OKXWSProvider) Disconnect() error {
	o.connMu.Lock()
	defer o.connMu.Unlock()

	if o.cancel != nil {
		o.cancel()
	}

	if o.conn != nil {
		err := o.conn.Close()
		o.conn = nil
		o.SetState(StateDisconnected)
		return err
	}

	return nil
}

// Subscribe subscribes to ticker updates for the given pairs.
func (o *OKXWSProvider) Subscribe(pairs []string) error {
	o.connMu.Lock()
	conn := o.conn
	o.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	// Build subscription args
	args := make([]okxSubArg, 0, len(pairs))
	for _, pair := range pairs {
		instID := strings.ReplaceAll(pair, "/", "-")
		args = append(args, okxSubArg{
			Channel: "tickers",
			InstID:  instID,
		})
		o.AddSubscription(pair)
	}

	msg := okxSubscribeMsg{
		Op:   "subscribe",
		Args: args,
	}

	return conn.WriteJSON(msg)
}

// Unsubscribe unsubscribes from ticker updates for the given pairs.
func (o *OKXWSProvider) Unsubscribe(pairs []string) error {
	o.connMu.Lock()
	conn := o.conn
	o.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	args := make([]okxSubArg, 0, len(pairs))
	for _, pair := range pairs {
		instID := strings.ReplaceAll(pair, "/", "-")
		args = append(args, okxSubArg{
			Channel: "tickers",
			InstID:  instID,
		})
		o.RemoveSubscription(pair)
	}

	msg := okxSubscribeMsg{
		Op:   "unsubscribe",
		Args: args,
	}

	return conn.WriteJSON(msg)
}

type okxSubscribeMsg struct {
	Op   string      `json:"op"`
	Args []okxSubArg `json:"args"`
}

type okxSubArg struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

// handleMessages processes incoming WebSocket messages.
func (o *OKXWSProvider) handleMessages() {
	for {
		select {
		case <-o.ctx.Done():
			return
		default:
		}

		o.connMu.Lock()
		conn := o.conn
		o.connMu.Unlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			o.EmitError(fmt.Errorf("read error: %w", err))
			o.reconnect()
			return
		}

		o.processMessage(message)
	}
}

// processMessage parses and processes a WebSocket message.
func (o *OKXWSProvider) processMessage(data []byte) {
	var msg okxMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	// Handle different message types
	switch msg.Event {
	case "subscribe", "unsubscribe":
		return // Acknowledgment messages
	case "error":
		o.EmitError(fmt.Errorf("okx error: %s (code: %s)", msg.Msg, msg.Code))
		return
	}

	// Handle ticker data
	if msg.Arg.Channel == "tickers" && len(msg.Data) > 0 {
		for _, tickerData := range msg.Data {
			o.handleTicker(&tickerData, msg.Arg.InstID)
		}
	}
}

type okxMessage struct {
	Event string      `json:"event"`
	Code  string      `json:"code"`
	Msg   string      `json:"msg"`
	Arg   okxSubArg   `json:"arg"`
	Data  []okxTicker `json:"data"`
}

type okxTicker struct {
	InstID    string `json:"instId"`
	Last      string `json:"last"`
	LastSz    string `json:"lastSz"`
	AskPx     string `json:"askPx"`
	AskSz     string `json:"askSz"`
	BidPx     string `json:"bidPx"`
	BidSz     string `json:"bidSz"`
	Open24h   string `json:"open24h"`
	High24h   string `json:"high24h"`
	Low24h    string `json:"low24h"`
	Vol24h    string `json:"vol24h"`
	VolCcy24h string `json:"volCcy24h"`
	Ts        string `json:"ts"`
}

// handleTicker processes a ticker update.
func (o *OKXWSProvider) handleTicker(ticker *okxTicker, instID string) {
	pair := strings.ReplaceAll(ticker.InstID, "-", "/")
	if pair == "" {
		pair = strings.ReplaceAll(instID, "-", "/")
	}

	bidPrice := new(big.Float)
	bidPrice.SetString(ticker.BidPx)

	askPrice := new(big.Float)
	askPrice.SetString(ticker.AskPx)

	bidSize := new(big.Float)
	bidSize.SetString(ticker.BidSz)

	askSize := new(big.Float)
	askSize.SetString(ticker.AskSz)

	lastPrice := new(big.Float)
	lastPrice.SetString(ticker.Last)

	volume := new(big.Float)
	volume.SetString(ticker.Vol24h)

	timestamp := time.Now()
	if ticker.Ts != "" {
		if ts, err := parseInt64(ticker.Ts); err == nil {
			timestamp = time.UnixMilli(ts)
		}
	}

	update := &PriceUpdate{
		Exchange:  o.Name(),
		Pair:      pair,
		BidPrice:  bidPrice,
		AskPrice:  askPrice,
		BidSize:   bidSize,
		AskSize:   askSize,
		LastPrice: lastPrice,
		Volume24h: volume,
		Timestamp: timestamp,
	}

	o.EmitPriceUpdate(update)
}

// pingHandler sends periodic pings to keep the connection alive.
func (o *OKXWSProvider) pingHandler() {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
			o.connMu.Lock()
			conn := o.conn
			o.connMu.Unlock()

			if conn != nil {
				if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
					o.EmitError(fmt.Errorf("ping failed: %w", err))
				}
			}
		}
	}
}

// reconnect attempts to reconnect to the WebSocket.
func (o *OKXWSProvider) reconnect() {
	o.SetState(StateReconnecting)

	pairs := o.GetSubscribedPairs()

	for {
		select {
		case <-o.ctx.Done():
			return
		default:
		}

		delay := o.CalculateReconnectDelay()
		time.Sleep(delay)

		if err := o.Connect(o.ctx); err != nil {
			o.EmitError(fmt.Errorf("reconnect failed: %w", err))
			continue
		}

		if len(pairs) > 0 {
			if err := o.Subscribe(pairs); err != nil {
				o.EmitError(fmt.Errorf("resubscribe failed: %w", err))
			}
		}

		return
	}
}

func parseInt64(s string) (int64, error) {
	var v int64
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}
