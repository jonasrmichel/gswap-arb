// Package bridge provides cross-chain bridge functionality between GalaChain and Ethereum.
package bridge

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

const (
	// GalaConnect DEX API (for bridge operations)
	galaConnectAPI = "https://dex-api-platform-dex-prod-gala.gala.com"

	// GalaChain Gateway API (for balance queries)
	galaChainAssetsAPI = "https://gateway-mainnet.galachain.com/api/asset/token-contract"

	// Ethereum bridge contract
	ethereumBridgeAddress = "0x9f452b7cC24e6e6FA690fe77CF5dD2ba3DbF1ED9"

	// GalaChain destination chain ID for bridge
	galaChainID = 1
)

// ERC20 ABI for approve and allowance
const erc20ABI = `[
	{"constant":true,"inputs":[],"name":"decimals","outputs":[{"name":"","type":"uint8"}],"type":"function"},
	{"constant":true,"inputs":[{"name":"owner","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"type":"function"},
	{"constant":false,"inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"name":"approve","outputs":[{"name":"","type":"bool"}],"type":"function"},
	{"constant":true,"inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"}],"name":"allowance","outputs":[{"name":"","type":"uint256"}],"type":"function"},
	{"constant":true,"inputs":[{"name":"owner","type":"address"}],"name":"nonces","outputs":[{"name":"","type":"uint256"}],"type":"function"},
	{"constant":true,"inputs":[],"name":"name","outputs":[{"name":"","type":"string"}],"type":"function"}
]`

// Bridge contract ABI
const bridgeContractABI = `[
	{"inputs":[{"name":"token","type":"address"},{"name":"amount","type":"uint256"},{"name":"tokenId","type":"uint256"},{"name":"destinationChainId","type":"uint16"},{"name":"recipient","type":"bytes"}],"name":"bridgeOut","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"token","type":"address"},{"name":"amount","type":"uint256"},{"name":"destinationChainId","type":"uint16"},{"name":"recipient","type":"bytes"},{"name":"deadline","type":"uint256"},{"name":"v","type":"uint8"},{"name":"r","type":"bytes32"},{"name":"s","type":"bytes32"}],"name":"bridgeOutWithPermit","outputs":[],"stateMutability":"nonpayable","type":"function"}
]`

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
			Type       string `json:"type"`
			Quantity   string `json:"quantity"`
		} `json:"Data"`
	}

	if err := json.Unmarshal(body, &assetsResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Find the requested token by matching the token class fields
	tokenInfo, ok := GetTokenBySymbol(token)
	if !ok {
		return nil, fmt.Errorf("unsupported token: %s", token)
	}

	for _, asset := range assetsResp.Data {
		// Match on collection and type (the key identifiers)
		if asset.Collection == tokenInfo.Collection && asset.Type == tokenInfo.Type {
			balance := new(big.Float)
			balance.SetString(asset.Quantity)
			return balance, nil
		}
	}

	// Token not found, return zero
	return big.NewFloat(0), nil
}

