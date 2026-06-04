package sandbox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// TaskRequest defines the JSON payload accepted by the POST /task endpoint.
type TaskRequest struct {
	TaskName          string `json:"taskName"`
	CallbackURL       string `json:"callbackURL"`
	CallbackTokenPath string `json:"callbackTokenPath,omitempty"`
	GitHubTokenPath   string `json:"githubTokenPath,omitempty"`
	CallbackToken     string `json:"callbackToken,omitempty"`
	GitHubToken       string `json:"githubToken,omitempty"`
	RepoOwner         string `json:"repoOwner"`
	RepoName          string `json:"repoName"`
	TaskOwner         string `json:"taskOwner"`
	TaskOwnerEmail    string `json:"taskOwnerEmail"`
	WorkspaceDir      string `json:"workspaceDir,omitempty"`
	TaskType          string `json:"taskType,omitempty"`
	AgentBinary       string `json:"agentBinary,omitempty"`
	Prompt            string `json:"prompt"`
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
	if req.RepoOwner == "" {
		missing = append(missing, "repoOwner")
	}
	if req.RepoName == "" {
		missing = append(missing, "repoName")
	}
	if req.TaskOwner == "" {
		missing = append(missing, "taskOwner")
	}
	if req.TaskOwnerEmail == "" {
		missing = append(missing, "taskOwnerEmail")
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

	// Write tokens to file if they are passed in the request body
	if req.CallbackToken != "" {
		cbPath := req.CallbackTokenPath
		if cbPath == "" {
			cbPath = "/etc/cloud-agent/callback-token"
		}
		if err := os.MkdirAll(filepath.Dir(cbPath), 0755); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(TaskResponse{
				Status:  "error",
				Message: fmt.Sprintf("Failed to create parent directory for callback token: %v", err),
			})
			return
		}
		if err := os.WriteFile(cbPath, []byte(req.CallbackToken), 0600); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(TaskResponse{
				Status:  "error",
				Message: fmt.Sprintf("Failed to write callback token to %s: %v", cbPath, err),
			})
			return
		}
	}

	if req.GitHubToken != "" {
		ghPath := req.GitHubTokenPath
		if ghPath == "" {
			ghPath = "/etc/github-token/github-token"
		}
		if err := os.MkdirAll(filepath.Dir(ghPath), 0755); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(TaskResponse{
				Status:  "error",
				Message: fmt.Sprintf("Failed to create parent directory for github token: %v", err),
			})
			return
		}
		if err := os.WriteFile(ghPath, []byte(req.GitHubToken), 0600); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(TaskResponse{
				Status:  "error",
				Message: fmt.Sprintf("Failed to write github token to %s: %v", ghPath, err),
			})
			return
		}
	}

	// Create and execute the runner
	runner := NewRunner(
		req.TaskName,
		req.CallbackURL,
		req.CallbackTokenPath,
		req.GitHubTokenPath,
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

	if err := runner.Run(r.Context()); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(TaskResponse{
			Status:  "error",
			Message: fmt.Sprintf("Task execution failed: %v", err),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(TaskResponse{
		Status:  "success",
		Message: "Task completed successfully",
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
