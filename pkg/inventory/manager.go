// Package inventory provides balance tracking and rebalancing logic for arbitrage trading.
package inventory

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/executor"
)

// Manager tracks balances across exchanges and monitors for drift.
type Manager struct {
	registry *executor.ExecutorRegistry
	config   *InventoryConfig

	// Current state
	snapshot    *InventorySnapshot
	driftStatus map[string]*DriftStatus // currency -> drift status
	snapshotMu  sync.RWMutex

	// Statistics
	stats   InventoryStats
	statsMu sync.RWMutex

	// Callbacks
	onDriftAlert      func(status *DriftStatus)
	onRebalanceNeeded func(rec *RebalanceRecommendation)
}

// NewManager creates a new inventory manager.
func NewManager(registry *executor.ExecutorRegistry, config *InventoryConfig) *Manager {
	if config == nil {
		config = DefaultInventoryConfig()
	}

	return &Manager{
		registry:    registry,
		config:      config,
		driftStatus: make(map[string]*DriftStatus),
	}
}

// SetDriftAlertCallback sets the callback for drift alerts.
func (m *Manager) SetDriftAlertCallback(cb func(status *DriftStatus)) {
	m.onDriftAlert = cb
}

// SetRebalanceCallback sets the callback for rebalance recommendations.
func (m *Manager) SetRebalanceCallback(cb func(rec *RebalanceRecommendation)) {
	m.onRebalanceNeeded = cb
}

// CollectSnapshot collects current balances from all exchanges.
func (m *Manager) CollectSnapshot(ctx context.Context) (*InventorySnapshot, error) {
	snapshot := &InventorySnapshot{
		Exchanges:  make(map[string]*ExchangeBalance),
		TotalUSD:   big.NewFloat(0),
		CapturedAt: time.Now(),
	}

	executors := m.registry.GetReady()
	if len(executors) == 0 {
		return nil, fmt.Errorf("no ready executors available")
	}

	// Collect balances from each exchange concurrently
	var wg sync.WaitGroup
	var mu sync.Mutex
	errors := make([]error, 0)

	for _, exec := range executors {
		wg.Add(1)
		go func(e executor.TradeExecutor) {
			defer wg.Done()

			balances, err := e.GetBalances(ctx)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Errorf("%s: %w", e.Name(), err))
				mu.Unlock()
				return
			}

			exchangeBalance := &ExchangeBalance{
				Exchange:  e.Name(),
				Balances:  make(map[string]*TokenBalance),
				UpdatedAt: time.Now(),
			}

			for currency, bal := range balances {
				// Skip if not in tracked currencies (when tracking is configured)
				if len(m.config.TrackedCurrencies) > 0 && !m.isTrackedCurrency(currency) {
					continue
				}

				// Skip zero balances
				if bal.Total == nil || bal.Total.Sign() == 0 {
					continue
				}

				exchangeBalance.Balances[currency] = &TokenBalance{
					Currency:  currency,
					Free:      bal.Free,
					Locked:    bal.Locked,
					Total:     bal.Total,
					UpdatedAt: bal.UpdatedAt,
				}
			}

			mu.Lock()
			snapshot.Exchanges[e.Name()] = exchangeBalance
			mu.Unlock()
		}(exec)
	}

	wg.Wait()

	// Store snapshot
	m.snapshotMu.Lock()
	m.snapshot = snapshot
	m.snapshotMu.Unlock()

	// Update stats
	m.statsMu.Lock()
	m.stats.SnapshotsCollected++
	m.stats.LastSnapshotAt = time.Now()
	m.statsMu.Unlock()

	if len(errors) > 0 {
		return snapshot, fmt.Errorf("partial snapshot: %d errors: %v", len(errors), errors[0])
	}

	return snapshot, nil
}

// isTrackedCurrency checks if a currency should be tracked.
func (m *Manager) isTrackedCurrency(currency string) bool {
	for _, c := range m.config.TrackedCurrencies {
		if c == currency {
			return true
		}
	}
	return false
}

// GetSnapshot returns the current snapshot.
func (m *Manager) GetSnapshot() *InventorySnapshot {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	return m.snapshot
}