// BridgeToEthereum initiates a bridge transfer from GalaChain to Ethereum.
// This uses the GalaConnect DEX API which requires a two-step process:
// 1. RequestTokenBridgeOut - get bridge fee info and create request
// 2. BridgeTokenOut - execute the bridge with signed payload
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

	// Step 1: Get bridge fee information
	feeReq := map[string]interface{}{
		"chainId": "Ethereum",
		"bridgeToken": map[string]string{
			"collection":    tokenInfo.Collection,
			"category":      tokenInfo.Category,
			"type":          tokenInfo.Type,
			"additionalKey": tokenInfo.AdditionalKey,
		},
	}

	feeBody, err := json.Marshal(feeReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal fee request: %w", err)
	}

	feeHTTPReq, err := http.NewRequestWithContext(ctx, "POST", galaConnectAPI+"/v1/bridge/fee", bytes.NewReader(feeBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create fee request: %w", err)
	}
	feeHTTPReq.Header.Set("Content-Type", "application/json")
	feeHTTPReq.Header.Set("X-Wallet-Address", b.galaChainAddress)

	feeResp, err := b.client.Do(feeHTTPReq)
	if err != nil {
		return nil, fmt.Errorf("fee API request failed: %w", err)
	}
	defer feeResp.Body.Close()

	feeRespBody, err := io.ReadAll(feeResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read fee response: %w", err)
	}

	if feeResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fee request failed with status %d: %s", feeResp.StatusCode, string(feeRespBody))
	}

	// Parse fee response to get destinationChainTxFee
	var feeData map[string]interface{}
	if err := json.Unmarshal(feeRespBody, &feeData); err != nil {
		return nil, fmt.Errorf("failed to decode fee response: %w", err)
	}

	// Step 2: Create and sign the bridge request
	uuid, err := generateBridgeRequestID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate unique key: %w", err)
	}
	uniqueKey := "galaconnect-operation-" + uuid

	// Determine if using cross-rate (check for galaExchangeCrossRate in fee data)
	_, hasCrossRate := feeData["galaExchangeCrossRate"]

	// Build the message for signing (this is what gets hashed for EIP-712)
	// Note: destinationChainId should be numeric for EIP-712 signing
	signMessage := map[string]interface{}{
		"uniqueKey": uniqueKey,
		"tokenInstance": map[string]interface{}{
			"collection":    tokenInfo.Collection,
			"category":      tokenInfo.Category,
			"type":          tokenInfo.Type,
			"additionalKey": tokenInfo.AdditionalKey,
			"instance":      "0",
		},
		"destinationChainId":    2, // Numeric for EIP-712
		"quantity":              req.Amount.Text('f', 0),
		"recipient":             strings.TrimPrefix(toAddress, "0x"),
		"destinationChainTxFee": feeData,
	}

	// Sign the bridge request using EIP-712 style signing
	signature, err := b.signEIP712BridgeRequest(signMessage, hasCrossRate)
	if err != nil {
		return nil, fmt.Errorf("failed to sign bridge request: %w", err)
	}

	// EIP-712 domain
	domain := map[string]interface{}{
		"name":    "GalaConnect",
		"chainId": 1,
	}

	// EIP-712 types (simplified - using legacy types)
	types := getLegacyTypedDataTypes()

	// Build the prefix for the signed message
	messageJSON, _ := json.Marshal(signMessage)
	typedDataPayload := map[string]interface{}{
		"domain":      domain,
		"message":     signMessage,
		"primaryType": "GalaTransaction",
		"types":       types,
	}
	typedDataJSON, _ := json.Marshal(typedDataPayload)
	prefix := fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(typedDataJSON))

	// Build the full request DTO (chainId must be numeric)
	bridgeReqDTO := map[string]interface{}{
		"uniqueKey": uniqueKey,
		"tokenInstance": map[string]interface{}{
			"collection":    tokenInfo.Collection,
			"category":      tokenInfo.Category,
			"type":          tokenInfo.Type,
			"additionalKey": tokenInfo.AdditionalKey,
			"instance":      "0",
		},
		"destinationChainId":    2, // Ethereum chain ID (numeric)
		"quantity":              req.Amount.Text('f', 0),
		"recipient":             strings.TrimPrefix(toAddress, "0x"),
		"destinationChainTxFee": feeData,
		"signature":             signature,
		"prefix":                prefix,
		"types":                 types,
		"domain":                domain,
	}

	// Ignore unused variable
	_ = messageJSON

	bridgeBody, err := json.Marshal(bridgeReqDTO)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal bridge request: %w", err)
	}

	// Step 3: Submit the bridge request
	bridgeHTTPReq, err := http.NewRequestWithContext(ctx, "POST", galaConnectAPI+"/v1/RequestTokenBridgeOut", bytes.NewReader(bridgeBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create bridge request: %w", err)
	}
	bridgeHTTPReq.Header.Set("Content-Type", "application/json")
	bridgeHTTPReq.Header.Set("X-Wallet-Address", b.galaChainAddress)

	bridgeResp, err := b.client.Do(bridgeHTTPReq)
	if err != nil {
		return nil, fmt.Errorf("bridge API request failed: %w", err)
	}
	defer bridgeResp.Body.Close()

	bridgeRespBody, err := io.ReadAll(bridgeResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read bridge response: %w", err)
	}

	if bridgeResp.StatusCode != http.StatusOK && bridgeResp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("bridge request failed with status %d: %s", bridgeResp.StatusCode, string(bridgeRespBody))
	}

	// Parse bridge request response to get bridgeRequestId
	var bridgeReqResp map[string]interface{}
	if err := json.Unmarshal(bridgeRespBody, &bridgeReqResp); err != nil {
		return nil, fmt.Errorf("failed to decode bridge request response: %w", err)
	}

	// Extract bridgeRequestId from response (handle different response formats)
	bridgeRequestID := extractBridgeRequestID(bridgeReqResp)
	if bridgeRequestID == "" {
		return nil, fmt.Errorf("no bridgeRequestId in response: %s", string(bridgeRespBody))
	}

	// Step 4: Execute the bridge with BridgeTokenOut
	// Note: bridgeTokenOut only needs bridgeFromChannel and bridgeRequestId
	bridgeOutReq := map[string]interface{}{
		"bridgeFromChannel": "asset",
		"bridgeRequestId":   bridgeRequestID,
	}

	bridgeOutBody, err := json.Marshal(bridgeOutReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal bridge out request: %w", err)
	}

	bridgeOutHTTPReq, err := http.NewRequestWithContext(ctx, "POST", galaConnectAPI+"/v1/BridgeTokenOut", bytes.NewReader(bridgeOutBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create bridge out request: %w", err)
	}
	bridgeOutHTTPReq.Header.Set("Content-Type", "application/json")
	bridgeOutHTTPReq.Header.Set("X-Wallet-Address", b.galaChainAddress)

	bridgeOutResp, err := b.client.Do(bridgeOutHTTPReq)
	if err != nil {
		return nil, fmt.Errorf("bridge out API request failed: %w", err)
	}
	defer bridgeOutResp.Body.Close()

	bridgeOutRespBody, err := io.ReadAll(bridgeOutResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read bridge out response: %w", err)
	}

	if bridgeOutResp.StatusCode != http.StatusOK && bridgeOutResp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("bridge out failed with status %d: %s", bridgeOutResp.StatusCode, string(bridgeOutRespBody))
	}

	// Parse response for transaction hash
	var bridgeOutData map[string]interface{}
	if err := json.Unmarshal(bridgeOutRespBody, &bridgeOutData); err != nil {
		return nil, fmt.Errorf("failed to decode bridge out response: %w", err)
	}

	txHash := extractTransactionHash(bridgeOutData)

	return &BridgeResult{
		TransactionID: bridgeRequestID,
		Token:         req.Token,
		Amount:        req.Amount,
		Direction:     BridgeToEthereum,
		FromAddress:   b.galaWalletAddress,
		ToAddress:     toAddress,
		Status:        BridgeStatusPending,
		SourceTxHash:  txHash,
		EstimatedTime: 15 * time.Minute,
		CreatedAt:     time.Now(),
	}, nil
}

