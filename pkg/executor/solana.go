// Package executor provides trade execution implementations.
package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/jupiter"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

const (
	// Default Solana RPC endpoint
	defaultSolanaRPC = "https://api.mainnet-beta.solana.com"

	// Default slippage for swaps (50 bps = 0.5%)
	defaultSlippageBps = 50

	// Transaction timeout
	txTimeout = 60 * time.Second
)

// SolanaExecutor implements TradeExecutor for Solana via Jupiter aggregator.
type SolanaExecutor struct {
	rpcURL         string
	privateKey     string // Base58 encoded private key
	walletAddress  string // Base58 encoded public key
	jupiterClient  *jupiter.Client
	tokens         map[string]jupiter.TokenInfo
	slippageBps    int
	httpClient     *http.Client

	ready bool
	mu    sync.RWMutex
}

// SolanaExecutorConfig contains configuration for the Solana executor.
type SolanaExecutorConfig struct {
	RPCURL         string
	PrivateKey     string // Base58 encoded keypair (64 bytes) or secret key (32 bytes)
	WalletAddress  string // Optional: derived from private key if not provided
	JupiterBaseURL string
	JupiterAPIKey  string
	SlippageBps    int
	Tokens         map[string]jupiter.TokenInfo
}

// NewSolanaExecutor creates a new Solana trade executor.
func NewSolanaExecutor(config *SolanaExecutorConfig) (*SolanaExecutor, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	if config.PrivateKey == "" {
		return nil, fmt.Errorf("private key is required")
	}

	rpcURL := config.RPCURL
	if rpcURL == "" {
		rpcURL = defaultSolanaRPC
	}

	slippageBps := config.SlippageBps
	if slippageBps == 0 {
		slippageBps = defaultSlippageBps
	}

	tokens := config.Tokens
	if tokens == nil {
		tokens = jupiter.DefaultTokens()
	}

	jupiterConfig := &jupiter.ClientConfig{
		BaseURL: config.JupiterBaseURL,
		APIKey:  config.JupiterAPIKey,
	}

	executor := &SolanaExecutor{
		rpcURL:        rpcURL,
		privateKey:    config.PrivateKey,
		walletAddress: config.WalletAddress,
		jupiterClient: jupiter.NewClient(jupiterConfig),
		tokens:        tokens,
		slippageBps:   slippageBps,
		httpClient: &http.Client{
			Timeout: txTimeout,
		},
	}

	return executor, nil
}

// Name returns the exchange name.
func (s *SolanaExecutor) Name() string {
	return "jupiter"
}

// Type returns the exchange type.
func (s *SolanaExecutor) Type() types.ExchangeType {
	return types.ExchangeTypeDEX
}

// Initialize sets up the executor and validates configuration.
func (s *SolanaExecutor) Initialize(ctx context.Context) error {
	// Validate RPC connection
	if err := s.testRPCConnection(ctx); err != nil {
		return fmt.Errorf("failed to connect to Solana RPC: %w", err)
	}

	// Derive wallet address from private key if not provided
	if s.walletAddress == "" {
		addr, err := s.deriveWalletAddress()
		if err != nil {
			return fmt.Errorf("failed to derive wallet address: %w", err)
		}
		s.walletAddress = addr
	}

	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()

	return nil
}

