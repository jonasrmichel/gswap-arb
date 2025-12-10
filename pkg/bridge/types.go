// Package bridge provides cross-chain bridge functionality between GalaChain and Ethereum.
package bridge

import (
	"math/big"
	"time"
)

// BridgeDirection represents the direction of a bridge transfer.
type BridgeDirection string

const (
	// BridgeToEthereum transfers tokens from GalaChain to Ethereum
	BridgeToEthereum BridgeDirection = "to_ethereum"
	// BridgeToGalaChain transfers tokens from Ethereum to GalaChain
	BridgeToGalaChain BridgeDirection = "to_galachain"
	// BridgeToSolana transfers tokens from GalaChain to Solana
	BridgeToSolana BridgeDirection = "to_solana"
	// BridgeFromSolana transfers tokens from Solana to GalaChain
	BridgeFromSolana BridgeDirection = "from_solana"
)

// Chain identifiers
const (
	ChainGalaChain = "GC"
	ChainEthereum  = "Ethereum"
	ChainSolana    = "Solana" // GalaConnect API uses "Solana", not "SOL"
)

// BridgeToken holds information about a token for bridging across chains.
type BridgeToken struct {
	Symbol           string // Token symbol (e.g., "GALA", "GWETH")
	Decimals         int    // Token decimals (may vary by chain)
	GalaChainSymbol  string // Corresponding GalaChain symbol

	// GalaChain token class fields
	Collection    string // e.g., "GALA" or "Token"
	Category      string // e.g., "Unit"
	Type          string // e.g., "none" or "BENE"
	AdditionalKey string // e.g., "none" or "client:5c806869e7fd0e2384461ce9"

	// Ethereum-specific fields
	EthereumAddress  string // Ethereum contract address
	BridgeUsesPermit bool   // Whether the Ethereum bridge uses EIP-2612 permit

	// Solana-specific fields
	SolanaMint string // Solana SPL token mint address
}

// EthereumToken holds information about an Ethereum token for bridging.
// Deprecated: Use BridgeToken instead for multi-chain support.
type EthereumToken struct {
	Symbol           string // Token symbol (e.g., "GALA", "GWETH")
	Address          string // Ethereum contract address
	BridgeUsesPermit bool   // Whether the bridge uses EIP-2612 permit
	Decimals         int    // Token decimals on Ethereum
	GalaChainSymbol  string // Corresponding GalaChain symbol (deprecated, use TokenClass)

	// GalaChain token class fields
	Collection    string // e.g., "GALA" or "Token"
	Category      string // e.g., "Unit"
	Type          string // e.g., "none" or "BENE"
	AdditionalKey string // e.g., "none" or "client:5c806869e7fd0e2384461ce9"
}

