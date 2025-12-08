// Package bridge provides cross-chain bridge functionality between GalaChain and Ethereum.
package bridge

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
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// GalaChain bridge API endpoints
	galaChainBridgeAPI = "https://gateway-mainnet.galachain.com/api/asset/token-contract"
	galaChainAssetsAPI = "https://gateway-mainnet.galachain.com/api/asset/token-contract"

	// Ethereum bridge contract
	ethereumBridgeAddress = "0x9b9a11e3F0C3B8A8F8e7eA7C3B4B3B3B3B3B3B3B" // Placeholder - needs actual address
)

// BridgeExecutor handles bridge operations between GalaChain and Ethereum.
type BridgeExecutor struct {
	// GalaChain credentials
	galaPrivateKey    *ecdsa.PrivateKey
	galaWalletAddress string
	galaChainAddress  string // eth|... format

	// Ethereum credentials
	ethPrivateKey    *ecdsa.PrivateKey
	ethWalletAddress string
	ethRPCURL        string

	client *http.Client
}

// NewBridgeExecutor creates a new bridge executor.
func NewBridgeExecutor(config *BridgeConfig) (*BridgeExecutor, error) {
	executor := &BridgeExecutor{
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		ethRPCURL: config.EthereumRPCURL,
	}

	// Parse GalaChain private key
	if config.GalaChainPrivateKey != "" {
		keyHex := strings.TrimPrefix(config.GalaChainPrivateKey, "0x")
		keyBytes, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("invalid GalaChain private key: %w", err)
		}

		privateKey, err := crypto.ToECDSA(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse GalaChain private key: %w", err)
		}

		executor.galaPrivateKey = privateKey

		// Derive address if not provided
		if config.GalaChainAddress == "" {
			publicKey := privateKey.Public().(*ecdsa.PublicKey)
			executor.galaWalletAddress = crypto.PubkeyToAddress(*publicKey).Hex()
		} else {
			executor.galaWalletAddress = config.GalaChainAddress
		}

		// Normalize and create GalaChain address format
		if !strings.HasPrefix(executor.galaWalletAddress, "0x") {
			executor.galaWalletAddress = "0x" + executor.galaWalletAddress
		}
		executor.galaChainAddress = "eth|" + strings.TrimPrefix(executor.galaWalletAddress, "0x")
	}

	// Parse Ethereum private key (can be same as GalaChain)
	if config.EthereumPrivateKey != "" {
		keyHex := strings.TrimPrefix(config.EthereumPrivateKey, "0x")
		keyBytes, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("invalid Ethereum private key: %w", err)
		}

		privateKey, err := crypto.ToECDSA(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse Ethereum private key: %w", err)
		}

		executor.ethPrivateKey = privateKey
		publicKey := privateKey.Public().(*ecdsa.PublicKey)
		executor.ethWalletAddress = crypto.PubkeyToAddress(*publicKey).Hex()
	} else if executor.galaPrivateKey != nil {
		// Use same key for both chains
		executor.ethPrivateKey = executor.galaPrivateKey
		executor.ethWalletAddress = executor.galaWalletAddress
	}

	return executor, nil
}