// extractBridgeRequestID extracts the bridge request ID from various response formats
func extractBridgeRequestID(resp map[string]interface{}) string {
	// Try different paths where bridgeRequestId might be
	if id, ok := resp["bridgeRequestId"].(string); ok {
		return id
	}
	if data, ok := resp["Data"].(map[string]interface{}); ok {
		if id, ok := data["bridgeRequestId"].(string); ok {
			return id
		}
	}
	if data, ok := resp["data"].(map[string]interface{}); ok {
		if id, ok := data["bridgeRequestId"].(string); ok {
			return id
		}
	}

	// GalaChain response format: Data is a string that IS the bridgeRequestId
	// It may contain null characters as part of a composite key like "\x00GCTXR\x002\x00eth|...\x001765295497802\x00"
	// The reference implementation uses this raw Data string as the bridgeRequestId
	if dataStr, ok := resp["Data"].(string); ok && len(dataStr) > 0 {
		return dataStr
	}
	if dataStr, ok := resp["data"].(string); ok && len(dataStr) > 0 {
		return dataStr
	}

	return ""
}

// extractTransactionHash extracts transaction hash from various response formats
func extractTransactionHash(resp map[string]interface{}) string {
	// Try different paths
	if hash, ok := resp["transactionHash"].(string); ok {
		return hash
	}
	if hash, ok := resp["hash"].(string); ok {
		return hash
	}
	if data, ok := resp["Data"].(map[string]interface{}); ok {
		if hash, ok := data["transactionHash"].(string); ok {
			return hash
		}
		if hash, ok := data["hash"].(string); ok {
			return hash
		}
	}
	if data, ok := resp["data"].(map[string]interface{}); ok {
		if hash, ok := data["transactionHash"].(string); ok {
			return hash
		}
		if hash, ok := data["hash"].(string); ok {
			return hash
		}
	}
	return ""
}

