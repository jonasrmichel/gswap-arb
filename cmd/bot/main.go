// Package main is the entry point for the arbitrage bot.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/arbitrage"
	"github.com/jonasrmichel/gswap-arb/pkg/config"
	"github.com/jonasrmichel/gswap-arb/pkg/providers"
	"github.com/jonasrmichel/gswap-arb/pkg/providers/cex"
	"github.com/jonasrmichel/gswap-arb/pkg/providers/gswap"
	"github.com/jonasrmichel/gswap-arb/pkg/reporter"
)

var (
	configPath   = flag.String("config", "", "Path to configuration file (JSON)")
	outputFormat = flag.String("format", "text", "Output format: text, json, csv")
	verbose      = flag.Bool("verbose", true, "Enable verbose output")
	dryRun       = flag.Bool("dry-run", true, "Dry run mode (no actual trades)")
	once         = flag.Bool("once", false, "Run once and exit")
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

	// Apply command line overrides
	cfg.Verbose = *verbose
	cfg.DryRun = *dryRun

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}

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

	// Initialize provider registry
	registry := providers.NewProviderRegistry()

	// Setup providers based on configuration
	if err := setupProviders(ctx, registry, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up providers: %v\n", err)
		os.Exit(1)
	}

	// Create arbitrage detector
	detector := arbitrage.NewDetector(registry, cfg.ToArbitrageConfig())

	// Setup tracked pairs
	for _, pair := range cfg.GetEnabledPairs() {
		detector.TrackPair(pair.Pair, pair.Exchanges)
	}

	// Create reporter
	format := reporter.FormatText
	switch *outputFormat {
	case "json":
		format = reporter.FormatJSON
	case "csv":
		format = reporter.FormatCSV
	}
	rep := reporter.NewReporter(os.Stdout, format, cfg.Verbose)

	// Print configuration summary
	printConfig(cfg)

	// Run the bot
	if *once {
		runOnce(ctx, detector, rep)
	} else {
		runLoop(ctx, detector, rep, cfg)
	}

	// Print final stats
	rep.PrintStats()

	// Cleanup
	registry.CloseAll()
	fmt.Println("Bot stopped.")
}

