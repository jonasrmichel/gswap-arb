// Package main is the entry point for the GSwap graph-based arbitrage detector.
// This bot focuses solely on finding arbitrage cycles within GalaChain DEX (GSwap).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/arbitrage/graph"
	"github.com/jonasrmichel/gswap-arb/pkg/executor"
)

var (
	// Detection settings
	apiURL         = flag.String("api", "https://arb.gala.com/api", "GSwap API URL")
	maxCycleLength = flag.Int("max-cycle", 5, "Maximum cycle length to detect")
	minProfitBps   = flag.Int("min-profit", 10, "Minimum profit in basis points to report")
	defaultFeeBps  = flag.Int("fee", 100, "Default fee in basis points (1% = 100)")
	pollInterval   = flag.Duration("poll", 5*time.Second, "Price polling interval")

	// Trading settings
	enableTrading = flag.Bool("trade", false, "Enable trade execution (requires credentials)")
	dryRun        = flag.Bool("dry-run", true, "Dry run mode - simulate trades without executing")
	tradeSize     = flag.Float64("trade-size", 100, "Trade size for cycle execution")
	maxTradeSize  = flag.Float64("max-trade-size", 1000, "Maximum trade size")
	minTradeSize  = flag.Float64("min-trade-size", 10, "Minimum trade size")
	execMinProfit = flag.Int("exec-min-profit", 50, "Minimum profit in bps to execute trade")
	slippageBps   = flag.Int("slippage", 100, "Slippage tolerance in basis points")
	autoExecute   = flag.Bool("auto-execute", false, "Automatically execute profitable cycles")

	// Display settings
	verbose    = flag.Bool("verbose", true, "Enable verbose output")
	logFile    = flag.String("log", "bot-gswap.log", "Log file path (empty to disable)")
	showGraph  = flag.Bool("show-graph", false, "Print graph summary at startup")
	showCycles = flag.Bool("show-cycles", false, "Print all cycles at startup")
	continuous = flag.Bool("continuous", true, "Run continuously (poll for prices)")

	// Credentials (can also use env vars)
	privateKey    = flag.String("private-key", "", "GSwap private key (or GSWAP_PRIVATE_KEY env)")
	walletAddress = flag.String("wallet", "", "Wallet address (or GSWAP_WALLET_ADDRESS env)")
)

var logWriter io.Writer

