package sandbox

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskHandler_MethodNotAllowed(t *testing.T) {
	methods := []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, "/task", nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(TaskHandler)
			handler.ServeHTTP(rr, req)

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
	req, err := http.NewRequest(http.MethodPost, "/task", bytes.NewBufferString("{invalid-json"))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(TaskHandler)
	handler.ServeHTTP(rr, req)

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
	tests := []struct {
		name         string
		payload      TaskRequest
		missingField string
	}{
		{
			name: "missing taskName",
			payload: TaskRequest{
				CallbackURL:    "http://example.com/callback",
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
			name: "missing callbackURL",
			payload: TaskRequest{
				TaskName:       "task-123",
				CallbackToken:  "cb-token",
				GitHubToken:    "gh-token",
				RepoOwner:      "owner",
				RepoName:       "repo",
				TaskOwner:      "user",
				TaskOwnerEmail: "user@example.com",
				TaskType:       "issue",
				Prompt:         "fix bug",
			},
			missingField: "callbackURL",
		},
		{
			name: "missing callbackToken",
			payload: TaskRequest{
				TaskName:       "task-123",
				CallbackURL:    "http://example.com/callback",
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
				CallbackURL:    "http://example.com/callback",
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
				CallbackURL:    "http://example.com/callback",
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
				CallbackURL:    "http://example.com/callback",
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
				CallbackURL:    "http://example.com/callback",
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
				CallbackURL:    "http://example.com/callback",
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
				CallbackURL:   "http://example.com/callback",
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
				CallbackURL:    "http://example.com/callback",
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
			handler := http.HandlerFunc(TaskHandler)
			handler.ServeHTTP(rr, req)

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
				t.Errorf("Expected message to mention missing field %q, got %q", tt.missingField, resp.Message)
			}
		})
	}
}
