// Package main is the entry point for the trading bot with execution capability.
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

	"github.com/jonasrmichel/gswap-arb/pkg/config"
	"github.com/jonasrmichel/gswap-arb/pkg/executor"
	"github.com/jonasrmichel/gswap-arb/pkg/providers/websocket"
	"github.com/jonasrmichel/gswap-arb/pkg/reporter"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

var (
	configPath   = flag.String("config", "", "Path to configuration file (JSON)")
	outputFormat = flag.String("format", "text", "Output format: text, json, csv")
	verbose      = flag.Bool("verbose", true, "Enable verbose output")
	dryRun       = flag.Bool("dry-run", true, "Dry run mode (default: true for safety)")
	maxTradeSize = flag.Float64("max-trade", 10.0, "Maximum trade size in quote currency")
	minProfit    = flag.Int("min-profit", 20, "Minimum profit in basis points")
)

func main() {
	flag.Parse()

	// Print banner
	printBanner()

	// Load configuration
	var cfg *config.Config
	var err error

	if *configPath != "" {
		cfg, err = config.LoadFromFile(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
	} else {
		cfg = config.LoadFromEnv()
	}

	cfg.Verbose = *verbose
	cfg.DryRun = *dryRun

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutting down gracefully...")
		cancel()
	}()

	// Create reporter
	format := reporter.FormatText
	switch *outputFormat {
	case "json":
		format = reporter.FormatJSON
	case "csv":
		format = reporter.FormatCSV
	}
	rep := reporter.NewReporter(os.Stdout, format, cfg.Verbose)

	// Create executor registry
	registry := executor.NewExecutorRegistry()

	// Initialize executors based on configuration
	if err := setupExecutors(ctx, registry, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	// Print executor status
	printExecutorStatus(registry)

	// Create coordinator
	coordConfig := &executor.CoordinatorConfig{
		DryRun:               *dryRun,
		MaxTradeSize:         big.NewFloat(*maxTradeSize),
		MinTradeSize:         big.NewFloat(1.0),
		MaxSlippageBps:       100,
		MinProfitBps:         *minProfit,
		MinTimeBetweenTrades: 5 * time.Second,
		MinBalanceBuffer:     big.NewFloat(0.1),
		ExecutionTimeout:     30 * time.Second,
		RetryAttempts:        2,
	}
	coordinator := executor.NewArbitrageCoordinator(registry, coordConfig)

	// Create WebSocket aggregator
	aggConfig := &websocket.AggregatorConfig{
		MinSpreadBps:    cfg.Arbitrage.MinSpreadBps,
		MinNetProfitBps: cfg.Arbitrage.MinNetProfitBps,
		StaleThreshold:  10 * time.Second,
	}
	aggregator := websocket.NewPriceAggregator(aggConfig)

	// Add WebSocket providers
	setupWebSocketProviders(aggregator, cfg)

	// Print configuration summary
	printConfig(cfg, coordinator)

	// Set up arbitrage callback with execution
	aggregator.OnArbitrage(func(opp *types.ArbitrageOpportunity) {
		rep.ReportOpportunities([]*types.ArbitrageOpportunity{opp})

		// Attempt execution if conditions are met
		if opp.NetProfitBps >= *minProfit {
			execution, err := coordinator.ExecuteOpportunity(ctx, opp)
			if err != nil {
				fmt.Printf("  [EXEC ERROR] %v\n", err)
				return
			}

			if execution.DryRun {
				fmt.Printf("  [DRY RUN] Would execute: %s -> %s, estimated profit: %s\n",
					opp.BuyExchange, opp.SellExchange,
					execution.NetProfit.Text('f', 4))
			} else if execution.Success {
				fmt.Printf("  [EXECUTED] Profit: %s, Fees: %s\n",
					execution.NetProfit.Text('f', 4),
					execution.TotalFees.Text('f', 4))
			} else {
				fmt.Printf("  [SKIPPED] %s\n", execution.Error)
			}
		}
	})

	// Chain arbitrage callback
	aggregator.OnChainArbitrage(func(opp *types.ChainArbitrageOpportunity) {
		rep.ReportChainOpportunities([]*types.ChainArbitrageOpportunity{opp})

		if opp.NetProfitBps >= *minProfit {
			execution, err := coordinator.ExecuteChainOpportunity(ctx, opp)
			if err != nil {
				fmt.Printf("  [CHAIN EXEC ERROR] %v\n", err)
				return
			}

			if execution.DryRun {
				fmt.Printf("  [CHAIN DRY RUN] Would execute: %v, estimated profit: %s\n",
					opp.Chain, execution.NetProfit.Text('f', 4))
			} else if execution.Success {
				fmt.Printf("  [CHAIN EXECUTED] Profit: %s\n",
					execution.NetProfit.Text('f', 4))
			} else {
				fmt.Printf("  [CHAIN SKIPPED] %s\n", execution.Error)
			}
		}
	})

	// Get pairs to subscribe
	pairs := getPairs(cfg)
	if len(pairs) == 0 {
		fmt.Fprintln(os.Stderr, "No pairs configured")
		os.Exit(1)
	}

	fmt.Printf("Subscribing to %d pairs...\n", len(pairs))
	for _, pair := range pairs {
		fmt.Printf("  - %s\n", pair)
	}
	fmt.Println()

	// Start the aggregator
	if err := aggregator.Start(ctx, pairs); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start aggregator: %v\n", err)
		os.Exit(1)
	}

	// Wait for connections
	fmt.Println("Connecting to exchanges...")
	time.Sleep(2 * time.Second)

	// Print connection status
	status := aggregator.GetConnectionStatus()
	fmt.Println("\nConnection status:")
	for name, state := range status {
		fmt.Printf("  - %s: %s\n", name, state)
	}
	fmt.Println()

	fmt.Println("Listening for arbitrage opportunities...")
	if *dryRun {
		fmt.Println("** DRY RUN MODE - No actual trades will be executed **")
	} else {
		fmt.Println("** LIVE TRADING MODE - Real trades will be executed **")
	}
	fmt.Println("Press Ctrl+C to stop")
	fmt.Println()

	// Status ticker
	statusTicker := time.NewTicker(60 * time.Second)
	defer statusTicker.Stop()

	// Main loop
	for {
		select {
		case <-ctx.Done():
			// Print final stats
			rep.PrintStats()
			printExecutionStats(coordinator)

			// Stop aggregator
			aggregator.Stop()

			// Close executors
			registry.Close()

			fmt.Println("Bot stopped.")
			return

		case <-statusTicker.C:
			printStatus(aggregator, coordinator, rep)
		}
	}
}

