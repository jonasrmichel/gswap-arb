// Package inventory provides balance tracking and rebalancing logic for arbitrage trading.
package inventory

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/bridge"
)

// AutoRebalancer handles automated inventory rebalancing.
type AutoRebalancer struct {
	manager       *Manager
	bridgeExec    *bridge.BridgeExecutor
	config        *RebalancerConfig

	// State tracking
	pendingBridge   *PendingBridge
	pendingMu       sync.RWMutex

	// Circuit breaker
	consecutiveFailures int
	lastFailureAt       time.Time
	circuitOpen         bool
	circuitMu           sync.RWMutex

	// Statistics
	stats   RebalancerStats
	statsMu sync.RWMutex

	// Callbacks
	onBridgeStarted   func(rec *RebalanceRecommendation, result *bridge.BridgeResult)
	onBridgeCompleted func(rec *RebalanceRecommendation, result *bridge.BridgeResult)
	onBridgeFailed    func(rec *RebalanceRecommendation, err error)
	onCircuitOpen     func(failures int)
	onCircuitClose    func()

	// Control
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// PendingBridge represents an in-flight bridge operation.
type PendingBridge struct {
	Recommendation *RebalanceRecommendation
	Result         *bridge.BridgeResult
	StartedAt      time.Time
	LastCheckedAt  time.Time
	CheckCount     int
}

// RebalancerConfig holds configuration for the auto-rebalancer.
type RebalancerConfig struct {
	// Enable/disable automation
	Enabled bool `json:"enabled"`

	// Timing
	CheckIntervalSeconds      int           `json:"check_interval_seconds"`       // How often to check for rebalancing needs
	BridgeStatusPollSeconds   int           `json:"bridge_status_poll_seconds"`   // How often to poll bridge status
	MaxBridgeWaitMinutes      int           `json:"max_bridge_wait_minutes"`      // Maximum time to wait for bridge completion
	MinTimeBetweenRebalances  time.Duration `json:"min_time_between_rebalances"`  // Minimum time between rebalance operations

	// Thresholds (override Manager config if set)
	DriftThresholdPct     float64 `json:"drift_threshold_pct,omitempty"`     // Override for triggering rebalance
	CriticalDriftPct      float64 `json:"critical_drift_pct,omitempty"`      // Override for critical level
	MinRebalanceAmountUSD float64 `json:"min_rebalance_amount_usd,omitempty"`
	MaxRebalanceAmountUSD float64 `json:"max_rebalance_amount_usd,omitempty"`

	// Limits
	MaxPendingBridges int `json:"max_pending_bridges"` // Maximum concurrent bridges (usually 1)

	// Circuit breaker
	CircuitBreakerThreshold   int           `json:"circuit_breaker_threshold"`    // Consecutive failures to open circuit
	CircuitBreakerResetTime   time.Duration `json:"circuit_breaker_reset_time"`   // Time to wait before closing circuit

	// Safety
	RequireConfirmation bool `json:"require_confirmation"` // If true, only recommend, don't execute
}

// DefaultRebalancerConfig returns sensible defaults for the rebalancer.
func DefaultRebalancerConfig() *RebalancerConfig {
	return &RebalancerConfig{
		Enabled:                   false, // Disabled by default for safety
		CheckIntervalSeconds:      300,   // 5 minutes
		BridgeStatusPollSeconds:   60,    // 1 minute
		MaxBridgeWaitMinutes:      30,    // 30 minutes max wait
		MinTimeBetweenRebalances:  10 * time.Minute,
		MaxPendingBridges:         1,     // One bridge at a time
		CircuitBreakerThreshold:   3,     // Open after 3 consecutive failures
		CircuitBreakerResetTime:   30 * time.Minute,
		RequireConfirmation:       true,  // Safe default: require confirmation
	}
}

// RebalancerStats tracks rebalancer statistics.
type RebalancerStats struct {
	ChecksPerformed      int64         `json:"checks_performed"`
	RebalancesTriggered  int64         `json:"rebalances_triggered"`
	RebalancesCompleted  int64         `json:"rebalances_completed"`
	RebalancesFailed     int64         `json:"rebalances_failed"`
	TotalBridgedAmount   *big.Float    `json:"total_bridged_amount"`
	TotalBridgeFees      *big.Float    `json:"total_bridge_fees"`
	AverageBridgeTime    time.Duration `json:"average_bridge_time"`
	CircuitBreakerTrips  int64         `json:"circuit_breaker_trips"`
	LastRebalanceAt      time.Time     `json:"last_rebalance_at"`
	LastCheckAt          time.Time     `json:"last_check_at"`
}

// NewAutoRebalancer creates a new auto-rebalancer.
func NewAutoRebalancer(manager *Manager, bridgeExec *bridge.BridgeExecutor, config *RebalancerConfig) *AutoRebalancer {
	if config == nil {
		config = DefaultRebalancerConfig()
	}

	return &AutoRebalancer{
		manager:    manager,
		bridgeExec: bridgeExec,
		config:     config,
		stopCh:     make(chan struct{}),
		stats: RebalancerStats{
			TotalBridgedAmount: big.NewFloat(0),
			TotalBridgeFees:    big.NewFloat(0),
		},
	}
}

// SetBridgeStartedCallback sets the callback for when a bridge starts.
func (r *AutoRebalancer) SetBridgeStartedCallback(cb func(rec *RebalanceRecommendation, result *bridge.BridgeResult)) {
	r.onBridgeStarted = cb
}

// SetBridgeCompletedCallback sets the callback for when a bridge completes.
func (r *AutoRebalancer) SetBridgeCompletedCallback(cb func(rec *RebalanceRecommendation, result *bridge.BridgeResult)) {
	r.onBridgeCompleted = cb
}

// SetBridgeFailedCallback sets the callback for when a bridge fails.
func (r *AutoRebalancer) SetBridgeFailedCallback(cb func(rec *RebalanceRecommendation, err error)) {
	r.onBridgeFailed = cb
}

// SetCircuitOpenCallback sets the callback for when circuit breaker opens.
func (r *AutoRebalancer) SetCircuitOpenCallback(cb func(failures int)) {
	r.onCircuitOpen = cb
}

// SetCircuitCloseCallback sets the callback for when circuit breaker closes.
func (r *AutoRebalancer) SetCircuitCloseCallback(cb func()) {
	r.onCircuitClose = cb
}

// Start begins the automated rebalancing loop.
func (r *AutoRebalancer) Start(ctx context.Context) error {
	if !r.config.Enabled {
		return fmt.Errorf("auto-rebalancer is disabled")
	}

	if r.bridgeExec == nil {
		return fmt.Errorf("bridge executor not configured")
	}

	r.wg.Add(1)
	go r.runLoop(ctx)

	return nil
}

// Stop stops the automated rebalancing loop.
func (r *AutoRebalancer) Stop() {
	close(r.stopCh)
	r.wg.Wait()
}

// runLoop is the main rebalancing loop.
func (r *AutoRebalancer) runLoop(ctx context.Context) {
	defer r.wg.Done()

	checkTicker := time.NewTicker(time.Duration(r.config.CheckIntervalSeconds) * time.Second)
	defer checkTicker.Stop()

	statusTicker := time.NewTicker(time.Duration(r.config.BridgeStatusPollSeconds) * time.Second)
	defer statusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return

		case <-checkTicker.C:
			r.performCheck(ctx)

		case <-statusTicker.C:
			r.checkPendingBridge(ctx)
		}
	}
}

