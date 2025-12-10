// Package solana provides a client for interacting with the Solana blockchain.
package solana

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Client wraps the Solana RPC client with signing capabilities.
type Client struct {
	rpc        *rpc.Client
	privateKey solana.PrivateKey
	publicKey  solana.PublicKey
	rpcURL     string
}

// ClientConfig contains configuration for the Solana client.
type ClientConfig struct {
	RPCURL        string // Solana RPC endpoint
	PrivateKey    string // Base58 encoded private key
	WalletAddress string // Optional: derived from private key if not provided
}

// NewClient creates a new Solana client with signing capabilities.
func NewClient(config *ClientConfig) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	if config.PrivateKey == "" {
		return nil, fmt.Errorf("private key is required")
	}

	rpcURL := config.RPCURL
	if rpcURL == "" {
		rpcURL = rpc.MainNetBeta_RPC
	}

	// Parse private key from Base58
	privateKey, err := solana.PrivateKeyFromBase58(config.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	publicKey := privateKey.PublicKey()

	// Validate wallet address if provided
	if config.WalletAddress != "" {
		expectedPubKey, err := solana.PublicKeyFromBase58(config.WalletAddress)
		if err != nil {
			return nil, fmt.Errorf("invalid wallet address: %w", err)
		}
		if !publicKey.Equals(expectedPubKey) {
			return nil, fmt.Errorf("wallet address does not match private key: expected %s, got %s",
				publicKey.String(), expectedPubKey.String())
		}
	}

	return &Client{
		rpc:        rpc.New(rpcURL),
		privateKey: privateKey,
		publicKey:  publicKey,
		rpcURL:     rpcURL,
	}, nil
}

// PublicKey returns the wallet's public key.
func (c *Client) PublicKey() solana.PublicKey {
	return c.publicKey
}

// PublicKeyString returns the wallet's public key as a Base58 string.
func (c *Client) PublicKeyString() string {
	return c.publicKey.String()
}

// GetSOLBalance returns the SOL balance in SOL (not lamports).
func (c *Client) GetSOLBalance(ctx context.Context) (*big.Float, error) {
	balance, err := c.rpc.GetBalance(ctx, c.publicKey, rpc.CommitmentFinalized)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	// Convert lamports to SOL (9 decimals)
	solBalance := new(big.Float).SetUint64(balance.Value)
	solBalance.Quo(solBalance, big.NewFloat(1e9))

	return solBalance, nil
}

