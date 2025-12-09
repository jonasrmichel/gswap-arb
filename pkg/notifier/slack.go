// Package notifier provides notification services for the arbitrage bot.
package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jonasrmichel/gswap-arb/pkg/types"
)

// SlackNotifier sends notifications to a Slack channel.
type SlackNotifier struct {
	apiToken   string
	channel    string
	httpClient *http.Client
	enabled    bool
}

// SlackConfig holds Slack configuration.
type SlackConfig struct {
	APIToken string
	Channel  string
	Enabled  bool
}

// slackMessage represents a Slack message payload.
type slackMessage struct {
	Channel string        `json:"channel"`
	Text    string        `json:"text,omitempty"`
	Blocks  []slackBlock  `json:"blocks,omitempty"`
}

type slackBlock struct {
	Type   string      `json:"type"`
	Text   *slackText  `json:"text,omitempty"`
	Fields []slackText `json:"fields,omitempty"`
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// NewSlackNotifier creates a new Slack notifier.
func NewSlackNotifier(config *SlackConfig) *SlackNotifier {
	if config == nil || config.APIToken == "" || config.Channel == "" {
		return &SlackNotifier{enabled: false}
	}

	return &SlackNotifier{
		apiToken: config.APIToken,
		channel:  config.Channel,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		enabled: config.Enabled,
	}
}

// IsEnabled returns whether the notifier is enabled.
func (s *SlackNotifier) IsEnabled() bool {
	return s.enabled
}

// NotifyArbitrageOpportunity sends a notification about an arbitrage opportunity.
func (s *SlackNotifier) NotifyArbitrageOpportunity(opp *types.ArbitrageOpportunity) error {
	if !s.enabled || opp == nil {
		return nil
	}

	blocks := []slackBlock{
		{
			Type: "header",
			Text: &slackText{
				Type: "plain_text",
				Text: fmt.Sprintf("🔔 Arbitrage Opportunity: %s", opp.Pair),
			},
		},
		{
			Type: "section",
			Fields: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("*Buy Exchange:*\n%s", opp.BuyExchange)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Sell Exchange:*\n%s", opp.SellExchange)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Buy Price:*\n%s", opp.BuyPrice.Text('f', 8))},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Sell Price:*\n%s", opp.SellPrice.Text('f', 8))},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Spread:*\n%.2f%% (%d bps)", opp.SpreadPercent, opp.SpreadBps)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Net Profit:*\n%d bps", opp.NetProfitBps)},
			},
		},
		{
			Type: "context",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("Detected at %s", opp.DetectedAt.Format(time.RFC3339)),
			},
		},
	}

	return s.sendMessage(blocks, fmt.Sprintf("Arbitrage: %s - Buy %s @ %s, Sell %s @ %s (%.2f%%)",
		opp.Pair, opp.BuyExchange, opp.BuyPrice.Text('f', 6),
		opp.SellExchange, opp.SellPrice.Text('f', 6), opp.SpreadPercent))
}

// NotifyChainArbitrageOpportunity sends a notification about a chain arbitrage opportunity.
func (s *SlackNotifier) NotifyChainArbitrageOpportunity(opp *types.ChainArbitrageOpportunity) error {
	if !s.enabled || opp == nil {
		return nil
	}

	// Build chain description
	chainDesc := ""
	for i, hop := range opp.Hops {
		if i > 0 {
			chainDesc += " → "
		}
		chainDesc += fmt.Sprintf("%s (%s)", hop.Exchange, hop.Action)
	}

	blocks := []slackBlock{
		{
			Type: "header",
			Text: &slackText{
				Type: "plain_text",
				Text: fmt.Sprintf("🔗 Chain Arbitrage: %s", opp.Pair),
			},
		},
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Chain:* %s\n*Hops:* %d", chainDesc, opp.HopCount),
			},
		},
		{
			Type: "section",
			Fields: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("*Total Spread:*\n%.2f%% (%d bps)", opp.SpreadPercent, opp.SpreadBps)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Net Profit:*\n%d bps", opp.NetProfitBps)},
			},
		},
		{
			Type: "context",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("Detected at %s", opp.DetectedAt.Format(time.RFC3339)),
			},
		},
	}

	return s.sendMessage(blocks, fmt.Sprintf("Chain Arbitrage: %s - %d hops (%.2f%%)",
		opp.Pair, opp.HopCount, opp.SpreadPercent))
}

