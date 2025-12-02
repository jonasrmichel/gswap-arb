// Package websocket provides WebSocket-based real-time price feeds.
package websocket

import (
	"context"
	"math/big"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// ConnectionState represents the WebSocket connection state.
type ConnectionState int

const (
	StateDisconnected ConnectionState = iota
	StateConnecting
	StateConnected
	StateReconnecting
)

func (s ConnectionState) String() string {
	switch s {
	case StateDisconnected:
		return "disconnected"
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	default:
		return "unknown"
	}
}

// PriceUpdate represents a real-time price update from an exchange.
type PriceUpdate struct {
	Exchange   string     `json:"exchange"`
	Pair       string     `json:"pair"`
	BidPrice   *big.Float `json:"bid_price"`
	AskPrice   *big.Float `json:"ask_price"`
	BidSize    *big.Float `json:"bid_size"`
	AskSize    *big.Float `json:"ask_size"`
	LastPrice  *big.Float `json:"last_price,omitempty"`
	Volume24h  *big.Float `json:"volume_24h,omitempty"`
	Timestamp  time.Time  `json:"timestamp"`
	SequenceID int64      `json:"sequence_id,omitempty"`
}

// ToQuote converts a PriceUpdate to a PriceQuote.
func (p *PriceUpdate) ToQuote() *types.PriceQuote {
	midPrice := new(big.Float)
	if p.BidPrice != nil && p.AskPrice != nil {
		midPrice.Add(p.BidPrice, p.AskPrice)
		midPrice.Quo(midPrice, big.NewFloat(2))
	}

	return &types.PriceQuote{
		Exchange:  p.Exchange,
		Pair:      p.Pair,
		Price:     midPrice,
		BidPrice:  p.BidPrice,
		AskPrice:  p.AskPrice,
		BidSize:   p.BidSize,
		AskSize:   p.AskSize,
		Volume24h: p.Volume24h,
		Timestamp: p.Timestamp,
		Source:    "websocket",
	}
}

// OrderBookUpdate represents a real-time order book update.
type OrderBookUpdate struct {
	Exchange   string                  `json:"exchange"`
	Pair       string                  `json:"pair"`
	Bids       []types.OrderBookEntry  `json:"bids"`
	Asks       []types.OrderBookEntry  `json:"asks"`
	IsSnapshot bool                    `json:"is_snapshot"`
	Timestamp  time.Time               `json:"timestamp"`
	SequenceID int64                   `json:"sequence_id,omitempty"`
}

// WSProvider is the interface for WebSocket price providers.
type WSProvider interface {
	// Name returns the exchange name.
	Name() string

	// Connect establishes the WebSocket connection.
	Connect(ctx context.Context) error

	// Disconnect closes the WebSocket connection.
	Disconnect() error

	// Subscribe subscribes to price updates for the given pairs.
	Subscribe(pairs []string) error

	// Unsubscribe unsubscribes from price updates for the given pairs.
	Unsubscribe(pairs []string) error

	// State returns the current connection state.
	State() ConnectionState

	// PriceUpdates returns the channel for receiving price updates.
	PriceUpdates() <-chan *PriceUpdate

	// OrderBookUpdates returns the channel for receiving order book updates.
	OrderBookUpdates() <-chan *OrderBookUpdate

	// Errors returns the channel for receiving errors.
	Errors() <-chan error

	// GetLatestPrice returns the latest price for a pair (from cache).
	GetLatestPrice(pair string) *PriceUpdate

	// GetFees returns the fee structure for this exchange.
	GetFees() *types.FeeStructure
}

// BaseWSProvider provides common functionality for WebSocket providers.
type BaseWSProvider struct {
	name             string
	state            ConnectionState
	stateMu          sync.RWMutex
	priceUpdates     chan *PriceUpdate
	orderBookUpdates chan *OrderBookUpdate
	errors           chan error
	subscribedPairs  map[string]bool
	pairsMu          sync.RWMutex
	latestPrices     map[string]*PriceUpdate
	pricesMu         sync.RWMutex
	fees             *types.FeeStructure

	// Reconnection settings
	ReconnectDelay    time.Duration
	MaxReconnectDelay time.Duration
	reconnectAttempts int
}

// NewBaseWSProvider creates a new base WebSocket provider.
func NewBaseWSProvider(name string, fees *types.FeeStructure) *BaseWSProvider {
	return &BaseWSProvider{
		name:              name,
		state:             StateDisconnected,
		priceUpdates:      make(chan *PriceUpdate, 1000),
		orderBookUpdates:  make(chan *OrderBookUpdate, 1000),
		errors:            make(chan error, 100),
		subscribedPairs:   make(map[string]bool),
		latestPrices:      make(map[string]*PriceUpdate),
		fees:              fees,
		ReconnectDelay:    time.Second,
		MaxReconnectDelay: 30 * time.Second,
	}
}

// Name returns the provider name.
func (b *BaseWSProvider) Name() string {
	return b.name
}

// State returns the current connection state.
func (b *BaseWSProvider) State() ConnectionState {
	b.stateMu.RLock()
	defer b.stateMu.RUnlock()
	return b.state
}

// SetState sets the connection state.
func (b *BaseWSProvider) SetState(state ConnectionState) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	b.state = state
}

