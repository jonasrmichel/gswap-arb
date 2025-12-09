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

	"github.com/jonasrmichel/gswap-arb/pkg/arbitrage"
	"github.com/jonasrmichel/gswap-arb/pkg/bridge"
	"github.com/jonasrmichel/gswap-arb/pkg/config"
	"github.com/jonasrmichel/gswap-arb/pkg/executor"
	"github.com/jonasrmichel/gswap-arb/pkg/inventory"
	"github.com/jonasrmichel/gswap-arb/pkg/notifier"
	"github.com/jonasrmichel/gswap-arb/pkg/providers/websocket"
	"github.com/jonasrmichel/gswap-arb/pkg/reporter"
	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

var (
	configPath       = flag.String("config", "", "Path to configuration file (JSON)")
	outputFormat     = flag.String("format", "text", "Output format: text, json, csv")
	verbose          = flag.Bool("verbose", true, "Enable verbose output")
	dryRun           = flag.Bool("dry-run", true, "Dry run mode (default: true for safety)")
	maxTradeSize     = flag.Float64("max-trade", 10.0, "Maximum trade size in quote currency")
	minProfit        = flag.Int("min-profit", 20, "Minimum profit in basis points")
	inventoryCheck   = flag.Bool("inventory", true, "Enable inventory monitoring")
	driftThreshold   = flag.Float64("drift-threshold", 20.0, "Drift threshold percentage for alerts")
	autoRebalance    = flag.Bool("auto-rebalance", false, "Enable automatic rebalancing (requires bridge config)")
	ethRPC           = flag.String("eth-rpc", "", "Ethereum RPC URL for bridging (or use ETH_RPC_URL env)")
	crossChainArb    = flag.Bool("cross-chain", false, "Enable cross-chain arbitrage detection")
	crossChainMinSpread = flag.Float64("cross-chain-min-spread", 3.0, "Minimum spread % for cross-chain arb")
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

	// Create inventory manager for balance monitoring
	var invManager *inventory.Manager
	if *inventoryCheck {
		invConfig := inventory.DefaultInventoryConfig()
		invConfig.DriftThresholdPct = *driftThreshold
		invConfig.AlertOnDrift = true

		invManager = inventory.NewManager(registry, invConfig)

		// Set up drift alert callback
		invManager.SetDriftAlertCallback(func(status *inventory.DriftStatus) {
			if *verbose {
				fmt.Printf("\n[DRIFT ALERT] %s: %.1f%% drift detected\n", status.Currency, status.MaxDriftPct)
			}

			// Send Slack notification
			alert := &notifier.DriftAlert{
				Currency:       status.Currency,
				MaxDriftPct:    status.MaxDriftPct,
				ExchangeDrifts: status.DriftPct,
				IsCritical:     status.MaxDriftPct >= invConfig.CriticalDriftPct,
			}
			if err := slackNotifier.NotifyDriftAlert(alert); err != nil {
				fmt.Printf("  [SLACK ERROR] %v\n", err)
			}
		})

		// Set up rebalance recommendation callback
		invManager.SetRebalanceCallback(func(rec *inventory.RebalanceRecommendation) {
			if *verbose {
				fmt.Printf("\n[REBALANCE] %s: Bridge %s from %s to %s\n",
					rec.Currency, rec.Amount.Text('f', 4), rec.FromExchange, rec.ToExchange)
			}

			// Send Slack notification
			priority := "LOW"
			if rec.Priority == inventory.PriorityHigh {
				priority = "HIGH"
			} else if rec.Priority == inventory.PriorityMedium {
				priority = "MEDIUM"
			}

			slackRec := &notifier.RebalanceRecommendation{
				Currency:     rec.Currency,
				FromExchange: rec.FromExchange,
				ToExchange:   rec.ToExchange,
				Amount:       rec.Amount.Text('f', 4),
				Priority:     priority,
				Reason:       rec.Reason,
			}
			if err := slackNotifier.NotifyRebalanceRecommendation(slackRec); err != nil {
				fmt.Printf("  [SLACK ERROR] %v\n", err)
			}
		})

		fmt.Printf("Inventory monitoring enabled (drift threshold: %.1f%%)\n", *driftThreshold)
	}

	// Create auto-rebalancer if enabled
	var autoRebalancer *inventory.AutoRebalancer
	if *autoRebalance && invManager != nil {
		// Get private key for bridge operations
		privateKey := os.Getenv("GSWAP_PRIVATE_KEY")
		if privateKey == "" {
			fmt.Println("Warning: Auto-rebalance enabled but GSWAP_PRIVATE_KEY not set")
		} else {
			// Get Ethereum RPC URL
			ethRPCURL := *ethRPC
			if ethRPCURL == "" {
				ethRPCURL = os.Getenv("ETH_RPC_URL")
			}

			// Create bridge executor
			bridgeExec, err := bridge.NewBridgeExecutor(&bridge.BridgeConfig{
				GalaChainPrivateKey: privateKey,
				EthereumPrivateKey:  privateKey, // Same key for both chains
				EthereumRPCURL:      ethRPCURL,
			})
			if err != nil {
				fmt.Printf("Warning: Failed to create bridge executor: %v\n", err)
			} else {
				// Create rebalancer config from settings
				rebalConfig := &inventory.RebalancerConfig{
					Enabled:                   true,
					CheckIntervalSeconds:      cfg.Rebalancing.CheckIntervalSeconds,
					BridgeStatusPollSeconds:   cfg.Rebalancing.BridgeStatusPollSeconds,
					MaxBridgeWaitMinutes:      cfg.Rebalancing.MaxBridgeWaitMinutes,
					MinTimeBetweenRebalances:  time.Duration(cfg.Rebalancing.MinTimeBetweenRebalances) * time.Second,
					MaxPendingBridges:         cfg.Rebalancing.MaxPendingBridges,
					CircuitBreakerThreshold:   cfg.Rebalancing.CircuitBreakerThreshold,
					CircuitBreakerResetTime:   time.Duration(cfg.Rebalancing.CircuitBreakerResetMins) * time.Minute,
					RequireConfirmation:       cfg.Rebalancing.RequireConfirmation,
				}

				autoRebalancer = inventory.NewAutoRebalancer(invManager, bridgeExec, rebalConfig)

				// Set up Slack callbacks for auto-rebalance events
				autoRebalancer.SetBridgeStartedCallback(func(rec *inventory.RebalanceRecommendation, result *bridge.BridgeResult) {
					if *verbose {
						fmt.Printf("\n[AUTO-REBALANCE] Started: %s %s from %s to %s\n",
							rec.Amount.Text('f', 4), rec.Currency, rec.FromExchange, rec.ToExchange)
						fmt.Printf("  Transaction: %s\n", result.TransactionID)
					}
					slackNotifier.NotifyAutoRebalanceStarted(&notifier.AutoRebalanceStarted{
						Currency:      rec.Currency,
						FromExchange:  rec.FromExchange,
						ToExchange:    rec.ToExchange,
						Amount:        rec.Amount.Text('f', 4),
						TransactionID: result.TransactionID,
						Reason:        rec.Reason,
					})
				})

				autoRebalancer.SetBridgeCompletedCallback(func(rec *inventory.RebalanceRecommendation, result *bridge.BridgeResult) {
					if *verbose {
						fmt.Printf("\n[AUTO-REBALANCE] Completed: %s %s\n", rec.Amount.Text('f', 4), rec.Currency)
					}
					feeStr := "N/A"
					if result.Fee != nil {
						feeStr = result.Fee.Text('f', 4)
					}
					slackNotifier.NotifyAutoRebalanceCompleted(&notifier.AutoRebalanceCompleted{
						Currency:      rec.Currency,
						FromExchange:  rec.FromExchange,
						ToExchange:    rec.ToExchange,
						Amount:        rec.Amount.Text('f', 4),
						TransactionID: result.TransactionID,
						Duration:      time.Since(result.CreatedAt),
						Fee:           feeStr,
					})
				})

				autoRebalancer.SetBridgeFailedCallback(func(rec *inventory.RebalanceRecommendation, err error) {
					if *verbose {
						fmt.Printf("\n[AUTO-REBALANCE] Failed: %s %s - %v\n",
							rec.Amount.Text('f', 4), rec.Currency, err)
					}
					slackNotifier.NotifyAutoRebalanceFailed(&notifier.AutoRebalanceFailed{
						Currency:     rec.Currency,
						FromExchange: rec.FromExchange,
						ToExchange:   rec.ToExchange,
						Amount:       rec.Amount.Text('f', 4),
						Error:        err.Error(),
						WillRetry:    !autoRebalancer.IsCircuitBreakerOpen(),
					})
				})

				autoRebalancer.SetCircuitOpenCallback(func(failures int) {
					if *verbose {
						fmt.Printf("\n[CIRCUIT BREAKER] OPEN - %d consecutive failures\n", failures)
					}
					slackNotifier.NotifyCircuitBreakerAlert(&notifier.CircuitBreakerAlert{
						State:            "OPEN",
						ConsecutiveFails: failures,
						Reason:           fmt.Sprintf("Auto-rebalancing paused after %d consecutive failures", failures),
					})
				})

				autoRebalancer.SetCircuitCloseCallback(func() {
					if *verbose {
						fmt.Println("\n[CIRCUIT BREAKER] CLOSED - resuming auto-rebalance")
					}
					slackNotifier.NotifyCircuitBreakerAlert(&notifier.CircuitBreakerAlert{
						State:            "CLOSED",
						ConsecutiveFails: 0,
						Reason:           "Auto-rebalancing resumed after cooldown period",
					})
				})

				// Start the auto-rebalancer
				if err := autoRebalancer.Start(ctx); err != nil {
					fmt.Printf("Warning: Failed to start auto-rebalancer: %v\n", err)
				} else {
					fmt.Println("Auto-rebalancing enabled")
					if ethRPCURL != "" {
						fmt.Println("  Bidirectional bridging available (GalaChain <-> Ethereum)")
					} else {
						fmt.Println("  GalaChain -> Ethereum bridging only (set ETH_RPC_URL for bidirectional)")
					}
				}
			}
		}
	}

	// Create cross-chain arbitrage detector if enabled
	var crossChainDetector *arbitrage.CrossChainArbitrageDetector
	if *crossChainArb || cfg.CrossChainArbitrage.Enabled {
		volatilityConfig := &arbitrage.VolatilityConfig{
			WindowMinutes:        cfg.CrossChainArbitrage.VolatilityWindowMinutes,
			MinSamples:           10,
			DefaultVolatilityBps: cfg.CrossChainArbitrage.DefaultVolatilityBps,
			ConfidenceMultiplier: cfg.CrossChainArbitrage.ConfidenceMultiplier,
			BridgeTimeToEthMin:   cfg.CrossChainArbitrage.BridgeTimeToEthMin,
			BridgeTimeToGalaMin:  cfg.CrossChainArbitrage.BridgeTimeToGalaMin,
		}

		ccConfig := &arbitrage.CrossChainConfig{
			MinSpreadPercent:         *crossChainMinSpread,
			MinRiskAdjustedProfitBps: cfg.CrossChainArbitrage.MinRiskAdjustedProfitBps,
			MaxBridgeTimeMinutes:     cfg.CrossChainArbitrage.MaxBridgeTimeMinutes,
			BridgeTimeToEthMin:       cfg.CrossChainArbitrage.BridgeTimeToEthMin,
			BridgeTimeToGalaMin:      cfg.CrossChainArbitrage.BridgeTimeToGalaMin,
			ExecutionStrategy:        cfg.CrossChainArbitrage.ExecutionStrategy,
			AllowedTokens:            cfg.CrossChainArbitrage.AllowedTokens,
		}

		crossChainDetector = arbitrage.NewCrossChainArbitrageDetector(ccConfig, arbitrage.NewVolatilityModel(volatilityConfig))
		fmt.Printf("Cross-chain arbitrage detection enabled (min spread: %.1f%%)\n", *crossChainMinSpread)
	}

	// Set up arbitrage callback with execution
	aggregator.OnArbitrage(func(opp *types.ArbitrageOpportunity) {
		rep.ReportOpportunities([]*types.ArbitrageOpportunity{opp})

		// Notify Slack about opportunity
		if err := slackNotifier.NotifyArbitrageOpportunity(opp); err != nil {
			fmt.Printf("  [SLACK ERROR] %v\n", err)
		}

		// Attempt execution if conditions are met
		if opp.NetProfitBps >= *minProfit {
			execution, err := coordinator.ExecuteOpportunity(ctx, opp)
			if err != nil {
				fmt.Printf("  [EXEC ERROR] %v\n", err)
				return
			}

			// Notify Slack about trade execution
			tradeNotif := &notifier.TradeExecution{
				Pair:         opp.Pair,
				BuyExchange:  opp.BuyExchange,
				SellExchange: opp.SellExchange,
				BuyPrice:     opp.BuyPrice.Text('f', 8),
				SellPrice:    opp.SellPrice.Text('f', 8),
				Amount:       opp.TradeSize.Text('f', 4),
				Profit:       execution.NetProfit.Text('f', 4),
				Fees:         execution.TotalFees.Text('f', 4),
				Success:      execution.Success,
				DryRun:       execution.DryRun,
				Error:        execution.Error,
			}
			if err := slackNotifier.NotifyTradeExecution(tradeNotif); err != nil {
				fmt.Printf("  [SLACK ERROR] %v\n", err)
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

		// Notify Slack about chain opportunity
		if err := slackNotifier.NotifyChainArbitrageOpportunity(opp); err != nil {
			fmt.Printf("  [SLACK ERROR] %v\n", err)
		}

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

	// Set up cross-chain arbitrage detection callbacks
	if crossChainDetector != nil {
		// Record price updates for volatility model
		aggregator.OnPriceUpdate(func(update *websocket.PriceUpdate) {
			// Calculate mid price for volatility tracking
			if update.BidPrice != nil && update.AskPrice != nil {
				midPrice, _ := new(big.Float).Quo(
					new(big.Float).Add(update.BidPrice, update.AskPrice),
					big.NewFloat(2),
				).Float64()
				if midPrice > 0 {
					crossChainDetector.GetVolatilityModel().RecordPrice(update.Pair, midPrice)
				}
			}
		})

		// Hook into the cross-chain check callback for opportunity detection
		aggregator.OnCrossChainCheck(func(pair string, prices map[string]*arbitrage.ExchangePrice, tradeSize *big.Float) {
			// Detect cross-chain opportunities
			crossChainOpps := crossChainDetector.DetectCrossChainOpportunities(pair, prices, tradeSize)

			for _, ccOpp := range crossChainOpps {
				// Report cross-chain opportunity
				if *verbose {
					report := arbitrage.FormatOpportunityReport(ccOpp)
					if report != nil {
						fmt.Println(report.FormatReportString())
					}
				}

				// Notify Slack about cross-chain opportunity
				if ccOpp.IsValid {
					bridgeDir := ""
					for _, hop := range ccOpp.Hops {
						if hop.IsBridge() {
							bridgeDir = hop.BridgeDirection
							break
						}
					}

					slackNotifier.NotifyCrossChainOpportunity(&notifier.CrossChainOpportunity{
						Pair:              ccOpp.Pair,
						BuyExchange:       ccOpp.StartExchange,
						SellExchange:      ccOpp.EndExchange,
						BridgeDirection:   bridgeDir,
						SpreadPercent:     ccOpp.SpreadPercent,
						GrossProfitBps:    ccOpp.SpreadBps,
						BridgeCostBps:     ccOpp.TotalBridgeCostBps,
						VolatilityRiskBps: ccOpp.VolatilityRiskBps,
						RiskAdjustedBps:   ccOpp.RiskAdjustedProfit,
						BridgeTimeMin:     ccOpp.TotalBridgeTimeMin,
						TradeSize:         tradeSize.Text('f', 2),
						ExpectedProfit:    ccOpp.NetProfit.Text('f', 4),
						IsRecommended:     ccOpp.IsValid,
					})
				}
			}
		})
	}

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

	// Inventory check ticker (every 5 minutes)
	var inventoryTicker *time.Ticker
	if invManager != nil {
		inventoryTicker = time.NewTicker(5 * time.Minute)
		defer inventoryTicker.Stop()

		// Do initial inventory snapshot
		go func() {
			time.Sleep(5 * time.Second) // Wait for executors to be ready
			if _, err := invManager.CollectSnapshot(ctx); err != nil {
				fmt.Printf("Warning: Initial inventory snapshot failed: %v\n", err)
			} else if *verbose {
				fmt.Println("\nInitial inventory snapshot collected")
				fmt.Println(invManager.FormatSnapshotReport())
			}
		}()
	}

	// Main loop
	for {
		select {
		case <-ctx.Done():
			// Print final stats
			rep.PrintStats()
			printExecutionStats(coordinator)

			// Print final inventory status
			if invManager != nil {
				fmt.Println("\nFinal Inventory Status:")
				fmt.Println(invManager.FormatDriftReport())
			}

			// Print auto-rebalancer status
			if autoRebalancer != nil {
				fmt.Println("\nAuto-Rebalancer Status:")
				fmt.Println(autoRebalancer.FormatStatusReport())
				autoRebalancer.Stop()
			}

			// Stop aggregator
			aggregator.Stop()

			// Close executors
			registry.Close()

			fmt.Println("Bot stopped.")
			return

		case <-statusTicker.C:
			printStatus(aggregator, coordinator, rep, invManager, autoRebalancer)

		case <-func() <-chan time.Time {
			if inventoryTicker != nil {
				return inventoryTicker.C
			}
			return make(chan time.Time) // Never fires if nil
		}():
			// Periodic inventory check
			if invManager != nil {
				if _, err := invManager.CollectSnapshot(ctx); err != nil {
					fmt.Printf("Warning: Inventory snapshot failed: %v\n", err)
				} else {
					// Check drift and generate recommendations
					invManager.CheckDrift(ctx)
					recommendations := invManager.GenerateRebalanceRecommendations()

					if *verbose && len(recommendations) > 0 {
						fmt.Println(invManager.FormatRecommendationsReport(recommendations))
					}
				}
			}
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
func printStatus(agg *websocket.PriceAggregator, coord *executor.ArbitrageCoordinator, rep *reporter.Reporter, invManager *inventory.Manager, autoRebalancer *inventory.AutoRebalancer) {
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

	// Inventory status
	if invManager != nil {
		invStats := invManager.GetStats()
		driftStatus := invManager.GetDriftStatus()

		driftWarnings := 0
		for _, ds := range driftStatus {
			if ds.NeedsRebalance {
				driftWarnings++
			}
		}

		fmt.Printf("  Inventory: %d snapshots, %d drift alerts, %d rebalances recommended\n",
			invStats.SnapshotsCollected, invStats.DriftAlertsTriggered, invStats.RebalancesRecommended)
		if driftWarnings > 0 {
			fmt.Printf("  [!] %d currencies with drift above threshold\n", driftWarnings)
		}
	}

	// Auto-rebalancer status
	if autoRebalancer != nil {
		rebalStats := autoRebalancer.GetStats()
		pending := autoRebalancer.GetPendingBridge()
		circuitOpen := autoRebalancer.IsCircuitBreakerOpen()

		rebalStatus := "idle"
		if circuitOpen {
			rebalStatus = "CIRCUIT OPEN"
		} else if pending != nil {
			rebalStatus = fmt.Sprintf("bridging %s", pending.Recommendation.Currency)
		}

		fmt.Printf("  Auto-rebalance: %s (triggered=%d, completed=%d, failed=%d)\n",
			rebalStatus, rebalStats.RebalancesTriggered, rebalStats.RebalancesCompleted, rebalStats.RebalancesFailed)
	}

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