// TradeExecution represents a trade execution result for notification.
type TradeExecution struct {
	Pair         string
	BuyExchange  string
	SellExchange string
	BuyPrice     string
	SellPrice    string
	Amount       string
	Profit       string
	Fees         string
	Success      bool
	DryRun       bool
	Error        string
}

// NotifyTradeExecution sends a notification about a trade execution.
func (s *SlackNotifier) NotifyTradeExecution(trade *TradeExecution) error {
	if !s.enabled || trade == nil {
		return nil
	}

	var emoji, status string
	if trade.DryRun {
		emoji = "🧪"
		status = "DRY RUN"
	} else if trade.Success {
		emoji = "✅"
		status = "EXECUTED"
	} else {
		emoji = "❌"
		status = "FAILED"
	}

	blocks := []slackBlock{
		{
			Type: "header",
			Text: &slackText{
				Type: "plain_text",
				Text: fmt.Sprintf("%s Trade %s: %s", emoji, status, trade.Pair),
			},
		},
		{
			Type: "section",
			Fields: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("*Buy:*\n%s @ %s", trade.BuyExchange, trade.BuyPrice)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Sell:*\n%s @ %s", trade.SellExchange, trade.SellPrice)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Amount:*\n%s", trade.Amount)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Profit:*\n%s", trade.Profit)},
			},
		},
	}

	if trade.Fees != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Fees:* %s", trade.Fees),
			},
		})
	}

	if trade.Error != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Error:* %s", trade.Error),
			},
		})
	}

	blocks = append(blocks, slackBlock{
		Type: "context",
		Text: &slackText{
			Type: "mrkdwn",
			Text: fmt.Sprintf("Executed at %s", time.Now().Format(time.RFC3339)),
		},
	})

	return s.sendMessage(blocks, fmt.Sprintf("Trade %s: %s - %s", status, trade.Pair, trade.Profit))
}

// sendMessage sends a message to Slack.
func (s *SlackNotifier) sendMessage(blocks []slackBlock, fallbackText string) error {
	msg := slackMessage{
		Channel: s.channel,
		Text:    fallbackText,
		Blocks:  blocks,
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequest("POST", "https://slack.com/api/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack API returned status %d", resp.StatusCode)
	}

	// Parse response to check for errors
	var slackResp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&slackResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if !slackResp.OK {
		return fmt.Errorf("slack API error: %s", slackResp.Error)
	}

	return nil
}

// SendTestMessage sends a test message to verify the connection.
func (s *SlackNotifier) SendTestMessage() error {
	if !s.enabled {
		return fmt.Errorf("slack notifier is not enabled")
	}

	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: "🤖 *GSwap Arbitrage Bot* connected and ready to send notifications!",
			},
		},
	}

	return s.sendMessage(blocks, "GSwap Arbitrage Bot connected")
}

// DriftAlert represents a balance drift alert for notification.
type DriftAlert struct {
	Currency       string
	MaxDriftPct    float64
	ExchangeDrifts map[string]float64 // exchange -> drift percentage
	IsCritical     bool
}

// NotifyDriftAlert sends a notification about balance drift.
func (s *SlackNotifier) NotifyDriftAlert(alert *DriftAlert) error {
	if !s.enabled || alert == nil {
		return nil
	}

	emoji := "⚠️"
	severity := "WARNING"
	if alert.IsCritical {
		emoji = "🚨"
		severity = "CRITICAL"
	}

	// Build drift details
	driftDetails := ""
	for exchange, drift := range alert.ExchangeDrifts {
		indicator := ""
		if drift > 0 {
			indicator = "↑ surplus"
		} else if drift < 0 {
			indicator = "↓ deficit"
		}
		driftDetails += fmt.Sprintf("• %s: %+.1f%% %s\n", exchange, drift, indicator)
	}

	blocks := []slackBlock{
		{
			Type: "header",
			Text: &slackText{
				Type: "plain_text",
				Text: fmt.Sprintf("%s Balance Drift %s: %s", emoji, severity, alert.Currency),
			},
		},
		{
			Type: "section",
			Fields: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("*Currency:*\n%s", alert.Currency)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Max Drift:*\n%.1f%%", alert.MaxDriftPct)},
			},
		},
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Drift by Exchange:*\n%s", driftDetails),
			},
		},
		{
			Type: "context",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("Detected at %s | Consider rebalancing inventory", time.Now().Format(time.RFC3339)),
			},
		},
	}

	return s.sendMessage(blocks, fmt.Sprintf("Balance Drift %s: %s at %.1f%%", severity, alert.Currency, alert.MaxDriftPct))
}

