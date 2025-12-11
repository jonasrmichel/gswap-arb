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

	// Execution strategy settings (hybrid #5 + #7)
	PreValidateQuotes    bool // Fetch fresh quotes for all edges before executing
	ProfitScalingPerHop  int  // Additional profit required per hop (bps)
	MidCycleBailout      bool // Check remaining path profitability after each swap
	BailoutThresholdBps  int  // Minimum remaining profit to continue (bps)
	SwapDeadlineSecs     int  // Deadline for each swap (seconds)
	QuoteMaxAgeSecs      int  // Maximum age for quotes during validation (seconds)

	// Liquidity validation settings
	ValidateLiquidity    bool // Check price impact at trade size (not just 1 unit)
	MaxPriceImpactBps    int  // Maximum acceptable price impact per edge (bps)
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

		// Execution strategy defaults
		PreValidateQuotes:   true,  // Always validate quotes before execution
		ProfitScalingPerHop: 20,    // +20 bps minimum per additional hop
		MidCycleBailout:     true,  // Enable mid-cycle profitability checks
		BailoutThresholdBps: -100,  // Bail if remaining path would lose > 1%
		SwapDeadlineSecs:    60,    // 1 minute deadline per swap (faster than default 5 min)
		QuoteMaxAgeSecs:     5,     // Quotes older than 5 seconds are stale

		// Liquidity validation defaults
		ValidateLiquidity: true, // Check price impact at trade size
		MaxPriceImpactBps: 200,  // Max 2% price impact per edge
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

// PreExecutionQuote holds a fresh quote fetched during pre-validation.
type PreExecutionQuote struct {
	TokenIn        string
	TokenOut       string
	Rate           float64   // Output per unit input (1-unit quote)
	RateAtSize     float64   // Output per unit at actual trade size
	PriceImpactBps int       // Price impact in basis points (RateAtSize vs Rate)
	Timestamp      time.Time
	Error          error
}