// performCheck checks drift and triggers rebalancing if needed.
func (r *AutoRebalancer) performCheck(ctx context.Context) {
	r.statsMu.Lock()
	r.stats.ChecksPerformed++
	r.stats.LastCheckAt = time.Now()
	r.statsMu.Unlock()

	// Check if circuit breaker is open
	if r.isCircuitOpen() {
		return
	}

	// Check if there's already a pending bridge
	if r.hasPendingBridge() {
		return
	}

	// Collect fresh snapshot
	_, err := r.manager.CollectSnapshot(ctx)
	if err != nil {
		return // Skip this check if we can't get balances
	}

	// Check drift
	r.manager.CheckDrift(ctx)

	// Generate recommendations
	recommendations := r.manager.GenerateRebalanceRecommendations()
	if len(recommendations) == 0 {
		return
	}

	// Take the highest priority recommendation
	rec := recommendations[0]

	// Check minimum time between rebalances
	r.statsMu.RLock()
	lastRebalance := r.stats.LastRebalanceAt
	r.statsMu.RUnlock()

	if time.Since(lastRebalance) < r.config.MinTimeBetweenRebalances {
		return
	}

	// If require confirmation is set, just return (callbacks handle notification)
	if r.config.RequireConfirmation {
		return
	}

	// Execute rebalance
	r.executeRebalance(ctx, rec)
}

