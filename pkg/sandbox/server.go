package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

type Runner struct {
	taskName          string
	callbackURL       string
	callbackTokenPath string
	githubTokenPath   string
	repoOwner         string
	repoName          string
	taskOwner         string
	taskOwnerEmail    string
	workspaceDir      string
	taskType          string
	agentBinary       string
	prompt            string
	httpClient        *http.Client
}

func NewRunner(
	taskName string,
	callbackURL string,
	callbackTokenPath string,
	githubTokenPath string,
	repoOwner string,
	repoName string,
	taskOwner string,
	taskOwnerEmail string,
	workspaceDir string,
	taskType string,
	agentBinary string,
	prompt string,
	httpClient *http.Client,
) *Runner {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if callbackTokenPath == "" {
		callbackTokenPath = "/etc/cloud-agent/callback-token"
	}
	if githubTokenPath == "" {
		githubTokenPath = "/etc/github-token/github-token"
	}
	if workspaceDir == "" {
		workspaceDir = "/workspace"
	}
	if taskType == "" {
		taskType = "comment"
	}
	if agentBinary == "" {
		agentBinary = "opencode"
	}
	return &Runner{
		taskName:          taskName,
		callbackURL:       callbackURL,
		callbackTokenPath: callbackTokenPath,
		githubTokenPath:   githubTokenPath,
		repoOwner:         repoOwner,
		repoName:          repoName,
		taskOwner:         taskOwner,
		taskOwnerEmail:    taskOwnerEmail,
		workspaceDir:      workspaceDir,
		taskType:          taskType,
		agentBinary:       agentBinary,
		prompt:            prompt,
		httpClient:        httpClient,
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

func (r *Runner) Run(ctx context.Context) error {
	// Read callback token from file
	tokenBytes, err := os.ReadFile(r.callbackTokenPath)
	if err != nil {
		return fmt.Errorf("failed to read callback token from %s: %v", r.callbackTokenPath, err)
	}
	token := string(bytes.TrimSpace(tokenBytes))

	// Read GitHub token from file
	var ghToken string
	ghTokenBytes, err := os.ReadFile(r.githubTokenPath)
	if err == nil {
		ghToken = string(bytes.TrimSpace(ghTokenBytes))
	} else {
		return fmt.Errorf("failed to read GitHub token from %s: %v", r.githubTokenPath, err)
	}

	// 1. Prepare Workspace Directory
	if err := os.MkdirAll(r.workspaceDir, 0755); err != nil {
		return fmt.Errorf("failed to create workspace directory %s: %v", r.workspaceDir, err)
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
		return sanitizeError(fmt.Errorf("failed to clone repository: %v (stderr: %s)", err, cloneStderr.String()), ghToken)
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

	// 3. Configure Local Git Attribution
	if err := runGit("config", "user.name", r.taskOwner); err != nil {
		return err
	}
	if err := runGit("config", "user.email", r.taskOwnerEmail); err != nil {
		return err
	}

	// 4. Create and Checkout New Branch (PR tasks only)
	if r.taskType == "pr" {
		branchName := fmt.Sprintf("attribution-test-%s", r.taskName)
		if err := runGit("checkout", "-b", branchName); err != nil {
			return err
		}
	}

	// 5. Invoke CLI coding agent binary inside workspace
	agentCmd := exec.CommandContext(ctx, r.agentBinary, r.prompt)
	agentCmd.Dir = r.workspaceDir
	var agentStdout, agentStderr bytes.Buffer
	agentCmd.Stdout = &agentStdout
	agentCmd.Stderr = &agentStderr

	if err := agentCmd.Run(); err != nil {
		return sanitizeError(fmt.Errorf("agent %s failed: %v (stderr: %s)", r.agentBinary, err, agentStderr.String()), ghToken)
	}

	responseStr := strings.TrimSpace(agentStdout.String())

	// Construct request body
	payload := map[string]string{
		"taskName": r.taskName,
		"response": responseStr,
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