// GetTokenBalance returns the balance for an SPL token.
func (c *Client) GetTokenBalance(ctx context.Context, mintAddress string) (*big.Float, error) {
	mint, err := solana.PublicKeyFromBase58(mintAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid mint address: %w", err)
	}

	// Get token accounts for this mint
	accounts, err := c.rpc.GetTokenAccountsByOwner(
		ctx,
		c.publicKey,
		&rpc.GetTokenAccountsConfig{
			Mint: &mint,
		},
		&rpc.GetTokenAccountsOpts{
			Encoding: solana.EncodingJSONParsed,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get token accounts: %w", err)
	}

	// Sum up all token account balances
	totalBalance := big.NewFloat(0)
	for _, account := range accounts.Value {
		// The data is in JSON parsed format, extract from raw content
		if account.Account.Data.GetRawJSON() != nil {
			var parsed struct {
				Parsed struct {
					Info struct {
						TokenAmount struct {
							UIAmount float64 `json:"uiAmount"`
						} `json:"tokenAmount"`
					} `json:"info"`
				} `json:"parsed"`
			}
			if err := json.Unmarshal(account.Account.Data.GetRawJSON(), &parsed); err == nil {
				totalBalance.Add(totalBalance, big.NewFloat(parsed.Parsed.Info.TokenAmount.UIAmount))
			}
		}
	}

	return totalBalance, nil
}

// SignAndSendTransaction deserializes a Base64-encoded transaction, signs it, and sends it.
// This handles both legacy and versioned transactions.
func (c *Client) SignAndSendTransaction(ctx context.Context, txBase64 string) (string, error) {
	// Decode the transaction
	txBytes, err := base64.StdEncoding.DecodeString(txBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode transaction: %w", err)
	}

	// Try to parse as versioned transaction first (Jupiter uses versioned transactions)
	tx, err := solana.TransactionFromBytes(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse transaction: %w", err)
	}

	// Sign the transaction
	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if c.publicKey.Equals(key) {
			return &c.privateKey
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Send the transaction
	sig, err := c.rpc.SendTransactionWithOpts(
		ctx,
		tx,
		rpc.TransactionOpts{
			SkipPreflight:       false,
			PreflightCommitment: rpc.CommitmentConfirmed,
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	return sig.String(), nil
}

// SignAndSendVersionedTransaction signs and sends a versioned transaction (V0).
func (c *Client) SignAndSendVersionedTransaction(ctx context.Context, txBase64 string) (string, error) {
	// Decode the transaction
	txBytes, err := base64.StdEncoding.DecodeString(txBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode transaction: %w", err)
	}

	// Parse as versioned transaction
	tx, err := solana.TransactionFromBytes(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse versioned transaction: %w", err)
	}

	// Sign the transaction
	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if c.publicKey.Equals(key) {
			return &c.privateKey
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Send the transaction
	sig, err := c.rpc.SendTransactionWithOpts(
		ctx,
		tx,
		rpc.TransactionOpts{
			SkipPreflight:       false,
			PreflightCommitment: rpc.CommitmentConfirmed,
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	return sig.String(), nil
}

// ConfirmTransaction waits for a transaction to be confirmed.
func (c *Client) ConfirmTransaction(ctx context.Context, signature string) error {
	sig, err := solana.SignatureFromBase58(signature)
	if err != nil {
		return fmt.Errorf("invalid signature: %w", err)
	}

	// Get signature status with confirmation
	statuses, err := c.rpc.GetSignatureStatuses(ctx, false, sig)
	if err != nil {
		return fmt.Errorf("failed to get signature status: %w", err)
	}

	if len(statuses.Value) == 0 || statuses.Value[0] == nil {
		return fmt.Errorf("transaction not found")
	}

	status := statuses.Value[0]
	if status.Err != nil {
		return fmt.Errorf("transaction failed: %v", status.Err)
	}

	// Check confirmation status
	if status.ConfirmationStatus == rpc.ConfirmationStatusFinalized ||
		status.ConfirmationStatus == rpc.ConfirmationStatusConfirmed {
		return nil
	}

	return fmt.Errorf("transaction not yet confirmed: %s", status.ConfirmationStatus)
}

// GetLatestBlockhash returns the latest blockhash.
func (c *Client) GetLatestBlockhash(ctx context.Context) (solana.Hash, error) {
	result, err := c.rpc.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return solana.Hash{}, fmt.Errorf("failed to get blockhash: %w", err)
	}
	return result.Value.Blockhash, nil
}

// GetRPCClient returns the underlying RPC client for advanced operations.
func (c *Client) GetRPCClient() *rpc.Client {
	return c.rpc
}

// IsValidAddress validates a Solana address (Base58 public key).
func IsValidAddress(address string) bool {
	_, err := solana.PublicKeyFromBase58(address)
	return err == nil
}

// Gala bridge program instruction discriminators
var (
	bridgeOutSPLDiscriminator    = []byte{27, 194, 57, 119, 215, 165, 247, 150}
	bridgeOutNativeDiscriminator = []byte{243, 44, 75, 224, 249, 206, 98, 79}
)

// Default compute budget settings for bridge transactions
const (
	defaultComputeUnitLimit           = 200_000
	defaultComputeUnitPriceMicroLamps = 375_000
)

// BridgeToGalaChainParams contains parameters for bridging tokens from Solana to GalaChain.
type BridgeToGalaChainParams struct {
	BridgeProgramID   string // Gala bridge program ID on Solana
	GalaChainIdentity string // GalaChain wallet address (eth|... format)
	TokenMint         string // SPL token mint address (empty for native SOL)
	Amount            uint64 // Amount in base units (lamports for SOL, smallest unit for SPL)
}

// BridgeSPLTokenToGalaChain bridges an SPL token from Solana to GalaChain.
func (c *Client) BridgeSPLTokenToGalaChain(ctx context.Context, params *BridgeToGalaChainParams) (string, error) {
	programID, err := solana.PublicKeyFromBase58(params.BridgeProgramID)
	if err != nil {
		return "", fmt.Errorf("invalid bridge program ID: %w", err)
	}

	mint, err := solana.PublicKeyFromBase58(params.TokenMint)
	if err != nil {
		return "", fmt.Errorf("invalid token mint: %w", err)
	}

	// Derive PDAs
	bridgeTokenAuthority, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("bridge_token_authority")},
		programID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to derive bridge token authority: %w", err)
	}

	configPDA, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("configv1")},
		programID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to derive config PDA: %w", err)
	}

	mintLookup, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("mint_lookup_v1"), mint.Bytes()},
		programID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to derive mint lookup PDA: %w", err)
	}

	// Get mint lookup account to find token bridge PDA
	lookupAccountInfo, err := c.rpc.GetAccountInfo(ctx, mintLookup)
	if err != nil {
		return "", fmt.Errorf("failed to get mint lookup account: %w", err)
	}
	if lookupAccountInfo == nil || lookupAccountInfo.Value == nil {
		return "", fmt.Errorf("mint lookup account not found for %s", params.TokenMint)
	}

	lookupData := lookupAccountInfo.Value.Data.GetBinary()
	if len(lookupData) < 40 {
		return "", fmt.Errorf("mint lookup account data too short")
	}
	tokenBridge := solana.PublicKeyFromBytes(lookupData[8:40])

	// Derive token accounts
	userTokenAccount, _, err := solana.FindAssociatedTokenAddress(c.publicKey, mint)
	if err != nil {
		return "", fmt.Errorf("failed to derive user token account: %w", err)
	}

	bridgeTokenAccount, _, err := solana.FindAssociatedTokenAddress(bridgeTokenAuthority, mint)
	if err != nil {
		return "", fmt.Errorf("failed to derive bridge token account: %w", err)
	}

	// Build instruction data
	recipientBytes := []byte(params.GalaChainIdentity)
	instructionData := make([]byte, 0, 8+8+4+len(recipientBytes))
	instructionData = append(instructionData, bridgeOutSPLDiscriminator...)

	// Amount (u64, little endian)
	amountBytes := make([]byte, 8)
	for i := 0; i < 8; i++ {
		amountBytes[i] = byte(params.Amount >> (i * 8))
	}
	instructionData = append(instructionData, amountBytes...)

	// Recipient length (u32, little endian)
	recipientLen := uint32(len(recipientBytes))
	lenBytes := make([]byte, 4)
	for i := 0; i < 4; i++ {
		lenBytes[i] = byte(recipientLen >> (i * 8))
	}
	instructionData = append(instructionData, lenBytes...)
	instructionData = append(instructionData, recipientBytes...)

	// Build instruction
	instruction := solana.NewInstruction(
		programID,
		solana.AccountMetaSlice{
			{PublicKey: c.publicKey, IsSigner: true, IsWritable: true},
			{PublicKey: userTokenAccount, IsSigner: false, IsWritable: true},
			{PublicKey: mint, IsSigner: false, IsWritable: true},
			{PublicKey: mintLookup, IsSigner: false, IsWritable: false},
			{PublicKey: tokenBridge, IsSigner: false, IsWritable: false},
			{PublicKey: bridgeTokenAccount, IsSigner: false, IsWritable: true},
			{PublicKey: bridgeTokenAuthority, IsSigner: false, IsWritable: false},
			{PublicKey: configPDA, IsSigner: false, IsWritable: true},
			{PublicKey: solana.SystemProgramID, IsSigner: false, IsWritable: false},
			{PublicKey: solana.TokenProgramID, IsSigner: false, IsWritable: false},
		},
		instructionData,
	)

	// Get recent blockhash
	blockhashResult, err := c.rpc.GetLatestBlockhash(ctx, rpc.CommitmentConfirmed)
	if err != nil {
		return "", fmt.Errorf("failed to get blockhash: %w", err)
	}

	// Build transaction with compute budget
	tx, err := solana.NewTransaction(
		[]solana.Instruction{
			solana.NewInstruction(
				solana.MustPublicKeyFromBase58("ComputeBudget111111111111111111111111111111"),
				nil,
				buildSetComputeUnitPrice(defaultComputeUnitPriceMicroLamps),
			),
			solana.NewInstruction(
				solana.MustPublicKeyFromBase58("ComputeBudget111111111111111111111111111111"),
				nil,
				buildSetComputeUnitLimit(defaultComputeUnitLimit),
			),
			instruction,
		},
		blockhashResult.Value.Blockhash,
		solana.TransactionPayer(c.publicKey),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create transaction: %w", err)
	}

	// Sign transaction
	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if c.publicKey.Equals(key) {
			return &c.privateKey
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Send transaction
	sig, err := c.rpc.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{
		SkipPreflight:       false,
		PreflightCommitment: rpc.CommitmentConfirmed,
	})
	if err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	// Wait for confirmation
	_, err = c.rpc.GetLatestBlockhash(ctx, rpc.CommitmentConfirmed)
	if err != nil {
		return "", fmt.Errorf("failed to get blockhash for confirmation: %w", err)
	}

	return sig.String(), nil
}

