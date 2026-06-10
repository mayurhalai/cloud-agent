package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// TaskRequest defines the JSON payload accepted by the POST /task endpoint.
type TaskRequest struct {
	TaskName       string `json:"taskName"`
	CallbackURL    string `json:"callbackURL"`
	CallbackToken  string `json:"callbackToken,omitempty"`
	GitHubToken    string `json:"githubToken,omitempty"`
	RepoOwner      string `json:"repoOwner"`
	RepoName       string `json:"repoName"`
	TaskOwner      string `json:"taskOwner,omitempty"`
	TaskOwnerEmail string `json:"taskOwnerEmail,omitempty"`
	WorkspaceDir   string `json:"workspaceDir,omitempty"`
	TaskType       string `json:"taskType,omitempty"`
	AgentBinary    string `json:"agentBinary,omitempty"`
	Prompt         string `json:"prompt"`
}

// TaskResponse defines the JSON response returned by the POST /task endpoint.
type TaskResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// TaskHandler processes task configurations sent to the POST /task endpoint.
func TaskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(TaskResponse{
			Status:  "error",
			Message: "Method not allowed. Only POST is supported.",
		})
		return
	}

	var req TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(TaskResponse{
			Status:  "error",
			Message: fmt.Sprintf("Failed to decode JSON body: %v", err),
		})
		return
	}

	// Validate required fields
	var missing []string
	if req.TaskName == "" {
		missing = append(missing, "taskName")
	}
	if req.CallbackURL == "" {
		missing = append(missing, "callbackURL")
	}
	if req.CallbackToken == "" {
		missing = append(missing, "callbackToken")
	}
	if req.GitHubToken == "" {
		missing = append(missing, "githubToken")
	}
	if req.RepoOwner == "" {
		missing = append(missing, "repoOwner")
	}
	if req.RepoName == "" {
		missing = append(missing, "repoName")
	}
	if req.TaskType == "" {
		missing = append(missing, "taskType")
	}
	if req.TaskType == "pr" {
		if req.TaskOwner == "" {
			missing = append(missing, "taskOwner")
		}
		if req.TaskOwnerEmail == "" {
			missing = append(missing, "taskOwnerEmail")
		}
	}
	if req.Prompt == "" {
		missing = append(missing, "prompt")
	}

	if len(missing) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(TaskResponse{
			Status:  "error",
			Message: fmt.Sprintf("Missing required fields: %v", missing),
		})
		return
	}

	// Create and execute the runner
	runner := NewRunner(
		req.TaskName,
		req.CallbackURL,
		req.CallbackToken,
		req.GitHubToken,
		req.RepoOwner,
		req.RepoName,
		req.TaskOwner,
		req.TaskOwnerEmail,
		req.WorkspaceDir,
		req.TaskType,
		req.AgentBinary,
		req.Prompt,
		nil,
	)

	go func() {
		if err := runner.Run(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "Task execution failed: %v\n", err)
			os.Exit(1)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(TaskResponse{
		Status:  "success",
		Message: "Task started successfully",
	})
}

// HealthCheckHandler handles health check requests to the GET /health endpoint.
func HealthCheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(TaskResponse{
			Status:  "error",
			Message: "Method not allowed. Only GET is supported.",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(TaskResponse{
		Status:  "success",
		Message: "Health check success",
	})
}
