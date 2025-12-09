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
)

// EthereumToken holds information about an Ethereum token for bridging.
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

	// Bridge contract addresses
	GalaChainBridgeAPI string
	EthereumBridgeAddr string
}

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
)

// GetGalaConnectURL returns the GalaConnect explorer URL for a transaction.
func GetGalaConnectURL(txHash string) string {
	return GalaConnectBaseURL + txHash
}

// GetEtherscanURL returns the Etherscan explorer URL for a transaction.
func GetEtherscanURL(txHash string) string {
	return EtherscanBaseURL + txHash
}

// GetExplorerLinks returns explorer links for a bridge result.
// Returns (galaConnectURL, etherscanURL) based on available transaction hashes.
func (r *BridgeResult) GetExplorerLinks() (galaConnectURL, etherscanURL string) {
	// For GalaChain → Ethereum bridges, SourceTxHash is the GalaChain tx
	// For Ethereum → GalaChain bridges, SourceTxHash is the Ethereum tx

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
	default:
		// Unknown direction, try to provide links based on tx hash format
		if r.SourceTxHash != "" {
			if len(r.SourceTxHash) == 66 && r.SourceTxHash[:2] == "0x" {
				etherscanURL = GetEtherscanURL(r.SourceTxHash)
			} else {
				galaConnectURL = GetGalaConnectURL(r.SourceTxHash)
			}
		}
	}

	return galaConnectURL, etherscanURL
}