// BridgeNativeSOLToGalaChain bridges native SOL from Solana to GalaChain.
func (c *Client) BridgeNativeSOLToGalaChain(ctx context.Context, params *BridgeToGalaChainParams) (string, error) {
	programID, err := solana.PublicKeyFromBase58(params.BridgeProgramID)
	if err != nil {
		return "", fmt.Errorf("invalid bridge program ID: %w", err)
	}

	// Derive PDAs
	bridgeTokenAuthority, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("bridge_token_authority")},
		programID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to derive bridge token authority: %w", err)
	}

	nativeBridgePDA, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("native_sol_bridge")},
		programID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to derive native bridge PDA: %w", err)
	}

	configPDA, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("configv1")},
		programID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to derive config PDA: %w", err)
	}

	// Build instruction data
	recipientBytes := []byte(params.GalaChainIdentity)
	instructionData := make([]byte, 0, 8+8+4+len(recipientBytes))
	instructionData = append(instructionData, bridgeOutNativeDiscriminator...)

	// Amount (u64, little endian)
	amountBytes := make([]byte, 8)
	for i := 0; i < 8; i++ {
		amountBytes[i] = byte(params.Amount >> (i * 8))
	}
	instructionData = append(instructionData, amountBytes...)

	// Recipient length (u32, little endian)
	recipientLen := uint32(len(recipientBytes))
	lenBytes := make([]byte, 4)
	for i := 0; i < 4; i++ {
		lenBytes[i] = byte(recipientLen >> (i * 8))
	}
	instructionData = append(instructionData, lenBytes...)
	instructionData = append(instructionData, recipientBytes...)

	// Build instruction
	instruction := solana.NewInstruction(
		programID,
		solana.AccountMetaSlice{
			{PublicKey: c.publicKey, IsSigner: true, IsWritable: true},
			{PublicKey: bridgeTokenAuthority, IsSigner: false, IsWritable: true},
			{PublicKey: nativeBridgePDA, IsSigner: false, IsWritable: false},
			{PublicKey: configPDA, IsSigner: false, IsWritable: true},
			{PublicKey: solana.SystemProgramID, IsSigner: false, IsWritable: false},
		},
		instructionData,
	)

	// Get recent blockhash
	blockhashResult, err := c.rpc.GetLatestBlockhash(ctx, rpc.CommitmentConfirmed)
	if err != nil {
		return "", fmt.Errorf("failed to get blockhash: %w", err)
	}

	// Build transaction with compute budget
	tx, err := solana.NewTransaction(
		[]solana.Instruction{
			solana.NewInstruction(
				solana.MustPublicKeyFromBase58("ComputeBudget111111111111111111111111111111"),
				nil,
				buildSetComputeUnitPrice(defaultComputeUnitPriceMicroLamps),
			),
			solana.NewInstruction(
				solana.MustPublicKeyFromBase58("ComputeBudget111111111111111111111111111111"),
				nil,
				buildSetComputeUnitLimit(defaultComputeUnitLimit),
			),
			instruction,
		},
		blockhashResult.Value.Blockhash,
		solana.TransactionPayer(c.publicKey),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create transaction: %w", err)
	}

	// Sign transaction
	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if c.publicKey.Equals(key) {
			return &c.privateKey
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Send transaction
	sig, err := c.rpc.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{
		SkipPreflight:       false,
		PreflightCommitment: rpc.CommitmentConfirmed,
	})
	if err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	return sig.String(), nil
}

// buildSetComputeUnitPrice builds the instruction data for SetComputeUnitPrice
func buildSetComputeUnitPrice(microLamports uint64) []byte {
	data := make([]byte, 9)
	data[0] = 3 // SetComputeUnitPrice instruction
	for i := 0; i < 8; i++ {
		data[1+i] = byte(microLamports >> (i * 8))
	}
	return data
}

// buildSetComputeUnitLimit builds the instruction data for SetComputeUnitLimit
func buildSetComputeUnitLimit(units uint32) []byte {
	data := make([]byte, 5)
	data[0] = 2 // SetComputeUnitLimit instruction
	for i := 0; i < 4; i++ {
		data[1+i] = byte(units >> (i * 8))
	}
	return data
}
