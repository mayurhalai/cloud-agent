package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	agentsandbox "sigs.k8s.io/agent-sandbox/clients/go/sandbox"
)

// Sandbox wraps the agentsandbox.Sandbox to extend its capabilities.
type Sandbox struct {
	*agentsandbox.Sandbox
}

// Wrap creates a new custom Sandbox wrapper from an agentsandbox.Sandbox instance.
func Wrap(sb *agentsandbox.Sandbox) *Sandbox {
	return &Sandbox{Sandbox: sb}
}

// ExecuteTask delivers the task payload directly to the Sandbox Server via an HTTP POST request.
func (s *Sandbox) ExecuteTask(ctx context.Context, targetURL string, req *TaskRequest) error {
	payloadBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal task request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send task request to %s: %w", targetURL, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("task execution returned unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
