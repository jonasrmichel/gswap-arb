// Package executor provides trade execution implementations.
package executor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

const (
	// GalaChain API endpoints
	gswapQuotingAPI    = "https://gateway-mainnet.galachain.com/api/asset/dexv3-contract"
	gswapDexBackendAPI = "https://dex-backend-prod1.defi.gala.com"
	gswapAssetsAPI     = "https://gateway-mainnet.galachain.com/api/asset/token-contract"

	// Fee tiers
	gswapFeeTier005 = 500   // 0.05%
	gswapFeeTier030 = 3000  // 0.30%
	gswapFeeTier100 = 10000 // 1.00%
)

// GSwapExecutor implements TradeExecutor for GalaChain/GSwap DEX.
type GSwapExecutor struct {
	privateKey       *ecdsa.PrivateKey
	walletAddress    string // Ethereum address (0x...)
	galaChainAddress string // GalaChain format (eth|...)
	client           *http.Client

	// Token mappings
	tokens map[string]gswapToken

	ready bool
	mu    sync.RWMutex
}

// gswapToken holds token information for GSwap.
type gswapToken struct {
	Symbol       string
	GalaChainKey string // Format: "SYMBOL|Unit|none|none"
	Decimals     int
}

// gswapTokenClassKey represents a GalaChain token identifier.
type gswapTokenClassKey struct {
	Collection    string `json:"collection"`
	Category      string `json:"category"`
	Type          string `json:"type"`
	AdditionalKey string `json:"additionalKey"`
}

// gswapQuoteRequest is the request for QuoteExactAmount API.
type gswapQuoteRequest struct {
	Token0     gswapTokenClassKey `json:"token0"`
	Token1     gswapTokenClassKey `json:"token1"`
	ZeroForOne bool               `json:"zeroForOne"`
	Fee        int                `json:"fee"`
	Amount     string             `json:"amount"`
}

// gswapQuoteResponse is the response from QuoteExactAmount API.
type gswapQuoteResponse struct {
	Status int             `json:"Status"`
	Data   *gswapQuoteData `json:"Data"`
}

type gswapQuoteData struct {
	Amount0          string `json:"amount0"`
	Amount1          string `json:"amount1"`
	CurrentSqrtPrice string `json:"currentSqrtPrice"`
	NewSqrtPrice     string `json:"newSqrtPrice,omitempty"`
}

// gswapSwapRequest is the request for executing a swap.
type gswapSwapRequest struct {
	TokenIn          string `json:"tokenIn"`
	TokenOut         string `json:"tokenOut"`
	Fee              int    `json:"fee"`
	AmountIn         string `json:"amountIn,omitempty"`
	AmountOut        string `json:"amountOut,omitempty"`
	AmountInMaximum  string `json:"amountInMaximum,omitempty"`
	AmountOutMinimum string `json:"amountOutMinimum,omitempty"`
	Deadline         int64  `json:"deadline"`
	WalletAddress    string `json:"walletAddress"`
	Signature        string `json:"signature,omitempty"`
}

// gswapSwapResponse is the response from swap API.
type gswapSwapResponse struct {
	TransactionID string `json:"transactionId"`
	Status        string `json:"status"`
	Data          struct {
		AmountIn  string `json:"amountIn"`
		AmountOut string `json:"amountOut"`
	} `json:"data"`
}

// gswapAssetsResponse is the response from assets API.
type gswapAssetsResponse struct {
	Data struct {
		Tokens []struct {
			Symbol   string `json:"symbol"`
			Quantity string `json:"quantity"`
		} `json:"tokens"`
	} `json:"Data"`
}

// NewGSwapExecutor creates a new GSwap trade executor.
func NewGSwapExecutor(privateKeyHex, walletAddress string) (*GSwapExecutor, error) {
	// Parse private key
	privateKeyHex = strings.TrimPrefix(privateKeyHex, "0x")
	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key hex: %w", err)
	}

	privateKey, err := crypto.ToECDSA(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	// Derive address if not provided - always derive from private key to ensure correct checksum
	publicKey := privateKey.Public().(*ecdsa.PublicKey)
	derivedAddress := crypto.PubkeyToAddress(*publicKey)

	// Use provided address for display, but always use checksummed version for GalaChain
	if walletAddress == "" {
		walletAddress = derivedAddress.Hex()
	} else if !strings.HasPrefix(walletAddress, "0x") {
		walletAddress = "0x" + walletAddress
	}

	// Create GalaChain address format - must use checksummed address (mixed case) without 0x prefix
	galaChainAddress := "eth|" + strings.TrimPrefix(derivedAddress.Hex(), "0x")

	executor := &GSwapExecutor{
		privateKey:       privateKey,
		walletAddress:    walletAddress,
		galaChainAddress: galaChainAddress,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		tokens: make(map[string]gswapToken),
	}

	// Initialize known tokens
	executor.initializeTokens()

	return executor, nil
}

