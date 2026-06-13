package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Client struct {
	url string
}

func NewClient(baseUrl string) *Client {
	return &Client{
		url: baseUrl,
	}
}

func (c *Client) GenerateTokens(ctx context.Context, taskID string) (*TokenResponse, error) {
	tokenURL := fmt.Sprintf("%s/task/%s/tokens", c.url, taskID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request tokens: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}

	return &tokenResp, nil
}

func (c *Client) Callback(ctx context.Context, callbackToken, taskName, response string) error {
	callbackURL := fmt.Sprintf("%s/callback", c.url)

	callbackRequest := CallbackRequest{
		CallbackToken: callbackToken,
		TaskName:      taskName,
		Response:      response,
	}
	reqBodyBytes, err := json.Marshal(callbackRequest)
	if err != nil {
		return fmt.Errorf("failed to marshal callback request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("failed to send callback: %w", err)
	}
	_ = resp.Body.Close()

	return nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer func() {
			_ = resp.Body.Close()
		}()
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return resp, nil
}
