package graph

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/executor"
)

// CycleExecutor handles execution of arbitrage cycles on GSwap.
type CycleExecutor struct {
	gswap  *executor.GSwapExecutor
	config *ExecutorConfig

	// Statistics
	stats ExecutorStats
	mu    sync.RWMutex
}

// ExecutorConfig holds configuration for cycle execution.
type ExecutorConfig struct {
	DryRun           bool          // If true, simulate but don't execute
	MaxTradeSize     float64       // Maximum trade size per cycle
	MinTradeSize     float64       // Minimum trade size per cycle
	SlippageBps      int           // Slippage tolerance in basis points
	MinProfitBps     int           // Minimum profit to execute
	MaxConcurrent    int           // Maximum concurrent executions
	CooldownDuration time.Duration // Cooldown between executions
}

// DefaultExecutorConfig returns default executor configuration.
func DefaultExecutorConfig() *ExecutorConfig {
	return &ExecutorConfig{
		DryRun:           true,
		MaxTradeSize:     1000,
		MinTradeSize:     10,
		SlippageBps:      100, // 1%
		MinProfitBps:     20,
		MaxConcurrent:    1,
		CooldownDuration: 5 * time.Second,
	}
}

// ExecutorStats tracks execution statistics.
type ExecutorStats struct {
	TotalAttempts      int
	SuccessfulTrades   int
	FailedTrades       int
	DryRunSimulations  int
	TotalProfit        *big.Float
	TotalVolume        *big.Float
	LastExecutionTime  time.Time
	LastExecutionError string
}

// TradeStep represents a single swap in a cycle execution.
type TradeStep struct {
	TokenIn   string
	TokenOut  string
	AmountIn  *big.Float
	AmountOut *big.Float
	FeeTier   int
	TxID      string
	Status    string
	Error     string
}

// CycleExecution represents the result of executing a cycle.
type CycleExecution struct {
	Cycle           *Cycle
	StartTime       time.Time
	EndTime         time.Time
	InputAmount     *big.Float
	OutputAmount    *big.Float
	Profit          *big.Float
	ProfitBps       int
	Steps           []*TradeStep
	Success         bool
	DryRun          bool
	Error           string
	TransactionIDs  []string
}

// NewCycleExecutor creates a new cycle executor.
func NewCycleExecutor(gswap *executor.GSwapExecutor, config *ExecutorConfig) *CycleExecutor {
	if config == nil {
		config = DefaultExecutorConfig()
	}

	return &CycleExecutor{
		gswap:  gswap,
		config: config,
		stats: ExecutorStats{
			TotalProfit: big.NewFloat(0),
			TotalVolume: big.NewFloat(0),
		},
	}
}

// ExecuteCycle executes an arbitrage cycle.
func (e *CycleExecutor) ExecuteCycle(ctx context.Context, cycle *Cycle, result *CycleResult, inputAmount float64) (*CycleExecution, error) {
	e.mu.Lock()
	e.stats.TotalAttempts++
	e.mu.Unlock()

	execution := &CycleExecution{
		Cycle:       cycle,
		StartTime:   time.Now(),
		InputAmount: big.NewFloat(inputAmount),
		Steps:       make([]*TradeStep, 0, len(cycle.Path)-1),
		DryRun:      e.config.DryRun,
	}

	// Validate cycle
	if err := e.validateCycle(cycle, result, inputAmount); err != nil {
		execution.Error = err.Error()
		execution.EndTime = time.Now()
		return execution, err
	}

	// Check balance for first token
	startToken := string(cycle.Path[0])
	balance, err := e.gswap.GetBalance(ctx, startToken)
	if err != nil {
		execution.Error = fmt.Sprintf("failed to get balance: %v", err)
		execution.EndTime = time.Now()
		return execution, err
	}

	balanceFloat, _ := balance.Free.Float64()
	if balanceFloat < inputAmount {
		execution.Error = fmt.Sprintf("insufficient balance: have %.4f %s, need %.4f", balanceFloat, startToken, inputAmount)
		execution.EndTime = time.Now()
		return execution, fmt.Errorf(execution.Error)
	}

	// Execute cycle
	if e.config.DryRun {
		return e.simulateCycleExecution(ctx, cycle, inputAmount, execution)
	}

	return e.executeCycleReal(ctx, cycle, inputAmount, execution)
}

// validateCycle validates that a cycle can be executed.
func (e *CycleExecutor) validateCycle(cycle *Cycle, result *CycleResult, inputAmount float64) error {
	if cycle == nil {
		return fmt.Errorf("cycle is nil")
	}

	if len(cycle.Path) < 3 {
		return fmt.Errorf("cycle too short: %d tokens", len(cycle.Path))
	}

	if cycle.Path[0] != cycle.Path[len(cycle.Path)-1] {
		return fmt.Errorf("cycle does not return to start token")
	}

	if result != nil && result.ProfitBps < e.config.MinProfitBps {
		return fmt.Errorf("profit %d bps below minimum %d bps", result.ProfitBps, e.config.MinProfitBps)
	}

	if inputAmount < e.config.MinTradeSize {
		return fmt.Errorf("trade size %.4f below minimum %.4f", inputAmount, e.config.MinTradeSize)
	}

	if inputAmount > e.config.MaxTradeSize {
		return fmt.Errorf("trade size %.4f above maximum %.4f", inputAmount, e.config.MaxTradeSize)
	}

	return nil
}