// SupportedTokens maps token symbols to their Ethereum configuration.
var SupportedTokens = map[string]EthereumToken{
	"GALA": {
		Symbol:           "GALA",
		Address:          "0xd1d2Eb1B1e90B638588728b4130137D262C87cae",
		BridgeUsesPermit: true,
		Decimals:         8,
		GalaChainSymbol:  "GALA",
		Collection:       "GALA",
		Category:         "Unit",
		Type:             "none",
		AdditionalKey:    "none",
	},
	"GWETH": {
		Symbol:           "GWETH",
		Address:          "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", // WETH
		BridgeUsesPermit: false,
		Decimals:         18,
		GalaChainSymbol:  "GWETH",
		Collection:       "GWETH",
		Category:         "Unit",
		Type:             "none",
		AdditionalKey:    "none",
	},
	"GUSDC": {
		Symbol:           "GUSDC",
		Address:          "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", // USDC
		BridgeUsesPermit: false,
		Decimals:         6,
		GalaChainSymbol:  "GUSDC",
		Collection:       "GUSDC",
		Category:         "Unit",
		Type:             "none",
		AdditionalKey:    "none",
	},
	"GUSDT": {
		Symbol:           "GUSDT",
		Address:          "0xdAC17F958D2ee523a2206206994597C13D831ec7", // USDT
		BridgeUsesPermit: false,
		Decimals:         6,
		GalaChainSymbol:  "GUSDT",
		Collection:       "GUSDT",
		Category:         "Unit",
		Type:             "none",
		AdditionalKey:    "none",
	},
	"GWTRX": {
		Symbol:           "GWTRX",
		Address:          "0x50327c6c5a14DCaDE707ABad2E27eB517df87AB5",
		BridgeUsesPermit: false,
		Decimals:         6,
		GalaChainSymbol:  "GWTRX",
		Collection:       "GWTRX",
		Category:         "Unit",
		Type:             "none",
		AdditionalKey:    "none",
	},
	"GWBTC": {
		Symbol:           "GWBTC",
		Address:          "0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599", // WBTC
		BridgeUsesPermit: false,
		Decimals:         8,
		GalaChainSymbol:  "GWBTC",
		Collection:       "GWBTC",
		Category:         "Unit",
		Type:             "none",
		AdditionalKey:    "none",
	},
	"BENE": {
		Symbol:           "BENE",
		Address:          "0x624d739b88429a4cac97c9282adc226620c025d1",
		BridgeUsesPermit: false,
		Decimals:         18,
		GalaChainSymbol:  "BENE",
		Collection:       "Token",
		Category:         "Unit",
		Type:             "BENE",
		AdditionalKey:    "client:5c806869e7fd0e2384461ce9",
	},
	"GMEW": {
		Symbol:           "GMEW",
		Address:          "", // MEW on Solana, no Ethereum contract
		BridgeUsesPermit: false,
		Decimals:         8,
		GalaChainSymbol:  "GMEW",
		Collection:       "GMEW",
		Category:         "Unit",
		Type:             "none",
		AdditionalKey:    "none",
	},
	"GSOL": {
		Symbol:           "GSOL",
		Address:          "", // Wrapped SOL on GalaChain, no Ethereum contract
		BridgeUsesPermit: false,
		Decimals:         9,
		GalaChainSymbol:  "GSOL",
		Collection:       "GSOL",
		Category:         "Unit",
		Type:             "none",
		AdditionalKey:    "none",
	},
}

// SolanaBridgeTokens maps token symbols to their Solana SPL mint addresses.
// These are tokens that can be bridged between GalaChain and Solana.
var SolanaBridgeTokens = map[string]BridgeToken{
	"GALA": {
		Symbol:          "GALA",
		Decimals:        8,
		GalaChainSymbol: "GALA",
		Collection:      "GALA",
		Category:        "Unit",
		Type:            "none",
		AdditionalKey:   "none",
		// GALA on Solana (bridged via GalaConnect)
		SolanaMint:      "eEUiUs4JWYZrp72djAGF1A8PhpR6rHphGeGN7GbVLp6",
		EthereumAddress: "0xd1d2Eb1B1e90B638588728b4130137D262C87cae",
	},
	"GTRUMP": {
		Symbol:          "GTRUMP",
		Decimals:        6,
		GalaChainSymbol: "GTRUMP",
		Collection:      "GTRUMP",
		Category:        "Unit",
		Type:            "none",
		AdditionalKey:   "none",
		// TRUMP on Solana (native)
		SolanaMint: "6p6xgHyF7AeE6TZkSmFsko444wqoP15icUSqi2jfGiPN",
	},
	"GMEW": {
		Symbol:          "GMEW",
		Decimals:        8,
		GalaChainSymbol: "GMEW",
		Collection:      "GMEW",
		Category:        "Unit",
		Type:            "none",
		AdditionalKey:   "none",
		// MEW on Solana (native)
		SolanaMint: "MEW1gQWJ3nEXg2qgERiKu7FAFj79PHvQVREQUzScPP5",
	},
	"GSOL": {
		Symbol:          "GSOL",
		Decimals:        9,
		GalaChainSymbol: "GSOL",
		Collection:      "GSOL",
		Category:        "Unit",
		Type:            "none",
		AdditionalKey:   "none",
		// Native SOL on Solana (wrapped SOL mint address)
		SolanaMint: "So11111111111111111111111111111111111111112",
	},
}

