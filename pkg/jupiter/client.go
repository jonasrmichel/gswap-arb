// Package jupiter provides a client for the Jupiter aggregator API on Solana.
package jupiter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	// DefaultBaseURL is the Jupiter Lite API endpoint.
	DefaultBaseURL = "https://lite-api.jup.ag/swap/v1"

	// UltraBaseURL is the Jupiter Ultra API endpoint (requires API key).
	UltraBaseURL = "https://api.jup.ag/swap/v1"

	// DefaultTimeout is the HTTP request timeout.
	DefaultTimeout = 30 * time.Second

	// SwapModeExactIn specifies exact input amount.
	SwapModeExactIn = "ExactIn"

	// SwapModeExactOut specifies exact output amount.
	SwapModeExactOut = "ExactOut"
)

// Client is a Jupiter API client.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string // Optional: for Ultra API
}

// ClientConfig contains configuration for the Jupiter client.
type ClientConfig struct {
	BaseURL    string
	APIKey     string // Optional: for Ultra API
	Timeout    time.Duration
	HTTPClient *http.Client
}

// NewClient creates a new Jupiter API client.
func NewClient(config *ClientConfig) *Client {
	if config == nil {
		config = &ClientConfig{}
	}

	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	timeout := config.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: timeout,
		}
	}

	return &Client{
		httpClient: httpClient,
		baseURL:    baseURL,
		apiKey:     config.APIKey,
	}
}

// GetQuote fetches a swap quote from Jupiter.
func (c *Client) GetQuote(ctx context.Context, params *QuoteParams) (*QuoteResponse, error) {
	if params.InputMint == "" || params.OutputMint == "" {
		return nil, fmt.Errorf("inputMint and outputMint are required")
	}
	if params.Amount == "" {
		return nil, fmt.Errorf("amount is required")
	}

	// Build query parameters
	query := url.Values{}
	query.Set("inputMint", params.InputMint)
	query.Set("outputMint", params.OutputMint)
	query.Set("amount", params.Amount)

	if params.SlippageBps > 0 {
		query.Set("slippageBps", strconv.Itoa(params.SlippageBps))
	}
	if params.SwapMode != "" {
		query.Set("swapMode", params.SwapMode)
	}

	// Build request URL
	requestURL := fmt.Sprintf("%s/quote?%s", c.baseURL, query.Encode())

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check for errors
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Jupiter API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var quoteResp QuoteResponse
	if err := json.Unmarshal(body, &quoteResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &quoteResp, nil
}

// BuildSwapTransaction builds a swap transaction from a quote.
func (c *Client) BuildSwapTransaction(ctx context.Context, params *SwapParams) (*SwapResponse, error) {
	if params.QuoteResponse == nil {
		return nil, fmt.Errorf("quoteResponse is required")
	}
	if params.UserPublicKey == "" {
		return nil, fmt.Errorf("userPublicKey is required")
	}

	// Build request URL
	requestURL := fmt.Sprintf("%s/swap", c.baseURL)

	// Marshal request body
	jsonBody, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check for errors
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Jupiter API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var swapResp SwapResponse
	if err := json.Unmarshal(body, &swapResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &swapResp, nil
}

// GetPrice fetches the price for a token pair (convenience method).
// Returns the output amount for 1 unit of input token (adjusted for decimals).
func (c *Client) GetPrice(ctx context.Context, inputMint, outputMint string, inputDecimals, outputDecimals int) (float64, error) {
	// Use 1 unit of input token (in smallest units)
	amount := pow10(inputDecimals)

	quote, err := c.GetQuote(ctx, &QuoteParams{
		InputMint:   inputMint,
		OutputMint:  outputMint,
		Amount:      strconv.FormatInt(amount, 10),
		SlippageBps: 50, // 0.5% default slippage
		SwapMode:    SwapModeExactIn,
	})
	if err != nil {
		return 0, err
	}

	// Parse output amount
	outAmount, err := strconv.ParseInt(quote.OutAmount, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse output amount: %w", err)
	}

	// Convert to float with decimal adjustment
	price := float64(outAmount) / float64(pow10(outputDecimals))
	return price, nil
}

// pow10 returns 10^n as int64.
func pow10(n int) int64 {
	result := int64(1)
	for i := 0; i < n; i++ {
		result *= 10
	}
	return result
}
