// Package executor provides trade execution implementations.
package executor

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// ArbitrageCoordinator coordinates arbitrage execution across exchanges.
type ArbitrageCoordinator struct {
	registry *ExecutorRegistry
	config   *CoordinatorConfig

	// Statistics
	stats      ExecutionStats
	statsMu    sync.RWMutex

	// Rate limiting
	lastExecution time.Time
	executionMu   sync.Mutex
}

// CoordinatorConfig holds configuration for the arbitrage coordinator.
type CoordinatorConfig struct {
	// Safety limits
	DryRun           bool       // If true, don't execute real trades
	MaxTradeSize     *big.Float // Maximum trade size in quote currency
	MinTradeSize     *big.Float // Minimum trade size in quote currency
	MaxSlippageBps   int        // Maximum allowed slippage in basis points
	MinProfitBps     int        // Minimum profit threshold in basis points

	// Rate limiting
	MinTimeBetweenTrades time.Duration // Minimum time between trade attempts

	// Balance requirements
	MinBalanceBuffer *big.Float // Minimum balance to keep as buffer (don't use 100%)

	// Execution settings
	ExecutionTimeout time.Duration // Timeout for trade execution
	RetryAttempts    int           // Number of retry attempts on failure
}

// ExecutionStats tracks execution statistics.
type ExecutionStats struct {
	TotalOpportunities   int64
	ExecutedTrades       int64
	SuccessfulTrades     int64
	FailedTrades         int64
	TotalProfit          *big.Float
	TotalFees            *big.Float
	LastExecutionTime    time.Time
	LastError            string
	SkippedInsufficientBalance int64
	SkippedBelowMinProfit      int64
	SkippedRateLimited         int64
	SkippedDryRun              int64
}

// DefaultCoordinatorConfig returns sensible default configuration.
func DefaultCoordinatorConfig() *CoordinatorConfig {
	return &CoordinatorConfig{
		DryRun:               true,  // Safe default: dry run mode
		MaxTradeSize:         big.NewFloat(100),  // $100 max
		MinTradeSize:         big.NewFloat(10),   // $10 min
		MaxSlippageBps:       100,   // 1% max slippage
		MinProfitBps:         20,    // 0.2% min profit
		MinTimeBetweenTrades: 5 * time.Second,
		MinBalanceBuffer:     big.NewFloat(0.1), // Keep 10% buffer
		ExecutionTimeout:     30 * time.Second,
		RetryAttempts:        2,
	}
}

// NewArbitrageCoordinator creates a new arbitrage coordinator.
func NewArbitrageCoordinator(registry *ExecutorRegistry, config *CoordinatorConfig) *ArbitrageCoordinator {
	if config == nil {
		config = DefaultCoordinatorConfig()
	}

	return &ArbitrageCoordinator{
		registry: registry,
		config:   config,
		stats: ExecutionStats{
			TotalProfit: big.NewFloat(0),
			TotalFees:   big.NewFloat(0),
		},
	}
}