// GetSolanaBridgeToken returns the Solana bridge token configuration for a symbol.
func GetSolanaBridgeToken(symbol string) (*BridgeToken, bool) {
	token, ok := SolanaBridgeTokens[symbol]
	if !ok {
		return nil, false
	}
	return &token, true
}

// GetSolanaSupportedSymbols returns a list of tokens that can be bridged to/from Solana.
func GetSolanaSupportedSymbols() []string {
	symbols := make([]string, 0, len(SolanaBridgeTokens))
	for symbol := range SolanaBridgeTokens {
		symbols = append(symbols, symbol)
	}
	return symbols
}

// BridgeRequest represents a request to bridge tokens.
type BridgeRequest struct {
	Token       string          // Token symbol
	Amount      *big.Float      // Amount to bridge
	Direction   BridgeDirection // Bridge direction
	FromAddress string          // Source address
	ToAddress   string          // Destination address (optional, defaults to same)
}

// BridgeResult represents the result of a bridge operation.
type BridgeResult struct {
	TransactionID   string          // Transaction ID/hash
	Token           string          // Token bridged
	Amount          *big.Float      // Amount bridged
	Direction       BridgeDirection // Bridge direction
	FromAddress     string          // Source address
	ToAddress       string          // Destination address
	Status          BridgeStatus    // Current status
	SourceTxHash    string          // Source chain transaction hash
	DestTxHash      string          // Destination chain transaction hash (when complete)
	Fee             *big.Float      // Bridge fee (if any)
	EstimatedTime   time.Duration   // Estimated completion time
	CreatedAt       time.Time       // When the bridge was initiated
	CompletedAt     time.Time       // When the bridge completed (if done)
	Error           string          // Error message if failed
}

// BridgeStatus represents the status of a bridge operation.
type BridgeStatus string

const (
	BridgeStatusPending   BridgeStatus = "pending"
	BridgeStatusConfirmed BridgeStatus = "confirmed"
	BridgeStatusCompleted BridgeStatus = "completed"
	BridgeStatusFailed    BridgeStatus = "failed"
)

// BridgeConfig holds configuration for the bridge executor.
type BridgeConfig struct {
	// GalaChain configuration
	GalaChainPrivateKey string
	GalaChainAddress    string

	// Ethereum configuration
	EthereumPrivateKey string
	EthereumRPCURL     string

	// Solana configuration
	SolanaPrivateKey      string // Base58 encoded private key
	SolanaWalletAddress   string // Base58 encoded public key
	SolanaRPCURL          string
	SolanaBridgeProgramID string // Gala bridge program ID on Solana

	// Bridge contract addresses
	GalaChainBridgeAPI string
	EthereumBridgeAddr string
}

// DefaultSolanaBridgeProgramID is the default Gala bridge program on Solana mainnet.
const DefaultSolanaBridgeProgramID = "AaE4dTnL75XqgUJpdxBKg6vS9sTJgBPJwBQRVhD29WwS"

// GetTokenBySymbol returns the token configuration for a symbol.
func GetTokenBySymbol(symbol string) (*EthereumToken, bool) {
	token, ok := SupportedTokens[symbol]
	if !ok {
		return nil, false
	}
	return &token, true
}

// GetSupportedSymbols returns a list of all supported token symbols.
func GetSupportedSymbols() []string {
	symbols := make([]string, 0, len(SupportedTokens))
	for symbol := range SupportedTokens {
		symbols = append(symbols, symbol)
	}
	return symbols
}