// initializeTokens sets up known GalaChain tokens.
func (g *GSwapExecutor) initializeTokens() {
	g.tokens = map[string]gswapToken{
		"GALA":   {Symbol: "GALA", GalaChainKey: "GALA|Unit|none|none", Decimals: 8},
		"GWETH":  {Symbol: "GWETH", GalaChainKey: "GWETH|Unit|none|none", Decimals: 18},
		"GUSDC":  {Symbol: "GUSDC", GalaChainKey: "GUSDC|Unit|none|none", Decimals: 6},
		"GUSDT":  {Symbol: "GUSDT", GalaChainKey: "GUSDT|Unit|none|none", Decimals: 6},
		"GUSDUC": {Symbol: "GUSDUC", GalaChainKey: "GUSDUC|Unit|none|none", Decimals: 6},
		"GSOL":   {Symbol: "GSOL", GalaChainKey: "GSOL|Unit|none|none", Decimals: 9},
		"GWBTC":  {Symbol: "GWBTC", GalaChainKey: "GWBTC|Unit|none|none", Decimals: 8},
		"GMEW":   {Symbol: "GMEW", GalaChainKey: "GMEW|Unit|none|none", Decimals: 8},
		"BENE":   {Symbol: "BENE", GalaChainKey: "Token|Unit|BENE|client:5c806869e7fd0e2384461ce9", Decimals: 18},
		// Aliases
		"ETH":   {Symbol: "GWETH", GalaChainKey: "GWETH|Unit|none|none", Decimals: 18},
		"USDC":  {Symbol: "GUSDC", GalaChainKey: "GUSDC|Unit|none|none", Decimals: 6},
		"USDT":  {Symbol: "GUSDT", GalaChainKey: "GUSDT|Unit|none|none", Decimals: 6},
		"USDUC": {Symbol: "GUSDUC", GalaChainKey: "GUSDUC|Unit|none|none", Decimals: 6},
		"MEW":   {Symbol: "GMEW", GalaChainKey: "GMEW|Unit|none|none", Decimals: 8},
	}
}

// Name returns the exchange name.
func (g *GSwapExecutor) Name() string {
	return "gswap"
}

// Type returns the exchange type.
func (g *GSwapExecutor) Type() types.ExchangeType {
	return types.ExchangeTypeDEX
}

// Initialize sets up the executor and validates credentials.
func (g *GSwapExecutor) Initialize(ctx context.Context) error {
	if g.privateKey == nil {
		return fmt.Errorf("GSwap private key not configured")
	}

	// Test connection by fetching balances
	_, err := g.GetBalances(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to GSwap: %w", err)
	}

	g.mu.Lock()
	g.ready = true
	g.mu.Unlock()

	return nil
}

