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
