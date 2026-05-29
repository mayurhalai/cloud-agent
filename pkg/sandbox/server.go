package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

type Runner struct {
	taskName          string
	callbackURL       string
	callbackTokenPath string
	httpClient        *http.Client
}

func NewRunner(taskName, callbackURL, callbackTokenPath string, httpClient *http.Client) *Runner {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if callbackTokenPath == "" {
		callbackTokenPath = "/etc/cloud-agent/callback-token"
	}
	return &Runner{
		taskName:          taskName,
		callbackURL:       callbackURL,
		callbackTokenPath: callbackTokenPath,
		httpClient:        httpClient,
	}
}

func (r *Runner) Run(ctx context.Context) error {
	// Read callback token from file
	tokenBytes, err := os.ReadFile(r.callbackTokenPath)
	if err != nil {
		return fmt.Errorf("failed to read callback token from %s: %v", r.callbackTokenPath, err)
	}
	token := string(bytes.TrimSpace(tokenBytes))

	// Construct request body
	payload := map[string]string{
		"taskName": r.taskName,
		"response": "Hello World",
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal callback request: %v", err)
	}

	// Post back to listener callback
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.callbackURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create callback request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("callback request failed: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("callback returned unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