// printBanner prints the application banner.
func printBanner() {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║           GSwap Arbitrage Bot - Opportunity Detector      ║")
	fmt.Println("║                                                           ║")
	fmt.Println("║   Monitors GalaChain/GSwap and major CEXs for arbitrage   ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()
}

// printConfig prints the current configuration.
func printConfig(cfg *config.Config) {
	fmt.Println("Configuration:")
	fmt.Printf("  - Update interval: %v\n", cfg.GetUpdateInterval())
	fmt.Printf("  - Min spread: %d bps (%.2f%%)\n", cfg.Arbitrage.MinSpreadBps, float64(cfg.Arbitrage.MinSpreadBps)/100)
	fmt.Printf("  - Min net profit: %d bps (%.2f%%)\n", cfg.Arbitrage.MinNetProfitBps, float64(cfg.Arbitrage.MinNetProfitBps)/100)
	fmt.Printf("  - Default trade size: $%.2f\n", cfg.Arbitrage.DefaultTradeSize)
	fmt.Printf("  - Dry run: %v\n", cfg.DryRun)
	fmt.Println()

	fmt.Println("Enabled exchanges:")
	for _, ex := range cfg.GetEnabledExchanges() {
		fmt.Printf("  - %s (%s)\n", ex.Name, ex.Type)
	}
	fmt.Println()

	fmt.Println("Tracked pairs:")
	for _, pair := range cfg.GetEnabledPairs() {
		fmt.Printf("  - %s on %v\n", pair.Pair, pair.Exchanges)
	}
	fmt.Println()
	fmt.Println("Starting bot...")
	fmt.Println()
}

// setupProviders initializes all exchange providers.
func setupProviders(ctx context.Context, registry *providers.ProviderRegistry, cfg *config.Config) error {
	for _, ex := range cfg.GetEnabledExchanges() {
		var provider providers.PriceProvider

		switch ex.Type {
		case "dex":
			if ex.ID == "gswap" {
				gswapProvider := gswap.NewGSwapProvider()
				if err := gswapProvider.Initialize(ctx); err != nil {
					fmt.Printf("Warning: Failed to initialize GSwap: %v\n", err)
					continue
				}
				provider = gswapProvider
			}

		case "cex":
			var cexConfig cex.ExchangeConfig
			switch ex.ID {
			case "binance":
				cexConfig = cex.BinanceConfig
			case "coinbase":
				cexConfig = cex.CoinbaseConfig
			case "kraken":
				cexConfig = cex.KrakenConfig
			case "okx":
				cexConfig = cex.OKXConfig
			case "bybit":
				cexConfig = cex.BybitConfig
			case "kucoin":
				cexConfig = cex.KucoinConfig
			case "gate":
				cexConfig = cex.GateConfig
			default:
				fmt.Printf("Warning: Unknown exchange %s\n", ex.ID)
				continue
			}

			// Apply API keys from config
			cexConfig.APIKey = ex.APIKey
			cexConfig.Secret = ex.Secret

			cexProvider := cex.NewCEXProvider(cexConfig)
			if err := cexProvider.Initialize(ctx); err != nil {
				fmt.Printf("Warning: Failed to initialize %s: %v\n", ex.Name, err)
				continue
			}

			// Add pairs for this exchange
			for _, pair := range cfg.GetEnabledPairs() {
				for _, pairEx := range pair.Exchanges {
					if pairEx == ex.ID {
						parts := splitPair(pair.Pair)
						if len(parts) == 2 {
							cexProvider.AddPair(parts[0], parts[1])
						}
					}
				}
			}

			provider = cexProvider
		}

		if provider != nil {
			registry.Register(provider)
			fmt.Printf("Initialized provider: %s\n", provider.Name())
		}
	}

	return nil
}

// runOnce runs a single detection cycle.
func runOnce(ctx context.Context, detector *arbitrage.Detector, rep *reporter.Reporter) {
	fmt.Println("Running single detection cycle...")

	opportunities, err := detector.DetectOpportunities(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error detecting opportunities: %v\n", err)
		rep.RecordError()
		return
	}

	// Filter for profitable opportunities
	profitable := arbitrage.FilterProfitableOpportunities(opportunities, 0)
	rep.ReportOpportunities(profitable)
}

// runLoop runs the detection loop until context is cancelled.
func runLoop(ctx context.Context, detector *arbitrage.Detector, rep *reporter.Reporter, cfg *config.Config) {
	ticker := time.NewTicker(cfg.GetUpdateInterval())
	defer ticker.Stop()

	// Run immediately on start
	runCycle(ctx, detector, rep)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCycle(ctx, detector, rep)
		}
	}
}

// runCycle runs a single detection cycle.
func runCycle(ctx context.Context, detector *arbitrage.Detector, rep *reporter.Reporter) {
	start := time.Now()

	opportunities, err := detector.DetectOpportunities(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] Error: %v\n", time.Now().Format("15:04:05"), err)
		rep.RecordError()
		return
	}

	// Filter for profitable opportunities
	profitable := arbitrage.FilterProfitableOpportunities(opportunities, 0)
	rep.ReportOpportunities(profitable)

	duration := time.Since(start)
	if duration > time.Second {
		fmt.Printf("[%s] Cycle completed in %v\n", time.Now().Format("15:04:05"), duration.Round(time.Millisecond))
	}
}

// splitPair splits a trading pair into base and quote.
func splitPair(pair string) []string {
	for _, sep := range []string{"/", "-", "_"} {
		if idx := len(pair) / 2; idx > 0 {
			for i := range pair {
				if string(pair[i]) == sep {
					return []string{pair[:i], pair[i+1:]}
				}
			}
		}
	}
	return nil
}
