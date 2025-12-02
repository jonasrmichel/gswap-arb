// Package cex provides price providers for centralized exchanges using CCXT.
package cex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

const (
	// Cache settings
	tickerCacheDuration   = 5 * time.Second
	orderBookCacheDuration = 3 * time.Second
)

// ExchangeConfig holds configuration for a CEX.
type ExchangeConfig struct {
	ID        string
	Name      string
	BaseURL   string
	APIKey    string
	Secret    string
	Testnet   bool
}

// CEXProvider implements the PriceProvider interface for centralized exchanges.
// It uses direct REST API calls to avoid CCXT Go dependency complexity.
type CEXProvider struct {
	config     ExchangeConfig
	client     *http.Client
	pairs      []types.TradingPair
	fees       *types.FeeStructure

	// Cache
	tickerCache   map[string]*cachedTicker
	tickerCacheMu sync.RWMutex
	orderBookCache   map[string]*cachedOrderBook
	orderBookCacheMu sync.RWMutex
}

type cachedTicker struct {
	quote     *types.PriceQuote
	timestamp time.Time
}

type cachedOrderBook struct {
	book      *types.OrderBook
	timestamp time.Time
}

// NewCEXProvider creates a new CEX price provider.
func NewCEXProvider(config ExchangeConfig) *CEXProvider {
	return &CEXProvider{
		config: config,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		pairs:          make([]types.TradingPair, 0),
		tickerCache:    make(map[string]*cachedTicker),
		orderBookCache: make(map[string]*cachedOrderBook),
		fees: &types.FeeStructure{
			Exchange:      config.ID,
			MakerFeeBps:   10, // 0.1% default
			TakerFeeBps:   10, // 0.1% default
			WithdrawalFee: 0,
			DepositFee:    0,
		},
	}
}

// Name returns the provider name.
func (p *CEXProvider) Name() string {
	return p.config.ID
}

// Type returns the exchange type.
func (p *CEXProvider) Type() types.ExchangeType {
	return types.ExchangeTypeCEX
}

// Initialize sets up the provider.
func (p *CEXProvider) Initialize(ctx context.Context) error {
	// Set exchange-specific fees
	switch p.config.ID {
	case "binance":
		p.fees.MakerFeeBps = 10
		p.fees.TakerFeeBps = 10
	case "coinbase":
		p.fees.MakerFeeBps = 40
		p.fees.TakerFeeBps = 60
	case "kraken":
		p.fees.MakerFeeBps = 16
		p.fees.TakerFeeBps = 26
	case "okx":
		p.fees.MakerFeeBps = 8
		p.fees.TakerFeeBps = 10
	case "bybit":
		p.fees.MakerFeeBps = 10
		p.fees.TakerFeeBps = 10
	case "kucoin":
		p.fees.MakerFeeBps = 10
		p.fees.TakerFeeBps = 10
	case "gate":
		p.fees.MakerFeeBps = 20
		p.fees.TakerFeeBps = 20
	}

	return nil
}

// GetSupportedPairs returns all supported trading pairs.
func (p *CEXProvider) GetSupportedPairs(ctx context.Context) ([]types.TradingPair, error) {
	return p.pairs, nil
}

// AddPair adds a trading pair to monitor.
func (p *CEXProvider) AddPair(base, quote string) {
	symbol := fmt.Sprintf("%s/%s", base, quote)
	p.pairs = append(p.pairs, types.TradingPair{
		Base:     types.Token{Symbol: base},
		Quote:    types.Token{Symbol: quote},
		Symbol:   symbol,
		Exchange: p.config.ID,
	})
}

// GetQuote fetches a price quote for a trading pair.
func (p *CEXProvider) GetQuote(ctx context.Context, pair string) (*types.PriceQuote, error) {
	// Check cache first
	p.tickerCacheMu.RLock()
	if cached, ok := p.tickerCache[pair]; ok {
		if time.Since(cached.timestamp) < tickerCacheDuration {
			p.tickerCacheMu.RUnlock()
			return cached.quote, nil
		}
	}
	p.tickerCacheMu.RUnlock()

	// Fetch ticker from exchange
	ticker, err := p.fetchTicker(ctx, pair)
	if err != nil {
		return nil, err
	}

	// Cache the quote
	p.tickerCacheMu.Lock()
	p.tickerCache[pair] = &cachedTicker{
		quote:     ticker,
		timestamp: time.Now(),
	}
	p.tickerCacheMu.Unlock()

	return ticker, nil
}