// executeRebalance executes a rebalance operation.
func (r *AutoRebalancer) executeRebalance(ctx context.Context, rec *RebalanceRecommendation) {
	// Determine bridge direction based on exchanges
	var direction bridge.BridgeDirection
	fromGalaChain := rec.FromExchange == "gswap"
	toGalaChain := rec.ToExchange == "gswap"
	fromSolana := rec.FromExchange == "jupiter" || rec.FromExchange == "solana"
	toSolana := rec.ToExchange == "jupiter" || rec.ToExchange == "solana"

	if fromGalaChain && toSolana {
		direction = bridge.BridgeToSolana
	} else if fromSolana && toGalaChain {
		direction = bridge.BridgeFromSolana
	} else if fromGalaChain {
		direction = bridge.BridgeToEthereum
	} else if toGalaChain {
		direction = bridge.BridgeToGalaChain
	} else {
		// Both are CEXs/non-GalaChain - can't bridge between them directly
		// Would need to go through GalaChain first
		r.recordFailure(fmt.Errorf("cannot bridge directly between %s and %s - must route through GalaChain", rec.FromExchange, rec.ToExchange))
		return
	}

	// Map currency to bridge token
	bridgeToken := mapCurrencyToBridgeToken(rec.Currency)
	if bridgeToken == "" {
		r.recordFailure(fmt.Errorf("currency %s not supported for bridging", rec.Currency))
		return
	}

	// Create bridge request
	bridgeReq := &bridge.BridgeRequest{
		Token:     bridgeToken,
		Amount:    rec.Amount,
		Direction: direction,
	}

	// Execute bridge
	result, err := r.bridgeExec.Bridge(ctx, bridgeReq)
	if err != nil {
		r.recordFailure(err)
		if r.onBridgeFailed != nil {
			r.onBridgeFailed(rec, err)
		}
		return
	}

	// Record pending bridge
	r.pendingMu.Lock()
	r.pendingBridge = &PendingBridge{
		Recommendation: rec,
		Result:         result,
		StartedAt:      time.Now(),
		LastCheckedAt:  time.Now(),
		CheckCount:     0,
	}
	r.pendingMu.Unlock()

	// Update stats
	r.statsMu.Lock()
	r.stats.RebalancesTriggered++
	r.stats.LastRebalanceAt = time.Now()
	r.statsMu.Unlock()

	// Reset consecutive failures on success
	r.circuitMu.Lock()
	r.consecutiveFailures = 0
	r.circuitMu.Unlock()

	// Notify callback
	if r.onBridgeStarted != nil {
		r.onBridgeStarted(rec, result)
	}
}

// checkPendingBridge checks the status of a pending bridge operation.
func (r *AutoRebalancer) checkPendingBridge(ctx context.Context) {
	r.pendingMu.Lock()
	pending := r.pendingBridge
	if pending == nil {
		r.pendingMu.Unlock()
		return
	}

	pending.LastCheckedAt = time.Now()
	pending.CheckCount++
	r.pendingMu.Unlock()

	// Check if we've exceeded max wait time
	maxWait := time.Duration(r.config.MaxBridgeWaitMinutes) * time.Minute
	if time.Since(pending.StartedAt) > maxWait {
		r.handleBridgeTimeout(pending)
		return
	}

	// Check bridge status
	status, err := r.bridgeExec.GetBridgeStatus(ctx, pending.Result.TransactionID)
	if err != nil {
		// Log but don't fail - bridge might still be in progress
		return
	}

	switch status.Status {
	case bridge.BridgeStatusCompleted:
		r.handleBridgeComplete(pending, status)
	case bridge.BridgeStatusFailed:
		r.handleBridgeFailed(pending, fmt.Errorf("bridge failed: %s", status.Error))
	// Pending/Confirmed - continue waiting
	}
}

// handleBridgeComplete handles a completed bridge.
func (r *AutoRebalancer) handleBridgeComplete(pending *PendingBridge, result *bridge.BridgeResult) {
	duration := time.Since(pending.StartedAt)

	// Update stats
	r.statsMu.Lock()
	r.stats.RebalancesCompleted++
	r.stats.TotalBridgedAmount = new(big.Float).Add(r.stats.TotalBridgedAmount, pending.Recommendation.Amount)
	if result.Fee != nil {
		r.stats.TotalBridgeFees = new(big.Float).Add(r.stats.TotalBridgeFees, result.Fee)
	}
	// Update average bridge time
	if r.stats.AverageBridgeTime == 0 {
		r.stats.AverageBridgeTime = duration
	} else {
		r.stats.AverageBridgeTime = (r.stats.AverageBridgeTime + duration) / 2
	}
	r.statsMu.Unlock()

	// Clear pending
	r.pendingMu.Lock()
	r.pendingBridge = nil
	r.pendingMu.Unlock()

	// Notify callback
	if r.onBridgeCompleted != nil {
		r.onBridgeCompleted(pending.Recommendation, result)
	}
}