// GetBalance retrieves the balance for a specific currency.
func (g *GSwapExecutor) GetBalance(ctx context.Context, currency string) (*Balance, error) {
	balances, err := g.GetBalances(ctx)
	if err != nil {
		return nil, err
	}

	// Try both original symbol and GalaChain prefixed version
	balance, ok := balances[strings.ToUpper(currency)]
	if !ok {
		// Try with G prefix
		balance, ok = balances["G"+strings.ToUpper(currency)]
	}
	if !ok {
		// Return zero balance if not found
		return &Balance{
			Exchange:  g.Name(),
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
func (g *GSwapExecutor) GetBalances(ctx context.Context) (map[string]*Balance, error) {
	// Call GalaChain assets API
	url := fmt.Sprintf("%s/FetchBalances", gswapAssetsAPI)

	reqBody := map[string]interface{}{
		"owner": g.galaChainAddress,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var assetsResp struct {
		Data []struct {
			Collection string `json:"collection"`
			Category   string `json:"category"`
			Type       string `json:"type"`
			Quantity   string `json:"quantity"`
		} `json:"Data"`
	}

	if err := json.Unmarshal(body, &assetsResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	balances := make(map[string]*Balance)
	now := time.Now()

	for _, asset := range assetsResp.Data {
		quantity := new(big.Float)
		quantity.SetString(asset.Quantity)

		if quantity == nil || quantity.Sign() == 0 {
			continue
		}

		// Use collection as symbol (e.g., "GALA", "GWETH")
		symbol := asset.Collection

		balances[symbol] = &Balance{
			Exchange:  g.Name(),
			Currency:  symbol,
			Free:      quantity,
			Locked:    big.NewFloat(0),
			Total:     quantity,
			UpdatedAt: now,
		}
	}

	return balances, nil
}

// PlaceMarketOrder places a market order (swap).
func (g *GSwapExecutor) PlaceMarketOrder(ctx context.Context, pair string, side OrderSide, amount *big.Float) (*Order, error) {
	// Parse pair (e.g., "GALA/GUSDC" or "GALA/USDT")
	parts := strings.Split(pair, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid pair format: %s", pair)
	}

	baseSymbol := parts[0]
	quoteSymbol := parts[1]

	// Determine token in/out based on side
	var tokenIn, tokenOut string
	var amountIn *big.Float

	if side == OrderSideBuy {
		// Buying base with quote: we want to sell tokenIn (quote) to get tokenOut (base)
		// The amount parameter represents how much base (tokenOut) we want
		// We need to figure out how much quote (tokenIn) we need to provide
		tokenIn = g.getGalaChainKey(quoteSymbol)
		tokenOut = g.getGalaChainKey(baseSymbol)

		// Get a quote to determine how much tokenIn we need
		// First, get a quote to find the rate
		testAmount := big.NewFloat(1.0)
		testQuote, zeroForOne, err := g.getQuote(ctx, tokenIn, tokenOut, testAmount)
		if err != nil {
			return nil, fmt.Errorf("failed to get quote for buy: %w", err)
		}

		// Calculate rate from test quote
		var outAmountStr string
		if zeroForOne {
			outAmountStr = strings.TrimPrefix(testQuote.Data.Amount1, "-")
		} else {
			outAmountStr = strings.TrimPrefix(testQuote.Data.Amount0, "-")
		}
		testOut := new(big.Float)
		testOut.SetString(outAmountStr)

		// Rate = output per input
		// For buy, we want `amount` of base, so we need amount/rate of quote
		// Add 5% buffer for slippage on the input side
		if testOut.Sign() <= 0 {
			return nil, fmt.Errorf("invalid quote rate for buy order")
		}
		rate := new(big.Float).Quo(testOut, testAmount)
		amountIn = new(big.Float).Quo(amount, rate)
		amountIn = new(big.Float).Mul(amountIn, big.NewFloat(1.05)) // 5% buffer
	} else {
		// Selling base for quote: tokenIn=base, tokenOut=quote
		tokenIn = g.getGalaChainKey(baseSymbol)
		tokenOut = g.getGalaChainKey(quoteSymbol)
		amountIn = amount
	}

	if tokenIn == "" || tokenOut == "" {
		return nil, fmt.Errorf("unknown token in pair: %s", pair)
	}

	// Get quote for the actual trade
	quote, zeroForOne, err := g.getQuote(ctx, tokenIn, tokenOut, amountIn)
	if err != nil {
		return nil, fmt.Errorf("failed to get quote: %w", err)
	}

	// Calculate output amount based on direction
	var outAmountStr string
	if zeroForOne {
		outAmountStr = strings.TrimPrefix(quote.Data.Amount1, "-")
	} else {
		outAmountStr = strings.TrimPrefix(quote.Data.Amount0, "-")
	}
	amountOut := new(big.Float)
	amountOut.SetString(outAmountStr)

	// Calculate minimum output with 1% slippage
	minAmountOut := new(big.Float).Mul(amountOut, big.NewFloat(0.99)) // 1% slippage

	// Execute swap
	order, err := g.executeSwap(ctx, tokenIn, tokenOut, amountIn, minAmountOut, gswapFeeTier100)
	if err != nil {
		return nil, err
	}

	order.Pair = pair
	order.Side = side

	return order, nil
}

// PlaceLimitOrder places a limit order - not directly supported on DEX.
func (g *GSwapExecutor) PlaceLimitOrder(ctx context.Context, pair string, side OrderSide, amount, price *big.Float) (*Order, error) {
	// DEX doesn't support limit orders in the traditional sense
	// We can simulate by checking if current price meets limit
	return nil, fmt.Errorf("limit orders not supported on GSwap DEX - use market orders")
}

// GetOrder retrieves an order by ID.
func (g *GSwapExecutor) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	// Check transaction status
	url := fmt.Sprintf("https://api-galachain-prod.gala.com/transaction/%s", orderID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var txResp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Data   struct {
			AmountIn  string `json:"amountIn"`
			AmountOut string `json:"amountOut"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &txResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Map status
	var status OrderStatus
	switch strings.ToLower(txResp.Status) {
	case "confirmed", "success":
		status = OrderStatusFilled
	case "pending":
		status = OrderStatusOpen
	case "failed":
		status = OrderStatusFailed
	default:
		status = OrderStatusNew
	}

	filledAmount := new(big.Float)
	filledAmount.SetString(txResp.Data.AmountOut)

	return &Order{
		ID:            orderID,
		Exchange:      g.Name(),
		Status:        status,
		FilledAmount:  filledAmount,
		TransactionID: orderID,
		UpdatedAt:     time.Now(),
	}, nil
}

// CancelOrder cancels an open order - not supported on DEX.
func (g *GSwapExecutor) CancelOrder(ctx context.Context, orderID string) error {
	return fmt.Errorf("cancel not supported on GSwap DEX - transactions are atomic")
}

// GetOpenOrders retrieves all open orders - not applicable for DEX.
func (g *GSwapExecutor) GetOpenOrders(ctx context.Context, pair string) ([]*Order, error) {
	// DEX doesn't have persistent open orders
	return []*Order{}, nil
}

// IsReady returns true if the executor is ready to trade.
func (g *GSwapExecutor) IsReady() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.ready
}

// Close cleans up resources.
func (g *GSwapExecutor) Close() error {
	g.mu.Lock()
	g.ready = false
	g.mu.Unlock()
	return nil
}

// Helper methods

// getGalaChainKey returns the GalaChain token key for a symbol.
func (g *GSwapExecutor) getGalaChainKey(symbol string) string {
	symbol = strings.ToUpper(symbol)
	if token, ok := g.tokens[symbol]; ok {
		return token.GalaChainKey
	}
	// Default format
	return symbol + "|Unit|none|none"
}

// getQuote gets a quote for a swap using QuoteExactAmount API.
// Returns the quote response and whether tokenIn is token0 (for parsing amounts).
func (g *GSwapExecutor) getQuote(ctx context.Context, tokenIn, tokenOut string, amountIn *big.Float) (*gswapQuoteResponse, bool, error) {
	url := gswapQuotingAPI + "/QuoteExactAmount"

	// Parse token keys
	tkIn := parseTokenClassKey(tokenIn)
	tkOut := parseTokenClassKey(tokenOut)

	// Sort tokens lexicographically by collection (API requires token0 < token1)
	token0 := tkIn
	token1 := tkOut
	zeroForOne := true // selling token0 (tokenIn)
	if tkIn.Collection > tkOut.Collection {
		token0 = tkOut
		token1 = tkIn
		zeroForOne = false // selling token1 (tokenIn)
	}

	amountStr := amountIn.Text('f', 0) // Integer amount

	// Try different fee tiers and pick the best result
	feeTiers := []int{gswapFeeTier005, gswapFeeTier030, gswapFeeTier100}
	var bestResp *gswapQuoteResponse
	var bestAmount *big.Float

	for _, fee := range feeTiers {
		reqBody := gswapQuoteRequest{
			Token0:     token0,
			Token1:     token1,
			ZeroForOne: zeroForOne,
			Fee:        fee,
			Amount:     amountStr,
		}

		jsonBody, err := json.Marshal(reqBody)
		if err != nil {
			continue
		}

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := g.client.Do(req)
		if err != nil {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			continue
		}

		var quoteResp gswapQuoteResponse
		if err := json.Unmarshal(body, &quoteResp); err != nil {
			continue
		}

		if quoteResp.Data == nil {
			continue
		}

		// Determine output amount based on direction
		var outAmountStr string
		if zeroForOne {
			// Selling token0, receiving token1
			outAmountStr = strings.TrimPrefix(quoteResp.Data.Amount1, "-")
		} else {
			// Selling token1, receiving token0
			outAmountStr = strings.TrimPrefix(quoteResp.Data.Amount0, "-")
		}

		outAmount, ok := new(big.Float).SetString(outAmountStr)
		if !ok || outAmount.Sign() <= 0 {
			continue
		}

		// Keep the best (highest output) quote
		if bestAmount == nil || outAmount.Cmp(bestAmount) > 0 {
			bestAmount = outAmount
			bestResp = &quoteResp
		}
	}

	if bestResp == nil {
		return nil, false, fmt.Errorf("no valid quote from any fee tier")
	}

	return bestResp, zeroForOne, nil
}

// executeSwap executes a swap transaction.
func (g *GSwapExecutor) executeSwap(ctx context.Context, tokenIn, tokenOut string, amountIn, minAmountOut *big.Float, feeTier int) (*Order, error) {
	url := gswapQuotingAPI + "/ExactInputSingle"

	if feeTier == 0 {
		feeTier = gswapFeeTier100 // Default 1% fee tier
	}

	deadline := time.Now().Add(5 * time.Minute).Unix()

	// Create the swap request
	swapReq := map[string]interface{}{
		"tokenIn":          parseTokenClassKey(tokenIn),
		"tokenOut":         parseTokenClassKey(tokenOut),
		"fee":              feeTier,
		"amountIn":         amountIn.Text('f', 0),
		"amountOutMinimum": minAmountOut.Text('f', 0),
		"deadline":         deadline,
		"recipient":        g.galaChainAddress,
	}

	// Sign the transaction
	signature, err := g.signTransaction(swapReq)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Add signature to request
	signedReq := map[string]interface{}{
		"dto":       swapReq,
		"signature": signature,
	}

	jsonBody, err := json.Marshal(signedReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("swap failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var swapResp struct {
		Data struct {
			TransactionID string `json:"transactionId"`
			Hash          string `json:"hash"`
		} `json:"Data"`
	}

	if err := json.Unmarshal(body, &swapResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	txID := swapResp.Data.TransactionID
	if txID == "" {
		txID = swapResp.Data.Hash
	}

	return &Order{
		ID:            txID,
		Exchange:      g.Name(),
		Type:          OrderTypeMarket,
		Status:        OrderStatusNew,
		Amount:        amountIn,
		TransactionID: txID,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}, nil
}

// signTransaction signs a transaction for GalaChain.
func (g *GSwapExecutor) signTransaction(data interface{}) (string, error) {
	// Serialize the data to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to serialize data: %w", err)
	}

	// Hash the data using Keccak256
	hash := crypto.Keccak256Hash(jsonData)

	// Sign the hash
	signature, err := crypto.Sign(hash.Bytes(), g.privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign: %w", err)
	}

	// Return hex-encoded signature
	return "0x" + hex.EncodeToString(signature), nil
}

// parseTokenClassKey parses a token string into gswapTokenClassKey format.
func parseTokenClassKey(token string) gswapTokenClassKey {
	parts := strings.Split(token, "|")
	if len(parts) != 4 {
		return gswapTokenClassKey{
			Collection:    token,
			Category:      "Unit",
			Type:          "none",
			AdditionalKey: "none",
		}
	}
	return gswapTokenClassKey{
		Collection:    parts[0],
		Category:      parts[1],
		Type:          parts[2],
		AdditionalKey: parts[3],
	}
}

// GetQuote returns a quote for a swap (public method for external use).
func (g *GSwapExecutor) GetQuote(ctx context.Context, pair string, side OrderSide, amount *big.Float) (*types.PriceQuote, error) {
	parts := strings.Split(pair, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid pair format: %s", pair)
	}

	baseSymbol := parts[0]
	quoteSymbol := parts[1]

	var tokenIn, tokenOut string
	var amountIn *big.Float

	if side == OrderSideBuy {
		tokenIn = g.getGalaChainKey(quoteSymbol)
		tokenOut = g.getGalaChainKey(baseSymbol)
	} else {
		tokenIn = g.getGalaChainKey(baseSymbol)
		tokenOut = g.getGalaChainKey(quoteSymbol)
	}
	amountIn = amount

	quoteResp, zeroForOne, err := g.getQuote(ctx, tokenIn, tokenOut, amountIn)
	if err != nil {
		return nil, err
	}

	// Calculate output amount based on direction
	var outAmountStr string
	if zeroForOne {
		outAmountStr = strings.TrimPrefix(quoteResp.Data.Amount1, "-")
	} else {
		outAmountStr = strings.TrimPrefix(quoteResp.Data.Amount0, "-")
	}
	amountOut := new(big.Float)
	amountOut.SetString(outAmountStr)

	// Calculate effective price
	price := new(big.Float).Quo(amountOut, amountIn)
	if side == OrderSideBuy {
		// Invert for buy
		price = new(big.Float).Quo(amountIn, amountOut)
	}

	return &types.PriceQuote{
		Exchange:  g.Name(),
		Pair:      pair,
		Price:     price,
		BidPrice:  price,
		AskPrice:  price,
		BidSize:   amountOut,
		AskSize:   amountIn,
		Timestamp: time.Now(),
		Source:    "quote",
	}, nil
}

// GetWalletAddress returns the wallet address.
func (g *GSwapExecutor) GetWalletAddress() string {
	return g.walletAddress
}

// GetGalaChainAddress returns the GalaChain format address.
func (g *GSwapExecutor) GetGalaChainAddress() string {
	return g.galaChainAddress
}