// BridgeToGalaChain initiates a bridge transfer from Ethereum to GalaChain.
func (b *BridgeExecutor) BridgeToGalaChain(ctx context.Context, req *BridgeRequest) (*BridgeResult, error) {
	if b.ethPrivateKey == nil {
		return nil, fmt.Errorf("Ethereum private key not configured")
	}

	if b.ethRPCURL == "" {
		return nil, fmt.Errorf("Ethereum RPC URL not configured (set ETH_RPC_URL)")
	}

	// Validate token is supported
	tokenInfo, ok := GetTokenBySymbol(req.Token)
	if !ok {
		return nil, fmt.Errorf("unsupported token: %s", req.Token)
	}

	// Determine destination address (GalaChain format)
	toAddress := req.ToAddress
	if toAddress == "" {
		toAddress = b.galaChainAddress
	}

	// Connect to Ethereum
	client, err := ethclient.DialContext(ctx, b.ethRPCURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ethereum: %w", err)
	}
	defer client.Close()

	// Get chain ID
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get chain ID: %w", err)
	}

	// Parse ABIs
	erc20ParsedABI, err := abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ERC20 ABI: %w", err)
	}

	bridgeParsedABI, err := abi.JSON(strings.NewReader(bridgeContractABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse bridge ABI: %w", err)
	}

	// Token and bridge addresses
	tokenAddress := common.HexToAddress(tokenInfo.Address)
	bridgeAddress := common.HexToAddress(ethereumBridgeAddress)

	// Calculate amount in token base units
	amountInBaseUnits := toBaseUnits(req.Amount, tokenInfo.Decimals)

	// Check token balance on Ethereum
	tokenContract := bind.NewBoundContract(tokenAddress, erc20ParsedABI, client, client, client)
	var balanceResult []interface{}
	err = tokenContract.Call(&bind.CallOpts{Context: ctx}, &balanceResult, "balanceOf", common.HexToAddress(b.ethWalletAddress))
	if err != nil {
		return nil, fmt.Errorf("failed to get token balance: %w", err)
	}
	balance := balanceResult[0].(*big.Int)

	if balance.Cmp(amountInBaseUnits) < 0 {
		return nil, fmt.Errorf("insufficient Ethereum token balance: have %s, need %s",
			fromBaseUnits(balance, tokenInfo.Decimals).Text('f', 8),
			req.Amount.Text('f', 8))
	}

	// Create transaction signer
	auth, err := bind.NewKeyedTransactorWithChainID(b.ethPrivateKey, chainID)
	if err != nil {
		return nil, fmt.Errorf("failed to create transactor: %w", err)
	}
	auth.Context = ctx

	var bridgeTxHash string

	if tokenInfo.BridgeUsesPermit {
		// Use permit flow (EIP-2612) - sign permit and call bridgeOutWithPermit
		bridgeTxHash, err = b.bridgeWithPermit(ctx, client, auth, tokenContract, &bridgeParsedABI, tokenAddress, bridgeAddress, amountInBaseUnits, toAddress, chainID)
		if err != nil {
			return nil, fmt.Errorf("permit bridge failed: %w", err)
		}
	} else {
		// Standard approval flow
		bridgeTxHash, err = b.bridgeWithApproval(ctx, client, auth, tokenContract, &erc20ParsedABI, &bridgeParsedABI, tokenAddress, bridgeAddress, amountInBaseUnits, toAddress)
		if err != nil {
			return nil, fmt.Errorf("approval bridge failed: %w", err)
		}
	}

	return &BridgeResult{
		TransactionID: bridgeTxHash,
		Token:         req.Token,
		Amount:        req.Amount,
		Direction:     BridgeToGalaChain,
		FromAddress:   b.ethWalletAddress,
		ToAddress:     toAddress,
		Status:        BridgeStatusPending,
		SourceTxHash:  bridgeTxHash,
		EstimatedTime: 15 * time.Minute,
		CreatedAt:     time.Now(),
	}, nil
}

