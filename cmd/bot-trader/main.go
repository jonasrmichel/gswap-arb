// Package main is the entry point for the trading bot with execution capability.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
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
	configPath          = flag.String("config", "", "Path to configuration file (JSON)")
	outputFormat        = flag.String("format", "text", "Output format: text, json, csv")
	verbose             = flag.Bool("verbose", true, "Enable verbose output")
	dryRun              = flag.Bool("dry-run", true, "Dry run mode (default: true for safety)")
	maxTradeSize        = flag.Float64("max-trade", 10.0, "Maximum trade size in quote currency")
	minProfit           = flag.Int("min-profit", 20, "Minimum profit in basis points")
	inventoryCheck      = flag.Bool("inventory", true, "Enable inventory monitoring")
	driftThreshold      = flag.Float64("drift-threshold", 20.0, "Drift threshold percentage for alerts")
	autoRebalance       = flag.Bool("auto-rebalance", false, "Enable automatic rebalancing (requires bridge config)")
	ethRPC              = flag.String("eth-rpc", "", "Ethereum RPC URL for bridging (or use ETH_RPC_URL env)")
	crossChainArb       = flag.Bool("cross-chain", false, "Enable cross-chain arbitrage detection")
	crossChainMinSpread = flag.Float64("cross-chain-min-spread", 3.0, "Minimum spread % for cross-chain arb")
	logFile             = flag.String("log", "bot-trader.log", "Log file path (empty to disable file logging)")
)

// logWriter is a global writer that outputs to both stdout and log file
var logWriter io.Writer

