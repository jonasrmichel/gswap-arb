// Package reporter provides arbitrage opportunity reporting and output formatting.
package reporter

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// Reporter outputs arbitrage opportunities in various formats.
type Reporter struct {
	output       io.Writer
	format       OutputFormat
	verbose      bool
	stats        *types.BotStats
	statsMu      sync.Mutex
	history      []*types.ArbitrageOpportunity
	chainHistory []*types.ChainArbitrageOpportunity
	historyMu    sync.Mutex
	maxHistory   int
}

// OutputFormat specifies the output format for reports.
type OutputFormat string

const (
	FormatText OutputFormat = "text"
	FormatJSON OutputFormat = "json"
	FormatCSV  OutputFormat = "csv"
)

// NewReporter creates a new reporter.
func NewReporter(output io.Writer, format OutputFormat, verbose bool) *Reporter {
	if output == nil {
		output = os.Stdout
	}
	return &Reporter{
		output:       output,
		format:       format,
		verbose:      verbose,
		stats:        &types.BotStats{StartTime: time.Now()},
		history:      make([]*types.ArbitrageOpportunity, 0),
		chainHistory: make([]*types.ChainArbitrageOpportunity, 0),
		maxHistory:   1000,
	}
}

// ReportOpportunities reports detected arbitrage opportunities.
func (r *Reporter) ReportOpportunities(opportunities []*types.ArbitrageOpportunity) {
	r.updateStats(opportunities)

	if len(opportunities) == 0 {
		if r.verbose {
			r.printNoOpportunities()
		}
		return
	}

	// Add to history
	r.historyMu.Lock()
	for _, opp := range opportunities {
		if opp.IsValid {
			r.history = append(r.history, opp)
			if len(r.history) > r.maxHistory {
				r.history = r.history[1:]
			}
		}
	}
	r.historyMu.Unlock()

	switch r.format {
	case FormatJSON:
		r.reportJSON(opportunities)
	case FormatCSV:
		r.reportCSV(opportunities)
	default:
		r.reportText(opportunities)
	}
}