// simulateCycleExecution simulates executing a cycle without real trades.
func (e *CycleExecutor) simulateCycleExecution(ctx context.Context, cycle *Cycle, inputAmount float64, execution *CycleExecution) (*CycleExecution, error) {
	log.Printf("[executor] DRY RUN: Simulating cycle %d with %.4f %s", cycle.ID, inputAmount, cycle.Path[0])

	currentAmount := big.NewFloat(inputAmount)

	// Simulate each swap in the cycle
	for i := 0; i < len(cycle.Path)-1; i++ {
		tokenIn := string(cycle.Path[i])
		tokenOut := string(cycle.Path[i+1])

		// Get quote for this swap
		pair := tokenIn + "/" + tokenOut
		quote, err := e.gswap.GetQuote(ctx, pair, executor.OrderSideSell, currentAmount)
		if err != nil {
			// Try reverse pair
			pair = tokenOut + "/" + tokenIn
			quote, err = e.gswap.GetQuote(ctx, pair, executor.OrderSideBuy, currentAmount)
			if err != nil {
				execution.Error = fmt.Sprintf("failed to get quote for %s -> %s: %v", tokenIn, tokenOut, err)
				execution.EndTime = time.Now()
				return execution, fmt.Errorf(execution.Error)
			}
		}

		amountOut := quote.BidSize
		if amountOut == nil || amountOut.Sign() <= 0 {
			amountOut = new(big.Float).Mul(currentAmount, quote.Price)
		}

		step := &TradeStep{
			TokenIn:   tokenIn,
			TokenOut:  tokenOut,
			AmountIn:  new(big.Float).Copy(currentAmount),
			AmountOut: amountOut,
			Status:    "simulated",
			TxID:      fmt.Sprintf("dry-run-%d-%d", cycle.ID, i),
		}
		execution.Steps = append(execution.Steps, step)

		log.Printf("[executor] DRY RUN: Step %d: %s -> %s, in=%.6f, out=%.6f",
			i+1, tokenIn, tokenOut,
			floatValue(currentAmount), floatValue(amountOut))

		currentAmount = amountOut
	}

	execution.OutputAmount = currentAmount
	execution.Profit = new(big.Float).Sub(currentAmount, execution.InputAmount)
	profitFloat, _ := execution.Profit.Float64()
	inputFloat, _ := execution.InputAmount.Float64()
	execution.ProfitBps = int((profitFloat / inputFloat) * 10000)
	execution.Success = true
	execution.EndTime = time.Now()

	e.mu.Lock()
	e.stats.DryRunSimulations++
	e.stats.LastExecutionTime = time.Now()
	e.mu.Unlock()

	log.Printf("[executor] DRY RUN: Cycle %d complete. Input=%.6f, Output=%.6f, Profit=%.6f (%d bps)",
		cycle.ID, inputFloat, floatValue(currentAmount), profitFloat, execution.ProfitBps)

	return execution, nil
}

