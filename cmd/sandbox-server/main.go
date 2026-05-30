package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
)

func main() {
	if os.Getenv("TASK_NAME") != "" {
		runLegacyCLI()
		return
	}

	port := getEnvWithDefault("PORT", "8080")
	log.Printf("Sandbox Server: Starting HTTP daemon listening on port %s", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/task", sandbox.TaskHandler)

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Sandbox Server: HTTP server failed: %v", err)
	}
}

func runLegacyCLI() {
	taskName := os.Getenv("TASK_NAME")
	callbackURL := os.Getenv("CALLBACK_URL")
	tokenPath := getEnvWithDefault("CALLBACK_TOKEN_PATH", "/etc/cloud-agent/callback-token")
	githubTokenPath := getEnvWithDefault("GITHUB_TOKEN_PATH", "/etc/github-token/github-token")
	repoOwner := os.Getenv("REPO_OWNER")
	repoName := os.Getenv("REPO_NAME")
	taskOwner := os.Getenv("TASK_OWNER")
	taskOwnerEmail := os.Getenv("TASK_OWNER_EMAIL")
	workspaceDir := getEnvWithDefault("WORKSPACE_DIR", "/workspace")
	taskType := getEnvWithDefault("TASK_TYPE", "comment")
	agentBinary := getEnvWithDefault("AGENT_BINARY", "opencode")
	prompt := os.Getenv("PROMPT")

	if taskName == "" {
		log.Fatal("TASK_NAME environment variable is required")
	}
	if callbackURL == "" {
		log.Fatal("CALLBACK_URL environment variable is required")
	}
	if repoOwner == "" {
		log.Fatal("REPO_OWNER environment variable is required")
	}
	if repoName == "" {
		log.Fatal("REPO_NAME environment variable is required")
	}
	if taskOwner == "" {
		log.Fatal("TASK_OWNER environment variable is required")
	}
	if taskOwnerEmail == "" {
		log.Fatal("TASK_OWNER_EMAIL environment variable is required")
	}
	if prompt == "" {
		log.Fatal("PROMPT environment variable is required")
	}

	runner := sandbox.NewRunner(
		taskName,
		callbackURL,
		tokenPath,
		githubTokenPath,
		repoOwner,
		repoName,
		taskOwner,
		taskOwnerEmail,
		workspaceDir,
		taskType,
		agentBinary,
		prompt,
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("Sandbox Server (CLI): Running coding agent task %s of type %s", taskName, taskType)
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("Sandbox Server run failed: %s", err.Error())
	}

	log.Println("Sandbox Server (CLI): Task completed successfully")
}

func getEnvWithDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