// printBanner prints the application banner.
func printBanner() {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║       GSwap Arbitrage Bot - Trading Mode                  ║")
	fmt.Println("║                                                           ║")
	fmt.Println("║   Real-time arbitrage detection AND execution             ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()
}

// printConfig prints the current configuration.
func printConfig(cfg *config.Config, coord *executor.ArbitrageCoordinator) {
	fmt.Println("Configuration:")
	fmt.Printf("  - Min spread: %d bps (%.2f%%)\n", cfg.Arbitrage.MinSpreadBps, float64(cfg.Arbitrage.MinSpreadBps)/100)
	fmt.Printf("  - Min net profit: %d bps (%.2f%%)\n", cfg.Arbitrage.MinNetProfitBps, float64(cfg.Arbitrage.MinNetProfitBps)/100)
	fmt.Printf("  - Dry run: %v\n", coord.IsDryRun())
	fmt.Println()
}

// setupExecutors initializes trade executors based on configuration.
func setupExecutors(ctx context.Context, registry *executor.ExecutorRegistry, cfg *config.Config) error {
	var lastErr error

	for _, ex := range cfg.GetEnabledExchanges() {
		if !ex.TradingEnabled {
			continue
		}

		// GSwap is a DEX on GalaChain - use native executor
		if ex.ID == "gswap" {
			if ex.PrivateKey == "" {
				fmt.Printf("Skipping GSwap executor: Private key not configured\n")
				continue
			}
			gswapExec, err := executor.NewGSwapExecutor(ex.PrivateKey, ex.WalletAddress)
			if err != nil {
				fmt.Printf("Warning: Failed to create GSwap executor: %v\n", err)
				lastErr = err
				continue
			}
			if err := gswapExec.Initialize(ctx); err != nil {
				fmt.Printf("Warning: Failed to initialize GSwap executor: %v\n", err)
				lastErr = err
				continue
			}
			registry.Register(gswapExec)
			fmt.Printf("Initialized GSwap executor (wallet: %s)\n", gswapExec.GetWalletAddress())
			continue
		}

		// Use CCXT for all supported CEX exchanges
		if executor.SupportedCCXTExchanges[strings.ToLower(ex.ID)] {
			if ex.APIKey == "" || ex.Secret == "" {
				fmt.Printf("Skipping %s executor: API credentials not configured\n", ex.ID)
				continue
			}

			ccxtConfig := &executor.CCXTConfig{
				ExchangeID: ex.ID,
				APIKey:     ex.APIKey,
				Secret:     ex.Secret,
				Password:   ex.Passphrase, // For exchanges requiring passphrase
				Sandbox:    false,
			}

			ccxtExec, err := executor.NewCCXTExecutor(ccxtConfig)
			if err != nil {
				fmt.Printf("Warning: Failed to create %s executor: %v\n", ex.ID, err)
				lastErr = err
				continue
			}
			if err := ccxtExec.Initialize(ctx); err != nil {
				fmt.Printf("Warning: Failed to initialize %s executor: %v\n", ex.ID, err)
				lastErr = err
				continue
			}
			registry.Register(ccxtExec)
			fmt.Printf("Initialized %s executor (via CCXT)\n", ex.ID)
		} else {
			fmt.Printf("Skipping %s: not a supported CCXT exchange\n", ex.ID)
		}
	}

	return lastErr
}

// printExecutorStatus prints the status of all executors.
func printExecutorStatus(registry *executor.ExecutorRegistry) {
	fmt.Println("\nExecutor status:")
	executors := registry.GetAll()
	if len(executors) == 0 {
		fmt.Println("  No executors configured (detection only mode)")
	} else {
		for name, exec := range executors {
			status := "not ready"
			if exec.IsReady() {
				status = "ready"
			}
			fmt.Printf("  - %s (%s): %s\n", name, exec.Type(), status)
		}
	}
	fmt.Println()
}

// setupWebSocketProviders adds WebSocket providers based on configuration.
func setupWebSocketProviders(agg *websocket.PriceAggregator, cfg *config.Config) {
	for _, ex := range cfg.GetEnabledExchanges() {
		switch ex.ID {
		case "gswap":
			agg.AddProvider(websocket.NewGSwapPollerProvider(5 * time.Second))
		case "binance":
			agg.AddProvider(websocket.NewBinanceWSProvider())
		case "coinbase":
			agg.AddProvider(websocket.NewCoinbaseWSProvider())
		case "kraken":
			agg.AddProvider(websocket.NewKrakenWSProvider())
		case "okx":
			agg.AddProvider(websocket.NewOKXWSProvider())
		case "bybit":
			agg.AddProvider(websocket.NewBybitWSProvider())
		}
	}
}

// getPairs extracts unique pairs from configuration.
func getPairs(cfg *config.Config) []string {
	pairSet := make(map[string]bool)
	for _, p := range cfg.GetEnabledPairs() {
		pairSet[p.Pair] = true
	}

	pairs := make([]string, 0, len(pairSet))
	for pair := range pairSet {
		pairs = append(pairs, pair)
	}
	return pairs
}

// printStatus prints current status.
func printStatus(agg *websocket.PriceAggregator, coord *executor.ArbitrageCoordinator, rep *reporter.Reporter) {
	stats := rep.GetStats()
	execStats := coord.GetStats()

	fmt.Println()
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("[%s] Status Update\n", time.Now().Format("15:04:05"))
	fmt.Printf("  Uptime: %s\n", time.Since(stats.StartTime).Round(time.Second))
	fmt.Printf("  Opportunities found: %d\n", stats.OpportunitiesFound)
	fmt.Printf("  Executions attempted: %d (successful: %d, failed: %d)\n",
		execStats.ExecutedTrades, execStats.SuccessfulTrades, execStats.FailedTrades)
	fmt.Printf("  Skipped: insufficient balance=%d, below min profit=%d, rate limited=%d, dry run=%d\n",
		execStats.SkippedInsufficientBalance, execStats.SkippedBelowMinProfit,
		execStats.SkippedRateLimited, execStats.SkippedDryRun)

	if execStats.TotalProfit != nil && execStats.TotalProfit.Sign() != 0 {
		fmt.Printf("  Total profit: %s\n", execStats.TotalProfit.Text('f', 4))
	}

	// Connection status
	status := agg.GetConnectionStatus()
	fmt.Print("  Connections: ")
	parts := make([]string, 0, len(status))
	for name, state := range status {
		parts = append(parts, fmt.Sprintf("%s=%s", name, state))
	}
	fmt.Println(strings.Join(parts, ", "))

	fmt.Println(strings.Repeat("-", 60))
	fmt.Println()
}

// printExecutionStats prints final execution statistics.
func printExecutionStats(coord *executor.ArbitrageCoordinator) {
	stats := coord.GetStats()

	fmt.Println("\nExecution Statistics:")
	fmt.Println(strings.Repeat("=", 40))
	fmt.Printf("  Total opportunities: %d\n", stats.TotalOpportunities)
	fmt.Printf("  Executed trades: %d\n", stats.ExecutedTrades)
	fmt.Printf("  Successful: %d\n", stats.SuccessfulTrades)
	fmt.Printf("  Failed: %d\n", stats.FailedTrades)
	fmt.Printf("  Skipped (insufficient balance): %d\n", stats.SkippedInsufficientBalance)
	fmt.Printf("  Skipped (below min profit): %d\n", stats.SkippedBelowMinProfit)
	fmt.Printf("  Skipped (rate limited): %d\n", stats.SkippedRateLimited)
	fmt.Printf("  Skipped (dry run): %d\n", stats.SkippedDryRun)
	if stats.TotalProfit != nil {
		fmt.Printf("  Total profit: %s\n", stats.TotalProfit.Text('f', 4))
	}
	if stats.TotalFees != nil {
		fmt.Printf("  Total fees: %s\n", stats.TotalFees.Text('f', 4))
	}
	fmt.Println(strings.Repeat("=", 40))
}