// testRPCConnection tests the RPC connection.
func (s *SolanaExecutor) testRPCConnection(ctx context.Context) error {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getHealth",
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.rpcURL, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("RPC returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// deriveWalletAddress derives the wallet address from the private key.
// Note: This is a placeholder - actual derivation requires ed25519 key handling.
func (s *SolanaExecutor) deriveWalletAddress() (string, error) {
	// For now, require the wallet address to be provided
	// Full implementation would use ed25519 to derive public key from private key
	return "", fmt.Errorf("wallet address must be provided in config (automatic derivation not yet implemented)")
}

// GetBalance retrieves the balance for a specific token.
func (s *SolanaExecutor) GetBalance(ctx context.Context, currency string) (*Balance, error) {
	balances, err := s.GetBalances(ctx)
	if err != nil {
		return nil, err
	}

	balance, ok := balances[strings.ToUpper(currency)]
	if !ok {
		// Return zero balance if not found
		return &Balance{
			Exchange:  s.Name(),
			Currency:  currency,
			Free:      big.NewFloat(0),
			Locked:    big.NewFloat(0),
			Total:     big.NewFloat(0),
			UpdatedAt: time.Now(),
		}, nil
	}

	return balance, nil
}

// GetBalances retrieves all token balances.
func (s *SolanaExecutor) GetBalances(ctx context.Context) (map[string]*Balance, error) {
	balances := make(map[string]*Balance)
	now := time.Now()

	// Get SOL balance
	solBalance, err := s.getSOLBalance(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get SOL balance: %w", err)
	}

	balances["SOL"] = &Balance{
		Exchange:  s.Name(),
		Currency:  "SOL",
		Free:      solBalance,
		Locked:    big.NewFloat(0),
		Total:     solBalance,
		UpdatedAt: now,
	}

	// Get token balances for known tokens
	for symbol, token := range s.tokens {
		if symbol == "SOL" {
			continue // Already fetched
		}

		tokenBalance, err := s.getTokenBalance(ctx, token.Mint)
		if err != nil {
			// Log but don't fail - token might not have an account
			continue
		}

		if tokenBalance.Sign() > 0 {
			balances[symbol] = &Balance{
				Exchange:  s.Name(),
				Currency:  symbol,
				Free:      tokenBalance,
				Locked:    big.NewFloat(0),
				Total:     tokenBalance,
				UpdatedAt: now,
			}
		}
	}

	return balances, nil
}

// getSOLBalance gets the native SOL balance.
func (s *SolanaExecutor) getSOLBalance(ctx context.Context) (*big.Float, error) {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getBalance",
		"params":  []interface{}{s.walletAddress},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.rpcURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Result struct {
			Value uint64 `json:"value"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", result.Error.Message)
	}

	// Convert lamports to SOL (9 decimals)
	balance := new(big.Float).SetUint64(result.Result.Value)
	balance.Quo(balance, big.NewFloat(1e9))

	return balance, nil
}

// getTokenBalance gets the balance for a specific SPL token.
func (s *SolanaExecutor) getTokenBalance(ctx context.Context, mint string) (*big.Float, error) {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getTokenAccountsByOwner",
		"params": []interface{}{
			s.walletAddress,
			map[string]string{"mint": mint},
			map[string]string{"encoding": "jsonParsed"},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.rpcURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Result struct {
			Value []struct {
				Account struct {
					Data struct {
						Parsed struct {
							Info struct {
								TokenAmount struct {
									UIAmount float64 `json:"uiAmount"`
								} `json:"tokenAmount"`
							} `json:"info"`
						} `json:"parsed"`
					} `json:"data"`
				} `json:"account"`
			} `json:"value"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", result.Error.Message)
	}

	// Sum up balances from all token accounts
	totalBalance := big.NewFloat(0)
	for _, account := range result.Result.Value {
		amount := account.Account.Data.Parsed.Info.TokenAmount.UIAmount
		totalBalance.Add(totalBalance, big.NewFloat(amount))
	}

	return totalBalance, nil
}

// PlaceMarketOrder places a market order (swap) on Jupiter.
func (s *SolanaExecutor) PlaceMarketOrder(ctx context.Context, pair string, side OrderSide, amount *big.Float) (*Order, error) {
	// Parse pair (e.g., "SOL/USDC")
	parts := strings.Split(pair, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid pair format: %s", pair)
	}

	baseSymbol := strings.ToUpper(parts[0])
	quoteSymbol := strings.ToUpper(parts[1])

	baseToken, ok := s.tokens[baseSymbol]
	if !ok {
		return nil, fmt.Errorf("unknown base token: %s", baseSymbol)
	}

	quoteToken, ok := s.tokens[quoteSymbol]
	if !ok {
		return nil, fmt.Errorf("unknown quote token: %s", quoteSymbol)
	}

	// Determine input/output based on side
	var inputMint, outputMint string
	var inputDecimals int
	var swapMode string

	if side == OrderSideBuy {
		// Buy: spend quote to get base (ExactOut - we want exact amount of base)
		inputMint = quoteToken.Mint
		outputMint = baseToken.Mint
		inputDecimals = quoteToken.Decimals
		swapMode = jupiter.SwapModeExactOut
	} else {
		// Sell: spend base to get quote (ExactIn - we're selling exact amount of base)
		inputMint = baseToken.Mint
		outputMint = quoteToken.Mint
		inputDecimals = baseToken.Decimals
		swapMode = jupiter.SwapModeExactIn
	}

	// Convert amount to smallest units
	amountFloat, _ := amount.Float64()
	amountUnits := int64(amountFloat * float64(pow10(inputDecimals)))

	// Get quote from Jupiter
	quote, err := s.jupiterClient.GetQuote(ctx, &jupiter.QuoteParams{
		InputMint:   inputMint,
		OutputMint:  outputMint,
		Amount:      strconv.FormatInt(amountUnits, 10),
		SlippageBps: s.slippageBps,
		SwapMode:    swapMode,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get Jupiter quote: %w", err)
	}

	// Build swap transaction
	swapResp, err := s.jupiterClient.BuildSwapTransaction(ctx, &jupiter.SwapParams{
		QuoteResponse:            quote,
		UserPublicKey:            s.walletAddress,
		WrapAndUnwrapSol:         true,
		DynamicComputeUnitLimit:  true,
		PrioritizationFeeLamports: "auto",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build swap transaction: %w", err)
	}

	// Sign and send transaction
	txSig, err := s.signAndSendTransaction(ctx, swapResp.SwapTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to send transaction: %w", err)
	}

	// Parse amounts for order response
	outAmount, _ := strconv.ParseInt(quote.OutAmount, 10, 64)
	var filledAmount *big.Float
	if side == OrderSideBuy {
		filledAmount = new(big.Float).SetInt64(outAmount)
		filledAmount.Quo(filledAmount, big.NewFloat(float64(pow10(baseToken.Decimals))))
	} else {
		filledAmount = new(big.Float).SetInt64(outAmount)
		filledAmount.Quo(filledAmount, big.NewFloat(float64(pow10(quoteToken.Decimals))))
	}

	return &Order{
		ID:            txSig,
		Exchange:      s.Name(),
		Pair:          pair,
		Side:          side,
		Type:          OrderTypeMarket,
		Status:        OrderStatusFilled,
		Amount:        amount,
		FilledAmount:  filledAmount,
		TransactionID: txSig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}, nil
}

// signAndSendTransaction signs and sends a transaction to the Solana network.
// Note: This implementation uses the experimental sendTransaction with external signing.
// For production, consider using a proper Solana SDK for Go.
func (s *SolanaExecutor) signAndSendTransaction(ctx context.Context, txBase64 string) (string, error) {
	// Decode transaction
	txBytes, err := base64.StdEncoding.DecodeString(txBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode transaction: %w", err)
	}

	// For now, we'll use the simulateTransaction endpoint to validate
	// and then send using a raw transaction approach
	// Full signing requires ed25519 implementation

	// This is a simplified implementation that assumes the transaction is already signed
	// by Jupiter (some Jupiter endpoints return pre-signed transactions)
	// For full implementation, you would:
	// 1. Deserialize the versioned transaction
	// 2. Sign with the wallet's private key using ed25519
	// 3. Serialize and send

	// Send raw transaction
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "sendTransaction",
		"params": []interface{}{
			base64.StdEncoding.EncodeToString(txBytes),
			map[string]interface{}{
				"encoding":          "base64",
				"skipPreflight":     false,
				"preflightCommitment": "confirmed",
				"maxRetries":        3,
			},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.rpcURL, bytes.NewReader(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result struct {
		Result string `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Logs []string `json:"logs"`
			} `json:"data"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("transaction failed: %s", result.Error.Message)
	}

	// Wait for confirmation
	if err := s.confirmTransaction(ctx, result.Result); err != nil {
		return "", fmt.Errorf("transaction confirmation failed: %w", err)
	}

	return result.Result, nil
}

// confirmTransaction waits for transaction confirmation.
func (s *SolanaExecutor) confirmTransaction(ctx context.Context, signature string) error {
	// Poll for confirmation
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	timeout := time.After(txTimeout)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("transaction confirmation timeout")
		case <-ticker.C:
			confirmed, err := s.checkTransactionStatus(ctx, signature)
			if err != nil {
				continue // Keep polling on errors
			}
			if confirmed {
				return nil
			}
		}
	}
}

// checkTransactionStatus checks if a transaction is confirmed.
func (s *SolanaExecutor) checkTransactionStatus(ctx context.Context, signature string) (bool, error) {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getSignatureStatuses",
		"params": []interface{}{
			[]string{signature},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.rpcURL, bytes.NewReader(jsonBody))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	var result struct {
		Result struct {
			Value []struct {
				Slot               uint64  `json:"slot"`
				Confirmations      *uint64 `json:"confirmations"`
				ConfirmationStatus string  `json:"confirmationStatus"`
				Err                interface{} `json:"err"`
			} `json:"value"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return false, err
	}

	if len(result.Result.Value) == 0 || result.Result.Value[0].ConfirmationStatus == "" {
		return false, nil // Not found yet
	}

	status := result.Result.Value[0]
	if status.Err != nil {
		return false, fmt.Errorf("transaction failed: %v", status.Err)
	}

	// Check for finalized or confirmed status
	return status.ConfirmationStatus == "confirmed" || status.ConfirmationStatus == "finalized", nil
}

// PlaceLimitOrder places a limit order - not supported on DEX.
func (s *SolanaExecutor) PlaceLimitOrder(ctx context.Context, pair string, side OrderSide, amount, price *big.Float) (*Order, error) {
	return nil, fmt.Errorf("limit orders not supported on Jupiter DEX - use market orders")
}

// GetOrder retrieves an order by transaction signature.
func (s *SolanaExecutor) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	// Check transaction status
	confirmed, err := s.checkTransactionStatus(ctx, orderID)
	if err != nil {
		return &Order{
			ID:        orderID,
			Exchange:  s.Name(),
			Status:    OrderStatusFailed,
			UpdatedAt: time.Now(),
		}, nil
	}

	status := OrderStatusOpen
	if confirmed {
		status = OrderStatusFilled
	}

	return &Order{
		ID:            orderID,
		Exchange:      s.Name(),
		Status:        status,
		TransactionID: orderID,
		UpdatedAt:     time.Now(),
	}, nil
}

// CancelOrder cancels an open order - not supported on DEX.
func (s *SolanaExecutor) CancelOrder(ctx context.Context, orderID string) error {
	return fmt.Errorf("cancel not supported on Jupiter DEX - transactions are atomic")
}

// GetOpenOrders retrieves all open orders - not applicable for DEX.
func (s *SolanaExecutor) GetOpenOrders(ctx context.Context, pair string) ([]*Order, error) {
	// DEX doesn't have persistent open orders
	return []*Order{}, nil
}

// IsReady returns true if the executor is ready to trade.
func (s *SolanaExecutor) IsReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

// Close cleans up resources.
func (s *SolanaExecutor) Close() error {
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
	return nil
}

// AddToken adds a custom token to the executor.
func (s *SolanaExecutor) AddToken(symbol, mint string, decimals int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[symbol] = jupiter.TokenInfo{
		Symbol:   symbol,
		Mint:     mint,
		Decimals: decimals,
	}
}

// GetWalletAddress returns the wallet address.
func (s *SolanaExecutor) GetWalletAddress() string {
	return s.walletAddress
}

// GetQuote returns a quote for a swap (public method for external use).
func (s *SolanaExecutor) GetQuote(ctx context.Context, pair string, side OrderSide, amount *big.Float) (*types.PriceQuote, error) {
	parts := strings.Split(pair, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid pair format: %s", pair)
	}

	baseSymbol := strings.ToUpper(parts[0])
	quoteSymbol := strings.ToUpper(parts[1])

	baseToken, ok := s.tokens[baseSymbol]
	if !ok {
		return nil, fmt.Errorf("unknown base token: %s", baseSymbol)
	}

	quoteToken, ok := s.tokens[quoteSymbol]
	if !ok {
		return nil, fmt.Errorf("unknown quote token: %s", quoteSymbol)
	}

	var inputMint, outputMint string
	var inputDecimals, outputDecimals int
	var swapMode string

	if side == OrderSideBuy {
		inputMint = quoteToken.Mint
		outputMint = baseToken.Mint
		inputDecimals = quoteToken.Decimals
		outputDecimals = baseToken.Decimals
		swapMode = jupiter.SwapModeExactOut
	} else {
		inputMint = baseToken.Mint
		outputMint = quoteToken.Mint
		inputDecimals = baseToken.Decimals
		outputDecimals = quoteToken.Decimals
		swapMode = jupiter.SwapModeExactIn
	}

	amountFloat, _ := amount.Float64()
	amountUnits := int64(amountFloat * float64(pow10(inputDecimals)))

	quote, err := s.jupiterClient.GetQuote(ctx, &jupiter.QuoteParams{
		InputMint:   inputMint,
		OutputMint:  outputMint,
		Amount:      strconv.FormatInt(amountUnits, 10),
		SlippageBps: s.slippageBps,
		SwapMode:    swapMode,
	})
	if err != nil {
		return nil, err
	}

	outAmount, _ := strconv.ParseInt(quote.OutAmount, 10, 64)
	inAmount, _ := strconv.ParseInt(quote.InAmount, 10, 64)

	// Calculate effective price
	var price *big.Float
	if side == OrderSideBuy {
		// Price = quote spent / base received
		price = new(big.Float).Quo(
			new(big.Float).SetInt64(inAmount),
			new(big.Float).SetInt64(outAmount),
		)
		price.Mul(price, big.NewFloat(float64(pow10(outputDecimals-inputDecimals))))
	} else {
		// Price = quote received / base spent
		price = new(big.Float).Quo(
			new(big.Float).SetInt64(outAmount),
			new(big.Float).SetInt64(inAmount),
		)
		price.Mul(price, big.NewFloat(float64(pow10(inputDecimals-outputDecimals))))
	}

	var priceImpact float64
	if quote.PriceImpactPct != "" {
		priceImpact, _ = strconv.ParseFloat(quote.PriceImpactPct, 64)
	}

	return &types.PriceQuote{
		Exchange:    s.Name(),
		Pair:        pair,
		Price:       price,
		BidPrice:    price,
		AskPrice:    price,
		Timestamp:   time.Now(),
		Source:      "jupiter_quote",
		PriceImpact: priceImpact,
	}, nil
}

// pow10 returns 10^n as int64.
func pow10(n int) int64 {
	result := int64(1)
	for i := 0; i < n; i++ {
		result *= 10
	}
	return result
}