// ExecuteOpportunity attempts to execute an arbitrage opportunity.
func (c *ArbitrageCoordinator) ExecuteOpportunity(ctx context.Context, opp *types.ArbitrageOpportunity) (*ArbitrageExecution, error) {
	c.statsMu.Lock()
	c.stats.TotalOpportunities++
	c.statsMu.Unlock()

	execution := &ArbitrageExecution{
		ID:          fmt.Sprintf("exec-%s-%d", opp.ID, time.Now().UnixMilli()),
		Opportunity: opp,
		StartedAt:   time.Now(),
		DryRun:      c.config.DryRun,
	}

	// Check rate limiting
	c.executionMu.Lock()
	if time.Since(c.lastExecution) < c.config.MinTimeBetweenTrades {
		c.executionMu.Unlock()
		c.statsMu.Lock()
		c.stats.SkippedRateLimited++
		c.statsMu.Unlock()
		execution.Success = false
		execution.Error = "rate limited"
		return execution, nil
	}
	c.lastExecution = time.Now()
	c.executionMu.Unlock()

	// Validate opportunity
	if err := c.validateOpportunity(opp); err != nil {
		execution.Success = false
		execution.Error = fmt.Sprintf("validation failed: %v", err)
		return execution, nil
	}

	// Check if dry run
	if c.config.DryRun {
		c.statsMu.Lock()
		c.stats.SkippedDryRun++
		c.statsMu.Unlock()
		execution.Success = true
		execution.Error = "dry run mode - no actual trade executed"
		execution.NetProfit = opp.NetProfit
		execution.CompletedAt = time.Now()
		return execution, nil
	}

	// Get executors
	buyExecutor, ok := c.registry.Get(opp.BuyExchange)
	if !ok {
		execution.Success = false
		execution.Error = fmt.Sprintf("buy executor not found: %s", opp.BuyExchange)
		return execution, nil
	}

	sellExecutor, ok := c.registry.Get(opp.SellExchange)
	if !ok {
		execution.Success = false
		execution.Error = fmt.Sprintf("sell executor not found: %s", opp.SellExchange)
		return execution, nil
	}

	// Check executors are ready
	if !buyExecutor.IsReady() {
		execution.Success = false
		execution.Error = fmt.Sprintf("buy executor not ready: %s", opp.BuyExchange)
		return execution, nil
	}
	if !sellExecutor.IsReady() {
		execution.Success = false
		execution.Error = fmt.Sprintf("sell executor not ready: %s", opp.SellExchange)
		return execution, nil
	}

	// Parse pair to get currencies
	baseCurrency, quoteCurrency, err := parsePair(opp.Pair)
	if err != nil {
		execution.Success = false
		execution.Error = fmt.Sprintf("invalid pair: %v", err)
		return execution, nil
	}

	// Check balances
	tradeSize := c.determineTradeSize(opp)

	// Check quote currency balance on buy exchange
	buyBalance, err := buyExecutor.GetBalance(ctx, quoteCurrency)
	if err != nil {
		execution.Success = false
		execution.Error = fmt.Sprintf("failed to get buy balance: %v", err)
		return execution, nil
	}

	requiredBuyAmount := new(big.Float).Mul(tradeSize, opp.BuyPrice)
	if buyBalance.Free.Cmp(requiredBuyAmount) < 0 {
		c.statsMu.Lock()
		c.stats.SkippedInsufficientBalance++
		c.statsMu.Unlock()
		execution.Success = false
		execution.Error = fmt.Sprintf("insufficient %s balance on %s: have %s, need %s",
			quoteCurrency, opp.BuyExchange, buyBalance.Free.Text('f', 4), requiredBuyAmount.Text('f', 4))
		return execution, nil
	}

	// Check base currency balance on sell exchange (for the sell leg)
	sellBalance, err := sellExecutor.GetBalance(ctx, baseCurrency)
	if err != nil {
		execution.Success = false
		execution.Error = fmt.Sprintf("failed to get sell balance: %v", err)
		return execution, nil
	}

	if sellBalance.Free.Cmp(tradeSize) < 0 {
		c.statsMu.Lock()
		c.stats.SkippedInsufficientBalance++
		c.statsMu.Unlock()
		execution.Success = false
		execution.Error = fmt.Sprintf("insufficient %s balance on %s: have %s, need %s",
			baseCurrency, opp.SellExchange, sellBalance.Free.Text('f', 4), tradeSize.Text('f', 4))
		return execution, nil
	}

	// Execute buy leg
	buyCtx, buyCancel := context.WithTimeout(ctx, c.config.ExecutionTimeout)
	defer buyCancel()

	buyOrder, err := buyExecutor.PlaceMarketOrder(buyCtx, opp.Pair, OrderSideBuy, tradeSize)
	if err != nil {
		c.statsMu.Lock()
		c.stats.FailedTrades++
		c.statsMu.Unlock()
		execution.Success = false
		execution.Error = fmt.Sprintf("buy order failed: %v", err)
		execution.BuyTrade = &TradeResult{
			Success: false,
			Error:   err.Error(),
		}
		return execution, nil
	}

	execution.BuyTrade = &TradeResult{
		Order:   buyOrder,
		Success: true,
	}

	// Execute sell leg
	sellCtx, sellCancel := context.WithTimeout(ctx, c.config.ExecutionTimeout)
	defer sellCancel()

	// Use actual filled amount for sell
	sellAmount := buyOrder.FilledAmount
	if sellAmount == nil || sellAmount.Sign() == 0 {
		sellAmount = tradeSize
	}

	sellOrder, err := sellExecutor.PlaceMarketOrder(sellCtx, opp.Pair, OrderSideSell, sellAmount)
	if err != nil {
		c.statsMu.Lock()
		c.stats.FailedTrades++
		c.statsMu.Unlock()
		execution.Success = false
		execution.Error = fmt.Sprintf("sell order failed: %v", err)
		execution.SellTrade = &TradeResult{
			Success: false,
			Error:   err.Error(),
		}
		// Note: Buy order succeeded, may need manual intervention
		return execution, nil
	}

	execution.SellTrade = &TradeResult{
		Order:   sellOrder,
		Success: true,
	}

	// Calculate actual profit
	buyValue := new(big.Float).Mul(buyOrder.FilledAmount, buyOrder.AveragePrice)
	sellValue := new(big.Float).Mul(sellOrder.FilledAmount, sellOrder.AveragePrice)

	execution.GrossProfit = new(big.Float).Sub(sellValue, buyValue)

	// Calculate total fees
	totalFees := big.NewFloat(0)
	if buyOrder.Fee != nil {
		totalFees = new(big.Float).Add(totalFees, buyOrder.Fee)
	}
	if sellOrder.Fee != nil {
		totalFees = new(big.Float).Add(totalFees, sellOrder.Fee)
	}
	execution.TotalFees = totalFees

	execution.NetProfit = new(big.Float).Sub(execution.GrossProfit, totalFees)
	execution.Success = true
	execution.CompletedAt = time.Now()

	// Update stats
	c.statsMu.Lock()
	c.stats.ExecutedTrades++
	c.stats.SuccessfulTrades++
	c.stats.TotalProfit = new(big.Float).Add(c.stats.TotalProfit, execution.NetProfit)
	c.stats.TotalFees = new(big.Float).Add(c.stats.TotalFees, totalFees)
	c.stats.LastExecutionTime = time.Now()
	c.statsMu.Unlock()

	return execution, nil
}