// GetOrderBook fetches the order book for a trading pair.
func (p *CEXProvider) GetOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	// Check cache first
	cacheKey := fmt.Sprintf("%s:%d", pair, depth)
	p.orderBookCacheMu.RLock()
	if cached, ok := p.orderBookCache[cacheKey]; ok {
		if time.Since(cached.timestamp) < orderBookCacheDuration {
			p.orderBookCacheMu.RUnlock()
			return cached.book, nil
		}
	}
	p.orderBookCacheMu.RUnlock()

	// Fetch order book from exchange
	book, err := p.fetchOrderBook(ctx, pair, depth)
	if err != nil {
		return nil, err
	}

	// Cache the order book
	p.orderBookCacheMu.Lock()
	p.orderBookCache[cacheKey] = &cachedOrderBook{
		book:      book,
		timestamp: time.Now(),
	}
	p.orderBookCacheMu.Unlock()

	return book, nil
}

// GetFees returns the fee structure.
func (p *CEXProvider) GetFees() *types.FeeStructure {
	return p.fees
}

// Close cleans up resources.
func (p *CEXProvider) Close() error {
	return nil
}

// fetchTicker fetches ticker data from the exchange API.
func (p *CEXProvider) fetchTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
	switch p.config.ID {
	case "binance":
		return p.fetchBinanceTicker(ctx, pair)
	case "coinbase":
		return p.fetchCoinbaseTicker(ctx, pair)
	case "kraken":
		return p.fetchKrakenTicker(ctx, pair)
	case "okx":
		return p.fetchOKXTicker(ctx, pair)
	case "bybit":
		return p.fetchBybitTicker(ctx, pair)
	case "kucoin":
		return p.fetchKucoinTicker(ctx, pair)
	case "gate":
		return p.fetchGateTicker(ctx, pair)
	default:
		return nil, fmt.Errorf("unsupported exchange: %s", p.config.ID)
	}
}

// fetchOrderBook fetches order book from the exchange API.
func (p *CEXProvider) fetchOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	switch p.config.ID {
	case "binance":
		return p.fetchBinanceOrderBook(ctx, pair, depth)
	case "coinbase":
		return p.fetchCoinbaseOrderBook(ctx, pair, depth)
	case "kraken":
		return p.fetchKrakenOrderBook(ctx, pair, depth)
	case "okx":
		return p.fetchOKXOrderBook(ctx, pair, depth)
	case "bybit":
		return p.fetchBybitOrderBook(ctx, pair, depth)
	case "kucoin":
		return p.fetchKucoinOrderBook(ctx, pair, depth)
	case "gate":
		return p.fetchGateOrderBook(ctx, pair, depth)
	default:
		return nil, fmt.Errorf("unsupported exchange: %s", p.config.ID)
	}
}

// Binance implementation
func (p *CEXProvider) fetchBinanceTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
	symbol := strings.ReplaceAll(pair, "/", "")
	url := fmt.Sprintf("https://api.binance.com/api/v3/ticker/bookTicker?symbol=%s", symbol)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Symbol   string `json:"symbol"`
		BidPrice string `json:"bidPrice"`
		AskPrice string `json:"askPrice"`
		BidQty   string `json:"bidQty"`
		AskQty   string `json:"askQty"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Binance ticker: %w", err)
	}

	bidPrice := parseFloat(data.BidPrice)
	askPrice := parseFloat(data.AskPrice)
	midPrice := new(big.Float).Add(bidPrice, askPrice)
	midPrice = midPrice.Quo(midPrice, big.NewFloat(2))

	return &types.PriceQuote{
		Exchange:  p.Name(),
		Pair:      pair,
		Price:     midPrice,
		BidPrice:  bidPrice,
		AskPrice:  askPrice,
		BidSize:   parseFloat(data.BidQty),
		AskSize:   parseFloat(data.AskQty),
		Timestamp: time.Now(),
		Source:    "ticker",
	}, nil
}

func (p *CEXProvider) fetchBinanceOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	symbol := strings.ReplaceAll(pair, "/", "")
	if depth > 1000 {
		depth = 1000
	}
	url := fmt.Sprintf("https://api.binance.com/api/v3/depth?symbol=%s&limit=%d", symbol, depth)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Binance order book: %w", err)
	}

	return &types.OrderBook{
		Exchange:  p.Name(),
		Pair:      pair,
		Bids:      parseOrderBookEntries(data.Bids),
		Asks:      parseOrderBookEntries(data.Asks),
		Timestamp: time.Now(),
	}, nil
}

// Coinbase implementation
func (p *CEXProvider) fetchCoinbaseTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
	productID := strings.ReplaceAll(pair, "/", "-")
	url := fmt.Sprintf("https://api.exchange.coinbase.com/products/%s/ticker", productID)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Price string `json:"price"`
		Bid   string `json:"bid"`
		Ask   string `json:"ask"`
		Size  string `json:"size"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Coinbase ticker: %w", err)
	}

	return &types.PriceQuote{
		Exchange:  p.Name(),
		Pair:      pair,
		Price:     parseFloat(data.Price),
		BidPrice:  parseFloat(data.Bid),
		AskPrice:  parseFloat(data.Ask),
		Timestamp: time.Now(),
		Source:    "ticker",
	}, nil
}