// handleBridgeFailed handles a failed bridge.
func (r *AutoRebalancer) handleBridgeFailed(pending *PendingBridge, err error) {
	r.recordFailure(err)

	// Update stats
	r.statsMu.Lock()
	r.stats.RebalancesFailed++
	r.statsMu.Unlock()

	// Clear pending
	r.pendingMu.Lock()
	r.pendingBridge = nil
	r.pendingMu.Unlock()

	// Notify callback
	if r.onBridgeFailed != nil {
		r.onBridgeFailed(pending.Recommendation, err)
	}
}

// handleBridgeTimeout handles a timed-out bridge.
func (r *AutoRebalancer) handleBridgeTimeout(pending *PendingBridge) {
	err := fmt.Errorf("bridge timed out after %d minutes", r.config.MaxBridgeWaitMinutes)
	r.handleBridgeFailed(pending, err)
}

// recordFailure records a failure and potentially opens the circuit breaker.
func (r *AutoRebalancer) recordFailure(err error) {
	r.circuitMu.Lock()
	defer r.circuitMu.Unlock()

	r.consecutiveFailures++
	r.lastFailureAt = time.Now()

	if r.consecutiveFailures >= r.config.CircuitBreakerThreshold && !r.circuitOpen {
		r.circuitOpen = true
		r.statsMu.Lock()
		r.stats.CircuitBreakerTrips++
		r.statsMu.Unlock()

		if r.onCircuitOpen != nil {
			r.onCircuitOpen(r.consecutiveFailures)
		}
	}
}

// isCircuitOpen checks if the circuit breaker is open.
func (r *AutoRebalancer) isCircuitOpen() bool {
	r.circuitMu.Lock()
	defer r.circuitMu.Unlock()

	if !r.circuitOpen {
		return false
	}

	// Check if enough time has passed to try again
	if time.Since(r.lastFailureAt) > r.config.CircuitBreakerResetTime {
		r.circuitOpen = false
		r.consecutiveFailures = 0
		if r.onCircuitClose != nil {
			r.onCircuitClose()
		}
		return false
	}

	return true
}

// hasPendingBridge checks if there's a pending bridge operation.
func (r *AutoRebalancer) hasPendingBridge() bool {
	r.pendingMu.RLock()
	defer r.pendingMu.RUnlock()
	return r.pendingBridge != nil
}

// GetPendingBridge returns the current pending bridge, if any.
func (r *AutoRebalancer) GetPendingBridge() *PendingBridge {
	r.pendingMu.RLock()
	defer r.pendingMu.RUnlock()
	return r.pendingBridge
}

// GetStats returns the rebalancer statistics.
func (r *AutoRebalancer) GetStats() RebalancerStats {
	r.statsMu.RLock()
	defer r.statsMu.RUnlock()
	return r.stats
}

// IsEnabled returns whether the rebalancer is enabled.
func (r *AutoRebalancer) IsEnabled() bool {
	return r.config.Enabled
}

// IsCircuitBreakerOpen returns whether the circuit breaker is currently open.
func (r *AutoRebalancer) IsCircuitBreakerOpen() bool {
	return r.isCircuitOpen()
}

// ResetCircuitBreaker manually resets the circuit breaker.
func (r *AutoRebalancer) ResetCircuitBreaker() {
	r.circuitMu.Lock()
	defer r.circuitMu.Unlock()
	r.circuitOpen = false
	r.consecutiveFailures = 0
	if r.onCircuitClose != nil {
		r.onCircuitClose()
	}
}

// TriggerManualRebalance manually triggers a rebalance check and execution.
func (r *AutoRebalancer) TriggerManualRebalance(ctx context.Context, rec *RebalanceRecommendation) error {
	if r.hasPendingBridge() {
		return fmt.Errorf("cannot start new rebalance: bridge already in progress")
	}

	if r.isCircuitOpen() {
		return fmt.Errorf("cannot start rebalance: circuit breaker is open")
	}

	r.executeRebalance(ctx, rec)
	return nil
}

