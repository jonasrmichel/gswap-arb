// Package jupiter provides a client for the Jupiter aggregator API on Solana.
package jupiter

// QuoteParams contains the parameters for requesting a quote from Jupiter.
type QuoteParams struct {
	InputMint   string `json:"inputMint"`           // Input token mint address
	OutputMint  string `json:"outputMint"`          // Output token mint address
	Amount      string `json:"amount"`              // Amount in smallest units (lamports/base units)
	SlippageBps int    `json:"slippageBps"`         // Slippage tolerance in basis points
	SwapMode    string `json:"swapMode,omitempty"`  // "ExactIn" or "ExactOut"
}

// QuoteResponse contains the response from Jupiter's quote API.
type QuoteResponse struct {
	InputMint            string      `json:"inputMint"`
	InAmount             string      `json:"inAmount"`
	OutputMint           string      `json:"outputMint"`
	OutAmount            string      `json:"outAmount"`
	OtherAmountThreshold string      `json:"otherAmountThreshold"`
	SwapMode             string      `json:"swapMode"`
	SlippageBps          int         `json:"slippageBps"`
	PriceImpactPct       string      `json:"priceImpactPct"`
	RoutePlan            []RoutePlan `json:"routePlan"`
	ContextSlot          int64       `json:"contextSlot,omitempty"`
	TimeTaken            float64     `json:"timeTaken,omitempty"`
}

// RoutePlan describes a single step in the swap route.
type RoutePlan struct {
	SwapInfo SwapInfo `json:"swapInfo"`
	Percent  int      `json:"percent"`
}

// SwapInfo contains details about a swap step.
type SwapInfo struct {
	AmmKey     string `json:"ammKey"`
	Label      string `json:"label"`
	InputMint  string `json:"inputMint"`
	OutputMint string `json:"outputMint"`
	InAmount   string `json:"inAmount"`
	OutAmount  string `json:"outAmount"`
	FeeAmount  string `json:"feeAmount"`
	FeeMint    string `json:"feeMint"`
}

// SwapParams contains the parameters for building a swap transaction.
type SwapParams struct {
	QuoteResponse            *QuoteResponse `json:"quoteResponse"`
	UserPublicKey            string         `json:"userPublicKey"`
	WrapAndUnwrapSol         bool           `json:"wrapAndUnwrapSol"`
	DynamicComputeUnitLimit  bool           `json:"dynamicComputeUnitLimit"`
	PrioritizationFeeLamports interface{}   `json:"prioritizationFeeLamports"` // Can be "auto" or int
}

// SwapResponse contains the response from Jupiter's swap API.
type SwapResponse struct {
	SwapTransaction           string `json:"swapTransaction"`           // Base64-encoded transaction
	LastValidBlockHeight      int64  `json:"lastValidBlockHeight"`
	PrioritizationFeeLamports int64  `json:"prioritizationFeeLamports,omitempty"`
	ComputeUnitLimit          int    `json:"computeUnitLimit,omitempty"`
}

// TokenInfo contains information about a Solana token.
type TokenInfo struct {
	Symbol   string
	Mint     string // Base58-encoded mint address
	Decimals int
}

// Well-known Solana token mint addresses (mainnet).
var (
	// Native SOL (wrapped)
	SOLMint = "So11111111111111111111111111111111111111112"

	// Stablecoins
	USDCMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	USDTMint = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"

	// GALA on Solana (wormhole wrapped)
	GALAMint = "GALAxveLUPZLARuXA5WyJQ5ThEyc5T49xF1dN3BJGALA"

	// Popular memecoins
	BONKMint     = "DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263"
	WIFMint      = "EKpQGSJtjMFqKZ9KQanSqYXRcF8fBopzLHYxdM65zcjm"
	POPCATMint   = "7GCihgDB8fe6KNjn2MYtkzZcRjQy3t9GHdC8uHYmW2hr"
	FARTCOINMint = "9BB6NFEcjBCtnNLFko2FqVQBq8HHM13kCyYcdQbgpump"
)

// DefaultTokens returns a map of well-known tokens with their configurations.
func DefaultTokens() map[string]TokenInfo {
	return map[string]TokenInfo{
		"SOL": {
			Symbol:   "SOL",
			Mint:     SOLMint,
			Decimals: 9,
		},
		"USDC": {
			Symbol:   "USDC",
			Mint:     USDCMint,
			Decimals: 6,
		},
		"USDT": {
			Symbol:   "USDT",
			Mint:     USDTMint,
			Decimals: 6,
		},
		"GALA": {
			Symbol:   "GALA",
			Mint:     GALAMint,
			Decimals: 8,
		},
		"BONK": {
			Symbol:   "BONK",
			Mint:     BONKMint,
			Decimals: 5,
		},
		"WIF": {
			Symbol:   "WIF",
			Mint:     WIFMint,
			Decimals: 6,
		},
		"POPCAT": {
			Symbol:   "POPCAT",
			Mint:     POPCATMint,
			Decimals: 9,
		},
		"FARTCOIN": {
			Symbol:   "FARTCOIN",
			Mint:     FARTCOINMint,
			Decimals: 6,
		},
	}
}