func (p *CEXProvider) fetchCoinbaseOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	productID := strings.ReplaceAll(pair, "/", "-")
	level := "2"
	if depth <= 1 {
		level = "1"
	}
	url := fmt.Sprintf("https://api.exchange.coinbase.com/products/%s/book?level=%s", productID, level)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Bids [][]interface{} `json:"bids"`
		Asks [][]interface{} `json:"asks"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Coinbase order book: %w", err)
	}

	return &types.OrderBook{
		Exchange:  p.Name(),
		Pair:      pair,
		Bids:      parseOrderBookEntriesInterface(data.Bids),
		Asks:      parseOrderBookEntriesInterface(data.Asks),
		Timestamp: time.Now(),
	}, nil
}

// Kraken implementation
func (p *CEXProvider) fetchKrakenTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
	// Convert pair format (e.g., BTC/USD -> XBTUSD)
	symbol := convertToKrakenSymbol(pair)
	url := fmt.Sprintf("https://api.kraken.com/0/public/Ticker?pair=%s", symbol)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Error  []string               `json:"error"`
		Result map[string]interface{} `json:"result"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Kraken ticker: %w", err)
	}

	if len(data.Error) > 0 {
		return nil, fmt.Errorf("Kraken API error: %v", data.Error)
	}

	// Get first result key
	var tickerData map[string]interface{}
	for _, v := range data.Result {
		tickerData = v.(map[string]interface{})
		break
	}

	// Parse ticker data
	bid := getKrakenPrice(tickerData, "b")
	ask := getKrakenPrice(tickerData, "a")
	last := getKrakenPrice(tickerData, "c")

	return &types.PriceQuote{
		Exchange:  p.Name(),
		Pair:      pair,
		Price:     last,
		BidPrice:  bid,
		AskPrice:  ask,
		Timestamp: time.Now(),
		Source:    "ticker",
	}, nil
}

func (p *CEXProvider) fetchKrakenOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	symbol := convertToKrakenSymbol(pair)
	if depth > 500 {
		depth = 500
	}
	url := fmt.Sprintf("https://api.kraken.com/0/public/Depth?pair=%s&count=%d", symbol, depth)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Error  []string                          `json:"error"`
		Result map[string]map[string][][]interface{} `json:"result"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Kraken order book: %w", err)
	}

	// Get first result
	var bookData map[string][][]interface{}
	for _, v := range data.Result {
		bookData = v
		break
	}

	return &types.OrderBook{
		Exchange:  p.Name(),
		Pair:      pair,
		Bids:      parseOrderBookEntriesInterface(bookData["bids"]),
		Asks:      parseOrderBookEntriesInterface(bookData["asks"]),
		Timestamp: time.Now(),
	}, nil
}

// OKX implementation
func (p *CEXProvider) fetchOKXTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
	instId := strings.ReplaceAll(pair, "/", "-")
	url := fmt.Sprintf("https://www.okx.com/api/v5/market/ticker?instId=%s", instId)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Data []struct {
			BidPx string `json:"bidPx"`
			AskPx string `json:"askPx"`
			Last  string `json:"last"`
			BidSz string `json:"bidSz"`
			AskSz string `json:"askSz"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse OKX ticker: %w", err)
	}

	if len(data.Data) == 0 {
		return nil, fmt.Errorf("no ticker data returned")
	}

	t := data.Data[0]
	return &types.PriceQuote{
		Exchange:  p.Name(),
		Pair:      pair,
		Price:     parseFloat(t.Last),
		BidPrice:  parseFloat(t.BidPx),
		AskPrice:  parseFloat(t.AskPx),
		BidSize:   parseFloat(t.BidSz),
		AskSize:   parseFloat(t.AskSz),
		Timestamp: time.Now(),
		Source:    "ticker",
	}, nil
}

