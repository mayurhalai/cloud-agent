package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// TaskRequest defines the JSON payload accepted by the POST /task endpoint.
type TaskRequest struct {
	TaskName       string `json:"taskName"`
	CallbackToken  string `json:"callbackToken,omitempty"`
	GitHubToken    string `json:"githubToken,omitempty"`
	RepoOwner      string `json:"repoOwner"`
	RepoName       string `json:"repoName"`
	TaskOwner      string `json:"taskOwner,omitempty"`
	TaskOwnerEmail string `json:"taskOwnerEmail,omitempty"`
	TaskType       string `json:"taskType,omitempty"`
	Prompt         string `json:"prompt"`
}

// TaskResponse defines the JSON response returned by the POST /task endpoint.
type TaskResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

var taskAccepted atomic.Bool

// ResetTaskAccepted resets the accepted task state. Used primarily for testing.
func ResetTaskAccepted() {
	taskAccepted.Store(false)
}

// TaskHandler processes task configurations sent to the POST /task endpoint.
func TaskHandler(c *gin.Context) {
	if taskAccepted.Load() {
		c.Status(http.StatusMethodNotAllowed)
		return
	}

	if c.Request.Method != http.MethodPost {
		c.JSON(http.StatusMethodNotAllowed, TaskResponse{
			Status:  "error",
			Message: "Method not allowed. Only POST is supported.",
		})
		return
	}

	var req TaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, TaskResponse{
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
		c.JSON(http.StatusBadRequest, TaskResponse{
			Status:  "error",
			Message: fmt.Sprintf("Missing required fields: %v", missing),
		})
		return
	}

	// Create and execute the runner
	runner := NewRunner(
		req.TaskName,
		req.CallbackToken,
		req.GitHubToken,
		req.RepoOwner,
		req.RepoName,
		req.TaskOwner,
		req.TaskOwnerEmail,
		req.TaskType,
		req.Prompt,
	)

	taskAccepted.Store(true)

	go func() {
		exitCode, err := runner.Run(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Task execution failed: %v\n", err)
			if os.Getenv("TEST_SANDBOX_API_URL") == "" {
				os.Exit(exitCode)
			}
			return
		}
		if os.Getenv("TEST_SANDBOX_API_URL") == "" {
			os.Exit(0)
		}
	}()

	c.JSON(http.StatusOK, TaskResponse{
		Status:  "success",
		Message: "Task started successfully",
	})
}

// HealthCheckHandler handles health check requests to the GET /health endpoint.
func HealthCheckHandler(c *gin.Context) {
	if c.Request.Method != http.MethodGet {
		c.JSON(http.StatusMethodNotAllowed, TaskResponse{
			Status:  "error",
			Message: "Method not allowed. Only GET is supported.",
		})
		return
	}

	c.JSON(http.StatusOK, TaskResponse{
		Status:  "success",
		Message: "Health check success",
	})
}