// GetTokenAllocation calculates the allocation of a token across exchanges.
func (m *Manager) GetTokenAllocation(currency string) *TokenAllocation {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()

	if m.snapshot == nil {
		return nil
	}

	allocation := &TokenAllocation{
		Currency:      currency,
		TotalAmount:   big.NewFloat(0),
		TotalUSDValue: big.NewFloat(0),
		ByExchange:    make(map[string]*big.Float),
		ByExchangePct: make(map[string]float64),
	}

	// Collect amounts from each exchange
	for exchangeName, exchBal := range m.snapshot.Exchanges {
		if bal, ok := exchBal.Balances[currency]; ok {
			allocation.ByExchange[exchangeName] = bal.Total
			allocation.TotalAmount = new(big.Float).Add(allocation.TotalAmount, bal.Total)
		} else {
			allocation.ByExchange[exchangeName] = big.NewFloat(0)
		}
	}

	// Calculate percentages
	if allocation.TotalAmount.Sign() > 0 {
		for exchangeName, amount := range allocation.ByExchange {
			pct := new(big.Float).Quo(amount, allocation.TotalAmount)
			pctFloat, _ := pct.Float64()
			allocation.ByExchangePct[exchangeName] = pctFloat * 100
		}
	}

	return allocation
}

// CheckDrift calculates drift from target allocations for all tracked currencies.
func (m *Manager) CheckDrift(ctx context.Context) map[string]*DriftStatus {
	m.snapshotMu.RLock()
	snapshot := m.snapshot
	m.snapshotMu.RUnlock()

	if snapshot == nil {
		return nil
	}

	driftStatuses := make(map[string]*DriftStatus)

	// Get all currencies present in the snapshot
	currencies := m.getUniqueCurrencies(snapshot)

	for _, currency := range currencies {
		status := m.calculateDriftForCurrency(currency, snapshot)
		if status != nil {
			driftStatuses[currency] = status

			// Trigger alert if drift exceeds threshold
			if status.NeedsRebalance && m.config.AlertOnDrift && m.onDriftAlert != nil {
				m.onDriftAlert(status)
				m.statsMu.Lock()
				m.stats.DriftAlertsTriggered++
				m.statsMu.Unlock()
			}
		}
	}

	// Store drift status
	m.snapshotMu.Lock()
	m.driftStatus = driftStatuses
	m.snapshotMu.Unlock()

	// Update stats
	m.statsMu.Lock()
	m.stats.LastDriftCheckAt = time.Now()
	m.statsMu.Unlock()

	return driftStatuses
}