func main() {
	flag.Parse()

	// Set up logging
	var logFileHandle *os.File
	if *logFile != "" {
		var err error
		logFileHandle, err = os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to open log file %s: %v\n", *logFile, err)
			logWriter = os.Stdout
		} else {
			logWriter = io.MultiWriter(os.Stdout, logFileHandle)
			log.SetOutput(logWriter)
		}
	} else {
		logWriter = os.Stdout
	}

	defer func() {
		if logFileHandle != nil {
			logFileHandle.Close()
		}
	}()

	// Print banner
	printBanner()

	if *logFile != "" && logFileHandle != nil {
		logf("Logging to file: %s\n", *logFile)
	}

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logln("\nShutting down gracefully...")
		cancel()
	}()

	// Create detector configuration
	config := &graph.Config{
		MaxCycleLength: *maxCycleLength,
		MinProfitBps:   *minProfitBps,
		DefaultFeeBps:  *defaultFeeBps,
		StaleThreshold: 30 * time.Second,
		PollInterval:   *pollInterval,
		APIURL:         *apiURL,
	}

	// Print configuration
	printConfig(config)

	// Create detector
	detector := graph.NewDetector(config)

	// Set up executor if trading is enabled
	var cycleExecutor *graph.CycleExecutor
	if *enableTrading {
		exec, err := setupExecutor(ctx)
		if err != nil {
			logf("Warning: Failed to set up executor: %v\n", err)
			logln("Trading disabled - running in detection-only mode")
		} else {
			cycleExecutor = exec
			logln()
			if cycleExecutor.IsDryRun() {
				logln("** TRADING ENABLED (DRY RUN MODE) **")
				logln("   Trades will be simulated but not executed")
			} else {
				logln("** TRADING ENABLED (LIVE MODE) **")
				logln("   WARNING: Real trades will be executed!")
			}
			logln()
		}
	}

	// Set up opportunity callback
	detector.SetOpportunityCallback(func(result *graph.CycleResult) {
		logln()
		logln(strings.Repeat("*", 60))
		logf("[OPPORTUNITY] %s\n", time.Now().Format("15:04:05"))
		logf("  Cycle: %s\n", graph.FormatCycleResult(result))
		logf("  Profit: %d bps (%.2f%%)\n", result.ProfitBps, float64(result.ProfitBps)/100)
		logf("  Profit Ratio: %.6f\n", result.ProfitRatio)
		logf("  Min Liquidity: %.2f\n", result.MinLiquidity)

		// Simulate a trade with configured trade size
		if result.Cycle != nil {
			output, profit, fees, err := detector.SimulateCycleTrade(result.Cycle.ID, *tradeSize)
			if err == nil {
				logf("  Simulation (%.0f units): output=%.4f, profit=%.4f, fees=%.4f\n",
					*tradeSize, output, profit, fees)
			}
		}

		// Execute trade if conditions are met
		if cycleExecutor != nil && *autoExecute && result.ProfitBps >= *execMinProfit {
			logf("  -> Executing trade (profit %d bps >= threshold %d bps)\n", result.ProfitBps, *execMinProfit)
			execution, err := cycleExecutor.ExecuteCycle(ctx, result.Cycle, result, *tradeSize)
			if err != nil {
				logf("  -> Execution failed: %v\n", err)
			} else {
				logln(graph.FormatExecution(execution))
			}
		} else if cycleExecutor != nil && result.ProfitBps < *execMinProfit {
			logf("  -> Skipping execution (profit %d bps < threshold %d bps)\n", result.ProfitBps, *execMinProfit)
		}

		logln(strings.Repeat("*", 60))
		logln()
	})

	// Initialize detector
	logln("Initializing detector...")
	if err := detector.Initialize(ctx); err != nil {
		logf("Failed to initialize detector: %v\n", err)
		os.Exit(1)
	}

	// Print graph summary if requested
	if *showGraph {
		detector.PrintGraphSummary()
	}

	// Print all cycles if requested
	if *showCycles {
		printAllCycles(detector)
	}

	// Print initialization summary
	stats := detector.Stats()
	logln()
	logln("Initialization complete:")
	logf("  Cycles enumerated: %d\n", stats.CyclesEnumerated)
	logf("  Init duration: %v\n", stats.InitDuration)
	logln()

	// Evaluate all cycles with initial prices
	logln("Evaluating cycles with current prices...")
	results := detector.EvaluateAll()
	if len(results) > 0 {
		logf("Found %d profitable cycles:\n", len(results))
		for i, r := range results {
			if i >= 10 {
				logf("  ... and %d more\n", len(results)-10)
				break
			}
			logf("  [%d] %s\n", i+1, graph.FormatCycleResult(r))
		}
	} else {
		logln("No profitable cycles found at current prices")
	}
	logln()

	// If not continuous mode, exit here
	if !*continuous {
		logln("One-shot mode complete. Use -continuous=true to run continuously.")
		printFinalStats(detector, cycleExecutor)
		return
	}

	// Start continuous polling
	logln("Starting continuous price monitoring...")
	logf("  Poll interval: %v\n", *pollInterval)
	logf("  Min profit threshold (report): %d bps\n", *minProfitBps)
	if cycleExecutor != nil {
		logf("  Min profit threshold (execute): %d bps\n", *execMinProfit)
		logf("  Auto-execute: %v\n", *autoExecute)
	}
	logln("Press Ctrl+C to stop")
	logln()

	// Poll ticker
	pollTicker := time.NewTicker(*pollInterval)
	defer pollTicker.Stop()

	// Status ticker (every minute)
	statusTicker := time.NewTicker(60 * time.Second)
	defer statusTicker.Stop()

	// Main loop
	for {
		select {
		case <-ctx.Done():
			printFinalStats(detector, cycleExecutor)
			logln("Bot stopped.")
			return

		case <-pollTicker.C:
			// Refresh prices from API
			if err := detector.RefreshPrices(ctx); err != nil {
				if *verbose {
					logf("[WARN] Failed to refresh prices: %v\n", err)
				}
				continue
			}

			// Evaluate all cycles
			results := detector.EvaluateAll()

			if *verbose && len(results) > 0 {
				logf("[%s] Found %d profitable cycles\n",
					time.Now().Format("15:04:05"), len(results))
			}

		case <-statusTicker.C:
			printStatus(detector, cycleExecutor)
		}
	}
}