// bridgeWithApproval handles the standard approve + bridgeOut flow
func (b *BridgeExecutor) bridgeWithApproval(
	ctx context.Context,
	client *ethclient.Client,
	auth *bind.TransactOpts,
	tokenContract *bind.BoundContract,
	erc20ABI *abi.ABI,
	bridgeABI *abi.ABI,
	tokenAddress, bridgeAddress common.Address,
	amount *big.Int,
	recipient string,
) (string, error) {
	// Check current allowance
	var allowanceResult []interface{}
	err := tokenContract.Call(&bind.CallOpts{Context: ctx}, &allowanceResult, "allowance",
		common.HexToAddress(b.ethWalletAddress), bridgeAddress)
	if err != nil {
		return "", fmt.Errorf("failed to check allowance: %w", err)
	}
	currentAllowance := allowanceResult[0].(*big.Int)

	// Approve if needed
	if currentAllowance.Cmp(amount) < 0 {
		// Approve max uint256 to avoid repeated approvals
		maxApproval := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

		approveTx, err := tokenContract.Transact(auth, "approve", bridgeAddress, maxApproval)
		if err != nil {
			return "", fmt.Errorf("failed to approve: %w", err)
		}

		// Wait for approval confirmation
		receipt, err := bind.WaitMined(ctx, client, approveTx)
		if err != nil {
			return "", fmt.Errorf("failed to wait for approval: %w", err)
		}
		if receipt.Status != types.ReceiptStatusSuccessful {
			return "", fmt.Errorf("approval transaction failed")
		}

		// Reset nonce for next transaction
		nonce, err := client.PendingNonceAt(ctx, common.HexToAddress(b.ethWalletAddress))
		if err != nil {
			return "", fmt.Errorf("failed to get nonce: %w", err)
		}
		auth.Nonce = big.NewInt(int64(nonce))
	}

	// Call bridgeOut
	bridgeContract := bind.NewBoundContract(bridgeAddress, *bridgeABI, client, client, client)

	// Recipient is the GalaChain address as bytes
	recipientBytes := []byte(recipient)

	bridgeTx, err := bridgeContract.Transact(auth, "bridgeOut",
		tokenAddress,
		amount,
		big.NewInt(0), // tokenId (0 for fungible tokens)
		uint16(galaChainID),
		recipientBytes,
	)
	if err != nil {
		return "", fmt.Errorf("failed to call bridgeOut: %w", err)
	}

	// Wait for bridge transaction confirmation
	receipt, err := bind.WaitMined(ctx, client, bridgeTx)
	if err != nil {
		return "", fmt.Errorf("failed to wait for bridge transaction: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return "", fmt.Errorf("bridge transaction failed")
	}

	return bridgeTx.Hash().Hex(), nil
}

// bridgeWithPermit handles the EIP-2612 permit + bridgeOutWithPermit flow
func (b *BridgeExecutor) bridgeWithPermit(
	ctx context.Context,
	client *ethclient.Client,
	auth *bind.TransactOpts,
	tokenContract *bind.BoundContract,
	bridgeABI *abi.ABI,
	tokenAddress, bridgeAddress common.Address,
	amount *big.Int,
	recipient string,
	chainID *big.Int,
) (string, error) {
	// Get token name for EIP-712 domain
	var nameResult []interface{}
	err := tokenContract.Call(&bind.CallOpts{Context: ctx}, &nameResult, "name")
	if err != nil {
		return "", fmt.Errorf("failed to get token name: %w", err)
	}
	tokenName := nameResult[0].(string)

	// Get current nonce for permit
	var nonceResult []interface{}
	err = tokenContract.Call(&bind.CallOpts{Context: ctx}, &nonceResult, "nonces",
		common.HexToAddress(b.ethWalletAddress))
	if err != nil {
		return "", fmt.Errorf("failed to get permit nonce: %w", err)
	}
	nonce := nonceResult[0].(*big.Int)

	// Set deadline (1 hour from now)
	deadline := big.NewInt(time.Now().Add(time.Hour).Unix())

	// Sign EIP-712 permit
	v, r, s, err := b.signPermit(tokenName, tokenAddress, bridgeAddress, amount, nonce, deadline, chainID)
	if err != nil {
		return "", fmt.Errorf("failed to sign permit: %w", err)
	}

	// Call bridgeOutWithPermit
	bridgeContract := bind.NewBoundContract(bridgeAddress, *bridgeABI, client, client, client)
	recipientBytes := []byte(recipient)

	bridgeTx, err := bridgeContract.Transact(auth, "bridgeOutWithPermit",
		tokenAddress,
		amount,
		uint16(galaChainID),
		recipientBytes,
		deadline,
		v,
		r,
		s,
	)
	if err != nil {
		return "", fmt.Errorf("failed to call bridgeOutWithPermit: %w", err)
	}

	// Wait for bridge transaction confirmation
	receipt, err := bind.WaitMined(ctx, client, bridgeTx)
	if err != nil {
		return "", fmt.Errorf("failed to wait for bridge transaction: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return "", fmt.Errorf("bridge transaction failed")
	}

	return bridgeTx.Hash().Hex(), nil
}

// signPermit signs an EIP-2612 permit using EIP-712
func (b *BridgeExecutor) signPermit(
	tokenName string,
	tokenAddress, spender common.Address,
	value, nonce, deadline, chainID *big.Int,
) (uint8, [32]byte, [32]byte, error) {
	// EIP-712 domain
	domainSeparator := crypto.Keccak256Hash(
		crypto.Keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")),
		crypto.Keccak256([]byte(tokenName)),
		crypto.Keccak256([]byte("1")),
		common.LeftPadBytes(chainID.Bytes(), 32),
		common.LeftPadBytes(tokenAddress.Bytes(), 32),
	)

	// Permit type hash
	permitTypeHash := crypto.Keccak256Hash([]byte("Permit(address owner,address spender,uint256 value,uint256 nonce,uint256 deadline)"))

	// Struct hash
	structHash := crypto.Keccak256Hash(
		permitTypeHash.Bytes(),
		common.LeftPadBytes(common.HexToAddress(b.ethWalletAddress).Bytes(), 32),
		common.LeftPadBytes(spender.Bytes(), 32),
		common.LeftPadBytes(value.Bytes(), 32),
		common.LeftPadBytes(nonce.Bytes(), 32),
		common.LeftPadBytes(deadline.Bytes(), 32),
	)

	// Final message hash
	messageHash := crypto.Keccak256Hash(
		[]byte("\x19\x01"),
		domainSeparator.Bytes(),
		structHash.Bytes(),
	)

	// Sign
	sig, err := crypto.Sign(messageHash.Bytes(), b.ethPrivateKey)
	if err != nil {
		return 0, [32]byte{}, [32]byte{}, err
	}

	// Extract r, s, v
	var r, s [32]byte
	copy(r[:], sig[:32])
	copy(s[:], sig[32:64])
	v := sig[64] + 27

	return v, r, s, nil
}

// toBaseUnits converts a human-readable amount to base units (wei, satoshi, etc.)
func toBaseUnits(amount *big.Float, decimals int) *big.Int {
	multiplier := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	result := new(big.Float).Mul(amount, multiplier)
	intResult, _ := result.Int(nil)
	return intResult
}

// fromBaseUnits converts base units to human-readable amount
func fromBaseUnits(amount *big.Int, decimals int) *big.Float {
	divisor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	result := new(big.Float).SetInt(amount)
	return result.Quo(result, divisor)
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

// generateBridgeRequestID generates a unique bridge request ID (UUID v4 format).
func generateBridgeRequestID() (string, error) {
	uuid := make([]byte, 16)
	_, err := rand.Read(uuid)
	if err != nil {
		return "", err
	}

	// Set version (4) and variant (RFC4122)
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4],
		uuid[4:6],
		uuid[6:8],
		uuid[8:10],
		uuid[10:16],
	), nil
}

// signGalaChainTransaction signs a transaction for GalaChain using EIP-712.
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

// signEIP712BridgeRequest signs a bridge request using proper EIP-712 typed data signing.
func (b *BridgeExecutor) signEIP712BridgeRequest(message map[string]interface{}, hasCrossRate bool) (string, error) {
	// Build EIP-712 typed data
	types := getLegacyTypedDataTypes()

	// Convert to apitypes format
	apiTypes := make(apitypes.Types)
	for typeName, fields := range types {
		apiFields := make([]apitypes.Type, len(fields))
		for i, f := range fields {
			apiFields[i] = apitypes.Type{Name: f["name"], Type: f["type"]}
		}
		apiTypes[typeName] = apiFields
	}

	// Add EIP712Domain type
	apiTypes["EIP712Domain"] = []apitypes.Type{
		{Name: "name", Type: "string"},
		{Name: "chainId", Type: "uint256"},
	}

	// Convert message values to proper EIP-712 types
	convertedMessage := convertToEIP712Types(message, apiTypes)

	typedData := apitypes.TypedData{
		Types:       apiTypes,
		PrimaryType: "GalaTransaction",
		Domain: apitypes.TypedDataDomain{
			Name:    "GalaConnect",
			ChainId: (*math.HexOrDecimal256)(big.NewInt(1)),
		},
		Message: convertedMessage,
	}

	// Hash the typed data
	dataHash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return "", fmt.Errorf("failed to hash typed data: %w", err)
	}

	// Sign
	signature, err := crypto.Sign(dataHash, b.galaPrivateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign: %w", err)
	}

	// Adjust v value for Ethereum compatibility (27 or 28)
	if signature[64] < 27 {
		signature[64] += 27
	}

	return "0x" + hex.EncodeToString(signature), nil
}