// getUniqueCurrencies extracts all unique currencies from a snapshot.
func (m *Manager) getUniqueCurrencies(snapshot *InventorySnapshot) []string {
	currencySet := make(map[string]bool)
	for _, exchBal := range snapshot.Exchanges {
		for currency := range exchBal.Balances {
			currencySet[currency] = true
		}
	}

	currencies := make([]string, 0, len(currencySet))
	for c := range currencySet {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	return currencies
}

// calculateDriftForCurrency calculates drift for a single currency.
func (m *Manager) calculateDriftForCurrency(currency string, snapshot *InventorySnapshot) *DriftStatus {
	allocation := m.GetTokenAllocation(currency)
	if allocation == nil || allocation.TotalAmount.Sign() == 0 {
		return nil
	}

	status := &DriftStatus{
		Currency:     currency,
		CurrentAlloc: allocation.ByExchangePct,
		TargetAlloc:  make(map[string]float64),
		DriftPct:     make(map[string]float64),
		MaxDriftPct:  0,
		UpdatedAt:    time.Now(),
	}

	// Get target allocations for this currency
	hasTargets := false
	for exchangeName := range allocation.ByExchange {
		if targets, ok := m.config.TargetAllocations[exchangeName]; ok {
			if target, ok := targets[currency]; ok {
				status.TargetAlloc[exchangeName] = target
				hasTargets = true
			}
		}
	}

	// If no targets configured, use equal distribution as default
	if !hasTargets {
		numExchanges := len(allocation.ByExchange)
		if numExchanges > 0 {
			equalPct := 100.0 / float64(numExchanges)
			for exchangeName := range allocation.ByExchange {
				status.TargetAlloc[exchangeName] = equalPct
			}
		}
	}

	// Calculate drift for each exchange
	for exchangeName, currentPct := range status.CurrentAlloc {
		targetPct := status.TargetAlloc[exchangeName]
		drift := currentPct - targetPct
		status.DriftPct[exchangeName] = drift

		absDrift := math.Abs(drift)
		if absDrift > status.MaxDriftPct {
			status.MaxDriftPct = absDrift
		}
	}

	// Determine if rebalancing is needed
	status.NeedsRebalance = status.MaxDriftPct >= m.config.DriftThresholdPct

	return status
}

// GetDriftStatus returns current drift status for all currencies.
func (m *Manager) GetDriftStatus() map[string]*DriftStatus {
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()

	// Return a copy
	result := make(map[string]*DriftStatus)
	for k, v := range m.driftStatus {
		result[k] = v
	}
	return result
}

// GenerateRebalanceRecommendations generates recommendations for rebalancing.
func (m *Manager) GenerateRebalanceRecommendations() []*RebalanceRecommendation {
	m.snapshotMu.RLock()
	snapshot := m.snapshot
	driftStatus := m.driftStatus
	m.snapshotMu.RUnlock()

	if snapshot == nil || len(driftStatus) == 0 {
		return nil
	}

	recommendations := make([]*RebalanceRecommendation, 0)

	for currency, status := range driftStatus {
		if !status.NeedsRebalance {
			continue
		}

		allocation := m.GetTokenAllocation(currency)
		if allocation == nil {
			continue
		}

		// Find exchange with highest surplus and highest deficit
		var surplusExchange, deficitExchange string
		var maxSurplus, maxDeficit float64

		for exchange, drift := range status.DriftPct {
			if drift > maxSurplus {
				maxSurplus = drift
				surplusExchange = exchange
			}
			if drift < maxDeficit {
				maxDeficit = drift
				deficitExchange = exchange
			}
		}

		if surplusExchange == "" || deficitExchange == "" || surplusExchange == deficitExchange {
			continue
		}

		// Calculate recommended transfer amount
		// Transfer enough to reduce drift to half of threshold
		targetTransferPct := (maxSurplus - m.config.DriftThresholdPct/2) / 100.0
		if targetTransferPct <= 0 {
			continue
		}

		transferAmount := new(big.Float).Mul(allocation.TotalAmount, big.NewFloat(targetTransferPct))

		// Determine priority based on drift severity
		priority := PriorityLow
		if status.MaxDriftPct >= m.config.CriticalDriftPct {
			priority = PriorityHigh
		} else if status.MaxDriftPct >= m.config.DriftThresholdPct*1.5 {
			priority = PriorityMedium
		}

		rec := &RebalanceRecommendation{
			ID:            fmt.Sprintf("rebal-%s-%d", currency, time.Now().UnixMilli()),
			Currency:      currency,
			FromExchange:  surplusExchange,
			ToExchange:    deficitExchange,
			Amount:        transferAmount,
			CurrentDrift:  maxSurplus,
			ExpectedDrift: m.config.DriftThresholdPct / 2,
			Priority:      priority,
			Reason:        fmt.Sprintf("%s has %.1f%% drift (threshold: %.1f%%)", currency, status.MaxDriftPct, m.config.DriftThresholdPct),
			CreatedAt:     time.Now(),
		}

		recommendations = append(recommendations, rec)

		// Trigger callback
		if m.onRebalanceNeeded != nil {
			m.onRebalanceNeeded(rec)
			m.statsMu.Lock()
			m.stats.RebalancesRecommended++
			m.statsMu.Unlock()
		}
	}

	// Sort by priority
	sort.Slice(recommendations, func(i, j int) bool {
		return recommendations[i].Priority < recommendations[j].Priority
	})

	return recommendations
}

// GetStats returns inventory monitoring statistics.
func (m *Manager) GetStats() InventoryStats {
	m.statsMu.RLock()
	defer m.statsMu.RUnlock()
	return m.stats
}

// FormatSnapshotReport generates a human-readable report of current balances.
func (m *Manager) FormatSnapshotReport() string {
	m.snapshotMu.RLock()
	snapshot := m.snapshot
	m.snapshotMu.RUnlock()

	if snapshot == nil {
		return "No snapshot available"
	}

	report := fmt.Sprintf("Inventory Snapshot (%s)\n", snapshot.CapturedAt.Format(time.RFC3339))
	report += "========================================\n\n"

	for exchangeName, exchBal := range snapshot.Exchanges {
		report += fmt.Sprintf("%s:\n", exchangeName)
		report += "----------------------------------------\n"

		currencies := make([]string, 0, len(exchBal.Balances))
		for c := range exchBal.Balances {
			currencies = append(currencies, c)
		}
		sort.Strings(currencies)

		for _, currency := range currencies {
			bal := exchBal.Balances[currency]
			report += fmt.Sprintf("  %-10s Free: %15s  Locked: %15s  Total: %15s\n",
				currency,
				bal.Free.Text('f', 4),
				bal.Locked.Text('f', 4),
				bal.Total.Text('f', 4))
		}
		report += "\n"
	}

	return report
}

// FormatDriftReport generates a human-readable report of current drift status.
func (m *Manager) FormatDriftReport() string {
	m.snapshotMu.RLock()
	driftStatus := m.driftStatus
	m.snapshotMu.RUnlock()

	if len(driftStatus) == 0 {
		return "No drift data available"
	}

	report := "Drift Status Report\n"
	report += "========================================\n\n"

	currencies := make([]string, 0, len(driftStatus))
	for c := range driftStatus {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)

	for _, currency := range currencies {
		status := driftStatus[currency]

		needsRebalance := ""
		if status.NeedsRebalance {
			needsRebalance = " [REBALANCE NEEDED]"
		}

		report += fmt.Sprintf("%s (Max Drift: %.1f%%)%s\n", currency, status.MaxDriftPct, needsRebalance)
		report += "----------------------------------------\n"

		exchanges := make([]string, 0, len(status.CurrentAlloc))
		for e := range status.CurrentAlloc {
			exchanges = append(exchanges, e)
		}
		sort.Strings(exchanges)

		report += fmt.Sprintf("  %-15s %10s %10s %10s\n", "Exchange", "Current", "Target", "Drift")
		for _, exchange := range exchanges {
			current := status.CurrentAlloc[exchange]
			target := status.TargetAlloc[exchange]
			drift := status.DriftPct[exchange]

			driftIndicator := ""
			if math.Abs(drift) >= m.config.DriftThresholdPct {
				if drift > 0 {
					driftIndicator = " [SURPLUS]"
				} else {
					driftIndicator = " [DEFICIT]"
				}
			}

			report += fmt.Sprintf("  %-15s %9.1f%% %9.1f%% %+9.1f%%%s\n",
				exchange, current, target, drift, driftIndicator)
		}
		report += "\n"
	}

	return report
}

// FormatRecommendationsReport generates a human-readable report of rebalance recommendations.
func (m *Manager) FormatRecommendationsReport(recommendations []*RebalanceRecommendation) string {
	if len(recommendations) == 0 {
		return "No rebalancing needed"
	}

	report := "Rebalance Recommendations\n"
	report += "========================================\n\n"

	for i, rec := range recommendations {
		priorityStr := "LOW"
		if rec.Priority == PriorityHigh {
			priorityStr = "HIGH"
		} else if rec.Priority == PriorityMedium {
			priorityStr = "MEDIUM"
		}

		report += fmt.Sprintf("%d. [%s] %s\n", i+1, priorityStr, rec.Currency)
		report += fmt.Sprintf("   Transfer: %s %s\n", rec.Amount.Text('f', 4), rec.Currency)
		report += fmt.Sprintf("   From: %s -> To: %s\n", rec.FromExchange, rec.ToExchange)
		report += fmt.Sprintf("   Reason: %s\n", rec.Reason)
		report += "\n"
	}

	return report
}