// Transaction explorer URLs
const (
	// GalaConnect bridge transaction explorer
	GalaConnectBaseURL = "https://connect.gala.com/bridge/transaction/"
	// Etherscan transaction explorer
	EtherscanBaseURL = "https://etherscan.io/tx/"
	// Solana explorer
	SolscanBaseURL = "https://solscan.io/tx/"
)

// GetGalaConnectURL returns the GalaConnect explorer URL for a transaction.
func GetGalaConnectURL(txHash string) string {
	return GalaConnectBaseURL + txHash
}

// GetEtherscanURL returns the Etherscan explorer URL for a transaction.
func GetEtherscanURL(txHash string) string {
	return EtherscanBaseURL + txHash
}

// GetSolscanURL returns the Solscan explorer URL for a Solana transaction.
func GetSolscanURL(txSig string) string {
	return SolscanBaseURL + txSig
}

// GetExplorerLinks returns explorer links for a bridge result.
// Returns (galaConnectURL, etherscanURL) based on available transaction hashes.
// Deprecated: Use GetAllExplorerLinks for Solana support.
func (r *BridgeResult) GetExplorerLinks() (galaConnectURL, etherscanURL string) {
	galaConnectURL, etherscanURL, _ = r.GetAllExplorerLinks()
	return galaConnectURL, etherscanURL
}

// GetAllExplorerLinks returns explorer links for all chains.
// Returns (galaConnectURL, etherscanURL, solscanURL) based on available transaction hashes.
func (r *BridgeResult) GetAllExplorerLinks() (galaConnectURL, etherscanURL, solscanURL string) {
	// For GalaChain → Ethereum bridges, SourceTxHash is the GalaChain tx
	// For Ethereum → GalaChain bridges, SourceTxHash is the Ethereum tx
	// For GalaChain → Solana bridges, SourceTxHash is the GalaChain tx
	// For Solana → GalaChain bridges, SourceTxHash is the Solana tx

	switch r.Direction {
	case BridgeToEthereum:
		// Source is GalaChain, destination is Ethereum
		if r.SourceTxHash != "" {
			galaConnectURL = GetGalaConnectURL(r.SourceTxHash)
		}
		if r.DestTxHash != "" {
			etherscanURL = GetEtherscanURL(r.DestTxHash)
		}
	case BridgeToGalaChain:
		// Source is Ethereum, destination is GalaChain
		if r.SourceTxHash != "" {
			etherscanURL = GetEtherscanURL(r.SourceTxHash)
		}
		if r.DestTxHash != "" {
			galaConnectURL = GetGalaConnectURL(r.DestTxHash)
		}
	case BridgeToSolana:
		// Source is GalaChain, destination is Solana
		if r.SourceTxHash != "" {
			galaConnectURL = GetGalaConnectURL(r.SourceTxHash)
		}
		if r.DestTxHash != "" {
			solscanURL = GetSolscanURL(r.DestTxHash)
		}
	case BridgeFromSolana:
		// Source is Solana, destination is GalaChain
		if r.SourceTxHash != "" {
			solscanURL = GetSolscanURL(r.SourceTxHash)
		}
		if r.DestTxHash != "" {
			galaConnectURL = GetGalaConnectURL(r.DestTxHash)
		}
	default:
		// Unknown direction, try to provide links based on tx hash format
		if r.SourceTxHash != "" {
			if len(r.SourceTxHash) == 66 && r.SourceTxHash[:2] == "0x" {
				etherscanURL = GetEtherscanURL(r.SourceTxHash)
			} else if len(r.SourceTxHash) >= 32 && len(r.SourceTxHash) <= 44 {
				// Base58 encoded Solana signature (typically 87-88 chars) or shorter hash
				solscanURL = GetSolscanURL(r.SourceTxHash)
			} else {
				galaConnectURL = GetGalaConnectURL(r.SourceTxHash)
			}
		}
	}

	return galaConnectURL, etherscanURL, solscanURL
}
