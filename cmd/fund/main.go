// Package main is the entry point for the fund CLI tool.
// Fund allows swapping a source token for multiple target tokens on GSwap.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/executor"
)

var (
	// Swap flags
	sellToken = flag.String("sell", "", "Token to sell (source token, e.g., GALA)")
	buyTokens = flag.String("buy", "", "Comma-separated list of tokens to buy (e.g., GOSMI,BENE,FILM)")
	amount    = flag.Float64("amount", 0, "Total amount of sell token to use (divided equally among buy tokens)")
	dryRun    = flag.Bool("dry-run", false, "Preview swaps without executing")

	// Config flags
	privateKey    = flag.String("private-key", "", "Private key (or use GSWAP_PRIVATE_KEY env var)")
	walletAddress = flag.String("wallet", "", "Wallet address (or use GSWAP_WALLET_ADDRESS env var)")
)

type swapResult struct {
	buyToken  string
	success   bool
	txID      string
	amountIn  *big.Float
	amountOut *big.Float
	err       error
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Fund CLI - Swap tokens on GSwap DEX

Usage:
  fund -sell=<token> -buy=<tokens> -amount=<amount> [flags]

Flags:
  -sell           Token to sell (e.g., GALA)
  -buy            Comma-separated list of tokens to buy (e.g., GOSMI,BENE,FILM)
  -amount         Total amount of sell token to use (divided equally among buy tokens)
  -dry-run        Preview swaps without executing
  -private-key    Wallet private key (or set GSWAP_PRIVATE_KEY)
  -wallet         Wallet address (or set GSWAP_WALLET_ADDRESS)

Examples:
  # Swap 90 GALA equally for GOSMI, BENE, and FILM (30 GALA each)
  fund -sell=GALA -buy=GOSMI,BENE,FILM -amount=90

  # Preview swaps without executing
  fund -sell=GALA -buy=GOSMI,BENE -amount=60 -dry-run

  # Use explicit credentials
  fund -sell=GALA -buy=GWETH -amount=100 -private-key=<key>

Environment Variables:
  GSWAP_PRIVATE_KEY      GalaChain wallet private key
  GSWAP_WALLET_ADDRESS   Wallet address (EIP-55 checksummed)

`)
	}

	flag.Parse()

	// Validate inputs
	if *sellToken == "" {
		fmt.Fprintln(os.Stderr, "Error: -sell is required")
		flag.Usage()
		os.Exit(1)
	}

	if *buyTokens == "" {
		fmt.Fprintln(os.Stderr, "Error: -buy is required")
		flag.Usage()
		os.Exit(1)
	}

	if *amount <= 0 {
		fmt.Fprintln(os.Stderr, "Error: -amount must be positive")
		flag.Usage()
		os.Exit(1)
	}

	// Parse buy tokens
	buyList := parseTokenList(*buyTokens)
	if len(buyList) == 0 {
		fmt.Fprintln(os.Stderr, "Error: -buy must contain at least one token")
		os.Exit(1)
	}

	// Get credentials
	pk := *privateKey
	if pk == "" {
		pk = os.Getenv("GSWAP_PRIVATE_KEY")
	}
	if pk == "" {
		fmt.Fprintln(os.Stderr, "Error: Private key required. Use -private-key or set GSWAP_PRIVATE_KEY")
		os.Exit(1)
	}

	wallet := *walletAddress
	if wallet == "" {
		wallet = os.Getenv("GSWAP_WALLET_ADDRESS")
	}

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nCancelling...")
		cancel()
	}()

	// Create GSwap executor
	gswap, err := executor.NewGSwapExecutor(pk, wallet)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating GSwap executor: %v\n", err)
		os.Exit(1)
	}

	// Initialize executor
	if err := gswap.Initialize(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing GSwap: %v\n", err)
		os.Exit(1)
	}
	defer gswap.Close()

	// Calculate amount per token
	totalAmount := big.NewFloat(*amount)
	numTokens := len(buyList)
	amountPerToken := new(big.Float).Quo(totalAmount, big.NewFloat(float64(numTokens)))

	sellSymbol := strings.ToUpper(*sellToken)

	// Print summary
	fmt.Println("Fund Operation")
	fmt.Println("==============")
	fmt.Printf("Sell Token:    %s\n", sellSymbol)
	fmt.Printf("Buy Tokens:    %s\n", strings.Join(buyList, ", "))
	fmt.Printf("Total Amount:  %.8g %s\n", *amount, sellSymbol)
	fmt.Printf("Per Token:     %.8g %s\n", amountPerTokenFloat(amountPerToken), sellSymbol)
	fmt.Printf("Dry Run:       %v\n", *dryRun)
	fmt.Printf("Wallet:        %s\n", gswap.GetWalletAddress())
	fmt.Println()

	// Check balance
	balance, err := gswap.GetBalance(ctx, sellSymbol)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not check balance: %v\n", err)
	} else {
		fmt.Printf("Current %s balance: %s\n", sellSymbol, formatBalance(balance.Free))
		if balance.Free.Cmp(totalAmount) < 0 {
			fmt.Fprintf(os.Stderr, "Warning: Insufficient balance. Have %.8g, need %.8g\n",
				balanceToFloat(balance.Free), *amount)
		}
		fmt.Println()
	}

	if *dryRun {
		// Preview mode - get quotes
		fmt.Println("Dry Run - Quote Preview")
		fmt.Println("=======================")
		previewSwaps(ctx, gswap, sellSymbol, buyList, amountPerToken)
		return
	}

	// Confirm execution
	fmt.Print("Proceed with swaps? (y/N): ")
	var confirm string
	fmt.Scanln(&confirm)

	if strings.ToLower(confirm) != "y" && strings.ToLower(confirm) != "yes" {
		fmt.Println("Cancelled.")
		return
	}

	fmt.Println()
	fmt.Println("Executing Swaps")
	fmt.Println("===============")

	// Execute swaps sequentially
	results := executeSwaps(ctx, gswap, sellSymbol, buyList, amountPerToken)

	// Print summary
	printResults(results, sellSymbol)
}

func parseTokenList(tokens string) []string {
	var result []string
	for _, t := range strings.Split(tokens, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			result = append(result, strings.ToUpper(t))
		}
	}
	return result
}

func amountPerTokenFloat(amount *big.Float) float64 {
	f, _ := amount.Float64()
	return f
}

func balanceToFloat(balance *big.Float) float64 {
	f, _ := balance.Float64()
	return f
}

func formatBalance(balance *big.Float) string {
	balanceStr := balance.Text('f', 8)
	balanceStr = strings.TrimRight(strings.TrimRight(balanceStr, "0"), ".")
	if balanceStr == "" {
		balanceStr = "0"
	}
	return balanceStr
}

func previewSwaps(ctx context.Context, gswap *executor.GSwapExecutor, sellToken string, buyTokens []string, amountPerToken *big.Float) {
	fmt.Printf("%-10s %-15s %-20s %-15s\n", "Buy Token", "Sell Amount", "Est. Receive", "Rate")
	fmt.Println(strings.Repeat("-", 65))

	for _, buyToken := range buyTokens {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			fmt.Println("\nCancelled.")
			return
		default:
		}

		pair := sellToken + "/" + buyToken
		quote, err := gswap.GetQuote(ctx, pair, executor.OrderSideSell, amountPerToken)

		if err != nil {
			// Try reverse pair
			pair = buyToken + "/" + sellToken
			quote, err = gswap.GetQuote(ctx, pair, executor.OrderSideBuy, amountPerToken)
		}

		if err != nil {
			fmt.Printf("%-10s %-15s %-20s %-15s\n", buyToken, formatBalance(amountPerToken), "Error", err.Error())
			continue
		}

		// Calculate output from quote
		estOutput := new(big.Float).Mul(amountPerToken, quote.Price)
		rate := quote.Price

		fmt.Printf("%-10s %-15s %-20s %-15s\n",
			buyToken,
			formatBalance(amountPerToken)+" "+sellToken,
			formatBalance(estOutput)+" "+buyToken,
			formatBalance(rate))
	}
	fmt.Println()
}

func executeSwaps(ctx context.Context, gswap *executor.GSwapExecutor, sellToken string, buyTokens []string, amountPerToken *big.Float) []swapResult {
	var results []swapResult

	for i, buyToken := range buyTokens {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			fmt.Println("\nCancelled.")
			// Mark remaining as cancelled
			for j := i; j < len(buyTokens); j++ {
				results = append(results, swapResult{
					buyToken: buyTokens[j],
					success:  false,
					err:      ctx.Err(),
				})
			}
			return results
		default:
		}

		fmt.Printf("[%d/%d] Swapping %s %s -> %s... ",
			i+1, len(buyTokens),
			formatBalance(amountPerToken), sellToken, buyToken)

		startTime := time.Now()

		// Try sell order on pair
		pair := sellToken + "/" + buyToken
		order, err := gswap.PlaceMarketOrder(ctx, pair, executor.OrderSideSell, amountPerToken)

		if err != nil {
			// Try reverse pair with buy order
			pair = buyToken + "/" + sellToken
			order, err = gswap.PlaceMarketOrder(ctx, pair, executor.OrderSideBuy, amountPerToken)
		}

		duration := time.Since(startTime)

		if err != nil {
			fmt.Printf("FAILED (%v)\n", err)
			results = append(results, swapResult{
				buyToken: buyToken,
				success:  false,
				amountIn: amountPerToken,
				err:      err,
			})
			continue
		}

		fmt.Printf("OK (tx: %s, %.2fs)\n", truncateTxID(order.ID), duration.Seconds())
		results = append(results, swapResult{
			buyToken:  buyToken,
			success:   true,
			txID:      order.ID,
			amountIn:  order.Amount,
			amountOut: order.FilledAmount,
		})
	}

	return results
}

func truncateTxID(txID string) string {
	if len(txID) <= 16 {
		return txID
	}
	return txID[:8] + "..." + txID[len(txID)-4:]
}

func printResults(results []swapResult, sellToken string) {
	fmt.Println()
	fmt.Println("Results Summary")
	fmt.Println("===============")

	successCount := 0
	failCount := 0
	totalSpent := big.NewFloat(0)

	for _, r := range results {
		if r.success {
			successCount++
			if r.amountIn != nil {
				totalSpent = new(big.Float).Add(totalSpent, r.amountIn)
			}
			fmt.Printf("  ✓ %s: tx=%s\n", r.buyToken, truncateTxID(r.txID))
		} else {
			failCount++
			fmt.Printf("  ✗ %s: %v\n", r.buyToken, r.err)
		}
	}

	fmt.Println()
	fmt.Printf("Successful: %d/%d\n", successCount, len(results))
	fmt.Printf("Failed:     %d/%d\n", failCount, len(results))
	fmt.Printf("Total Spent: %s %s\n", formatBalance(totalSpent), sellToken)
}