// RebalanceRecommendation represents a rebalance recommendation for notification.
type RebalanceRecommendation struct {
	Currency     string
	FromExchange string
	ToExchange   string
	Amount       string
	Priority     string // "HIGH", "MEDIUM", "LOW"
	Reason       string
}

// NotifyRebalanceRecommendation sends a notification about a rebalance recommendation.
func (s *SlackNotifier) NotifyRebalanceRecommendation(rec *RebalanceRecommendation) error {
	if !s.enabled || rec == nil {
		return nil
	}

	emoji := "💡"
	if rec.Priority == "HIGH" {
		emoji = "🔴"
	} else if rec.Priority == "MEDIUM" {
		emoji = "🟡"
	}

	blocks := []slackBlock{
		{
			Type: "header",
			Text: &slackText{
				Type: "plain_text",
				Text: fmt.Sprintf("%s Rebalance Recommendation [%s]", emoji, rec.Priority),
			},
		},
		{
			Type: "section",
			Fields: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("*Currency:*\n%s", rec.Currency)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Amount:*\n%s", rec.Amount)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*From:*\n%s", rec.FromExchange)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*To:*\n%s", rec.ToExchange)},
			},
		},
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Reason:* %s", rec.Reason),
			},
		},
		{
			Type: "context",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("Generated at %s | Use bridge CLI to execute", time.Now().Format(time.RFC3339)),
			},
		},
	}

	return s.sendMessage(blocks, fmt.Sprintf("Rebalance: Bridge %s %s from %s to %s", rec.Amount, rec.Currency, rec.FromExchange, rec.ToExchange))
}

// InventorySummary represents a periodic inventory summary for notification.
type InventorySummary struct {
	TotalExchanges    int
	TotalCurrencies   int
	DriftWarnings     int
	CriticalDrifts    int
	TopDrifts         []string // "GALA: 25.3% on binance"
}

// NotifyInventorySummary sends a periodic inventory status summary.
func (s *SlackNotifier) NotifyInventorySummary(summary *InventorySummary) error {
	if !s.enabled || summary == nil {
		return nil
	}

	statusEmoji := "✅"
	statusText := "Healthy"
	if summary.CriticalDrifts > 0 {
		statusEmoji = "🚨"
		statusText = "Critical"
	} else if summary.DriftWarnings > 0 {
		statusEmoji = "⚠️"
		statusText = "Needs Attention"
	}

	driftText := "No significant drift detected"
	if len(summary.TopDrifts) > 0 {
		driftText = ""
		for _, d := range summary.TopDrifts {
			driftText += fmt.Sprintf("• %s\n", d)
		}
	}

	blocks := []slackBlock{
		{
			Type: "header",
			Text: &slackText{
				Type: "plain_text",
				Text: fmt.Sprintf("%s Inventory Status: %s", statusEmoji, statusText),
			},
		},
		{
			Type: "section",
			Fields: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("*Exchanges:*\n%d", summary.TotalExchanges)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Currencies:*\n%d", summary.TotalCurrencies)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Drift Warnings:*\n%d", summary.DriftWarnings)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Critical:*\n%d", summary.CriticalDrifts)},
			},
		},
		{
			Type: "section",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Top Drifts:*\n%s", driftText),
			},
		},
		{
			Type: "context",
			Text: &slackText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("Report generated at %s", time.Now().Format(time.RFC3339)),
			},
		},
	}

	return s.sendMessage(blocks, fmt.Sprintf("Inventory Status: %s - %d warnings, %d critical", statusText, summary.DriftWarnings, summary.CriticalDrifts))
}
