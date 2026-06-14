package sandbox

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupTestRouter() *gin.Engine {
	if err := os.Setenv("TEST_SANDBOX_API_URL", "http://localhost:8080"); err != nil {
		panic(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Any("/task", TaskHandler)
	return r
}

func TestTaskHandler_MethodNotAllowed(t *testing.T) {
	router := setupTestRouter()
	methods := []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, "/task", nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("Expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
			}

			var resp TaskResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("Failed to decode response JSON: %v", err)
			}

			if resp.Status != "error" {
				t.Errorf("Expected status 'error', got %q", resp.Status)
			}
			if !strings.Contains(resp.Message, "Only POST is supported") {
				t.Errorf("Expected message to contain 'Only POST is supported', got %q", resp.Message)
			}
		})
	}
}

func TestTaskHandler_InvalidJSON(t *testing.T) {
	router := setupTestRouter()
	req, err := http.NewRequest(http.MethodPost, "/task", bytes.NewBufferString("{invalid-json"))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}

	var resp TaskResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response JSON: %v", err)
	}

	if resp.Status != "error" {
		t.Errorf("Expected status 'error', got %q", resp.Status)
	}
	if !strings.Contains(resp.Message, "Failed to decode JSON body") {
		t.Errorf("Expected message to contain 'Failed to decode JSON body', got %q", resp.Message)
	}
}

func TestTaskHandler_MissingFields(t *testing.T) {
	router := setupTestRouter()
	tests := []struct {
		name         string
		payload      TaskRequest
		missingField string
	}{
		{
			name: "missing taskName",
			payload: TaskRequest{
				CallbackToken:  "cb-token",
				GitHubToken:    "gh-token",
				RepoOwner:      "owner",
				RepoName:       "repo",
				TaskOwner:      "user",
				TaskOwnerEmail: "user@example.com",
				TaskType:       "issue",
				Prompt:         "fix bug",
			},
			missingField: "taskName",
		},
		{
			name: "missing callbackToken",
			payload: TaskRequest{
				TaskName:       "task-123",
				GitHubToken:    "gh-token",
				RepoOwner:      "owner",
				RepoName:       "repo",
				TaskOwner:      "user",
				TaskOwnerEmail: "user@example.com",
				TaskType:       "issue",
				Prompt:         "fix bug",
			},
			missingField: "callbackToken",
		},
		{
			name: "missing githubToken",
			payload: TaskRequest{
				TaskName:       "task-123",
				CallbackToken:  "cb-token",
				RepoOwner:      "owner",
				RepoName:       "repo",
				TaskOwner:      "user",
				TaskOwnerEmail: "user@example.com",
				TaskType:       "issue",
				Prompt:         "fix bug",
			},
			missingField: "githubToken",
		},
		{
			name: "missing repoOwner",
			payload: TaskRequest{
				TaskName:       "task-123",
				CallbackToken:  "cb-token",
				GitHubToken:    "gh-token",
				RepoName:       "repo",
				TaskOwner:      "user",
				TaskOwnerEmail: "user@example.com",
				TaskType:       "issue",
				Prompt:         "fix bug",
			},
			missingField: "repoOwner",
		},
		{
			name: "missing repoName",
			payload: TaskRequest{
				TaskName:       "task-123",
				CallbackToken:  "cb-token",
				GitHubToken:    "gh-token",
				RepoOwner:      "owner",
				TaskOwner:      "user",
				TaskOwnerEmail: "user@example.com",
				TaskType:       "issue",
				Prompt:         "fix bug",
			},
			missingField: "repoName",
		},
		{
			name: "missing taskType",
			payload: TaskRequest{
				TaskName:       "task-123",
				CallbackToken:  "cb-token",
				GitHubToken:    "gh-token",
				RepoOwner:      "owner",
				RepoName:       "repo",
				TaskOwner:      "user",
				TaskOwnerEmail: "user@example.com",
				Prompt:         "fix bug",
			},
			missingField: "taskType",
		},
		{
			name: "missing taskOwner",
			payload: TaskRequest{
				TaskName:       "task-123",
				CallbackToken:  "cb-token",
				GitHubToken:    "gh-token",
				RepoOwner:      "owner",
				RepoName:       "repo",
				TaskOwnerEmail: "user@example.com",
				TaskType:       "pr",
				Prompt:         "fix bug",
			},
			missingField: "taskOwner",
		},
		{
			name: "missing taskOwnerEmail",
			payload: TaskRequest{
				TaskName:      "task-123",
				CallbackToken: "cb-token",
				GitHubToken:   "gh-token",
				RepoOwner:     "owner",
				RepoName:      "repo",
				TaskOwner:     "user",
				TaskType:      "pr",
				Prompt:        "fix bug",
			},
			missingField: "taskOwnerEmail",
		},
		{
			name: "missing prompt",
			payload: TaskRequest{
				TaskName:       "task-123",
				CallbackToken:  "cb-token",
				GitHubToken:    "gh-token",
				RepoOwner:      "owner",
				RepoName:       "repo",
				TaskOwner:      "user",
				TaskOwnerEmail: "user@example.com",
				TaskType:       "issue",
			},
			missingField: "prompt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("Failed to marshal request payload: %v", err)
			}

			req, err := http.NewRequest(http.MethodPost, "/task", bytes.NewBuffer(body))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("Expected status %d, got %d", http.StatusBadRequest, rr.Code)
			}

			var resp TaskResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("Failed to decode response JSON: %v", err)
			}

			if resp.Status != "error" {
				t.Errorf("Expected status 'error', got %q", resp.Status)
			}
			if !strings.Contains(resp.Message, tt.missingField) {
				t.Errorf("Expected message to contain %q, got %q", tt.missingField, resp.Message)
			}
		})
	}
}