// GetGalaChainBalance returns the balance for a token on GalaChain.
func (b *BridgeExecutor) GetGalaChainBalance(ctx context.Context, token string) (*big.Float, error) {
	url := galaChainAssetsAPI + "/FetchBalances"

	reqBody := map[string]interface{}{
		"owner": b.galaChainAddress,
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

	resp, err := b.client.Do(req)
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

	var assetsResp struct {
		Data []struct {
			Collection string `json:"collection"`
			Quantity   string `json:"quantity"`
		} `json:"Data"`
	}

	if err := json.Unmarshal(body, &assetsResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Find the requested token
	tokenUpper := strings.ToUpper(token)
	for _, asset := range assetsResp.Data {
		if strings.ToUpper(asset.Collection) == tokenUpper {
			balance := new(big.Float)
			balance.SetString(asset.Quantity)
			return balance, nil
		}
	}

	// Token not found, return zero
	return big.NewFloat(0), nil
}

// BridgeToEthereum initiates a bridge transfer from GalaChain to Ethereum.
func (b *BridgeExecutor) BridgeToEthereum(ctx context.Context, req *BridgeRequest) (*BridgeResult, error) {
	if b.galaPrivateKey == nil {
		return nil, fmt.Errorf("GalaChain private key not configured")
	}

	// Validate token is supported
	tokenInfo, ok := GetTokenBySymbol(req.Token)
	if !ok {
		return nil, fmt.Errorf("unsupported token: %s", req.Token)
	}

	// Check balance
	balance, err := b.GetGalaChainBalance(ctx, req.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	if balance.Cmp(req.Amount) < 0 {
		return nil, fmt.Errorf("insufficient balance: have %s, need %s", balance.Text('f', 8), req.Amount.Text('f', 8))
	}

	// Determine destination address
	toAddress := req.ToAddress
	if toAddress == "" {
		toAddress = b.ethWalletAddress
	}

	// Create the bridge out request for GalaChain
	// This calls the GalaChain bridge contract to burn tokens and emit bridge event
	bridgeReq := map[string]interface{}{
		"tokenClass": map[string]string{
			"collection":    tokenInfo.GalaChainSymbol,
			"category":      "Unit",
			"type":          "none",
			"additionalKey": "none",
		},
		"quantity":    req.Amount.Text('f', 0),
		"destination": strings.TrimPrefix(toAddress, "0x"), // Ethereum address without 0x
	}

	// Sign the transaction
	signature, err := b.signGalaChainTransaction(bridgeReq)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	signedReq := map[string]interface{}{
		"dto":       bridgeReq,
		"signature": signature,
	}

	jsonBody, err := json.Marshal(signedReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Call the bridge out endpoint
	url := galaChainBridgeAPI + "/BridgeTokenOut"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("bridge request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var bridgeResp struct {
		Data struct {
			TransactionID string `json:"transactionId"`
			Hash          string `json:"hash"`
		} `json:"Data"`
	}

	if err := json.Unmarshal(body, &bridgeResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	txID := bridgeResp.Data.TransactionID
	if txID == "" {
		txID = bridgeResp.Data.Hash
	}

	return &BridgeResult{
		TransactionID: txID,
		Token:         req.Token,
		Amount:        req.Amount,
		Direction:     BridgeToEthereum,
		FromAddress:   b.galaWalletAddress,
		ToAddress:     toAddress,
		Status:        BridgeStatusPending,
		SourceTxHash:  txID,
		EstimatedTime: 15 * time.Minute, // Typical bridge time
		CreatedAt:     time.Now(),
	}, nil
}

// BridgeToGalaChain initiates a bridge transfer from Ethereum to GalaChain.
func (b *BridgeExecutor) BridgeToGalaChain(ctx context.Context, req *BridgeRequest) (*BridgeResult, error) {
	if b.ethPrivateKey == nil {
		return nil, fmt.Errorf("Ethereum private key not configured")
	}

	// Validate token is supported
	tokenInfo, ok := GetTokenBySymbol(req.Token)
	if !ok {
		return nil, fmt.Errorf("unsupported token: %s", req.Token)
	}

	// Determine destination address
	toAddress := req.ToAddress
	if toAddress == "" {
		toAddress = b.galaChainAddress
	}

	// For Ethereum -> GalaChain, we need to:
	// 1. Approve the bridge contract to spend tokens (if not using permit)
	// 2. Call the bridge contract's deposit function

	// This requires actual Ethereum transaction signing and submission
	// For now, return instructions for manual execution
	return &BridgeResult{
		TransactionID: fmt.Sprintf("pending-%d", time.Now().UnixNano()),
		Token:         req.Token,
		Amount:        req.Amount,
		Direction:     BridgeToGalaChain,
		FromAddress:   b.ethWalletAddress,
		ToAddress:     toAddress,
		Status:        BridgeStatusPending,
		EstimatedTime: 15 * time.Minute,
		CreatedAt:     time.Now(),
		Error: fmt.Sprintf(
			"Manual steps required:\n"+
				"1. Approve bridge contract at %s to spend %s %s\n"+
				"2. Call bridge deposit function with destination: %s\n"+
				"Token contract: %s\n"+
				"Uses permit: %v",
			ethereumBridgeAddress,
			req.Amount.Text('f', 8),
			req.Token,
			toAddress,
			tokenInfo.Address,
			tokenInfo.BridgeUsesPermit,
		),
	}, nil
}

// Bridge executes a bridge transfer based on direction.
func (b *BridgeExecutor) Bridge(ctx context.Context, req *BridgeRequest) (*BridgeResult, error) {
	switch req.Direction {
	case BridgeToEthereum:
		return b.BridgeToEthereum(ctx, req)
	case BridgeToGalaChain:
		return b.BridgeToGalaChain(ctx, req)
	default:
		return nil, fmt.Errorf("invalid bridge direction: %s", req.Direction)
	}
}

// GetBridgeStatus checks the status of a bridge transaction.
func (b *BridgeExecutor) GetBridgeStatus(ctx context.Context, txID string) (*BridgeResult, error) {
	// Query GalaChain for transaction status
	url := fmt.Sprintf("https://api-galachain-prod.gala.com/transaction/%s", txID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := b.client.Do(req)
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
			Token  string `json:"token"`
			Amount string `json:"amount"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &txResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var status BridgeStatus
	switch strings.ToLower(txResp.Status) {
	case "confirmed", "success", "completed":
		status = BridgeStatusCompleted
	case "pending":
		status = BridgeStatusPending
	case "failed":
		status = BridgeStatusFailed
	default:
		status = BridgeStatusPending
	}

	amount := new(big.Float)
	amount.SetString(txResp.Data.Amount)

	return &BridgeResult{
		TransactionID: txID,
		Token:         txResp.Data.Token,
		Amount:        amount,
		Status:        status,
		SourceTxHash:  txID,
	}, nil
}

// signGalaChainTransaction signs a transaction for GalaChain.
func (b *BridgeExecutor) signGalaChainTransaction(data interface{}) (string, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to serialize data: %w", err)
	}

	hash := crypto.Keccak256Hash(jsonData)
	signature, err := crypto.Sign(hash.Bytes(), b.galaPrivateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign: %w", err)
	}

	return "0x" + hex.EncodeToString(signature), nil
}

// GetWalletAddresses returns the configured wallet addresses.
func (b *BridgeExecutor) GetWalletAddresses() (galaChain, ethereum string) {
	return b.galaWalletAddress, b.ethWalletAddress
}

// GetGalaChainAddress returns the GalaChain format address.
func (b *BridgeExecutor) GetGalaChainAddress() string {
	return b.galaChainAddress
}