// convertToEIP712Types recursively converts message values to proper EIP-712 types
func convertToEIP712Types(data map[string]interface{}, types apitypes.Types) apitypes.TypedDataMessage {
	result := make(apitypes.TypedDataMessage)

	for key, value := range data {
		result[key] = convertValue(value, types)
	}

	return result
}

// convertValue converts a single value to proper EIP-712 format
func convertValue(value interface{}, types apitypes.Types) interface{} {
	switch v := value.(type) {
	case int:
		return math.NewHexOrDecimal256(int64(v))
	case int64:
		return math.NewHexOrDecimal256(v)
	case float64:
		// Could be an integer disguised as float (from JSON unmarshaling)
		if v == float64(int64(v)) {
			return math.NewHexOrDecimal256(int64(v))
		}
		// Otherwise treat as string
		return fmt.Sprintf("%v", v)
	case map[string]interface{}:
		// Recursively convert nested maps
		nested := make(apitypes.TypedDataMessage)
		for k, val := range v {
			nested[k] = convertValue(val, types)
		}
		return nested
	case bool:
		return v
	case string:
		return v
	default:
		return value
	}
}

// hashEIP712Domain creates the domain separator hash
func hashEIP712Domain(domain map[string]interface{}) [32]byte {
	// EIP712Domain type hash
	typeHash := crypto.Keccak256Hash([]byte("EIP712Domain(string name,uint256 chainId)"))

	nameHash := crypto.Keccak256Hash([]byte(domain["name"].(string)))

	// Encode chainId as uint256
	chainID := big.NewInt(int64(domain["chainId"].(int)))
	chainIDBytes := make([]byte, 32)
	chainID.FillBytes(chainIDBytes)

	// Combine and hash
	var encoded []byte
	encoded = append(encoded, typeHash[:]...)
	encoded = append(encoded, nameHash[:]...)
	encoded = append(encoded, chainIDBytes...)

	return crypto.Keccak256Hash(encoded)
}