// reportText outputs opportunities in human-readable text format.
func (r *Reporter) reportText(opportunities []*types.ArbitrageOpportunity) {
	fmt.Fprintln(r.output)
	fmt.Fprintln(r.output, strings.Repeat("=", 80))
	fmt.Fprintf(r.output, "ARBITRAGE OPPORTUNITIES DETECTED: %d\n", len(opportunities))
	fmt.Fprintf(r.output, "Time: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintln(r.output, strings.Repeat("=", 80))

	for i, opp := range opportunities {
		if !opp.IsValid {
			continue
		}

		fmt.Fprintln(r.output)
		fmt.Fprintf(r.output, "--- Opportunity #%d ---\n", i+1)
		fmt.Fprintf(r.output, "Pair:         %s\n", opp.Pair)
		fmt.Fprintf(r.output, "Buy on:       %s @ %s\n", opp.BuyExchange, formatPrice(opp.BuyPrice))
		fmt.Fprintf(r.output, "Sell on:      %s @ %s\n", opp.SellExchange, formatPrice(opp.SellPrice))
		fmt.Fprintln(r.output)
		fmt.Fprintf(r.output, "Spread:       %s (%.2f%% / %d bps)\n",
			formatPrice(opp.SpreadAbsolute), opp.SpreadPercent, opp.SpreadBps)
		fmt.Fprintf(r.output, "Gross Profit: %s\n", formatPrice(opp.GrossProfit))
		fmt.Fprintf(r.output, "Est. Fees:    %s\n", formatPrice(opp.EstimatedFees))
		fmt.Fprintf(r.output, "Net Profit:   %s (%d bps)\n", formatPrice(opp.NetProfit), opp.NetProfitBps)
		fmt.Fprintln(r.output)
		fmt.Fprintf(r.output, "Trade Size:   %s\n", formatPrice(opp.TradeSize))
		fmt.Fprintf(r.output, "Valid Until:  %s\n", opp.ExpiresAt.Format(time.RFC3339))

		if r.verbose && len(opp.InvalidationReasons) > 0 {
			fmt.Fprintf(r.output, "Warnings:     %s\n", strings.Join(opp.InvalidationReasons, "; "))
		}
	}

	fmt.Fprintln(r.output)
	fmt.Fprintln(r.output, strings.Repeat("-", 80))
}

// reportJSON outputs opportunities in JSON format.
func (r *Reporter) reportJSON(opportunities []*types.ArbitrageOpportunity) {
	// Filter valid opportunities for cleaner output
	valid := make([]*opportunityJSON, 0)
	for _, opp := range opportunities {
		if opp.IsValid {
			valid = append(valid, toOpportunityJSON(opp))
		}
	}

	report := struct {
		Timestamp     string             `json:"timestamp"`
		Count         int                `json:"count"`
		Opportunities []*opportunityJSON `json:"opportunities"`
	}{
		Timestamp:     time.Now().Format(time.RFC3339),
		Count:         len(valid),
		Opportunities: valid,
	}

	encoder := json.NewEncoder(r.output)
	encoder.SetIndent("", "  ")
	encoder.Encode(report)
}

// opportunityJSON is a JSON-friendly representation of an opportunity.
type opportunityJSON struct {
	ID            string  `json:"id"`
	Pair          string  `json:"pair"`
	BuyExchange   string  `json:"buy_exchange"`
	SellExchange  string  `json:"sell_exchange"`
	BuyPrice      string  `json:"buy_price"`
	SellPrice     string  `json:"sell_price"`
	SpreadPercent float64 `json:"spread_percent"`
	SpreadBps     int     `json:"spread_bps"`
	NetProfitBps  int     `json:"net_profit_bps"`
	NetProfit     string  `json:"net_profit"`
	TradeSize     string  `json:"trade_size"`
	DetectedAt    string  `json:"detected_at"`
	ExpiresAt     string  `json:"expires_at"`
}

func toOpportunityJSON(opp *types.ArbitrageOpportunity) *opportunityJSON {
	return &opportunityJSON{
		ID:            opp.ID,
		Pair:          opp.Pair,
		BuyExchange:   opp.BuyExchange,
		SellExchange:  opp.SellExchange,
		BuyPrice:      formatPrice(opp.BuyPrice),
		SellPrice:     formatPrice(opp.SellPrice),
		SpreadPercent: opp.SpreadPercent,
		SpreadBps:     opp.SpreadBps,
		NetProfitBps:  opp.NetProfitBps,
		NetProfit:     formatPrice(opp.NetProfit),
		TradeSize:     formatPrice(opp.TradeSize),
		DetectedAt:    opp.DetectedAt.Format(time.RFC3339),
		ExpiresAt:     opp.ExpiresAt.Format(time.RFC3339),
	}
}

// reportCSV outputs opportunities in CSV format.
func (r *Reporter) reportCSV(opportunities []*types.ArbitrageOpportunity) {
	// Header
	fmt.Fprintln(r.output, "timestamp,pair,buy_exchange,sell_exchange,buy_price,sell_price,spread_bps,net_profit_bps,net_profit,trade_size")

	for _, opp := range opportunities {
		if !opp.IsValid {
			continue
		}
		fmt.Fprintf(r.output, "%s,%s,%s,%s,%s,%s,%d,%d,%s,%s\n",
			opp.DetectedAt.Format(time.RFC3339),
			opp.Pair,
			opp.BuyExchange,
			opp.SellExchange,
			formatPrice(opp.BuyPrice),
			formatPrice(opp.SellPrice),
			opp.SpreadBps,
			opp.NetProfitBps,
			formatPrice(opp.NetProfit),
			formatPrice(opp.TradeSize),
		)
	}
}

// printNoOpportunities prints a message when no opportunities are found.
func (r *Reporter) printNoOpportunities() {
	fmt.Fprintf(r.output, "[%s] No arbitrage opportunities found\n", time.Now().Format("15:04:05"))
}

// ReportChainOpportunities reports detected chain arbitrage opportunities.
func (r *Reporter) ReportChainOpportunities(opportunities []*types.ChainArbitrageOpportunity) {
	r.updateChainStats(opportunities)

	if len(opportunities) == 0 {
		return
	}

	// Add to history
	r.historyMu.Lock()
	for _, opp := range opportunities {
		if opp.IsValid {
			r.chainHistory = append(r.chainHistory, opp)
			if len(r.chainHistory) > r.maxHistory {
				r.chainHistory = r.chainHistory[1:]
			}
		}
	}
	r.historyMu.Unlock()

	switch r.format {
	case FormatJSON:
		r.reportChainJSON(opportunities)
	case FormatCSV:
		r.reportChainCSV(opportunities)
	default:
		r.reportChainText(opportunities)
	}
}

// ReportChainOpportunity reports a single chain arbitrage opportunity.
func (r *Reporter) ReportChainOpportunity(opp *types.ChainArbitrageOpportunity) {
	r.ReportChainOpportunities([]*types.ChainArbitrageOpportunity{opp})
}

// reportChainText outputs chain opportunities in human-readable text format.
func (r *Reporter) reportChainText(opportunities []*types.ChainArbitrageOpportunity) {
	fmt.Fprintln(r.output)
	fmt.Fprintln(r.output, strings.Repeat("=", 80))
	fmt.Fprintf(r.output, "CHAIN ARBITRAGE OPPORTUNITIES DETECTED: %d\n", len(opportunities))
	fmt.Fprintf(r.output, "Time: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintln(r.output, strings.Repeat("=", 80))

	for i, opp := range opportunities {
		if !opp.IsValid {
			continue
		}

		fmt.Fprintln(r.output)
		fmt.Fprintf(r.output, "--- Chain Opportunity #%d ---\n", i+1)
		fmt.Fprintf(r.output, "Pair:         %s\n", opp.Pair)
		fmt.Fprintf(r.output, "Chain:        %s\n", strings.Join(opp.Chain, " -> "))
		fmt.Fprintf(r.output, "Hops:         %d\n", opp.HopCount)
		fmt.Fprintln(r.output)

		// Display each hop
		for j, hop := range opp.Hops {
			fmt.Fprintf(r.output, "  Step %d: %s on %s @ %s\n",
				j+1, strings.ToUpper(hop.Action), hop.Exchange, formatPrice(hop.Price))
		}

		fmt.Fprintln(r.output)
		fmt.Fprintf(r.output, "Start Amount: %s\n", formatPrice(opp.StartAmount))
		fmt.Fprintf(r.output, "End Amount:   %s\n", formatPrice(opp.EndAmount))
		fmt.Fprintf(r.output, "Spread:       %.2f%% (%d bps)\n", opp.SpreadPercent, opp.SpreadBps)
		fmt.Fprintf(r.output, "Total Fees:   %s\n", formatPrice(opp.TotalFees))
		fmt.Fprintf(r.output, "Net Profit:   %s (%d bps)\n", formatPrice(opp.NetProfit), opp.NetProfitBps)
		fmt.Fprintf(r.output, "Valid Until:  %s\n", opp.ExpiresAt.Format(time.RFC3339))

		if r.verbose && len(opp.InvalidationReasons) > 0 {
			fmt.Fprintf(r.output, "Warnings:     %s\n", strings.Join(opp.InvalidationReasons, "; "))
		}
	}

	fmt.Fprintln(r.output)
	fmt.Fprintln(r.output, strings.Repeat("-", 80))
}

// chainOpportunityJSON is a JSON-friendly representation of a chain opportunity.
type chainOpportunityJSON struct {
	ID            string   `json:"id"`
	Pair          string   `json:"pair"`
	Chain         []string `json:"chain"`
	HopCount      int      `json:"hop_count"`
	StartExchange string   `json:"start_exchange"`
	EndExchange   string   `json:"end_exchange"`
	StartAmount   string   `json:"start_amount"`
	EndAmount     string   `json:"end_amount"`
	SpreadPercent float64  `json:"spread_percent"`
	SpreadBps     int      `json:"spread_bps"`
	TotalFees     string   `json:"total_fees"`
	NetProfit     string   `json:"net_profit"`
	NetProfitBps  int      `json:"net_profit_bps"`
	DetectedAt    string   `json:"detected_at"`
	ExpiresAt     string   `json:"expires_at"`
	Hops          []hopJSON `json:"hops"`
}

type hopJSON struct {
	Exchange string `json:"exchange"`
	Action   string `json:"action"`
	Price    string `json:"price"`
	FeeBps   int    `json:"fee_bps"`
}

func toChainOpportunityJSON(opp *types.ChainArbitrageOpportunity) *chainOpportunityJSON {
	hops := make([]hopJSON, len(opp.Hops))
	for i, hop := range opp.Hops {
		hops[i] = hopJSON{
			Exchange: hop.Exchange,
			Action:   hop.Action,
			Price:    formatPrice(hop.Price),
			FeeBps:   hop.FeeBps,
		}
	}

	return &chainOpportunityJSON{
		ID:            opp.ID,
		Pair:          opp.Pair,
		Chain:         opp.Chain,
		HopCount:      opp.HopCount,
		StartExchange: opp.StartExchange,
		EndExchange:   opp.EndExchange,
		StartAmount:   formatPrice(opp.StartAmount),
		EndAmount:     formatPrice(opp.EndAmount),
		SpreadPercent: opp.SpreadPercent,
		SpreadBps:     opp.SpreadBps,
		TotalFees:     formatPrice(opp.TotalFees),
		NetProfit:     formatPrice(opp.NetProfit),
		NetProfitBps:  opp.NetProfitBps,
		DetectedAt:    opp.DetectedAt.Format(time.RFC3339),
		ExpiresAt:     opp.ExpiresAt.Format(time.RFC3339),
		Hops:          hops,
	}
}

// reportChainJSON outputs chain opportunities in JSON format.
func (r *Reporter) reportChainJSON(opportunities []*types.ChainArbitrageOpportunity) {
	valid := make([]*chainOpportunityJSON, 0)
	for _, opp := range opportunities {
		if opp.IsValid {
			valid = append(valid, toChainOpportunityJSON(opp))
		}
	}

	report := struct {
		Timestamp     string                  `json:"timestamp"`
		Type          string                  `json:"type"`
		Count         int                     `json:"count"`
		Opportunities []*chainOpportunityJSON `json:"opportunities"`
	}{
		Timestamp:     time.Now().Format(time.RFC3339),
		Type:          "chain_arbitrage",
		Count:         len(valid),
		Opportunities: valid,
	}

	encoder := json.NewEncoder(r.output)
	encoder.SetIndent("", "  ")
	encoder.Encode(report)
}

// reportChainCSV outputs chain opportunities in CSV format.
func (r *Reporter) reportChainCSV(opportunities []*types.ChainArbitrageOpportunity) {
	// Header
	fmt.Fprintln(r.output, "timestamp,pair,chain,hop_count,start_exchange,end_exchange,start_amount,end_amount,spread_bps,net_profit_bps,net_profit")

	for _, opp := range opportunities {
		if !opp.IsValid {
			continue
		}
		fmt.Fprintf(r.output, "%s,%s,%s,%d,%s,%s,%s,%s,%d,%d,%s\n",
			opp.DetectedAt.Format(time.RFC3339),
			opp.Pair,
			strings.Join(opp.Chain, "->"),
			opp.HopCount,
			opp.StartExchange,
			opp.EndExchange,
			formatPrice(opp.StartAmount),
			formatPrice(opp.EndAmount),
			opp.SpreadBps,
			opp.NetProfitBps,
			formatPrice(opp.NetProfit),
		)
	}
}

// updateChainStats updates bot statistics for chain opportunities.
func (r *Reporter) updateChainStats(opportunities []*types.ChainArbitrageOpportunity) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()

	for _, opp := range opportunities {
		if opp.IsValid && opp.NetProfitBps > 0 {
			r.stats.ChainOpportunitiesFound++
		}
	}
}

// GetChainHistory returns historical chain opportunities.
func (r *Reporter) GetChainHistory() []*types.ChainArbitrageOpportunity {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()

	result := make([]*types.ChainArbitrageOpportunity, len(r.chainHistory))
	copy(result, r.chainHistory)
	return result
}

// updateStats updates bot statistics.
func (r *Reporter) updateStats(opportunities []*types.ArbitrageOpportunity) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()

	r.stats.TotalCycles++
	r.stats.LastCycleTime = time.Now()
	r.stats.OpportunitiesFound += int64(len(opportunities))

	for _, opp := range opportunities {
		if opp.IsValid && opp.NetProfitBps > 0 {
			r.stats.ProfitableOpportunities++
		}
	}
}

