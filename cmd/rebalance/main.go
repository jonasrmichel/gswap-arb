// Package main is the entry point for the rebalance CLI tool.
// This tool provides semi-automated inventory rebalancing between GalaChain and Ethereum.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/bridge"
	"github.com/jonasrmichel/gswap-arb/pkg/config"
	"github.com/jonasrmichel/gswap-arb/pkg/executor"
	"github.com/jonasrmichel/gswap-arb/pkg/inventory"
)

var (
	// Operation modes
	checkOnly      = flag.Bool("check", false, "Only check balances and drift, don't prompt for rebalancing")
	autoRecommend  = flag.Bool("recommend", false, "Generate rebalance recommendations")
	executeRebal   = flag.Bool("execute", false, "Execute a specific rebalance (requires --token, --from, --to, --amount)")

	// Rebalance parameters (for --execute mode)
	token      = flag.String("token", "", "Token to rebalance")
	fromExch   = flag.String("from", "", "Source exchange (gswap or ethereum)")
	toExch     = flag.String("to", "", "Destination exchange (gswap or ethereum)")
	amount     = flag.String("amount", "", "Amount to bridge")
	waitCompletion = flag.Bool("wait", false, "Wait for bridge completion and verify balances")
	maxWaitMins    = flag.Int("max-wait", 30, "Maximum minutes to wait for bridge completion")

	// Configuration
	driftThreshold = flag.Float64("drift-threshold", 20.0, "Drift threshold percentage for recommendations")
	privateKey     = flag.String("private-key", "", "Private key (or use GSWAP_PRIVATE_KEY env var)")
	ethRPC         = flag.String("eth-rpc", "", "Ethereum RPC URL (or use ETH_RPC_URL env var)")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Rebalance CLI - Semi-automated inventory rebalancing

Usage:
  rebalance [flags]

Modes:
  --check          Check current balances and drift status (default if no mode specified)
  --recommend      Generate rebalance recommendations based on drift
  --execute        Execute a specific rebalance operation

Check Mode:
  rebalance --check
  Shows current balances across exchanges and drift from target allocations.

Recommend Mode:
  rebalance --recommend [--drift-threshold 20.0]
  Analyzes inventory and suggests bridge operations to rebalance.

Execute Mode:
  rebalance --execute --token GALA --from gswap --to ethereum --amount 100
  rebalance --execute --token GALA --from gswap --to ethereum --amount 100 --wait
  Executes a bridge operation after confirmation. Use --wait to monitor completion.

Configuration:
  --private-key    Wallet private key (or set GSWAP_PRIVATE_KEY)
  --eth-rpc        Ethereum RPC URL (or set ETH_RPC_URL)
  --drift-threshold  Percentage drift to trigger recommendations (default: 20%%)

Examples:
  # Check current inventory status
  rebalance --check

  # Get rebalance recommendations
  rebalance --recommend

  # Execute a rebalance (bridge 100 GALA from GSwap to Ethereum)
  rebalance --execute --token GALA --from gswap --to ethereum --amount 100

Environment Variables:
  GSWAP_PRIVATE_KEY    Wallet private key
  ETH_RPC_URL          Ethereum RPC endpoint

`)
	}

	flag.Parse()

	// Default to check mode if no mode specified
	if !*checkOnly && !*autoRecommend && !*executeRebal {
		*checkOnly = true
	}

	// Get private key
	pk := *privateKey
	if pk == "" {
		pk = os.Getenv("GSWAP_PRIVATE_KEY")
	}

	if pk == "" {
		fmt.Fprintln(os.Stderr, "Error: Private key required. Use --private-key or set GSWAP_PRIVATE_KEY")
		os.Exit(1)
	}

	// Get Ethereum RPC URL
	ethRPCURL := *ethRPC
	if ethRPCURL == "" {
		ethRPCURL = os.Getenv("ETH_RPC_URL")
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

	// Create bridge executor
	bridgeExec, err := bridge.NewBridgeExecutor(&bridge.BridgeConfig{
		GalaChainPrivateKey: pk,
		EthereumPrivateKey:  pk,
		EthereumRPCURL:      ethRPCURL,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating bridge executor: %v\n", err)
		os.Exit(1)
	}

	// Load config for exchange setup
	cfg := config.LoadFromEnv()

	// Create executor registry for balance queries
	registry := executor.NewExecutorRegistry()
	if err := setupExecutors(ctx, registry, cfg, pk); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	// Create inventory manager
	invConfig := inventory.DefaultInventoryConfig()
	invConfig.DriftThresholdPct = *driftThreshold
	invManager := inventory.NewManager(registry, invConfig)

	// Execute based on mode
	switch {
	case *executeRebal:
		executeRebalance(ctx, bridgeExec, invManager)
	case *autoRecommend:
		showRecommendations(ctx, bridgeExec, invManager)
	default:
		showInventoryStatus(ctx, bridgeExec, invManager)
	}
}

// showInventoryStatus displays current balances and drift status.
func showInventoryStatus(ctx context.Context, bridgeExec *bridge.BridgeExecutor, invManager *inventory.Manager) {
	fmt.Println("Inventory Status Check")
	fmt.Println("======================")
	fmt.Println()

	// Show wallet addresses
	galaAddr, ethAddr := bridgeExec.GetWalletAddresses()
	fmt.Println("Wallet Addresses:")
	fmt.Printf("  GalaChain: %s\n", bridgeExec.GetGalaChainAddress())
	fmt.Printf("  Ethereum:  %s\n", ethAddr)
	fmt.Println()

	// Collect balances
	fmt.Println("Collecting balances from exchanges...")

	// Get GalaChain balances via bridge executor
	fmt.Println("\nGalaChain Balances:")
	fmt.Println("-------------------")
	printGalaChainBalances(ctx, bridgeExec)

	// Get Ethereum balances
	if bridgeExec.GetEthereumRPCConfigured() {
		fmt.Println("\nEthereum Balances:")
		fmt.Println("------------------")
		printEthereumBalances(ctx, bridgeExec)
	}

	// Try to collect from executor registry and show drift
	snapshot, err := invManager.CollectSnapshot(ctx)
	if err != nil {
		fmt.Printf("\nNote: Could not collect full inventory snapshot: %v\n", err)
		return
	}

	if snapshot != nil && len(snapshot.Exchanges) > 0 {
		fmt.Println("\nDrift Analysis:")
		fmt.Println("---------------")
		driftStatus := invManager.CheckDrift(ctx)
		if len(driftStatus) == 0 {
			fmt.Println("No drift data available (need balances on multiple exchanges)")
		} else {
			fmt.Println(invManager.FormatDriftReport())
		}
	}

	_ = galaAddr // Silence unused variable
}

// printGalaChainBalances prints GalaChain token balances.
func printGalaChainBalances(ctx context.Context, bridgeExec *bridge.BridgeExecutor) {
	symbols := bridge.GetSupportedSymbols()
	sort.Strings(symbols)

	fmt.Printf("%-10s %20s\n", "Token", "Balance")
	fmt.Println(strings.Repeat("-", 32))

	for _, symbol := range symbols {
		balance, err := bridgeExec.GetGalaChainBalance(ctx, symbol)
		if err != nil {
			fmt.Printf("%-10s %20s\n", symbol, "Error")
			continue
		}
		if balance.Sign() > 0 {
			fmt.Printf("%-10s %20s\n", symbol, formatBalance(balance))
		}
	}
}

// printEthereumBalances prints Ethereum token balances.
func printEthereumBalances(ctx context.Context, bridgeExec *bridge.BridgeExecutor) {
	// Print ETH first
	fmt.Printf("%-10s %20s\n", "Token", "Balance")
	fmt.Println(strings.Repeat("-", 32))

	ethBalance, err := bridgeExec.GetEthereumBalance(ctx, "ETH")
	if err != nil {
		fmt.Printf("%-10s %20s\n", "ETH", "Error")
	} else {
		fmt.Printf("%-10s %20s\n", "ETH", formatBalance(ethBalance))
	}

	// Print ERC-20 tokens
	symbols := bridge.GetSupportedSymbols()
	sort.Strings(symbols)

	for _, symbol := range symbols {
		balance, err := bridgeExec.GetEthereumBalance(ctx, symbol)
		if err != nil {
			continue // Skip errors silently for tokens
		}
		if balance.Sign() > 0 {
			fmt.Printf("%-10s %20s\n", symbol, formatBalance(balance))
		}
	}
}

// showRecommendations generates and displays rebalance recommendations.
func showRecommendations(ctx context.Context, bridgeExec *bridge.BridgeExecutor, invManager *inventory.Manager) {
	fmt.Println("Rebalance Recommendations")
	fmt.Println("=========================")
	fmt.Println()

	// First show current status
	showInventoryStatus(ctx, bridgeExec, invManager)

	// Generate recommendations
	fmt.Println("\nGenerating recommendations...")
	recommendations := invManager.GenerateRebalanceRecommendations()

	if len(recommendations) == 0 {
		fmt.Println("\nNo rebalancing needed - inventory is within acceptable drift thresholds.")
		return
	}

	fmt.Println()
	fmt.Println(invManager.FormatRecommendationsReport(recommendations))

	// Offer to execute
	fmt.Println("To execute a recommendation, run:")
	for _, rec := range recommendations {
		direction := "to-eth"
		if rec.ToExchange == "gswap" {
			direction = "to-gala"
		}
		fmt.Printf("  rebalance --execute --token %s --from %s --to %s --amount %s\n",
			rec.Currency, rec.FromExchange, rec.ToExchange, rec.Amount.Text('f', 4))
		fmt.Printf("  # Or use bridge CLI: bridge --direction %s --token %s --amount %s\n",
			direction, rec.Currency, rec.Amount.Text('f', 4))
		fmt.Println()
	}
}

// executeRebalance executes a specific rebalance operation.
func executeRebalance(ctx context.Context, bridgeExec *bridge.BridgeExecutor, invManager *inventory.Manager) {
	// Validate required parameters
	if *token == "" {
		fmt.Fprintln(os.Stderr, "Error: --token is required")
		os.Exit(1)
	}
	if *fromExch == "" {
		fmt.Fprintln(os.Stderr, "Error: --from is required (gswap or ethereum)")
		os.Exit(1)
	}
	if *toExch == "" {
		fmt.Fprintln(os.Stderr, "Error: --to is required (gswap or ethereum)")
		os.Exit(1)
	}
	if *amount == "" {
		fmt.Fprintln(os.Stderr, "Error: --amount is required")
		os.Exit(1)
	}

	// Parse amount
	amountVal := new(big.Float)
	_, ok := amountVal.SetString(*amount)
	if !ok || amountVal.Sign() <= 0 {
		fmt.Fprintf(os.Stderr, "Error: Invalid amount '%s'\n", *amount)
		os.Exit(1)
	}

	// Determine bridge direction
	var direction bridge.BridgeDirection
	fromLower := strings.ToLower(*fromExch)
	toLower := strings.ToLower(*toExch)

	if (fromLower == "gswap" || fromLower == "galachain") && (toLower == "ethereum" || toLower == "eth") {
		direction = bridge.BridgeToEthereum
	} else if (fromLower == "ethereum" || fromLower == "eth") && (toLower == "gswap" || toLower == "galachain") {
		direction = bridge.BridgeToGalaChain
	} else {
		fmt.Fprintf(os.Stderr, "Error: Invalid exchange pair. Use 'gswap' and 'ethereum'\n")
		os.Exit(1)
	}

	// Validate token
	tokenUpper := strings.ToUpper(*token)
	tokenInfo, ok := bridge.GetTokenBySymbol(tokenUpper)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: Unsupported token '%s'\n", *token)
		os.Exit(1)
	}

	// Show current balances for the token
	fmt.Println("Rebalance Operation")
	fmt.Println("===================")
	fmt.Println()

	fmt.Printf("Token:     %s\n", tokenUpper)
	fmt.Printf("Amount:    %s\n", amountVal.Text('f', 8))
	fmt.Printf("From:      %s\n", *fromExch)
	fmt.Printf("To:        %s\n", *toExch)
	fmt.Printf("Direction: %s\n", direction)
	fmt.Println()

	// Show current balances
	fmt.Println("Current Balances:")
	galaBalance, err := bridgeExec.GetGalaChainBalance(ctx, tokenUpper)
	if err != nil {
		fmt.Printf("  GalaChain %s: Error - %v\n", tokenUpper, err)
	} else {
		fmt.Printf("  GalaChain %s: %s\n", tokenUpper, formatBalance(galaBalance))
	}

	if bridgeExec.GetEthereumRPCConfigured() {
		ethBalance, err := bridgeExec.GetEthereumBalance(ctx, tokenUpper)
		if err != nil {
			fmt.Printf("  Ethereum %s:  Error - %v\n", tokenUpper, err)
		} else {
			fmt.Printf("  Ethereum %s:  %s\n", tokenUpper, formatBalance(ethBalance))
		}
	}
	fmt.Println()

	// Check sufficient balance on source
	var sourceBalance *big.Float
	if direction == bridge.BridgeToEthereum {
		sourceBalance = galaBalance
	} else {
		sourceBalance, _ = bridgeExec.GetEthereumBalance(ctx, tokenUpper)
	}

	if sourceBalance != nil && sourceBalance.Cmp(amountVal) < 0 {
		fmt.Printf("Warning: Insufficient balance on source. Have %s, want to bridge %s\n",
			formatBalance(sourceBalance), amountVal.Text('f', 8))
	}

	// Show expected result
	fmt.Println("Expected Result After Bridge:")
	if direction == bridge.BridgeToEthereum {
		newGala := new(big.Float).Sub(galaBalance, amountVal)
		ethBal, _ := bridgeExec.GetEthereumBalance(ctx, tokenUpper)
		if ethBal == nil {
			ethBal = big.NewFloat(0)
		}
		newEth := new(big.Float).Add(ethBal, amountVal)
		fmt.Printf("  GalaChain %s: %s -> %s\n", tokenUpper, formatBalance(galaBalance), formatBalance(newGala))
		fmt.Printf("  Ethereum %s:  %s -> %s\n", tokenUpper, formatBalance(ethBal), formatBalance(newEth))
	} else {
		ethBal, _ := bridgeExec.GetEthereumBalance(ctx, tokenUpper)
		if ethBal == nil {
			ethBal = big.NewFloat(0)
		}
		newEth := new(big.Float).Sub(ethBal, amountVal)
		newGala := new(big.Float).Add(galaBalance, amountVal)
		fmt.Printf("  Ethereum %s:  %s -> %s\n", tokenUpper, formatBalance(ethBal), formatBalance(newEth))
		fmt.Printf("  GalaChain %s: %s -> %s\n", tokenUpper, formatBalance(galaBalance), formatBalance(newGala))
	}
	fmt.Println()

	// Confirm execution
	fmt.Printf("Contract: %s\n", tokenInfo.Address)
	fmt.Println()
	fmt.Print("Proceed with rebalance? (y/N): ")

	reader := bufio.NewReader(os.Stdin)
	confirm, _ := reader.ReadString('\n')
	confirm = strings.TrimSpace(strings.ToLower(confirm))

	if confirm != "y" && confirm != "yes" {
		fmt.Println("Cancelled.")
		return
	}

	// Execute bridge
	fmt.Println()
	fmt.Println("Initiating bridge...")

	req := &bridge.BridgeRequest{
		Token:     tokenUpper,
		Amount:    amountVal,
		Direction: direction,
	}

	startTime := time.Now()
	result, err := bridgeExec.Bridge(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Bridge failed: %v\n", err)
		os.Exit(1)
	}

	// Print result
	fmt.Println()
	fmt.Println("Bridge Initiated")
	fmt.Println("================")
	fmt.Printf("Transaction ID:  %s\n", result.TransactionID)
	fmt.Printf("Status:          %s\n", result.Status)
	fmt.Printf("Token:           %s\n", result.Token)
	fmt.Printf("Amount:          %s\n", result.Amount.Text('f', 8))
	fmt.Printf("From:            %s\n", result.FromAddress)
	fmt.Printf("To:              %s\n", result.ToAddress)
	fmt.Printf("Estimated Time:  %s\n", result.EstimatedTime)
	fmt.Printf("Duration:        %s\n", time.Since(startTime).Round(time.Millisecond))

	// Show explorer links
	galaConnectURL, etherscanURL := result.GetExplorerLinks()
	if galaConnectURL != "" || etherscanURL != "" {
		fmt.Println()
		fmt.Println("Explorer Links:")
		if galaConnectURL != "" {
			fmt.Printf("  GalaConnect: %s\n", galaConnectURL)
		}
		if etherscanURL != "" {
			fmt.Printf("  Etherscan:   %s\n", etherscanURL)
		}
	}

	fmt.Println()

	// Wait for completion if requested
	if *waitCompletion {
		waitForBridgeCompletion(ctx, bridgeExec, result, tokenUpper, amountVal, direction)
	} else {
		fmt.Printf("Use 'bridge --status %s' to check the bridge status.\n", result.TransactionID)
		fmt.Println()
		fmt.Println("Note: Bridge operations typically take 10-30 minutes to complete.")
		fmt.Println("Run 'rebalance --check' after completion to verify new balances.")
	}
}

// waitForBridgeCompletion polls for bridge completion and verifies final balances.
func waitForBridgeCompletion(ctx context.Context, bridgeExec *bridge.BridgeExecutor, result *bridge.BridgeResult, token string, amount *big.Float, direction bridge.BridgeDirection) {
	fmt.Println("Waiting for bridge completion...")
	fmt.Printf("(Maximum wait time: %d minutes)\n", *maxWaitMins)
	fmt.Println()

	maxWait := time.Duration(*maxWaitMins) * time.Minute
	pollInterval := 30 * time.Second
	startTime := time.Now()

	// Get initial balances for comparison
	var initialSourceBal, initialDestBal *big.Float
	if direction == bridge.BridgeToEthereum {
		initialSourceBal, _ = bridgeExec.GetGalaChainBalance(ctx, token)
		initialDestBal, _ = bridgeExec.GetEthereumBalance(ctx, token)
	} else {
		initialSourceBal, _ = bridgeExec.GetEthereumBalance(ctx, token)
		initialDestBal, _ = bridgeExec.GetGalaChainBalance(ctx, token)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	checkCount := 0
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nWait cancelled.")
			return

		case <-ticker.C:
			checkCount++
			elapsed := time.Since(startTime).Round(time.Second)
			fmt.Printf("[%s] Check #%d - ", elapsed, checkCount)

			// Check bridge status
			status, err := bridgeExec.GetBridgeStatus(ctx, result.TransactionID)
			if err != nil {
				fmt.Printf("Status check error: %v\n", err)
			} else {
				fmt.Printf("Status: %s\n", status.Status)

				if status.Status == bridge.BridgeStatusCompleted {
					fmt.Println()
					fmt.Println("Bridge completed!")
					verifyFinalBalances(ctx, bridgeExec, token, amount, direction, initialSourceBal, initialDestBal)
					return
				}

				if status.Status == bridge.BridgeStatusFailed {
					fmt.Println()
					fmt.Println("Bridge FAILED!")
					if status.Error != "" {
						fmt.Printf("Error: %s\n", status.Error)
					}
					return
				}
			}

			// Check if destination balance increased (alternative completion detection)
			var currentDestBal *big.Float
			if direction == bridge.BridgeToEthereum {
				currentDestBal, _ = bridgeExec.GetEthereumBalance(ctx, token)
			} else {
				currentDestBal, _ = bridgeExec.GetGalaChainBalance(ctx, token)
			}

			if currentDestBal != nil && initialDestBal != nil {
				diff := new(big.Float).Sub(currentDestBal, initialDestBal)
				// If destination increased by at least 90% of expected amount, consider complete
				threshold := new(big.Float).Mul(amount, big.NewFloat(0.9))
				if diff.Cmp(threshold) >= 0 {
					fmt.Println()
					fmt.Println("Destination balance increased - bridge appears complete!")
					verifyFinalBalances(ctx, bridgeExec, token, amount, direction, initialSourceBal, initialDestBal)
					return
				}
			}

			// Check timeout
			if time.Since(startTime) >= maxWait {
				fmt.Println()
				fmt.Printf("Maximum wait time (%d minutes) reached.\n", *maxWaitMins)
				fmt.Println("Bridge may still be in progress. Check status manually:")
				fmt.Printf("  bridge --status %s\n", result.TransactionID)
				return
			}
		}
	}
}

// verifyFinalBalances shows the final balances after bridge completion.
func verifyFinalBalances(ctx context.Context, bridgeExec *bridge.BridgeExecutor, token string, amount *big.Float, direction bridge.BridgeDirection, initialSourceBal, initialDestBal *big.Float) {
	fmt.Println()
	fmt.Println("Final Balance Verification")
	fmt.Println("==========================")

	// Get current balances
	galaBalance, _ := bridgeExec.GetGalaChainBalance(ctx, token)
	ethBalance, _ := bridgeExec.GetEthereumBalance(ctx, token)

	fmt.Printf("\n%-12s %18s %18s %18s\n", "Location", "Before", "After", "Change")
	fmt.Println(strings.Repeat("-", 70))

	if direction == bridge.BridgeToEthereum {
		galaDiff := new(big.Float).Sub(galaBalance, initialSourceBal)
		ethDiff := new(big.Float).Sub(ethBalance, initialDestBal)

		fmt.Printf("%-12s %18s %18s %+18s\n", "GalaChain", formatBalance(initialSourceBal), formatBalance(galaBalance), formatBalance(galaDiff))
		fmt.Printf("%-12s %18s %18s %+18s\n", "Ethereum", formatBalance(initialDestBal), formatBalance(ethBalance), formatBalance(ethDiff))
	} else {
		ethDiff := new(big.Float).Sub(ethBalance, initialSourceBal)
		galaDiff := new(big.Float).Sub(galaBalance, initialDestBal)

		fmt.Printf("%-12s %18s %18s %+18s\n", "Ethereum", formatBalance(initialSourceBal), formatBalance(ethBalance), formatBalance(ethDiff))
		fmt.Printf("%-12s %18s %18s %+18s\n", "GalaChain", formatBalance(initialDestBal), formatBalance(galaBalance), formatBalance(galaDiff))
	}

	fmt.Println()
	fmt.Println("Rebalance complete!")
}

// setupExecutors initializes trade executors for balance queries.
func setupExecutors(ctx context.Context, registry *executor.ExecutorRegistry, cfg *config.Config, pk string) error {
	var lastErr error

	for _, ex := range cfg.GetEnabledExchanges() {
		// GSwap executor
		if ex.ID == "gswap" {
			privKey := ex.PrivateKey
			if privKey == "" {
				privKey = pk
			}
			if privKey == "" {
				continue
			}
			gswapExec, err := executor.NewGSwapExecutor(privKey, ex.WalletAddress)
			if err != nil {
				lastErr = err
				continue
			}
			if err := gswapExec.Initialize(ctx); err != nil {
				lastErr = err
				continue
			}
			registry.Register(gswapExec)
			continue
		}

		// CCXT executors for CEX
		if executor.SupportedCCXTExchanges[strings.ToLower(ex.ID)] {
			if ex.APIKey == "" || ex.Secret == "" {
				continue
			}

			ccxtConfig := &executor.CCXTConfig{
				ExchangeID: ex.ID,
				APIKey:     ex.APIKey,
				Secret:     ex.Secret,
				Password:   ex.Passphrase,
				Sandbox:    false,
			}

			ccxtExec, err := executor.NewCCXTExecutor(ccxtConfig)
			if err != nil {
				lastErr = err
				continue
			}
			if err := ccxtExec.Initialize(ctx); err != nil {
				lastErr = err
				continue
			}
			registry.Register(ccxtExec)
		}
	}

	return lastErr
}

// formatBalance formats a balance for display.
func formatBalance(balance *big.Float) string {
	if balance == nil {
		return "0"
	}
	balanceStr := balance.Text('f', 8)
	// Trim trailing zeros
	balanceStr = strings.TrimRight(strings.TrimRight(balanceStr, "0"), ".")
	if balanceStr == "" {
		balanceStr = "0"
	}
	return balanceStr
}