func (p *CEXProvider) fetchOKXOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	instId := strings.ReplaceAll(pair, "/", "-")
	if depth > 400 {
		depth = 400
	}
	url := fmt.Sprintf("https://www.okx.com/api/v5/market/books?instId=%s&sz=%d", instId, depth)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Data []struct {
			Bids [][]string `json:"bids"`
			Asks [][]string `json:"asks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse OKX order book: %w", err)
	}

	if len(data.Data) == 0 {
		return nil, fmt.Errorf("no order book data returned")
	}

	return &types.OrderBook{
		Exchange:  p.Name(),
		Pair:      pair,
		Bids:      parseOrderBookEntries(data.Data[0].Bids),
		Asks:      parseOrderBookEntries(data.Data[0].Asks),
		Timestamp: time.Now(),
	}, nil
}

// Bybit implementation
func (p *CEXProvider) fetchBybitTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
	symbol := strings.ReplaceAll(pair, "/", "")
	url := fmt.Sprintf("https://api.bybit.com/v5/market/tickers?category=spot&symbol=%s", symbol)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Result struct {
			List []struct {
				Bid1Price string `json:"bid1Price"`
				Ask1Price string `json:"ask1Price"`
				LastPrice string `json:"lastPrice"`
				Bid1Size  string `json:"bid1Size"`
				Ask1Size  string `json:"ask1Size"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Bybit ticker: %w", err)
	}

	if len(data.Result.List) == 0 {
		return nil, fmt.Errorf("no ticker data returned")
	}

	t := data.Result.List[0]
	return &types.PriceQuote{
		Exchange:  p.Name(),
		Pair:      pair,
		Price:     parseFloat(t.LastPrice),
		BidPrice:  parseFloat(t.Bid1Price),
		AskPrice:  parseFloat(t.Ask1Price),
		BidSize:   parseFloat(t.Bid1Size),
		AskSize:   parseFloat(t.Ask1Size),
		Timestamp: time.Now(),
		Source:    "ticker",
	}, nil
}

func (p *CEXProvider) fetchBybitOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	symbol := strings.ReplaceAll(pair, "/", "")
	if depth > 200 {
		depth = 200
	}
	url := fmt.Sprintf("https://api.bybit.com/v5/market/orderbook?category=spot&symbol=%s&limit=%d", symbol, depth)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Result struct {
			B [][]string `json:"b"` // bids
			A [][]string `json:"a"` // asks
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Bybit order book: %w", err)
	}

	return &types.OrderBook{
		Exchange:  p.Name(),
		Pair:      pair,
		Bids:      parseOrderBookEntries(data.Result.B),
		Asks:      parseOrderBookEntries(data.Result.A),
		Timestamp: time.Now(),
	}, nil
}

// KuCoin implementation
func (p *CEXProvider) fetchKucoinTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
	symbol := strings.ReplaceAll(pair, "/", "-")
	url := fmt.Sprintf("https://api.kucoin.com/api/v1/market/orderbook/level1?symbol=%s", symbol)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Data struct {
			BestBid     string `json:"bestBid"`
			BestAsk     string `json:"bestAsk"`
			Price       string `json:"price"`
			BestBidSize string `json:"bestBidSize"`
			BestAskSize string `json:"bestAskSize"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse KuCoin ticker: %w", err)
	}

	return &types.PriceQuote{
		Exchange:  p.Name(),
		Pair:      pair,
		Price:     parseFloat(data.Data.Price),
		BidPrice:  parseFloat(data.Data.BestBid),
		AskPrice:  parseFloat(data.Data.BestAsk),
		BidSize:   parseFloat(data.Data.BestBidSize),
		AskSize:   parseFloat(data.Data.BestAskSize),
		Timestamp: time.Now(),
		Source:    "ticker",
	}, nil
}

func (p *CEXProvider) fetchKucoinOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	symbol := strings.ReplaceAll(pair, "/", "-")
	level := "20"
	if depth > 20 {
		level = "100"
	}
	url := fmt.Sprintf("https://api.kucoin.com/api/v1/market/orderbook/level2_%s?symbol=%s", level, symbol)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Data struct {
			Bids [][]string `json:"bids"`
			Asks [][]string `json:"asks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse KuCoin order book: %w", err)
	}

	return &types.OrderBook{
		Exchange:  p.Name(),
		Pair:      pair,
		Bids:      parseOrderBookEntries(data.Data.Bids),
		Asks:      parseOrderBookEntries(data.Data.Asks),
		Timestamp: time.Now(),
	}, nil
}

