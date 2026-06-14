package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cenkalti/backoff/v5"
	"github.com/mayurhalai/cloud-agent/pkg/util"
	"github.com/mayurhalai/cloud-agent/pkg/webhook"
)

var agentHomeDir = getAgentHomeDir()

func getAgentHomeDir() string {
	if val := os.Getenv("AGENT_HOME_DIR"); val != "" {
		return val
	}
	return "/home/node"
}

type Runner struct {
	taskName       string
	callbackToken  string
	githubToken    string
	repoOwner      string
	repoName       string
	taskOwner      string
	taskOwnerEmail string
	taskType       string
	prompt         string
	webhooksClient *webhook.Client
}

func NewRunner(
	taskName string,
	callbackToken string,
	githubToken string,
	repoOwner string,
	repoName string,
	taskOwner string,
	taskOwnerEmail string,
	taskType string,
	prompt string,
) *Runner {
	client := webhook.NewClient(util.GetWebhookListenerURL(util.GetEnvWithDefault("KUBE_NAMESPACE", "cloud-agent")))
	if taskType == "" {
		taskType = "comment"
	}
	return &Runner{
		taskName:       taskName,
		callbackToken:  callbackToken,
		githubToken:    githubToken,
		repoOwner:      repoOwner,
		repoName:       repoName,
		taskOwner:      taskOwner,
		taskOwnerEmail: taskOwnerEmail,
		taskType:       taskType,
		prompt:         prompt,
		webhooksClient: client,
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
	ghToken := r.githubToken

	// 1. Prepare Workspace Directory
	workspaceDir := fmt.Sprintf("%s/%s", agentHomeDir, r.repoName)
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		return 1, fmt.Errorf("failed to create workspace directory %s: %v", workspaceDir, err)
	}

	// 2. Clone Repository
	var cloneURL string
	if testRemoteURL := os.Getenv("GIT_REMOTE_URL"); testRemoteURL != "" {
		cloneURL = testRemoteURL
	} else {
		cloneURL = fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", ghToken, r.repoOwner, r.repoName)
	}

	cloneCmd := exec.CommandContext(ctx, "git", "clone", cloneURL, workspaceDir)
	var cloneStderr bytes.Buffer
	cloneCmd.Stderr = &cloneStderr
	if err := cloneCmd.Run(); err != nil {
		return 1, sanitizeError(fmt.Errorf("failed to clone repository: %v (stderr: %s)", err, cloneStderr.String()), ghToken)
	}

	// Helper to run git commands inside workspace
	runGit := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workspaceDir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return sanitizeError(fmt.Errorf("git %v failed: %v (stderr: %s)", args, err, stderr.String()), ghToken)
		}
		return nil
	}

	runGitOutput := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workspaceDir
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return "", sanitizeError(fmt.Errorf("git %v failed: %v (stderr: %s)", args, err, stderr.String()), ghToken)
		}
		return stdout.String(), nil
	}

	var branchName string
	// 3. Configure Local Git Attribution and Branch (PR tasks only)
	if r.taskType == "pr" {
		if err := runGit("config", "user.name", r.taskOwner); err != nil {
			return 1, err
		}
		if err := runGit("config", "user.email", r.taskOwnerEmail); err != nil {
			return 1, err
		}

		branchName = fmt.Sprintf("attribution-test-%s", r.taskName)
		if err := runGit("checkout", "-b", branchName); err != nil {
			return 1, err
		}
	}

	agentBin := os.Getenv("AGENT_BIN")
	if agentBin == "" {
		log.Fatalf("AGENT_BIN is not set")
	}

	// 4. Resolve Model
	var model string
	modelsDir := os.Getenv("MODELS_DIR")
	if modelsDir == "" {
		modelsDir = "/etc/cloud-agent/models"
	}
	if r.taskType == "pr" {
		model = readModelFromFile(filepath.Join(modelsDir, "model.pr"))
	}
	if model == "" {
		model = readModelFromFile(filepath.Join(modelsDir, "model.general"))
	}

	// 5. Invoke CLI coding agent binary inside workspace
	var args []string
	switch agentBin {
	case "pi":
		args = []string{"-p", r.prompt}
	case "opencode":
		args = []string{"run", r.prompt}
	default:
		args = []string{}
	}

	if model != "" {
		args = append(args, "--model", model)
	}

	agentCmd := exec.CommandContext(ctx, agentBin, args...)
	agentCmd.Dir = workspaceDir
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

	// 6. Check, commit and push changes (PR tasks only)
	if r.taskType == "pr" {
		statusOut, err := runGitOutput("status", "--porcelain")
		if err != nil {
			return 1, err
		}
		if strings.TrimSpace(statusOut) != "" {
			log.Printf("Runner: Changes detected in repository, staging and committing...")
			if err := runGit("add", "-A"); err != nil {
				return 1, err
			}
			commitMsg := fmt.Sprintf("cloud-agent: automated commit for task %s", r.taskName)
			if err := runGit("commit", "-m", commitMsg); err != nil {
				return 1, err
			}
			log.Printf("Runner: Pushing branch %s to remote...", branchName)
			if err := runGit("push", "-f", "origin", branchName); err != nil {
				return 1, err
			}
		} else {
			log.Printf("Runner: No changes detected in repository, skipping commit and push.")
		}
	}

	_, err := backoff.Retry(ctx, func() (interface{}, error) {
		err := r.webhooksClient.Callback(ctx, r.callbackToken, r.taskName, responseStr)
		return nil, err
	}, backoff.WithMaxTries(5), backoff.WithBackOff(backoff.NewExponentialBackOff()))
	if err != nil {
		return 1, fmt.Errorf("failed to callback: %v", err)
	}

	return 0, nil
}

func readModelFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
