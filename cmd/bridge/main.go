// Package main is the entry point for the bridge CLI tool.
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

	"github.com/jonasrmichel/gswap-arb/pkg/bridge"
)

var (
	// Bridge operation flags
	direction  = flag.String("direction", "", "Bridge direction: 'to-eth' or 'to-gala'")
	token      = flag.String("token", "", "Token to bridge (GALA, GWETH, GUSDC, GUSDT, GWTRX, GWBTC, BENE)")
	amount     = flag.String("amount", "", "Amount to bridge")
	toAddress  = flag.String("to", "", "Destination address (optional, defaults to same wallet)")

	// Query flags
	balance = flag.Bool("balance", false, "Show balances for all supported tokens")
	status  = flag.String("status", "", "Check status of a bridge transaction")
	list    = flag.Bool("list", false, "List supported tokens")

	// Config flags
	privateKey = flag.String("private-key", "", "Private key (or use GSWAP_PRIVATE_KEY env var)")
	ethRPC     = flag.String("eth-rpc", "", "Ethereum RPC URL (or use ETH_RPC_URL env var)")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `GSwap Bridge CLI - Bridge tokens between GalaChain and Ethereum

Usage:
  bridge [flags]

Operations:
  --direction     Bridge direction: 'to-eth' (GalaChain→Ethereum) or 'to-gala' (Ethereum→GalaChain)
  --token         Token to bridge (e.g., GALA, GWETH, GUSDC)
  --amount        Amount to bridge
  --to            Destination address (optional)

Queries:
  --balance       Show balances for all supported tokens on GalaChain
  --status <txid> Check status of a bridge transaction
  --list          List supported tokens

Configuration:
  --private-key   Wallet private key (or set GSWAP_PRIVATE_KEY)
  --eth-rpc       Ethereum RPC URL (or set ETH_RPC_URL)

Examples:
  # List supported tokens
  bridge --list

  # Check balances
  bridge --balance

  # Bridge 100 GALA from GalaChain to Ethereum
  bridge --direction to-eth --token GALA --amount 100

  # Bridge 50 GUSDC from Ethereum to GalaChain
  bridge --direction to-gala --token GUSDC --amount 50

  # Check bridge transaction status
  bridge --status <transaction-id>

Environment Variables:
  GSWAP_PRIVATE_KEY    Wallet private key
  ETH_RPC_URL          Ethereum RPC endpoint

`)
	}

	flag.Parse()

	// Handle list command
	if *list {
		listTokens()
		return
	}

	// Get private key
	pk := *privateKey
	if pk == "" {
		pk = os.Getenv("GSWAP_PRIVATE_KEY")
	}

	if pk == "" && !*list {
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
	executor, err := bridge.NewBridgeExecutor(&bridge.BridgeConfig{
		GalaChainPrivateKey: pk,
		EthereumPrivateKey:  pk, // Use same key for both chains
		EthereumRPCURL:      ethRPCURL,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating bridge executor: %v\n", err)
		os.Exit(1)
	}

	// Handle balance query
	if *balance {
		showBalances(ctx, executor)
		return
	}

	// Handle status query
	if *status != "" {
		checkStatus(ctx, executor, *status)
		return
	}

	// Handle bridge operation
	if *direction != "" {
		executeBridge(ctx, executor)
		return
	}

	// No operation specified
	flag.Usage()
	os.Exit(1)
}

func listTokens() {
	fmt.Println("Supported Tokens for Bridging")
	fmt.Println("==============================")
	fmt.Println()
	fmt.Printf("%-8s %-44s %-8s %-6s\n", "Symbol", "Ethereum Address", "Decimals", "Permit")
	fmt.Println(strings.Repeat("-", 70))

	for _, symbol := range []string{"GALA", "GWETH", "GUSDC", "GUSDT", "GWTRX", "GWBTC", "BENE"} {
		token, _ := bridge.GetTokenBySymbol(symbol)
		permit := "No"
		if token.BridgeUsesPermit {
			permit = "Yes"
		}
		fmt.Printf("%-8s %-44s %-8d %-6s\n", token.Symbol, token.Address, token.Decimals, permit)
	}
	fmt.Println()
}

func showBalances(ctx context.Context, executor *bridge.BridgeExecutor) {
	galaAddr, ethAddr := executor.GetWalletAddresses()

	if galaAddr == "" {
		fmt.Println("Error: Wallet not configured")
		return
	}

	fmt.Println("Wallet Addresses")
	fmt.Println("================")
	fmt.Printf("GalaChain: %s\n", executor.GetGalaChainAddress())
	fmt.Printf("Ethereum:  %s\n", ethAddr)
	fmt.Println()

	symbols := bridge.GetSupportedSymbols()

	// Show GalaChain balances
	fmt.Println("GalaChain Balances")
	fmt.Println("==================")
	fmt.Printf("%-8s %20s\n", "Token", "Balance")
	fmt.Println(strings.Repeat("-", 30))

	for _, symbol := range symbols {
		balance, err := executor.GetGalaChainBalance(ctx, symbol)
		if err != nil {
			fmt.Printf("%-8s %20s\n", symbol, "Error")
			continue
		}
		fmt.Printf("%-8s %20s\n", symbol, formatBalance(balance))
	}
	fmt.Println()

	// Show Ethereum balances if RPC is configured
	if executor.GetEthereumRPCConfigured() {
		fmt.Println("Ethereum Balances")
		fmt.Println("=================")
		fmt.Printf("%-8s %20s\n", "Token", "Balance")
		fmt.Println(strings.Repeat("-", 30))

		// Show native ETH balance first
		ethBalance, err := executor.GetEthereumBalance(ctx, "ETH")
		if err != nil {
			fmt.Printf("%-8s %20s\n", "ETH", "Error")
		} else {
			fmt.Printf("%-8s %20s\n", "ETH", formatBalance(ethBalance))
		}

		// Show ERC-20 token balances
		for _, symbol := range symbols {
			balance, err := executor.GetEthereumBalance(ctx, symbol)
			if err != nil {
				fmt.Printf("%-8s %20s\n", symbol, "Error")
				continue
			}
			fmt.Printf("%-8s %20s\n", symbol, formatBalance(balance))
		}
		fmt.Println()
	} else {
		fmt.Println("Ethereum Balances: Not available (ETH_RPC_URL not configured)")
		fmt.Println()
	}
}

func formatBalance(balance *big.Float) string {
	balanceStr := balance.Text('f', 8)
	// Trim trailing zeros
	balanceStr = strings.TrimRight(strings.TrimRight(balanceStr, "0"), ".")
	if balanceStr == "" {
		balanceStr = "0"
	}
	return balanceStr
}

func checkStatus(ctx context.Context, executor *bridge.BridgeExecutor, txID string) {
	fmt.Printf("Checking bridge status for: %s\n", txID)
	fmt.Println()

	result, err := executor.GetBridgeStatus(ctx, txID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking status: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Transaction ID: %s\n", result.TransactionID)
	fmt.Printf("Status:         %s\n", result.Status)
	if result.Token != "" {
		fmt.Printf("Token:          %s\n", result.Token)
	}
	if result.Amount != nil && result.Amount.Sign() > 0 {
		fmt.Printf("Amount:         %s\n", result.Amount.Text('f', 8))
	}
	if result.SourceTxHash != "" {
		fmt.Printf("Source Tx:      %s\n", result.SourceTxHash)
	}
	if result.DestTxHash != "" {
		fmt.Printf("Dest Tx:        %s\n", result.DestTxHash)
	}

	// Show explorer links
	galaConnectURL, etherscanURL := result.GetExplorerLinks()
	if galaConnectURL != "" || etherscanURL != "" {
		fmt.Println()
		fmt.Println("Explorer Links:")
		if galaConnectURL != "" {
			fmt.Printf("  GalaConnect:  %s\n", galaConnectURL)
		}
		if etherscanURL != "" {
			fmt.Printf("  Etherscan:    %s\n", etherscanURL)
		}
	}

	if result.Error != "" {
		fmt.Println()
		fmt.Println("Notes:")
		fmt.Println(result.Error)
	}
	fmt.Println()
}

func executeBridge(ctx context.Context, executor *bridge.BridgeExecutor) {
	// Validate inputs
	if *token == "" {
		fmt.Fprintln(os.Stderr, "Error: --token is required")
		os.Exit(1)
	}

	if *amount == "" {
		fmt.Fprintln(os.Stderr, "Error: --amount is required")
		os.Exit(1)
	}

	// Parse direction
	var dir bridge.BridgeDirection
	switch strings.ToLower(*direction) {
	case "to-eth", "to-ethereum", "toeth":
		dir = bridge.BridgeToEthereum
	case "to-gala", "to-galachain", "togala":
		dir = bridge.BridgeToGalaChain
	default:
		fmt.Fprintf(os.Stderr, "Error: Invalid direction '%s'. Use 'to-eth' or 'to-gala'\n", *direction)
		os.Exit(1)
	}

	// Validate token
	tokenUpper := strings.ToUpper(*token)
	tokenInfo, ok := bridge.GetTokenBySymbol(tokenUpper)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: Unsupported token '%s'. Use --list to see supported tokens.\n", *token)
		os.Exit(1)
	}

	// Parse amount
	amountVal := new(big.Float)
	_, ok = amountVal.SetString(*amount)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: Invalid amount '%s'\n", *amount)
		os.Exit(1)
	}

	if amountVal.Sign() <= 0 {
		fmt.Fprintln(os.Stderr, "Error: Amount must be positive")
		os.Exit(1)
	}

	// Print operation summary
	fmt.Println("Bridge Operation")
	fmt.Println("================")
	fmt.Printf("Direction:   %s\n", dir)
	fmt.Printf("Token:       %s\n", tokenUpper)
	fmt.Printf("Amount:      %s\n", amountVal.Text('f', 8))
	if *toAddress != "" {
		fmt.Printf("Destination: %s\n", *toAddress)
	}
	fmt.Printf("Contract:    %s\n", tokenInfo.Address)
	fmt.Println()

	// Confirm operation
	fmt.Print("Proceed with bridge? (y/N): ")
	var confirm string
	fmt.Scanln(&confirm)

	if strings.ToLower(confirm) != "y" && strings.ToLower(confirm) != "yes" {
		fmt.Println("Cancelled.")
		return
	}

	fmt.Println()
	fmt.Println("Initiating bridge...")

	// Execute bridge
	req := &bridge.BridgeRequest{
		Token:       tokenUpper,
		Amount:      amountVal,
		Direction:   dir,
		ToAddress:   *toAddress,
	}

	startTime := time.Now()
	result, err := executor.Bridge(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Bridge failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Bridge Result")
	fmt.Println("=============")
	fmt.Printf("Transaction ID:  %s\n", result.TransactionID)
	fmt.Printf("Status:          %s\n", result.Status)
	fmt.Printf("Token:           %s\n", result.Token)
	fmt.Printf("Amount:          %s\n", result.Amount.Text('f', 8))
	fmt.Printf("From:            %s\n", result.FromAddress)
	fmt.Printf("To:              %s\n", result.ToAddress)
	if result.SourceTxHash != "" {
		fmt.Printf("Source Tx:       %s\n", result.SourceTxHash)
	}
	fmt.Printf("Estimated Time:  %s\n", result.EstimatedTime)
	fmt.Printf("Initiated:       %s\n", result.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Duration:        %s\n", time.Since(startTime).Round(time.Millisecond))

	// Show explorer links
	galaConnectURL, etherscanURL := result.GetExplorerLinks()
	if galaConnectURL != "" || etherscanURL != "" {
		fmt.Println()
		fmt.Println("Explorer Links:")
		if galaConnectURL != "" {
			fmt.Printf("  GalaConnect:   %s\n", galaConnectURL)
		}
		if etherscanURL != "" {
			fmt.Printf("  Etherscan:     %s\n", etherscanURL)
		}
	}

	if result.Error != "" {
		fmt.Println()
		fmt.Println("Notes:")
		fmt.Println(result.Error)
	}

	fmt.Println()
	fmt.Printf("Use 'bridge --status %s' to check the bridge status.\n", result.TransactionID)
}