func main() {
	flag.Parse()

	// Set up logging to both stdout and file
	var logFileHandle *os.File
	if *logFile != "" {
		var err error
		logFileHandle, err = os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to open log file %s: %v\n", *logFile, err)
			logWriter = os.Stdout
		} else {
			// Write to both stdout and log file
			logWriter = io.MultiWriter(os.Stdout, logFileHandle)
			log.SetOutput(logWriter)
		}
	} else {
		logWriter = os.Stdout
	}

	// Ensure log file is closed on exit
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

	// Create reporter (outputs to both stdout and log file)
	format := reporter.FormatText
	switch *outputFormat {
	case "json":
		format = reporter.FormatJSON
	case "csv":
		format = reporter.FormatCSV
	}
	rep := reporter.NewReporter(logWriter, format, cfg.Verbose)

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
		logln("Slack notifications enabled")
		if err := slackNotifier.SendTestMessage(); err != nil {
			logf("Warning: Failed to send Slack test message: %v\n", err)
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
				logf("\n[DRIFT ALERT] %s: %.1f%% drift detected\n", status.Currency, status.MaxDriftPct)
			}

			// Send Slack notification
			alert := &notifier.DriftAlert{
				Currency:       status.Currency,
				MaxDriftPct:    status.MaxDriftPct,
				ExchangeDrifts: status.DriftPct,
				IsCritical:     status.MaxDriftPct >= invConfig.CriticalDriftPct,
			}
			if err := slackNotifier.NotifyDriftAlert(alert); err != nil {
				logf("  [SLACK ERROR] %v\n", err)
			}
		})

		// Set up rebalance recommendation callback
		invManager.SetRebalanceCallback(func(rec *inventory.RebalanceRecommendation) {
			if *verbose {
				logf("\n[REBALANCE] %s: Bridge %s from %s to %s\n",
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
				logf("  [SLACK ERROR] %v\n", err)
			}
		})

		logf("Inventory monitoring enabled (drift threshold: %.1f%%)\n", *driftThreshold)
	}

	// Create auto-rebalancer if enabled
	var autoRebalancer *inventory.AutoRebalancer
	if *autoRebalance && invManager != nil {
		// Get private key for bridge operations
		privateKey := os.Getenv("GSWAP_PRIVATE_KEY")
		if privateKey == "" {
			logln("Warning: Auto-rebalance enabled but GSWAP_PRIVATE_KEY not set")
		} else {
			// Get Ethereum RPC URL
			ethRPCURL := *ethRPC
			if ethRPCURL == "" {
				ethRPCURL = os.Getenv("ETH_RPC_URL")
			}

			// Get Solana bridge config if enabled
			solanaBridgeProgramID := os.Getenv("SOLANA_BRIDGE_PROGRAM_ID")
			if solanaBridgeProgramID == "" {
				solanaBridgeProgramID = bridge.DefaultSolanaBridgeProgramID
			}

			// Get bridge wallet address (may differ from GSwap executor due to checksum requirements)
			bridgeWalletAddr := os.Getenv("GALACHAIN_BRIDGE_WALLET_ADDRESS")

			// Create bridge executor with Solana support
			bridgeExec, err := bridge.NewBridgeExecutor(&bridge.BridgeConfig{
				GalaChainPrivateKey:   privateKey,
				GalaChainAddress:      bridgeWalletAddr, // Use specific address if provided
				EthereumPrivateKey:    privateKey,       // Same key for both chains
				EthereumRPCURL:        ethRPCURL,
				SolanaPrivateKey:      cfg.Solana.PrivateKey,
				SolanaWalletAddress:   cfg.Solana.WalletAddress,
				SolanaRPCURL:          cfg.Solana.RPCURL,
				SolanaBridgeProgramID: solanaBridgeProgramID,
			})
			if err != nil {
				logf("Warning: Failed to create bridge executor: %v\n", err)
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
						logf("\n[AUTO-REBALANCE] Started: %s %s from %s to %s\n",
							rec.Amount.Text('f', 4), rec.Currency, rec.FromExchange, rec.ToExchange)
						logf("  Transaction: %s\n", result.TransactionID)
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
						logf("\n[AUTO-REBALANCE] Completed: %s %s\n", rec.Amount.Text('f', 4), rec.Currency)
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
						logf("\n[AUTO-REBALANCE] Failed: %s %s - %v\n",
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
						logf("\n[CIRCUIT BREAKER] OPEN - %d consecutive failures\n", failures)
					}
					slackNotifier.NotifyCircuitBreakerAlert(&notifier.CircuitBreakerAlert{
						State:            "OPEN",
						ConsecutiveFails: failures,
						Reason:           fmt.Sprintf("Auto-rebalancing paused after %d consecutive failures", failures),
					})
				})

				autoRebalancer.SetCircuitCloseCallback(func() {
					if *verbose {
						logln("\n[CIRCUIT BREAKER] CLOSED - resuming auto-rebalance")
					}
					slackNotifier.NotifyCircuitBreakerAlert(&notifier.CircuitBreakerAlert{
						State:            "CLOSED",
						ConsecutiveFails: 0,
						Reason:           "Auto-rebalancing resumed after cooldown period",
					})
				})

				// Start the auto-rebalancer
				if err := autoRebalancer.Start(ctx); err != nil {
					logf("Warning: Failed to start auto-rebalancer: %v\n", err)
				} else {
					logln("Auto-rebalancing enabled")
					if ethRPCURL != "" {
						logln("  Bidirectional bridging available (GalaChain <-> Ethereum)")
					} else {
						logln("  GalaChain -> Ethereum bridging only (set ETH_RPC_URL for bidirectional)")
					}
					if cfg.Solana.Enabled && cfg.Solana.PrivateKey != "" {
						logln("  Solana bridging available (GalaChain <-> Solana)")
					}
				}
			}
		}
	}

	// Create cross-chain arbitrage detector if enabled
	var crossChainDetector *arbitrage.CrossChainArbitrageDetector
	if *crossChainArb || cfg.CrossChainArbitrage.Enabled {
		volatilityConfig := &arbitrage.VolatilityConfig{
			WindowMinutes:          cfg.CrossChainArbitrage.VolatilityWindowMinutes,
			MinSamples:             10,
			DefaultVolatilityBps:   cfg.CrossChainArbitrage.DefaultVolatilityBps,
			ConfidenceMultiplier:   cfg.CrossChainArbitrage.ConfidenceMultiplier,
			BridgeTimeToEthMin:     cfg.CrossChainArbitrage.BridgeTimeToEthMin,
			BridgeTimeToGalaMin:    cfg.CrossChainArbitrage.BridgeTimeToGalaMin,
			BridgeTimeToSolanaMin:  cfg.CrossChainArbitrage.BridgeTimeToSolanaMin,
			BridgeTimeFromSolanaMin: cfg.CrossChainArbitrage.BridgeTimeFromSolanaMin,
		}

		ccConfig := &arbitrage.CrossChainConfig{
			MinSpreadPercent:         *crossChainMinSpread,
			MinRiskAdjustedProfitBps: cfg.CrossChainArbitrage.MinRiskAdjustedProfitBps,
			MaxBridgeTimeMinutes:     cfg.CrossChainArbitrage.MaxBridgeTimeMinutes,
			BridgeTimeToEthMin:       cfg.CrossChainArbitrage.BridgeTimeToEthMin,
			BridgeTimeToGalaMin:      cfg.CrossChainArbitrage.BridgeTimeToGalaMin,
			BridgeTimeToSolanaMin:    cfg.CrossChainArbitrage.BridgeTimeToSolanaMin,
			BridgeTimeFromSolanaMin:  cfg.CrossChainArbitrage.BridgeTimeFromSolanaMin,
			ExecutionStrategy:        cfg.CrossChainArbitrage.ExecutionStrategy,
			AllowedTokens:            cfg.CrossChainArbitrage.AllowedTokens,
		}

		crossChainDetector = arbitrage.NewCrossChainArbitrageDetector(ccConfig, arbitrage.NewVolatilityModel(volatilityConfig))
		logf("Cross-chain arbitrage detection enabled (min spread: %.1f%%)\n", *crossChainMinSpread)
		if cfg.CrossChainArbitrage.BridgeEnabled {
			logln("  Bridge execution: ENABLED")
		} else {
			logln("  Bridge execution: DISABLED (detection only)")
		}
		if cfg.Solana.Enabled {
			logln("  Solana cross-chain arbitrage enabled (GalaChain <-> Solana)")
		}
	}

	// Set up arbitrage callback with execution
	aggregator.OnArbitrage(func(opp *types.ArbitrageOpportunity) {
		rep.ReportOpportunities([]*types.ArbitrageOpportunity{opp})

		// Notify Slack about opportunity
		if err := slackNotifier.NotifyArbitrageOpportunity(opp); err != nil {
			logf("  [SLACK ERROR] %v\n", err)
		}

		// Attempt execution if conditions are met
		if opp.NetProfitBps >= *minProfit {
			execution, err := coordinator.ExecuteOpportunity(ctx, opp)
			if err != nil {
				logf("  [EXEC ERROR] %v\n", err)
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
				logf("  [SLACK ERROR] %v\n", err)
			}

			if execution.DryRun {
				logf("  [DRY RUN] Would execute: %s -> %s, estimated profit: %s\n",
					opp.BuyExchange, opp.SellExchange,
					execution.NetProfit.Text('f', 4))
			} else if execution.Success {
				logf("  [EXECUTED] Profit: %s, Fees: %s\n",
					execution.NetProfit.Text('f', 4),
					execution.TotalFees.Text('f', 4))
			} else {
				logf("  [SKIPPED] %s\n", execution.Error)
			}
		}
	})

	// Chain arbitrage callback
	aggregator.OnChainArbitrage(func(opp *types.ChainArbitrageOpportunity) {
		rep.ReportChainOpportunities([]*types.ChainArbitrageOpportunity{opp})

		// Notify Slack about chain opportunity
		if err := slackNotifier.NotifyChainArbitrageOpportunity(opp); err != nil {
			logf("  [SLACK ERROR] %v\n", err)
		}

		if opp.NetProfitBps >= *minProfit {
			execution, err := coordinator.ExecuteChainOpportunity(ctx, opp)
			if err != nil {
				logf("  [CHAIN EXEC ERROR] %v\n", err)
				return
			}

			if execution.DryRun {
				logf("  [CHAIN DRY RUN] Would execute: %v, estimated profit: %s\n",
					opp.Chain, execution.NetProfit.Text('f', 4))
			} else if execution.Success {
				logf("  [CHAIN EXECUTED] Profit: %s\n",
					execution.NetProfit.Text('f', 4))
			} else {
				logf("  [CHAIN SKIPPED] %s\n", execution.Error)
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
						logln(report.FormatReportString())
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
						BridgeEnabled:     cfg.CrossChainArbitrage.BridgeEnabled,
					})
				}
			}
		})
	}

	// Get pairs to subscribe
	pairs := getPairs(cfg)
	if len(pairs) == 0 {
		logln("No pairs configured")
		os.Exit(1)
	}

	logf("Subscribing to %d pairs...\n", len(pairs))
	for _, pair := range pairs {
		logf("  - %s\n", pair)
	}
	logln()

	// Start the aggregator
	if err := aggregator.Start(ctx, pairs); err != nil {
		logf("Failed to start aggregator: %v\n", err)
		os.Exit(1)
	}

	// Wait for connections
	logln("Connecting to exchanges...")
	time.Sleep(2 * time.Second)

	// Print connection status
	status := aggregator.GetConnectionStatus()
	logln("\nConnection status:")
	for name, state := range status {
		logf("  - %s: %s\n", name, state)
	}
	logln()

	logln("Listening for arbitrage opportunities...")
	if *dryRun {
		logln("** DRY RUN MODE - No actual trades will be executed **")
	} else {
		logln("** LIVE TRADING MODE - Real trades will be executed **")
	}
	logln("Press Ctrl+C to stop")
	logln()

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
				logf("Warning: Initial inventory snapshot failed: %v\n", err)
			} else if *verbose {
				logln("\nInitial inventory snapshot collected")
				logln(invManager.FormatSnapshotReport())
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
				logln("\nFinal Inventory Status:")
				logln(invManager.FormatDriftReport())
			}

			// Print auto-rebalancer status
			if autoRebalancer != nil {
				logln("\nAuto-Rebalancer Status:")
				logln(autoRebalancer.FormatStatusReport())
				autoRebalancer.Stop()
			}

			// Stop aggregator
			aggregator.Stop()

			// Close executors
			registry.Close()

			logln("Bot stopped.")
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
					logf("Warning: Inventory snapshot failed: %v\n", err)
				} else {
					// Check drift and generate recommendations
					invManager.CheckDrift(ctx)
					recommendations := invManager.GenerateRebalanceRecommendations()

					if *verbose && len(recommendations) > 0 {
						logln(invManager.FormatRecommendationsReport(recommendations))
					}
				}
			}
		}
	}
}