// GetStats returns current bot statistics.
func (r *Reporter) GetStats() types.BotStats {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	return *r.stats
}

// PrintStats prints bot statistics.
func (r *Reporter) PrintStats() {
	stats := r.GetStats()

	fmt.Fprintln(r.output)
	fmt.Fprintln(r.output, strings.Repeat("=", 50))
	fmt.Fprintln(r.output, "BOT STATISTICS")
	fmt.Fprintln(r.output, strings.Repeat("=", 50))
	fmt.Fprintf(r.output, "Running since:              %s\n", stats.StartTime.Format(time.RFC3339))
	fmt.Fprintf(r.output, "Uptime:                     %s\n", time.Since(stats.StartTime).Round(time.Second))
	fmt.Fprintf(r.output, "Total scan cycles:          %d\n", stats.TotalCycles)
	fmt.Fprintf(r.output, "Direct opportunities:       %d\n", stats.OpportunitiesFound)
	fmt.Fprintf(r.output, "Chain opportunities:        %d\n", stats.ChainOpportunitiesFound)
	fmt.Fprintf(r.output, "Profitable opportunities:   %d\n", stats.ProfitableOpportunities)
	fmt.Fprintf(r.output, "Errors:                     %d\n", stats.Errors)
	fmt.Fprintln(r.output, strings.Repeat("-", 50))
}

// GetHistory returns historical opportunities.
func (r *Reporter) GetHistory() []*types.ArbitrageOpportunity {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()

	result := make([]*types.ArbitrageOpportunity, len(r.history))
	copy(result, r.history)
	return result
}

// RecordError records an error in statistics.
func (r *Reporter) RecordError() {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	r.stats.Errors++
}

// Helper function to format price
func formatPrice(f interface{}) string {
	if f == nil {
		return "N/A"
	}

	switch v := f.(type) {
	case float64:
		return fmt.Sprintf("%.8f", v)
	case *float64:
		if v == nil {
			return "N/A"
		}
		return fmt.Sprintf("%.8f", *v)
	default:
		return fmt.Sprintf("%v", f)
	}
}