// PreValidationResult holds the result of pre-execution quote validation.
type PreValidationResult struct {
	Quotes            []*PreExecutionQuote
	ExpectedProfit    float64 // Expected profit ratio based on fresh quotes
	ExpectedProfitBps int
	MaxPriceImpactBps int  // Maximum price impact across all edges
	IsStillProfitable bool
	HasSufficientLiq  bool // True if all edges have acceptable price impact
	ValidationTime    time.Duration
	Error             string
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

// preValidateQuotes fetches fresh quotes for all edges in the cycle in parallel.
// This is Strategy #5: Parallel Quote Refresh before execution.
// Also validates liquidity by checking price impact at the actual trade size.
func (e *CycleExecutor) preValidateQuotes(ctx context.Context, cycle *Cycle, inputAmount float64) *PreValidationResult {
	startTime := time.Now()
	numEdges := len(cycle.Path) - 1

	result := &PreValidationResult{
		Quotes:           make([]*PreExecutionQuote, numEdges),
		HasSufficientLiq: true, // Assume sufficient until proven otherwise
	}

	// Calculate expected amounts at each step to check price impact
	// Starting with inputAmount, we need to know roughly how much to quote at each edge
	expectedAmounts := make([]float64, numEdges)
	expectedAmounts[0] = inputAmount

	// Fetch all quotes in parallel - both 1-unit quotes and quotes at expected trade size
	var wg sync.WaitGroup
	quoteChan := make(chan struct {
		idx   int
		quote *PreExecutionQuote
	}, numEdges)

	for i := 0; i < numEdges; i++ {
		wg.Add(1)
		go func(idx int, tradeAmount float64) {
			defer wg.Done()

			tokenIn := string(cycle.Path[idx])
			tokenOut := string(cycle.Path[idx+1])

			quote := &PreExecutionQuote{
				TokenIn:   tokenIn,
				TokenOut:  tokenOut,
				Timestamp: time.Now(),
			}

			// Get 1-unit quote for baseline rate
			pair := tokenIn + "/" + tokenOut
			q1, err := e.gswap.GetQuote(ctx, pair, executor.OrderSideSell, big.NewFloat(1.0))
			if err != nil {
				pair = tokenOut + "/" + tokenIn
				q1, err = e.gswap.GetQuote(ctx, pair, executor.OrderSideBuy, big.NewFloat(1.0))
			}

			if err != nil {
				quote.Error = err
				quoteChan <- struct {
					idx   int
					quote *PreExecutionQuote
				}{idx, quote}
				return
			}

			if q1.Price != nil {
				quote.Rate, _ = q1.Price.Float64()
			}

			// If liquidity validation is enabled, also get quote at trade size
			if e.config.ValidateLiquidity && tradeAmount > 1.0 {
				pair = tokenIn + "/" + tokenOut
				qSize, err := e.gswap.GetQuote(ctx, pair, executor.OrderSideSell, big.NewFloat(tradeAmount))
				if err != nil {
					pair = tokenOut + "/" + tokenIn
					qSize, err = e.gswap.GetQuote(ctx, pair, executor.OrderSideBuy, big.NewFloat(tradeAmount))
				}

				if err != nil {
					// Can't get quote at size - likely insufficient liquidity
					quote.Error = fmt.Errorf("insufficient liquidity at size %.2f: %w", tradeAmount, err)
				} else if qSize.Price != nil {
					rateAtSize, _ := qSize.Price.Float64()
					quote.RateAtSize = rateAtSize

					// Calculate price impact: (1-unit rate - trade size rate) / 1-unit rate * 10000
					if quote.Rate > 0 {
						impact := (quote.Rate - rateAtSize) / quote.Rate * 10000
						quote.PriceImpactBps = int(impact)
					}
				}
			} else {
				// No liquidity validation or small trade - assume no price impact
				quote.RateAtSize = quote.Rate
				quote.PriceImpactBps = 0
			}

			quoteChan <- struct {
				idx   int
				quote *PreExecutionQuote
			}{idx, quote}
		}(i, expectedAmounts[0]) // Use input amount for first edge; this is approximate
	}

	// Close channel when all goroutines complete
	go func() {
		wg.Wait()
		close(quoteChan)
	}()

	// Collect results
	for res := range quoteChan {
		result.Quotes[res.idx] = res.quote
	}

	result.ValidationTime = time.Since(startTime)

	// Calculate expected profit from fresh quotes and check liquidity
	profitRatio := 1.0
	maxPriceImpact := 0
	for i, quote := range result.Quotes {
		if quote.Error != nil {
			result.Error = fmt.Sprintf("quote %d (%s->%s) failed: %v", i, quote.TokenIn, quote.TokenOut, quote.Error)
			result.IsStillProfitable = false
			result.HasSufficientLiq = false
			return result
		}
		if quote.Rate <= 0 {
			result.Error = fmt.Sprintf("quote %d (%s->%s) has invalid rate: %v", i, quote.TokenIn, quote.TokenOut, quote.Rate)
			result.IsStillProfitable = false
			return result
		}

		// Use rate at trade size for profit calculation if available
		rateToUse := quote.Rate
		if quote.RateAtSize > 0 {
			rateToUse = quote.RateAtSize
		}
		profitRatio *= rateToUse

		// Track max price impact
		if quote.PriceImpactBps > maxPriceImpact {
			maxPriceImpact = quote.PriceImpactBps
		}

		// Check if price impact exceeds threshold
		if e.config.ValidateLiquidity && quote.PriceImpactBps > e.config.MaxPriceImpactBps {
			result.HasSufficientLiq = false
			log.Printf("[executor] Edge %s->%s has excessive price impact: %d bps (max: %d)",
				quote.TokenIn, quote.TokenOut, quote.PriceImpactBps, e.config.MaxPriceImpactBps)
		}
	}

	result.ExpectedProfit = profitRatio
	result.ExpectedProfitBps = int((profitRatio - 1.0) * 10000)
	result.MaxPriceImpactBps = maxPriceImpact

	// Check liquidity first
	if !result.HasSufficientLiq {
		result.Error = fmt.Sprintf("insufficient liquidity: max price impact %d bps exceeds threshold %d bps",
			maxPriceImpact, e.config.MaxPriceImpactBps)
		result.IsStillProfitable = false
		return result
	}

	// Calculate required profit based on cycle length (Strategy #3: profit scaling)
	requiredProfitBps := e.config.MinProfitBps + (numEdges-2)*e.config.ProfitScalingPerHop
	result.IsStillProfitable = result.ExpectedProfitBps >= requiredProfitBps

	if !result.IsStillProfitable {
		result.Error = fmt.Sprintf("profit %d bps below required %d bps for %d-hop cycle",
			result.ExpectedProfitBps, requiredProfitBps, numEdges)
	}

	return result
}

// calculateRemainingProfit calculates expected profit for remaining path from current position.
// Used for mid-cycle bailout detection (Strategy #6).
func (e *CycleExecutor) calculateRemainingProfit(ctx context.Context, cycle *Cycle, currentStep int, currentAmount *big.Float) (profitBps int, err error) {
	if currentStep >= len(cycle.Path)-1 {
		return 0, nil // No remaining steps
	}

	amount := new(big.Float).Copy(currentAmount)

	for i := currentStep; i < len(cycle.Path)-1; i++ {
		tokenIn := string(cycle.Path[i])
		tokenOut := string(cycle.Path[i+1])

		pair := tokenIn + "/" + tokenOut
		quote, err := e.gswap.GetQuote(ctx, pair, executor.OrderSideSell, amount)
		if err != nil {
			pair = tokenOut + "/" + tokenIn
			quote, err = e.gswap.GetQuote(ctx, pair, executor.OrderSideBuy, amount)
		}

		if err != nil {
			return 0, fmt.Errorf("failed to get quote for %s->%s: %w", tokenIn, tokenOut, err)
		}

		if quote.BidSize != nil && quote.BidSize.Sign() > 0 {
			amount = quote.BidSize
		} else if quote.Price != nil {
			amount = new(big.Float).Mul(amount, quote.Price)
		} else {
			return 0, fmt.Errorf("invalid quote for %s->%s", tokenIn, tokenOut)
		}
	}

	// Calculate profit vs current amount
	currentFloat, _ := currentAmount.Float64()
	finalFloat, _ := amount.Float64()

	if currentFloat > 0 {
		profitBps = int((finalFloat/currentFloat - 1.0) * 10000)
	}

	return profitBps, nil
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

	// Strategy #5: Pre-validate quotes before execution
	if e.config.PreValidateQuotes {
		log.Printf("[executor] Pre-validating %d quotes for cycle %d...", len(cycle.Path)-1, cycle.ID)
		validation := e.preValidateQuotes(ctx, cycle, inputAmount)

		if !validation.IsStillProfitable {
			execution.Error = fmt.Sprintf("pre-validation failed: %s", validation.Error)
			execution.EndTime = time.Now()
			log.Printf("[executor] Pre-validation FAILED for cycle %d: %s (took %v)",
				cycle.ID, validation.Error, validation.ValidationTime)
			return execution, fmt.Errorf(execution.Error)
		}

		log.Printf("[executor] Pre-validation PASSED for cycle %d: expected profit %d bps, max price impact %d bps (took %v)",
			cycle.ID, validation.ExpectedProfitBps, validation.MaxPriceImpactBps, validation.ValidationTime)
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

	totalSteps := len(cycle.Path) - 1

	// Simulate each swap in the cycle
	for i := 0; i < totalSteps; i++ {
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

		log.Printf("[executor] DRY RUN: Step %d/%d: %s -> %s, in=%.6f, out=%.6f",
			i+1, totalSteps, tokenIn, tokenOut,
			floatValue(currentAmount), floatValue(amountOut))

		currentAmount = amountOut

		// Strategy #6: Mid-cycle bailout detection (simulation mode - for testing logic)
		if e.config.MidCycleBailout && i < totalSteps-1 {
			remainingProfit, err := e.calculateRemainingProfit(ctx, cycle, i+1, currentAmount)
			if err != nil {
				log.Printf("[executor] DRY RUN: Could not calculate remaining profit after step %d: %v", i+1, err)
			} else {
				log.Printf("[executor] DRY RUN: Mid-cycle check after step %d/%d: remaining path profit = %d bps",
					i+1, totalSteps, remainingProfit)

				if remainingProfit < e.config.BailoutThresholdBps {
					log.Printf("[executor] DRY RUN: ALERT - Would trigger bailout (remaining %d bps < threshold %d bps)",
						remainingProfit, e.config.BailoutThresholdBps)
				}
			}
		}
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
	totalSteps := len(cycle.Path) - 1

	// Use configurable swap deadline
	swapTimeout := time.Duration(e.config.SwapDeadlineSecs) * time.Second
	if swapTimeout <= 0 {
		swapTimeout = 60 * time.Second // Default fallback
	}

	// Execute each swap in the cycle
	for i := 0; i < totalSteps; i++ {
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

		// Wait for order to fill (using configurable deadline)
		filledOrder, err := e.waitForOrderFill(ctx, order.ID, swapTimeout)
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

		log.Printf("[executor] LIVE: Step %d/%d complete: %s -> %s, tx=%s, out=%.6f",
			i+1, totalSteps, tokenIn, tokenOut, order.TransactionID, floatValue(filledOrder.FilledAmount))

		currentAmount = filledOrder.FilledAmount

		// Strategy #6: Mid-cycle bailout detection (after each swap except the last)
		if e.config.MidCycleBailout && i < totalSteps-1 {
			remainingProfit, err := e.calculateRemainingProfit(ctx, cycle, i+1, currentAmount)
			if err != nil {
				log.Printf("[executor] WARNING: Could not calculate remaining profit after step %d: %v", i+1, err)
			} else {
				log.Printf("[executor] Mid-cycle check after step %d/%d: remaining path profit = %d bps",
					i+1, totalSteps, remainingProfit)

				if remainingProfit < e.config.BailoutThresholdBps {
					// Log severe warning - we can't easily reverse DEX trades, so we continue
					// but this information is valuable for post-mortem analysis
					log.Printf("[executor] ALERT: Remaining profit %d bps is below bailout threshold %d bps!",
						remainingProfit, e.config.BailoutThresholdBps)
					log.Printf("[executor] ALERT: Continuing execution (cannot reverse DEX trades), expect reduced/negative profit")

					// Track bailout trigger in execution notes
					if execution.Error == "" {
						execution.Error = fmt.Sprintf("bailout threshold triggered at step %d (remaining profit %d bps < threshold %d bps)",
							i+1, remainingProfit, e.config.BailoutThresholdBps)
					}
				}
			}
		}
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