// ExecuteChainOpportunity attempts to execute a chain arbitrage opportunity.
func (c *ArbitrageCoordinator) ExecuteChainOpportunity(ctx context.Context, opp *types.ChainArbitrageOpportunity) (*ArbitrageExecution, error) {
	// For chain arbitrage, we need to execute multiple hops
	// This is more complex and risky, so we implement a simplified version

	execution := &ArbitrageExecution{
		ID:        fmt.Sprintf("chain-exec-%s-%d", opp.ID, time.Now().UnixMilli()),
		StartedAt: time.Now(),
		DryRun:    c.config.DryRun,
	}

	if c.config.DryRun {
		c.statsMu.Lock()
		c.stats.SkippedDryRun++
		c.statsMu.Unlock()
		execution.Success = true
		execution.Error = "dry run mode - no actual trade executed"
		execution.NetProfit = opp.NetProfit
		execution.CompletedAt = time.Now()
		return execution, nil
	}

	// Chain execution is more complex - for safety, we only support 2-hop chains for now
	if len(opp.Chain) != 2 {
		execution.Success = false
		execution.Error = fmt.Sprintf("chain arbitrage execution only supports 2-hop chains, got %d hops", len(opp.Chain))
		return execution, nil
	}

	// For 2-hop chain, it's equivalent to simple arbitrage
	simpleOpp := &types.ArbitrageOpportunity{
		ID:           opp.ID,
		Pair:         opp.Pair,
		BuyExchange:  opp.StartExchange,
		SellExchange: opp.EndExchange,
		BuyPrice:     opp.Hops[0].Price,
		SellPrice:    opp.Hops[len(opp.Hops)-1].Price,
		SpreadBps:    opp.SpreadBps,
		NetProfitBps: opp.NetProfitBps,
		DetectedAt:   opp.DetectedAt,
		ExpiresAt:    opp.ExpiresAt,
		IsValid:      opp.IsValid,
	}

	return c.ExecuteOpportunity(ctx, simpleOpp)
}