// printBanner prints the application banner.
func printBanner() {
	logln()
	logln("╔═══════════════════════════════════════════════════════════╗")
	logln("║       GSwap Arbitrage Bot - Trading Mode                  ║")
	logln("║                                                           ║")
	logln("║   Real-time arbitrage detection AND execution             ║")
	logln("╚═══════════════════════════════════════════════════════════╝")
	logln()
}

// printConfig prints the current configuration.
func printConfig(cfg *config.Config, coord *executor.ArbitrageCoordinator) {
	logln("Configuration:")
	logf("  - Min spread: %d bps (%.2f%%)\n", cfg.Arbitrage.MinSpreadBps, float64(cfg.Arbitrage.MinSpreadBps)/100)
	logf("  - Min net profit: %d bps (%.2f%%)\n", cfg.Arbitrage.MinNetProfitBps, float64(cfg.Arbitrage.MinNetProfitBps)/100)
	logf("  - Dry run: %v\n", coord.IsDryRun())
	logln()
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
				logf("Skipping GSwap executor: Private key not configured\n")
				continue
			}
			gswapExec, err := executor.NewGSwapExecutor(ex.PrivateKey, ex.WalletAddress)
			if err != nil {
				logf("Warning: Failed to create GSwap executor: %v\n", err)
				lastErr = err
				continue
			}
			if err := gswapExec.Initialize(ctx); err != nil {
				logf("Warning: Failed to initialize GSwap executor: %v\n", err)
				lastErr = err
				continue
			}
			registry.Register(gswapExec)
			logf("Initialized GSwap executor (wallet: %s)\n", gswapExec.GetWalletAddress())
			continue
		}

		// Use CCXT for all supported CEX exchanges
		if executor.SupportedCCXTExchanges[strings.ToLower(ex.ID)] {
			if ex.APIKey == "" || ex.Secret == "" {
				logf("Skipping %s executor: API credentials not configured\n", ex.ID)
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
				logf("Warning: Failed to create %s executor: %v\n", ex.ID, err)
				lastErr = err
				continue
			}
			if err := ccxtExec.Initialize(ctx); err != nil {
				logf("Warning: Failed to initialize %s executor: %v\n", ex.ID, err)
				lastErr = err
				continue
			}
			registry.Register(ccxtExec)
			logf("Initialized %s executor (via CCXT)\n", ex.ID)
		} else {
			logf("Skipping %s: not a supported CCXT exchange\n", ex.ID)
		}
	}

	// Add Solana/Jupiter executor if enabled for trading
	if cfg.Solana.Enabled && cfg.Solana.TradingEnabled {
		if cfg.Solana.PrivateKey == "" || cfg.Solana.WalletAddress == "" {
			logf("Skipping Jupiter executor: Solana credentials not configured\n")
		} else {
			solanaConfig := &executor.SolanaExecutorConfig{
				RPCURL:         cfg.Solana.RPCURL,
				PrivateKey:     cfg.Solana.PrivateKey,
				WalletAddress:  cfg.Solana.WalletAddress,
				JupiterBaseURL: cfg.Solana.JupiterAPIBase,
				JupiterAPIKey:  cfg.Solana.JupiterAPIKey,
				SlippageBps:    cfg.Solana.DefaultSlippageBps,
			}

			solanaExec, err := executor.NewSolanaExecutor(solanaConfig)
			if err != nil {
				logf("Warning: Failed to create Jupiter executor: %v\n", err)
				lastErr = err
			} else if err := solanaExec.Initialize(ctx); err != nil {
				logf("Warning: Failed to initialize Jupiter executor: %v\n", err)
				lastErr = err
			} else {
				registry.Register(solanaExec)
				logf("Initialized Jupiter executor (wallet: %s)\n", solanaExec.GetWalletAddress())
			}
		}
	}

	return lastErr
}