func TestTaskHandler_SingleTaskOnly(t *testing.T) {
	router := setupTestRouter()

	// Ensure we start with a clean state
	ResetTaskAccepted()
	defer ResetTaskAccepted()

	validPayload := TaskRequest{
		TaskName:       "task-1",
		CallbackToken:  "cb-token-1",
		GitHubToken:    "gh-token-1",
		RepoOwner:      "owner-1",
		RepoName:       "repo-1",
		TaskOwner:      "user-1",
		TaskOwnerEmail: "user1@example.com",
		TaskType:       "pr",
		Prompt:         "do something",
	}

	body1, err := json.Marshal(validPayload)
	if err != nil {
		t.Fatalf("Failed to marshal request payload: %v", err)
	}

	// 1. First valid request should succeed
	req1, err := http.NewRequest(http.MethodPost, "/task", bytes.NewBuffer(body1))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	rr1 := httptest.NewRecorder()
	router.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusOK {
		t.Errorf("Expected first request status %d, got %d. Body: %s", http.StatusOK, rr1.Code, rr1.Body.String())
	}

	// 2. Subsequent valid request should be rejected with 405 Method Not Allowed and empty body
	req2, err := http.NewRequest(http.MethodPost, "/task", bytes.NewBuffer(body1))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	rr2 := httptest.NewRecorder()
	router.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected second request status %d, got %d", http.StatusMethodNotAllowed, rr2.Code)
	}
	if rr2.Body.Len() != 0 {
		t.Errorf("Expected second request body to be empty, got %q", rr2.Body.String())
	}

	// 3. Reset state and test that invalid request doesn't lock future valid requests
	ResetTaskAccepted()

	invalidPayload := TaskRequest{
		TaskName: "", // missing required field
	}
	bodyInvalid, err := json.Marshal(invalidPayload)
	if err != nil {
		t.Fatalf("Failed to marshal request payload: %v", err)
	}

	reqInvalid, err := http.NewRequest(http.MethodPost, "/task", bytes.NewBuffer(bodyInvalid))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	rrInvalid := httptest.NewRecorder()
	router.ServeHTTP(rrInvalid, reqInvalid)

	if rrInvalid.Code != http.StatusBadRequest {
		t.Errorf("Expected invalid request status %d, got %d", http.StatusBadRequest, rrInvalid.Code)
	}

	// First valid request after a validation failure should still succeed
	req3, err := http.NewRequest(http.MethodPost, "/task", bytes.NewBuffer(body1))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	rr3 := httptest.NewRecorder()
	router.ServeHTTP(rr3, req3)

	if rr3.Code != http.StatusOK {
		t.Errorf("Expected third request status %d, got %d. Body: %s", http.StatusOK, rr3.Code, rr3.Body.String())
	}
}
