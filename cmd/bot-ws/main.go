// Package main is the entry point for the WebSocket-based arbitrage bot.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/config"
	"github.com/jonasrmichel/gswap-arb/pkg/notifier"
	"github.com/jonasrmichel/gswap-arb/pkg/providers/websocket"
	"github.com/jonasrmichel/gswap-arb/pkg/reporter"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

var (
	configPath   = flag.String("config", "", "Path to configuration file (JSON)")
	outputFormat = flag.String("format", "text", "Output format: text, json, csv")
	verbose      = flag.Bool("verbose", true, "Enable verbose output")
	showUpdates  = flag.Bool("show-updates", false, "Show all price updates (very verbose)")
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

	// Create WebSocket aggregator
	aggConfig := &websocket.AggregatorConfig{
		MinSpreadBps:    cfg.Arbitrage.MinSpreadBps,
		MinNetProfitBps: cfg.Arbitrage.MinNetProfitBps,
		StaleThreshold:  10 * time.Second,
	}
	aggregator := websocket.NewPriceAggregator(aggConfig)

	// Add WebSocket providers based on configuration
	setupWebSocketProviders(aggregator, cfg)

	// Print configuration summary
	printConfig(cfg, aggregator)

	// Create Slack notifier
	slackNotifier := notifier.NewSlackNotifier(&notifier.SlackConfig{
		APIToken: cfg.Slack.APIToken,
		Channel:  cfg.Slack.Channel,
		Enabled:  cfg.Slack.Enabled,
	})

	if slackNotifier.IsEnabled() {
		fmt.Println("Slack notifications enabled")
		if err := slackNotifier.SendTestMessage(); err != nil {
			fmt.Printf("Warning: Failed to send Slack test message: %v\n", err)
		}
	}

	// Set up callbacks
	if *showUpdates {
		aggregator.OnPriceUpdate(func(update *websocket.PriceUpdate) {
			fmt.Printf("[%s] %s %s: Bid=%s Ask=%s\n",
				time.Now().Format("15:04:05.000"),
				update.Exchange,
				update.Pair,
				formatBigFloat(update.BidPrice),
				formatBigFloat(update.AskPrice),
			)
		})
	}

	// Opportunity counters for batching reports
	var opportunities []*types.ArbitrageOpportunity
	var chainOpportunities []*types.ChainArbitrageOpportunity
	var lastReport time.Time
	var lastChainReport time.Time
	reportInterval := 5 * time.Second

	aggregator.OnArbitrage(func(opp *types.ArbitrageOpportunity) {
		opportunities = append(opportunities, opp)

		// Notify Slack about opportunity
		if err := slackNotifier.NotifyArbitrageOpportunity(opp); err != nil {
			fmt.Printf("  [SLACK ERROR] %v\n", err)
		}

		// Report immediately for high-value opportunities
		if opp.SpreadBps >= 100 || time.Since(lastReport) >= reportInterval {
			rep.ReportOpportunities(opportunities)
			opportunities = nil
			lastReport = time.Now()
		}
	})

	// Chain arbitrage callback
	aggregator.OnChainArbitrage(func(opp *types.ChainArbitrageOpportunity) {
		chainOpportunities = append(chainOpportunities, opp)

		// Notify Slack about chain opportunity
		if err := slackNotifier.NotifyChainArbitrageOpportunity(opp); err != nil {
			fmt.Printf("  [SLACK ERROR] %v\n", err)
		}

		// Report immediately for high-value opportunities or after interval
		if opp.SpreadBps >= 100 || time.Since(lastChainReport) >= reportInterval {
			rep.ReportChainOpportunities(chainOpportunities)
			chainOpportunities = nil
			lastChainReport = time.Now()
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

	if !aggregator.IsAllConnected() {
		fmt.Println("Warning: Not all exchanges connected")
	}

	fmt.Println("Listening for arbitrage opportunities...")
	fmt.Println("Press Ctrl+C to stop")
	fmt.Println()

	// Status ticker
	statusTicker := time.NewTicker(30 * time.Second)
	defer statusTicker.Stop()

	// Main loop
	for {
		select {
		case <-ctx.Done():
			// Report any remaining opportunities
			if len(opportunities) > 0 {
				rep.ReportOpportunities(opportunities)
			}
			if len(chainOpportunities) > 0 {
				rep.ReportChainOpportunities(chainOpportunities)
			}

			// Print final stats
			rep.PrintStats()

			// Stop aggregator
			aggregator.Stop()
			fmt.Println("Bot stopped.")
			return

		case <-statusTicker.C:
			printStatus(aggregator, rep)
		}
	}
}

// printBanner prints the application banner.
func printBanner() {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║       GSwap Arbitrage Bot - WebSocket Real-Time Mode      ║")
	fmt.Println("║                                                           ║")
	fmt.Println("║   Real-time price feeds from major centralized exchanges  ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()
}

// printConfig prints the current configuration.
func printConfig(cfg *config.Config, agg *websocket.PriceAggregator) {
	fmt.Println("Configuration:")
	fmt.Printf("  - Min spread: %d bps (%.2f%%)\n", cfg.Arbitrage.MinSpreadBps, float64(cfg.Arbitrage.MinSpreadBps)/100)
	fmt.Printf("  - Min net profit: %d bps (%.2f%%)\n", cfg.Arbitrage.MinNetProfitBps, float64(cfg.Arbitrage.MinNetProfitBps)/100)
	fmt.Println()

	status := agg.GetConnectionStatus()
	fmt.Println("WebSocket providers:")
	for name := range status {
		fmt.Printf("  - %s\n", name)
	}
	fmt.Println()
}

// setupWebSocketProviders adds WebSocket providers based on configuration.
func setupWebSocketProviders(agg *websocket.PriceAggregator, cfg *config.Config) {
	for _, ex := range cfg.GetEnabledExchanges() {
		switch ex.ID {
		case "gswap":
			// GSwap uses REST polling (no WebSocket available)
			agg.AddProvider(websocket.NewGSwapPollerProvider(5 * time.Second))
			fmt.Printf("Added GSwap polling provider (5s interval)\n")
		case "binance":
			agg.AddProvider(websocket.NewBinanceWSProvider())
			fmt.Printf("Added Binance WebSocket provider\n")
		case "coinbase":
			agg.AddProvider(websocket.NewCoinbaseWSProvider())
			fmt.Printf("Added Coinbase WebSocket provider\n")
		case "kraken":
			agg.AddProvider(websocket.NewKrakenWSProvider())
			fmt.Printf("Added Kraken WebSocket provider\n")
		case "okx":
			agg.AddProvider(websocket.NewOKXWSProvider())
			fmt.Printf("Added OKX WebSocket provider\n")
		case "bybit":
			agg.AddProvider(websocket.NewBybitWSProvider())
			fmt.Printf("Added Bybit WebSocket provider\n")
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
func printStatus(agg *websocket.PriceAggregator, rep *reporter.Reporter) {
	stats := rep.GetStats()

	fmt.Println()
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("[%s] Status Update\n", time.Now().Format("15:04:05"))
	fmt.Printf("  Uptime: %s\n", time.Since(stats.StartTime).Round(time.Second))
	fmt.Printf("  Opportunities found: %d (profitable: %d)\n",
		stats.OpportunitiesFound, stats.ProfitableOpportunities)

	// Connection status
	status := agg.GetConnectionStatus()
	fmt.Print("  Connections: ")
	parts := make([]string, 0, len(status))
	for name, state := range status {
		parts = append(parts, fmt.Sprintf("%s=%s", name, state))
	}
	fmt.Println(strings.Join(parts, ", "))

	// Latest prices summary
	prices := agg.GetLatestPrices()
	if len(prices) > 0 {
		fmt.Println("  Latest prices:")
		for pair, exchanges := range prices {
			fmt.Printf("    %s: ", pair)
			priceParts := make([]string, 0, len(exchanges))
			for ex, update := range exchanges {
				age := time.Since(update.Timestamp).Round(time.Millisecond)
				priceParts = append(priceParts, fmt.Sprintf("%s=%s (%.0fms ago)",
					ex, formatBigFloat(update.BidPrice), float64(age.Milliseconds())))
			}
			fmt.Println(strings.Join(priceParts, ", "))
		}
	}

	fmt.Println(strings.Repeat("-", 60))
	fmt.Println()
}

func formatBigFloat(f interface{}) string {
	if f == nil {
		return "N/A"
	}
	return fmt.Sprintf("%v", f)
}
