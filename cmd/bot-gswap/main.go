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
)

var (
	apiURL         = flag.String("api", "https://arb.gala.com/api", "GSwap API URL")
	maxCycleLength = flag.Int("max-cycle", 5, "Maximum cycle length to detect")
	minProfitBps   = flag.Int("min-profit", 10, "Minimum profit in basis points")
	defaultFeeBps  = flag.Int("fee", 100, "Default fee in basis points (1% = 100)")
	pollInterval   = flag.Duration("poll", 5*time.Second, "Price polling interval")
	verbose        = flag.Bool("verbose", true, "Enable verbose output")
	logFile        = flag.String("log", "bot-gswap.log", "Log file path (empty to disable)")
	showGraph      = flag.Bool("show-graph", false, "Print graph summary at startup")
	showCycles     = flag.Bool("show-cycles", false, "Print all cycles at startup")
	continuous     = flag.Bool("continuous", true, "Run continuously (poll for prices)")
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

	// Set up opportunity callback
	detector.SetOpportunityCallback(func(result *graph.CycleResult) {
		logln()
		logln(strings.Repeat("*", 60))
		logf("[OPPORTUNITY] %s\n", time.Now().Format("15:04:05"))
		logf("  Cycle: %s\n", graph.FormatCycleResult(result))
		logf("  Profit: %d bps (%.2f%%)\n", result.ProfitBps, float64(result.ProfitBps)/100)
		logf("  Profit Ratio: %.6f\n", result.ProfitRatio)
		logf("  Min Liquidity: %.2f\n", result.MinLiquidity)

		// Simulate a trade with 100 units
		if result.Cycle != nil {
			output, profit, fees, err := detector.SimulateCycleTrade(result.Cycle.ID, 100.0)
			if err == nil {
				logf("  Simulation (100 units): output=%.4f, profit=%.4f, fees=%.4f\n",
					output, profit, fees)
			}
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
		return
	}

	// Start continuous polling
	logln("Starting continuous price monitoring...")
	logf("  Poll interval: %v\n", *pollInterval)
	logf("  Min profit threshold: %d bps\n", *minProfitBps)
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
			printFinalStats(detector)
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
			printStatus(detector)
		}
	}
}

func printBanner() {
	logln()
	logln("=============================================================")
	logln("       GSwap Graph-Based Arbitrage Detector                  ")
	logln("                                                             ")
	logln("   Finds profitable cycles within GalaChain DEX (GSwap)      ")
	logln("=============================================================")
	logln()
}

func printConfig(cfg *graph.Config) {
	logln("Configuration:")
	logf("  - API URL: %s\n", cfg.APIURL)
	logf("  - Max cycle length: %d\n", cfg.MaxCycleLength)
	logf("  - Min profit: %d bps (%.2f%%)\n", cfg.MinProfitBps, float64(cfg.MinProfitBps)/100)
	logf("  - Default fee: %d bps (%.2f%%)\n", cfg.DefaultFeeBps, float64(cfg.DefaultFeeBps)/100)
	logf("  - Poll interval: %v\n", cfg.PollInterval)
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

func printStatus(d *graph.Detector) {
	stats := d.Stats()

	logln()
	logln(strings.Repeat("-", 50))
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

	logln(strings.Repeat("-", 50))
	logln()
}

func printFinalStats(d *graph.Detector) {
	stats := d.Stats()

	logln()
	logln(strings.Repeat("=", 50))
	logln("Final Statistics")
	logln(strings.Repeat("=", 50))
	logf("  Cycles enumerated: %d\n", stats.CyclesEnumerated)
	logf("  Price updates processed: %d\n", stats.PriceUpdates)
	logf("  Opportunities detected: %d\n", stats.OpportunitiesFound)
	logf("  Init duration: %v\n", stats.InitDuration)
	logln(strings.Repeat("=", 50))
	logln()
}

func logf(format string, args ...interface{}) {
	fmt.Fprintf(logWriter, format, args...)
}

func logln(args ...interface{}) {
	fmt.Fprintln(logWriter, args...)
}