// mapCurrencyToBridgeToken maps currency symbols to bridge token symbols.
func mapCurrencyToBridgeToken(currency string) string {
	// Map common currency variations to bridge tokens
	switch currency {
	case "GALA":
		return "GALA"
	case "USDT", "GUSDT":
		return "GUSDT"
	case "USDC", "GUSDC":
		return "GUSDC"
	case "ETH", "WETH", "GWETH":
		return "GWETH"
	case "BTC", "WBTC", "GWBTC":
		return "GWBTC"
	case "BENE":
		return "BENE"
	case "SOL", "GSOL":
		return "GSOL"
	case "MEW", "GMEW":
		return "GMEW"
	case "TRUMP", "GTRUMP":
		return "GTRUMP"
	default:
		// Check if it's already a bridge token in Ethereum tokens
		if _, ok := bridge.SupportedTokens[currency]; ok {
			return currency
		}
		// Check if it's a Solana bridge token
		if _, ok := bridge.SolanaBridgeTokens[currency]; ok {
			return currency
		}
		return ""
	}
}

// FormatStatusReport generates a human-readable status report.
func (r *AutoRebalancer) FormatStatusReport() string {
	stats := r.GetStats()
	pending := r.GetPendingBridge()
	circuitOpen := r.IsCircuitBreakerOpen()

	report := "Auto-Rebalancer Status\n"
	report += "========================================\n\n"

	// Configuration
	report += fmt.Sprintf("Enabled: %v\n", r.config.Enabled)
	report += fmt.Sprintf("Require Confirmation: %v\n", r.config.RequireConfirmation)
	report += fmt.Sprintf("Check Interval: %d seconds\n", r.config.CheckIntervalSeconds)
	report += fmt.Sprintf("Max Bridge Wait: %d minutes\n", r.config.MaxBridgeWaitMinutes)
	report += "\n"

	// Circuit breaker status
	if circuitOpen {
		report += "Circuit Breaker: OPEN (paused due to failures)\n"
	} else {
		report += "Circuit Breaker: CLOSED (operating normally)\n"
	}
	report += fmt.Sprintf("Circuit Breaker Trips: %d\n", stats.CircuitBreakerTrips)
	report += "\n"

	// Pending bridge
	if pending != nil {
		elapsed := time.Since(pending.StartedAt)
		report += "Pending Bridge:\n"
		report += fmt.Sprintf("  Currency: %s\n", pending.Recommendation.Currency)
		report += fmt.Sprintf("  Amount: %s\n", pending.Recommendation.Amount.Text('f', 4))
		report += fmt.Sprintf("  From: %s -> To: %s\n", pending.Recommendation.FromExchange, pending.Recommendation.ToExchange)
		report += fmt.Sprintf("  Transaction: %s\n", pending.Result.TransactionID)
		report += fmt.Sprintf("  Elapsed: %s\n", elapsed.Round(time.Second))
		report += fmt.Sprintf("  Status Checks: %d\n", pending.CheckCount)
		report += "\n"
	} else {
		report += "No pending bridge\n\n"
	}

	// Statistics
	report += "Statistics:\n"
	report += fmt.Sprintf("  Checks Performed: %d\n", stats.ChecksPerformed)
	report += fmt.Sprintf("  Rebalances Triggered: %d\n", stats.RebalancesTriggered)
	report += fmt.Sprintf("  Rebalances Completed: %d\n", stats.RebalancesCompleted)
	report += fmt.Sprintf("  Rebalances Failed: %d\n", stats.RebalancesFailed)
	if stats.TotalBridgedAmount != nil && stats.TotalBridgedAmount.Sign() > 0 {
		report += fmt.Sprintf("  Total Bridged: %s\n", stats.TotalBridgedAmount.Text('f', 4))
	}
	if stats.AverageBridgeTime > 0 {
		report += fmt.Sprintf("  Average Bridge Time: %s\n", stats.AverageBridgeTime.Round(time.Second))
	}
	if !stats.LastCheckAt.IsZero() {
		report += fmt.Sprintf("  Last Check: %s\n", stats.LastCheckAt.Format(time.RFC3339))
	}
	if !stats.LastRebalanceAt.IsZero() {
		report += fmt.Sprintf("  Last Rebalance: %s\n", stats.LastRebalanceAt.Format(time.RFC3339))
	}

	return report
}