// setupExecutor initializes the GSwap executor for trading.
func setupExecutor(ctx context.Context) (*graph.CycleExecutor, error) {
	// Get credentials from flags or environment
	privKey := *privateKey
	if privKey == "" {
		privKey = os.Getenv("GSWAP_PRIVATE_KEY")
	}
	if privKey == "" {
		return nil, fmt.Errorf("private key required (use -private-key or GSWAP_PRIVATE_KEY env)")
	}

	wallet := *walletAddress
	if wallet == "" {
		wallet = os.Getenv("GSWAP_WALLET_ADDRESS")
	}

	// Create GSwap executor
	gswapExec, err := executor.NewGSwapExecutor(privKey, wallet)
	if err != nil {
		return nil, fmt.Errorf("failed to create GSwap executor: %w", err)
	}

	// Initialize executor
	if err := gswapExec.Initialize(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize GSwap executor: %w", err)
	}

	logf("GSwap executor initialized (wallet: %s)\n", gswapExec.GetWalletAddress())

	// Create executor config
	execConfig := &graph.ExecutorConfig{
		DryRun:           *dryRun,
		MaxTradeSize:     *maxTradeSize,
		MinTradeSize:     *minTradeSize,
		SlippageBps:      *slippageBps,
		MinProfitBps:     *execMinProfit,
		MaxConcurrent:    1,
		CooldownDuration: 5 * time.Second,
	}

	return graph.NewCycleExecutor(gswapExec, execConfig), nil
}

func printBanner() {
	logln()
	logln("=============================================================")
	logln("       GSwap Graph-Based Arbitrage Bot                       ")
	logln("                                                             ")
	logln("   Detects and executes cycles within GalaChain DEX (GSwap)  ")
	logln("=============================================================")
	logln()
}

func printConfig(cfg *graph.Config) {
	logln("Configuration:")
	logf("  - API URL: %s\n", cfg.APIURL)
	logf("  - Max cycle length: %d\n", cfg.MaxCycleLength)
	logf("  - Min profit (report): %d bps (%.2f%%)\n", cfg.MinProfitBps, float64(cfg.MinProfitBps)/100)
	logf("  - Default fee: %d bps (%.2f%%)\n", cfg.DefaultFeeBps, float64(cfg.DefaultFeeBps)/100)
	logf("  - Poll interval: %v\n", cfg.PollInterval)

	if *enableTrading {
		logln()
		logln("Trading Settings:")
		logf("  - Dry run: %v\n", *dryRun)
		logf("  - Trade size: %.2f\n", *tradeSize)
		logf("  - Max trade size: %.2f\n", *maxTradeSize)
		logf("  - Min trade size: %.2f\n", *minTradeSize)
		logf("  - Min profit (execute): %d bps (%.2f%%)\n", *execMinProfit, float64(*execMinProfit)/100)
		logf("  - Slippage tolerance: %d bps (%.2f%%)\n", *slippageBps, float64(*slippageBps)/100)
		logf("  - Auto-execute: %v\n", *autoExecute)
	}
	logln()
}