// PriceUpdates returns the price updates channel.
func (b *BaseWSProvider) PriceUpdates() <-chan *PriceUpdate {
	return b.priceUpdates
}

// OrderBookUpdates returns the order book updates channel.
func (b *BaseWSProvider) OrderBookUpdates() <-chan *OrderBookUpdate {
	return b.orderBookUpdates
}

// Errors returns the errors channel.
func (b *BaseWSProvider) Errors() <-chan error {
	return b.errors
}

// GetLatestPrice returns the latest cached price for a pair.
func (b *BaseWSProvider) GetLatestPrice(pair string) *PriceUpdate {
	b.pricesMu.RLock()
	defer b.pricesMu.RUnlock()
	return b.latestPrices[pair]
}

// UpdateLatestPrice updates the cached price for a pair.
func (b *BaseWSProvider) UpdateLatestPrice(pair string, update *PriceUpdate) {
	b.pricesMu.Lock()
	defer b.pricesMu.Unlock()
	b.latestPrices[pair] = update
}

// GetFees returns the fee structure.
func (b *BaseWSProvider) GetFees() *types.FeeStructure {
	return b.fees
}

// IsSubscribed checks if a pair is subscribed.
func (b *BaseWSProvider) IsSubscribed(pair string) bool {
	b.pairsMu.RLock()
	defer b.pairsMu.RUnlock()
	return b.subscribedPairs[pair]
}

// AddSubscription marks a pair as subscribed.
func (b *BaseWSProvider) AddSubscription(pair string) {
	b.pairsMu.Lock()
	defer b.pairsMu.Unlock()
	b.subscribedPairs[pair] = true
}

// RemoveSubscription marks a pair as unsubscribed.
func (b *BaseWSProvider) RemoveSubscription(pair string) {
	b.pairsMu.Lock()
	defer b.pairsMu.Unlock()
	delete(b.subscribedPairs, pair)
}

// GetSubscribedPairs returns all subscribed pairs.
func (b *BaseWSProvider) GetSubscribedPairs() []string {
	b.pairsMu.RLock()
	defer b.pairsMu.RUnlock()
	pairs := make([]string, 0, len(b.subscribedPairs))
	for pair := range b.subscribedPairs {
		pairs = append(pairs, pair)
	}
	return pairs
}

// EmitPriceUpdate sends a price update to the channel.
func (b *BaseWSProvider) EmitPriceUpdate(update *PriceUpdate) {
	// Update cache
	b.UpdateLatestPrice(update.Pair, update)

	// Send to channel (non-blocking)
	select {
	case b.priceUpdates <- update:
	default:
		// Channel full, drop oldest
		select {
		case <-b.priceUpdates:
		default:
		}
		b.priceUpdates <- update
	}
}

// EmitOrderBookUpdate sends an order book update to the channel.
func (b *BaseWSProvider) EmitOrderBookUpdate(update *OrderBookUpdate) {
	select {
	case b.orderBookUpdates <- update:
	default:
		// Channel full, drop oldest
		select {
		case <-b.orderBookUpdates:
		default:
		}
		b.orderBookUpdates <- update
	}
}

// EmitError sends an error to the channel.
func (b *BaseWSProvider) EmitError(err error) {
	select {
	case b.errors <- err:
	default:
		// Drop if channel full
	}
}

// CalculateReconnectDelay calculates the next reconnection delay with exponential backoff.
func (b *BaseWSProvider) CalculateReconnectDelay() time.Duration {
	b.reconnectAttempts++
	delay := b.ReconnectDelay * time.Duration(1<<uint(b.reconnectAttempts-1))
	if delay > b.MaxReconnectDelay {
		delay = b.MaxReconnectDelay
	}
	return delay
}

// ResetReconnectAttempts resets the reconnection attempt counter.
func (b *BaseWSProvider) ResetReconnectAttempts() {
	b.reconnectAttempts = 0
}

// Close closes all channels.
func (b *BaseWSProvider) Close() {
	close(b.priceUpdates)
	close(b.orderBookUpdates)
	close(b.errors)
}