// hashEIP712Message hashes the message according to the GalaTransaction type structure
func hashEIP712Message(message map[string]interface{}, hasCrossRate bool) [32]byte {
	// For simplicity, we hash the JSON representation
	// A full implementation would recursively hash each nested type
	jsonData, _ := json.Marshal(message)
	return crypto.Keccak256Hash(jsonData)
}

// getLegacyTypedDataTypes returns the EIP-712 type definitions for legacy (non-cross-rate) transactions
func getLegacyTypedDataTypes() map[string][]map[string]string {
	return map[string][]map[string]string{
		"GalaTransaction": {
			{"name": "destinationChainId", "type": "uint256"},
			{"name": "destinationChainTxFee", "type": "destinationChainTxFee"},
			{"name": "quantity", "type": "string"},
			{"name": "recipient", "type": "string"},
			{"name": "tokenInstance", "type": "tokenInstance"},
			{"name": "uniqueKey", "type": "string"},
		},
		"destinationChainTxFee": {
			{"name": "bridgeToken", "type": "bridgeToken"},
			{"name": "bridgeTokenIsNonFungible", "type": "bool"},
			{"name": "estimatedPricePerTxFeeUnit", "type": "string"},
			{"name": "estimatedTotalTxFeeInExternalToken", "type": "string"},
			{"name": "estimatedTotalTxFeeInGala", "type": "string"},
			{"name": "estimatedTxFeeUnitsTotal", "type": "string"},
			{"name": "galaDecimals", "type": "uint256"},
			{"name": "galaExchangeRate", "type": "galaExchangeRate"},
			{"name": "timestamp", "type": "uint256"},
			{"name": "signingIdentity", "type": "string"},
			{"name": "signature", "type": "string"},
		},
		"bridgeToken": {
			{"name": "collection", "type": "string"},
			{"name": "category", "type": "string"},
			{"name": "type", "type": "string"},
			{"name": "additionalKey", "type": "string"},
		},
		"galaExchangeRate": {
			{"name": "identity", "type": "string"},
			{"name": "oracle", "type": "string"},
			{"name": "source", "type": "string"},
			{"name": "sourceUrl", "type": "string"},
			{"name": "timestamp", "type": "uint256"},
			{"name": "baseToken", "type": "baseToken"},
			{"name": "exchangeRate", "type": "string"},
			{"name": "externalQuoteToken", "type": "externalQuoteToken"},
		},
		"baseToken": {
			{"name": "collection", "type": "string"},
			{"name": "category", "type": "string"},
			{"name": "type", "type": "string"},
			{"name": "additionalKey", "type": "string"},
			{"name": "instance", "type": "string"},
		},
		"externalQuoteToken": {
			{"name": "name", "type": "string"},
			{"name": "symbol", "type": "string"},
		},
		"tokenInstance": {
			{"name": "collection", "type": "string"},
			{"name": "category", "type": "string"},
			{"name": "type", "type": "string"},
			{"name": "additionalKey", "type": "string"},
			{"name": "instance", "type": "string"},
		},
	}
}

// GetWalletAddresses returns the configured wallet addresses.
func (b *BridgeExecutor) GetWalletAddresses() (galaChain, ethereum string) {
	return b.galaWalletAddress, b.ethWalletAddress
}

// GetGalaChainAddress returns the GalaChain format address.
func (b *BridgeExecutor) GetGalaChainAddress() string {
	return b.galaChainAddress
}

