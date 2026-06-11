package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Runner struct {
	taskName       string
	callbackURL    string
	callbackToken  string
	githubToken    string
	repoOwner      string
	repoName       string
	taskOwner      string
	taskOwnerEmail string
	workspaceDir   string
	taskType       string
	prompt         string
	httpClient     *http.Client
}

func NewRunner(
	taskName string,
	callbackURL string,
	callbackToken string,
	githubToken string,
	repoOwner string,
	repoName string,
	taskOwner string,
	taskOwnerEmail string,
	workspaceDir string,
	taskType string,
	prompt string,
	httpClient *http.Client,
) *Runner {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if workspaceDir == "" {
		workspaceDir = "/workspace"
	}
	if taskType == "" {
		taskType = "comment"
	}
	return &Runner{
		taskName:       taskName,
		callbackURL:    callbackURL,
		callbackToken:  callbackToken,
		githubToken:    githubToken,
		repoOwner:      repoOwner,
		repoName:       repoName,
		taskOwner:      taskOwner,
		taskOwnerEmail: taskOwnerEmail,
		workspaceDir:   workspaceDir,
		taskType:       taskType,
		prompt:         prompt,
		httpClient:     httpClient,
	}
}

// sanitizeError replaces any occurrences of the token with "***" in the error message.
func sanitizeError(err error, token string) error {
	if err == nil {
		return nil
	}
	if token == "" {
		return err
	}
	msg := err.Error()
	msg = strings.ReplaceAll(msg, token, "***")
	return fmt.Errorf("%s", msg)
}

func (r *Runner) Run(ctx context.Context) (int, error) {
	token := r.callbackToken
	ghToken := r.githubToken

	// 1. Prepare Workspace Directory
	if err := os.MkdirAll(r.workspaceDir, 0755); err != nil {
		return 1, fmt.Errorf("failed to create workspace directory %s: %v", r.workspaceDir, err)
	}

	// 2. Clone Repository
	var cloneURL string
	if testRemoteURL := os.Getenv("GIT_REMOTE_URL"); testRemoteURL != "" {
		cloneURL = testRemoteURL
	} else {
		cloneURL = fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", ghToken, r.repoOwner, r.repoName)
	}

	cloneCmd := exec.CommandContext(ctx, "git", "clone", cloneURL, r.workspaceDir)
	var cloneStderr bytes.Buffer
	cloneCmd.Stderr = &cloneStderr
	if err := cloneCmd.Run(); err != nil {
		return 1, sanitizeError(fmt.Errorf("failed to clone repository: %v (stderr: %s)", err, cloneStderr.String()), ghToken)
	}

	// Helper to run git commands inside workspace
	runGit := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = r.workspaceDir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return sanitizeError(fmt.Errorf("git %v failed: %v (stderr: %s)", args, err, stderr.String()), ghToken)
		}
		return nil
	}

	// 3. Configure Local Git Attribution and Branch (PR tasks only)
	if r.taskType == "pr" {
		if err := runGit("config", "user.name", r.taskOwner); err != nil {
			return 1, err
		}
		if err := runGit("config", "user.email", r.taskOwnerEmail); err != nil {
			return 1, err
		}

		branchName := fmt.Sprintf("attribution-test-%s", r.taskName)
		if err := runGit("checkout", "-b", branchName); err != nil {
			return 1, err
		}
	}

	agentBin := os.Getenv("AGENT_BIN")
	if agentBin == "" {
		log.Fatalf("AGENT_BIN is not set")
	}

	// 5. Invoke CLI coding agent binary inside workspace
	var agentCmd *exec.Cmd
	switch agentBin {
	case "pi":
		agentCmd = exec.CommandContext(ctx, agentBin, "-p", r.prompt)
	case "opencode":
		agentCmd = exec.CommandContext(ctx, agentBin, "run", r.prompt)
	default:
		// Integration testing injects custom agent for testing
		agentCmd = exec.CommandContext(ctx, agentBin)
	}
	agentCmd.Dir = r.workspaceDir
	var agentStdout, agentStderr bytes.Buffer
	agentCmd.Stdout = &agentStdout
	agentCmd.Stderr = &agentStderr

	if err := agentCmd.Run(); err != nil {
		exitCode := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		return exitCode, sanitizeError(fmt.Errorf("agent %s failed: %v (stderr: %s)", agentBin, err, agentStderr.String()), ghToken)
	}

	responseStr := strings.TrimSpace(agentStdout.String())

	// Construct request body
	payload := map[string]string{
		"taskName": r.taskName,
		"response": responseStr,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return 1, fmt.Errorf("failed to marshal callback request: %v", err)
	}

	// Post back to listener callback with retries
	var resp *http.Response
	var lastErr error
	backoff := 1 * time.Second
	maxBackoff := 10 * time.Second

	for attempt := 0; attempt <= 5; attempt++ {
		if attempt > 0 {
			jitter := time.Duration(rand.Int63n(int64(backoff)))
			sleepTime := backoff + jitter
			if sleepTime > maxBackoff {
				sleepTime = maxBackoff
			}
			select {
			case <-ctx.Done():
				return 1, ctx.Err()
			case <-time.After(sleepTime):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.callbackURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			return 1, fmt.Errorf("failed to create callback request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, lastErr = r.httpClient.Do(req)
		if lastErr == nil {
			if resp.StatusCode == http.StatusOK {
				break
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("callback returned unexpected status %d: %s", resp.StatusCode, string(body))
		}
	}

	if lastErr != nil {
		return 1, lastErr
	}
	if resp != nil {
		_ = resp.Body.Close()
	}

	return 0, nil
}