// executeCycleReal executes a cycle with real trades.
func (e *CycleExecutor) executeCycleReal(ctx context.Context, cycle *Cycle, inputAmount float64, execution *CycleExecution) (*CycleExecution, error) {
	log.Printf("[executor] LIVE: Executing cycle %d with %.4f %s", cycle.ID, inputAmount, cycle.Path[0])

	currentAmount := big.NewFloat(inputAmount)

	// Execute each swap in the cycle
	for i := 0; i < len(cycle.Path)-1; i++ {
		tokenIn := string(cycle.Path[i])
		tokenOut := string(cycle.Path[i+1])

		step := &TradeStep{
			TokenIn:  tokenIn,
			TokenOut: tokenOut,
			AmountIn: new(big.Float).Copy(currentAmount),
		}

		// Execute the swap
		pair := tokenIn + "/" + tokenOut
		order, err := e.gswap.PlaceMarketOrder(ctx, pair, executor.OrderSideSell, currentAmount)
		if err != nil {
			// Try reverse pair with buy
			pair = tokenOut + "/" + tokenIn
			order, err = e.gswap.PlaceMarketOrder(ctx, pair, executor.OrderSideBuy, currentAmount)
		}

		if err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			execution.Steps = append(execution.Steps, step)
			execution.Error = fmt.Sprintf("swap %d failed (%s -> %s): %v", i+1, tokenIn, tokenOut, err)
			execution.EndTime = time.Now()

			e.mu.Lock()
			e.stats.FailedTrades++
			e.stats.LastExecutionError = execution.Error
			e.mu.Unlock()

			return execution, fmt.Errorf(execution.Error)
		}

		step.TxID = order.TransactionID
		step.Status = "submitted"
		execution.TransactionIDs = append(execution.TransactionIDs, order.TransactionID)

		// Wait for order to fill
		filledOrder, err := e.waitForOrderFill(ctx, order.ID, 30*time.Second)
		if err != nil {
			step.Status = "timeout"
			step.Error = err.Error()
			execution.Steps = append(execution.Steps, step)
			execution.Error = fmt.Sprintf("swap %d timeout (%s -> %s): %v", i+1, tokenIn, tokenOut, err)
			execution.EndTime = time.Now()

			e.mu.Lock()
			e.stats.FailedTrades++
			e.stats.LastExecutionError = execution.Error
			e.mu.Unlock()

			return execution, fmt.Errorf(execution.Error)
		}

		step.AmountOut = filledOrder.FilledAmount
		step.Status = "filled"
		execution.Steps = append(execution.Steps, step)

		log.Printf("[executor] LIVE: Step %d complete: %s -> %s, tx=%s, out=%.6f",
			i+1, tokenIn, tokenOut, order.TransactionID, floatValue(filledOrder.FilledAmount))

		currentAmount = filledOrder.FilledAmount
	}

	execution.OutputAmount = currentAmount
	execution.Profit = new(big.Float).Sub(currentAmount, execution.InputAmount)
	profitFloat, _ := execution.Profit.Float64()
	inputFloat, _ := execution.InputAmount.Float64()
	execution.ProfitBps = int((profitFloat / inputFloat) * 10000)
	execution.Success = true
	execution.EndTime = time.Now()

	e.mu.Lock()
	e.stats.SuccessfulTrades++
	e.stats.TotalProfit.Add(e.stats.TotalProfit, execution.Profit)
	e.stats.TotalVolume.Add(e.stats.TotalVolume, execution.InputAmount)
	e.stats.LastExecutionTime = time.Now()
	e.mu.Unlock()

	log.Printf("[executor] LIVE: Cycle %d complete. Input=%.6f, Output=%.6f, Profit=%.6f (%d bps)",
		cycle.ID, inputFloat, floatValue(currentAmount), profitFloat, execution.ProfitBps)

	return execution, nil
}

// waitForOrderFill waits for an order to be filled.
func (e *CycleExecutor) waitForOrderFill(ctx context.Context, orderID string, timeout time.Duration) (*executor.Order, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("timeout waiting for order %s", orderID)
			}

			order, err := e.gswap.GetOrder(ctx, orderID)
			if err != nil {
				continue // Retry on error
			}

			if order.Status == executor.OrderStatusFilled {
				return order, nil
			}

			if order.Status == executor.OrderStatusFailed || order.Status == executor.OrderStatusCanceled {
				return nil, fmt.Errorf("order %s failed with status %s", orderID, order.Status)
			}
		}
	}
}

// GetStats returns execution statistics.
func (e *CycleExecutor) GetStats() ExecutorStats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.stats
}

// IsDryRun returns whether the executor is in dry-run mode.
func (e *CycleExecutor) IsDryRun() bool {
	return e.config.DryRun
}

// SetDryRun sets the dry-run mode.
func (e *CycleExecutor) SetDryRun(dryRun bool) {
	e.config.DryRun = dryRun
}

// floatValue safely extracts float64 from big.Float.
func floatValue(f *big.Float) float64 {
	if f == nil {
		return 0
	}
	v, _ := f.Float64()
	return v
}

// FormatExecution returns a human-readable string for a cycle execution.
func FormatExecution(exec *CycleExecution) string {
	if exec == nil {
		return "<nil>"
	}

	mode := "LIVE"
	if exec.DryRun {
		mode = "DRY RUN"
	}

	status := "SUCCESS"
	if !exec.Success {
		status = "FAILED"
	}

	inputVal := floatValue(exec.InputAmount)
	outputVal := floatValue(exec.OutputAmount)
	profitVal := floatValue(exec.Profit)

	result := fmt.Sprintf("[%s] %s - Cycle %d\n", mode, status, exec.Cycle.ID)
	result += fmt.Sprintf("  Path: %s\n", formatPath(exec.Cycle.Path))
	result += fmt.Sprintf("  Input: %.6f %s\n", inputVal, exec.Cycle.Path[0])
	result += fmt.Sprintf("  Output: %.6f %s\n", outputVal, exec.Cycle.Path[0])
	result += fmt.Sprintf("  Profit: %.6f (%d bps)\n", profitVal, exec.ProfitBps)
	result += fmt.Sprintf("  Duration: %v\n", exec.EndTime.Sub(exec.StartTime))

	if exec.Error != "" {
		result += fmt.Sprintf("  Error: %s\n", exec.Error)
	}

	if len(exec.TransactionIDs) > 0 {
		result += fmt.Sprintf("  Transactions: %v\n", exec.TransactionIDs)
	}

	return result
}

// formatPath formats a token path as a string.
func formatPath(path []Token) string {
	result := ""
	for i, t := range path {
		if i > 0 {
			result += " -> "
		}
		result += string(t)
	}
	return result
}