// printExecutorStatus prints the status of all executors.
func printExecutorStatus(registry *executor.ExecutorRegistry) {
	logln("\nExecutor status:")
	executors := registry.GetAll()
	if len(executors) == 0 {
		logln("  No executors configured (detection only mode)")
	} else {
		for name, exec := range executors {
			status := "not ready"
			if exec.IsReady() {
				status = "ready"
			}
			logf("  - %s (%s): %s\n", name, exec.Type(), status)
		}
	}
	logln()
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

	// Add Solana/Jupiter provider if enabled
	if cfg.Solana.Enabled {
		pollInterval := time.Duration(cfg.Solana.PollIntervalSeconds) * time.Second
		if pollInterval == 0 {
			pollInterval = 5 * time.Second
		}

		jupiterConfig := &websocket.JupiterPollerConfig{
			PollInterval: pollInterval,
			BaseURL:      cfg.Solana.JupiterAPIBase,
			APIKey:       cfg.Solana.JupiterAPIKey,
		}

		agg.AddProvider(websocket.NewJupiterPollerProvider(jupiterConfig))
		logf("Added Jupiter polling provider (%v interval)\n", pollInterval)
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

	logln()
	logln(strings.Repeat("-", 60))
	logf("[%s] Status Update\n", time.Now().Format("15:04:05"))
	logf("  Uptime: %s\n", time.Since(stats.StartTime).Round(time.Second))
	logf("  Opportunities found: %d\n", stats.OpportunitiesFound)
	logf("  Executions attempted: %d (successful: %d, failed: %d)\n",
		execStats.ExecutedTrades, execStats.SuccessfulTrades, execStats.FailedTrades)
	logf("  Skipped: insufficient balance=%d, below min profit=%d, rate limited=%d, dry run=%d\n",
		execStats.SkippedInsufficientBalance, execStats.SkippedBelowMinProfit,
		execStats.SkippedRateLimited, execStats.SkippedDryRun)

	if execStats.TotalProfit != nil && execStats.TotalProfit.Sign() != 0 {
		logf("  Total profit: %s\n", execStats.TotalProfit.Text('f', 4))
	}

	// Connection status
	status := agg.GetConnectionStatus()
	logf("  Connections: ")
	parts := make([]string, 0, len(status))
	for name, state := range status {
		parts = append(parts, fmt.Sprintf("%s=%s", name, state))
	}
	logln(strings.Join(parts, ", "))

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

		logf("  Inventory: %d snapshots, %d drift alerts, %d rebalances recommended\n",
			invStats.SnapshotsCollected, invStats.DriftAlertsTriggered, invStats.RebalancesRecommended)
		if driftWarnings > 0 {
			logf("  [!] %d currencies with drift above threshold\n", driftWarnings)
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

		logf("  Auto-rebalance: %s (triggered=%d, completed=%d, failed=%d)\n",
			rebalStatus, rebalStats.RebalancesTriggered, rebalStats.RebalancesCompleted, rebalStats.RebalancesFailed)
	}

	logln(strings.Repeat("-", 60))
	logln()
}

// printExecutionStats prints final execution statistics.
func printExecutionStats(coord *executor.ArbitrageCoordinator) {
	stats := coord.GetStats()

	logf("\nExecution Statistics:\n")
	logf(strings.Repeat("=", 40) + "\n")
	logf("  Total opportunities: %d\n", stats.TotalOpportunities)
	logf("  Executed trades: %d\n", stats.ExecutedTrades)
	logf("  Successful: %d\n", stats.SuccessfulTrades)
	logf("  Failed: %d\n", stats.FailedTrades)
	logf("  Skipped (insufficient balance): %d\n", stats.SkippedInsufficientBalance)
	logf("  Skipped (below min profit): %d\n", stats.SkippedBelowMinProfit)
	logf("  Skipped (rate limited): %d\n", stats.SkippedRateLimited)
	logf("  Skipped (dry run): %d\n", stats.SkippedDryRun)
	if stats.TotalProfit != nil {
		logf("  Total profit: %s\n", stats.TotalProfit.Text('f', 4))
	}
	if stats.TotalFees != nil {
		logf("  Total fees: %s\n", stats.TotalFees.Text('f', 4))
	}
	logf(strings.Repeat("=", 40) + "\n")
}

// logf writes formatted output to both stdout and log file
func logf(format string, args ...interface{}) {
	fmt.Fprintf(logWriter, format, args...)
}

// logln writes a line to both stdout and log file
func logln(args ...interface{}) {
	fmt.Fprintln(logWriter, args...)
}
