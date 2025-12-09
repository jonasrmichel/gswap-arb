// Package inventory provides balance tracking and rebalancing logic for arbitrage trading.
package inventory

import (
	"math/big"
	"time"
)

// ExchangeBalance represents the balance state for a single exchange.
type ExchangeBalance struct {
	Exchange  string                  `json:"exchange"`
	Balances  map[string]*TokenBalance `json:"balances"` // currency -> balance
	UpdatedAt time.Time               `json:"updated_at"`
}

// TokenBalance represents the balance of a single token on an exchange.
type TokenBalance struct {
	Currency  string     `json:"currency"`
	Free      *big.Float `json:"free"`
	Locked    *big.Float `json:"locked"`
	Total     *big.Float `json:"total"`
	USDValue  *big.Float `json:"usd_value,omitempty"` // Estimated USD value
	UpdatedAt time.Time  `json:"updated_at"`
}

// InventorySnapshot represents a point-in-time snapshot of all balances across exchanges.
type InventorySnapshot struct {
	Exchanges  map[string]*ExchangeBalance `json:"exchanges"`
	TotalUSD   *big.Float                  `json:"total_usd"`
	CapturedAt time.Time                   `json:"captured_at"`
}

// TokenAllocation represents how a token is distributed across exchanges.
type TokenAllocation struct {
	Currency       string                       `json:"currency"`
	TotalAmount    *big.Float                   `json:"total_amount"`
	TotalUSDValue  *big.Float                   `json:"total_usd_value"`
	ByExchange     map[string]*big.Float        `json:"by_exchange"`      // exchange -> amount
	ByExchangePct  map[string]float64           `json:"by_exchange_pct"`  // exchange -> percentage
}

// DriftStatus represents how far current allocation has drifted from target.
type DriftStatus struct {
	Currency       string             `json:"currency"`
	CurrentAlloc   map[string]float64 `json:"current_allocation"`  // exchange -> percentage
	TargetAlloc    map[string]float64 `json:"target_allocation"`   // exchange -> percentage
	DriftPct       map[string]float64 `json:"drift_pct"`           // exchange -> drift percentage (current - target)
	MaxDriftPct    float64            `json:"max_drift_pct"`       // Maximum drift across all exchanges
	NeedsRebalance bool               `json:"needs_rebalance"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

// RebalanceRecommendation represents a suggested bridge operation to rebalance inventory.
type RebalanceRecommendation struct {
	ID             string     `json:"id"`
	Currency       string     `json:"currency"`
	FromExchange   string     `json:"from_exchange"`
	ToExchange     string     `json:"to_exchange"`
	Amount         *big.Float `json:"amount"`
	AmountUSD      *big.Float `json:"amount_usd"`
	CurrentDrift   float64    `json:"current_drift_pct"`   // Current drift on the from exchange
	ExpectedDrift  float64    `json:"expected_drift_pct"`  // Expected drift after rebalance
	Priority       int        `json:"priority"`            // 1 = high, 2 = medium, 3 = low
	Reason         string     `json:"reason"`
	CreatedAt      time.Time  `json:"created_at"`
}

// RebalancePriority levels
const (
	PriorityHigh   = 1
	PriorityMedium = 2
	PriorityLow    = 3
)

// InventoryConfig holds configuration for inventory management.
type InventoryConfig struct {
	// Drift thresholds
	DriftThresholdPct     float64 `json:"drift_threshold_pct"`      // Percentage drift to trigger warning (e.g., 20.0)
	CriticalDriftPct      float64 `json:"critical_drift_pct"`       // Critical drift level (e.g., 40.0)

	// Rebalancing limits
	MinRebalanceAmountUSD float64 `json:"min_rebalance_amount_usd"` // Minimum USD value to bridge
	MaxRebalanceAmountUSD float64 `json:"max_rebalance_amount_usd"` // Maximum USD value per bridge

	// Target allocations: exchange -> currency -> target percentage
	TargetAllocations map[string]map[string]float64 `json:"target_allocations"`

	// Monitoring
	CheckIntervalSeconds int  `json:"check_interval_seconds"`
	AlertOnDrift         bool `json:"alert_on_drift"`

	// Tracked currencies (if empty, track all)
	TrackedCurrencies []string `json:"tracked_currencies,omitempty"`
}

// DefaultInventoryConfig returns sensible default configuration.
func DefaultInventoryConfig() *InventoryConfig {
	return &InventoryConfig{
		DriftThresholdPct:     20.0,  // 20% drift triggers warning
		CriticalDriftPct:      40.0,  // 40% drift is critical
		MinRebalanceAmountUSD: 50.0,  // Minimum $50 to bridge
		MaxRebalanceAmountUSD: 1000.0, // Maximum $1000 per bridge
		TargetAllocations:     make(map[string]map[string]float64),
		CheckIntervalSeconds:  300,   // Check every 5 minutes
		AlertOnDrift:          true,
		TrackedCurrencies:     []string{"GALA", "USDT", "USDC", "GUSDC", "GUSDT"},
	}
}

// InventoryStats tracks inventory monitoring statistics.
type InventoryStats struct {
	SnapshotsCollected     int64     `json:"snapshots_collected"`
	DriftAlertsTriggered   int64     `json:"drift_alerts_triggered"`
	RebalancesRecommended  int64     `json:"rebalances_recommended"`
	LastSnapshotAt         time.Time `json:"last_snapshot_at"`
	LastDriftCheckAt       time.Time `json:"last_drift_check_at"`
}