// Gate.io implementation
func (p *CEXProvider) fetchGateTicker(ctx context.Context, pair string) (*types.PriceQuote, error) {
	currencyPair := strings.ReplaceAll(pair, "/", "_")
	url := fmt.Sprintf("https://api.gateio.ws/api/v4/spot/tickers?currency_pair=%s", currencyPair)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data []struct {
		HighestBid string `json:"highest_bid"`
		LowestAsk  string `json:"lowest_ask"`
		Last       string `json:"last"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Gate.io ticker: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("no ticker data returned")
	}

	t := data[0]
	return &types.PriceQuote{
		Exchange:  p.Name(),
		Pair:      pair,
		Price:     parseFloat(t.Last),
		BidPrice:  parseFloat(t.HighestBid),
		AskPrice:  parseFloat(t.LowestAsk),
		Timestamp: time.Now(),
		Source:    "ticker",
	}, nil
}

func (p *CEXProvider) fetchGateOrderBook(ctx context.Context, pair string, depth int) (*types.OrderBook, error) {
	currencyPair := strings.ReplaceAll(pair, "/", "_")
	if depth > 100 {
		depth = 100
	}
	url := fmt.Sprintf("https://api.gateio.ws/api/v4/spot/order_book?currency_pair=%s&limit=%d", currencyPair, depth)

	resp, err := p.doRequest(ctx, url)
	if err != nil {
		return nil, err
	}

	var data struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("failed to parse Gate.io order book: %w", err)
	}

	return &types.OrderBook{
		Exchange:  p.Name(),
		Pair:      pair,
		Bids:      parseOrderBookEntries(data.Bids),
		Asks:      parseOrderBookEntries(data.Asks),
		Timestamp: time.Now(),
	}, nil
}

// Helper functions
func (p *CEXProvider) doRequest(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

func parseFloat(s string) *big.Float {
	f := new(big.Float)
	f.SetString(s)
	return f
}

func parseOrderBookEntries(entries [][]string) []types.OrderBookEntry {
	result := make([]types.OrderBookEntry, 0, len(entries))
	for _, entry := range entries {
		if len(entry) >= 2 {
			result = append(result, types.OrderBookEntry{
				Price:  parseFloat(entry[0]),
				Amount: parseFloat(entry[1]),
			})
		}
	}
	return result
}

func parseOrderBookEntriesInterface(entries [][]interface{}) []types.OrderBookEntry {
	result := make([]types.OrderBookEntry, 0, len(entries))
	for _, entry := range entries {
		if len(entry) >= 2 {
			price := fmt.Sprintf("%v", entry[0])
			amount := fmt.Sprintf("%v", entry[1])
			result = append(result, types.OrderBookEntry{
				Price:  parseFloat(price),
				Amount: parseFloat(amount),
			})
		}
	}
	return result
}

func convertToKrakenSymbol(pair string) string {
	// Convert common symbols to Kraken format
	pair = strings.ReplaceAll(pair, "BTC", "XBT")
	pair = strings.ReplaceAll(pair, "/", "")
	return pair
}

func getKrakenPrice(data map[string]interface{}, key string) *big.Float {
	if arr, ok := data[key].([]interface{}); ok && len(arr) > 0 {
		return parseFloat(fmt.Sprintf("%v", arr[0]))
	}
	return big.NewFloat(0)
}

// Predefined exchange configurations
var (
	BinanceConfig = ExchangeConfig{
		ID:      "binance",
		Name:    "Binance",
		BaseURL: "https://api.binance.com",
	}

	CoinbaseConfig = ExchangeConfig{
		ID:      "coinbase",
		Name:    "Coinbase",
		BaseURL: "https://api.exchange.coinbase.com",
	}

	KrakenConfig = ExchangeConfig{
		ID:      "kraken",
		Name:    "Kraken",
		BaseURL: "https://api.kraken.com",
	}

	OKXConfig = ExchangeConfig{
		ID:      "okx",
		Name:    "OKX",
		BaseURL: "https://www.okx.com",
	}

	BybitConfig = ExchangeConfig{
		ID:      "bybit",
		Name:    "Bybit",
		BaseURL: "https://api.bybit.com",
	}

	KucoinConfig = ExchangeConfig{
		ID:      "kucoin",
		Name:    "KuCoin",
		BaseURL: "https://api.kucoin.com",
	}

	GateConfig = ExchangeConfig{
		ID:      "gate",
		Name:    "Gate.io",
		BaseURL: "https://api.gateio.ws",
	}
)
