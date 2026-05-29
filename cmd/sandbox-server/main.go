package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
)

func main() {
	taskName := flag.String("task-name", os.Getenv("TASK_NAME"), "Name of the AgentTask custom resource")
	callbackURL := flag.String("callback-url", os.Getenv("CALLBACK_URL"), "Callback URL of the webhook listener")
	tokenPath := flag.String("token-path", getEnvWithDefault("CALLBACK_TOKEN_PATH", "/etc/cloud-agent/callback-token"), "Path to the callback token secret volume file")
	githubTokenPath := flag.String("github-token-path", getEnvWithDefault("GITHUB_TOKEN_PATH", "/etc/github-token/github-token"), "Path to the GitHub token secret volume file")
	repoOwner := flag.String("repo-owner", os.Getenv("REPO_OWNER"), "Owner of the repository")
	repoName := flag.String("repo-name", os.Getenv("REPO_NAME"), "Name of the repository")
	taskOwner := flag.String("task-owner", os.Getenv("TASK_OWNER"), "Owner/triggerer of the task")
	taskOwnerEmail := flag.String("task-owner-email", os.Getenv("TASK_OWNER_EMAIL"), "Email address of the task owner")
	workspaceDir := flag.String("workspace-dir", getEnvWithDefault("WORKSPACE_DIR", "/workspace"), "Workspace directory for repository clone")
	flag.Parse()

	if *taskName == "" {
		log.Fatal("task-name parameter or TASK_NAME environment variable is required")
	}
	if *callbackURL == "" {
		log.Fatal("callback-url parameter or CALLBACK_URL environment variable is required")
	}
	if *repoOwner == "" {
		log.Fatal("repo-owner parameter or REPO_OWNER environment variable is required")
	}
	if *repoName == "" {
		log.Fatal("repo-name parameter or REPO_NAME environment variable is required")
	}
	if *taskOwner == "" {
		log.Fatal("task-owner parameter or TASK_OWNER environment variable is required")
	}
	if *taskOwnerEmail == "" {
		log.Fatal("task-owner-email parameter or TASK_OWNER_EMAIL environment variable is required")
	}

	runner := sandbox.NewRunner(
		*taskName,
		*callbackURL,
		*tokenPath,
		*githubTokenPath,
		*repoOwner,
		*repoName,
		*taskOwner,
		*taskOwnerEmail,
		*workspaceDir,
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("Sandbox Server: Running Hello World answer for task %s", *taskName)
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("Sandbox Server run failed: %s", err.Error())
	}

	log.Println("Sandbox Server: Hello World completed successfully")
}

func getEnvWithDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