// validateOpportunity validates an opportunity before execution.
func (c *ArbitrageCoordinator) validateOpportunity(opp *types.ArbitrageOpportunity) error {
	// Check if opportunity is still valid
	if !opp.IsValid {
		return fmt.Errorf("opportunity marked as invalid")
	}

	// Check if opportunity has expired
	if time.Now().After(opp.ExpiresAt) {
		return fmt.Errorf("opportunity expired")
	}

	// Check minimum profit
	if opp.NetProfitBps < c.config.MinProfitBps {
		return fmt.Errorf("profit %d bps below minimum %d bps", opp.NetProfitBps, c.config.MinProfitBps)
	}

	return nil
}

// determineTradeSize determines the optimal trade size for an opportunity.
func (c *ArbitrageCoordinator) determineTradeSize(opp *types.ArbitrageOpportunity) *big.Float {
	// Start with config max trade size
	tradeSize := new(big.Float).Set(c.config.MaxTradeSize)

	// Limit by available liquidity on buy side
	if opp.BuyLiquidity != nil && opp.BuyLiquidity.Cmp(tradeSize) < 0 {
		tradeSize = new(big.Float).Set(opp.BuyLiquidity)
	}

	// Limit by available liquidity on sell side
	if opp.SellLiquidity != nil && opp.SellLiquidity.Cmp(tradeSize) < 0 {
		tradeSize = new(big.Float).Set(opp.SellLiquidity)
	}

	// Ensure minimum trade size
	if tradeSize.Cmp(c.config.MinTradeSize) < 0 {
		tradeSize = new(big.Float).Set(c.config.MinTradeSize)
	}

	return tradeSize
}

// GetStats returns execution statistics.
func (c *ArbitrageCoordinator) GetStats() ExecutionStats {
	c.statsMu.RLock()
	defer c.statsMu.RUnlock()
	return c.stats
}

// ResetStats resets execution statistics.
func (c *ArbitrageCoordinator) ResetStats() {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	c.stats = ExecutionStats{
		TotalProfit: big.NewFloat(0),
		TotalFees:   big.NewFloat(0),
	}
}

// SetDryRun enables or disables dry run mode.
func (c *ArbitrageCoordinator) SetDryRun(dryRun bool) {
	c.config.DryRun = dryRun
}

// IsDryRun returns whether dry run mode is enabled.
func (c *ArbitrageCoordinator) IsDryRun() bool {
	return c.config.DryRun
}

// Helper functions

// parsePair parses a trading pair into base and quote currencies.
func parsePair(pair string) (base, quote string, err error) {
	// Handle different formats: "BTC/USDT", "BTCUSDT"
	if idx := findSeparator(pair); idx > 0 {
		return pair[:idx], pair[idx+1:], nil
	}

	// Try common quote currencies
	quotes := []string{"USDT", "USDC", "BUSD", "BTC", "ETH", "GALA", "GUSDC", "GUSDT"}
	for _, q := range quotes {
		if len(pair) > len(q) && pair[len(pair)-len(q):] == q {
			return pair[:len(pair)-len(q)], q, nil
		}
	}

	return "", "", fmt.Errorf("unable to parse pair: %s", pair)
}

// findSeparator finds the separator in a pair string.
func findSeparator(pair string) int {
	for i, c := range pair {
		if c == '/' || c == '-' || c == '_' {
			return i
		}
	}
	return -1
}