func printAllCycles(d *graph.Detector) {
	idx := d.CycleIndex()
	if idx == nil {
		logln("No cycles indexed")
		return
	}

	cycles := idx.AllCycles()
	logf("\nAll %d cycles:\n", len(cycles))

	// Group by length
	byLength := make(map[int][]*graph.Cycle)
	for _, c := range cycles {
		byLength[c.Length] = append(byLength[c.Length], c)
	}

	for length := 2; length <= 6; length++ {
		if cs, ok := byLength[length]; ok {
			logf("\n  %d-hop cycles (%d):\n", length, len(cs))
			for i, c := range cs {
				if i >= 20 {
					logf("    ... and %d more\n", len(cs)-20)
					break
				}
				path := formatPath(c.Path)
				logf("    [%d] %s\n", c.ID, path)
			}
		}
	}
	logln()
}

func formatPath(path []graph.Token) string {
	parts := make([]string, len(path))
	for i, t := range path {
		parts[i] = string(t)
	}
	return strings.Join(parts, " -> ")
}

func printStatus(d *graph.Detector, exec *graph.CycleExecutor) {
	stats := d.Stats()

	logln()
	logln(strings.Repeat("-", 60))
	logf("[%s] Status\n", time.Now().Format("15:04:05"))
	logf("  Price updates: %d\n", stats.PriceUpdates)
	logf("  Opportunities found: %d\n", stats.OpportunitiesFound)
	if !stats.LastUpdateTime.IsZero() {
		logf("  Last update: %s ago\n", time.Since(stats.LastUpdateTime).Round(time.Second))
	}

	// Evaluate current state
	results := d.EvaluateAll()
	logf("  Currently profitable cycles: %d\n", len(results))

	if len(results) > 0 && len(results) <= 5 {
		for _, r := range results {
			logf("    %s\n", graph.FormatCycleResult(r))
		}
	}

	// Print executor stats if available
	if exec != nil {
		execStats := exec.GetStats()
		logln()
		logln("  Execution Stats:")
		logf("    Total attempts: %d\n", execStats.TotalAttempts)
		logf("    Successful: %d\n", execStats.SuccessfulTrades)
		logf("    Failed: %d\n", execStats.FailedTrades)
		logf("    Dry run simulations: %d\n", execStats.DryRunSimulations)
		if execStats.TotalProfit != nil && execStats.TotalProfit.Sign() != 0 {
			logf("    Total profit: %s\n", execStats.TotalProfit.Text('f', 6))
		}
	}

	logln(strings.Repeat("-", 60))
	logln()
}

func printFinalStats(d *graph.Detector, exec *graph.CycleExecutor) {
	stats := d.Stats()

	logln()
	logln(strings.Repeat("=", 60))
	logln("Final Statistics")
	logln(strings.Repeat("=", 60))
	logln()
	logln("Detection:")
	logf("  Cycles enumerated: %d\n", stats.CyclesEnumerated)
	logf("  Price updates processed: %d\n", stats.PriceUpdates)
	logf("  Opportunities detected: %d\n", stats.OpportunitiesFound)
	logf("  Init duration: %v\n", stats.InitDuration)

	if exec != nil {
		execStats := exec.GetStats()
		logln()
		logln("Execution:")
		logf("  Total attempts: %d\n", execStats.TotalAttempts)
		logf("  Successful trades: %d\n", execStats.SuccessfulTrades)
		logf("  Failed trades: %d\n", execStats.FailedTrades)
		logf("  Dry run simulations: %d\n", execStats.DryRunSimulations)
		if execStats.TotalProfit != nil {
			logf("  Total profit: %s\n", execStats.TotalProfit.Text('f', 6))
		}
		if execStats.TotalVolume != nil {
			logf("  Total volume: %s\n", execStats.TotalVolume.Text('f', 6))
		}
		if !execStats.LastExecutionTime.IsZero() {
			logf("  Last execution: %s\n", execStats.LastExecutionTime.Format("15:04:05"))
		}
		if execStats.LastExecutionError != "" {
			logf("  Last error: %s\n", execStats.LastExecutionError)
		}
	}

	logln(strings.Repeat("=", 60))
	logln()
}

func logf(format string, args ...interface{}) {
	fmt.Fprintf(logWriter, format, args...)
}

func logln(args ...interface{}) {
	fmt.Fprintln(logWriter, args...)
}
