package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	"github.com/mayurhalai/cloud-agent/pkg/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestHandleWebhook_Comment(t *testing.T) {
	// Set AGENT_GITHUB_NAME for testing
	t.Setenv("AGENT_GITHUB_NAME", "my-test-bot")

	scheme := runtime.NewScheme()

	tests := []struct {
		name           string
		payload        GitHubWebhookEvent
		mockComments   []*github.IssueComment
		mockHasMore    bool
		expectedStatus int
		expectedBody   string
		expectTask     bool
		expectedPrompt string
	}{
		{
			name: "Ignored comment without mention",
			payload: func() GitHubWebhookEvent {
				var ev GitHubWebhookEvent
				ev.Action = "created"
				ev.Issue.Number = 42
				ev.Issue.Title = "Test Title"
				ev.Issue.Body = "Test Body"
				ev.Comment = &struct {
					Body string `json:"body"`
					User struct {
						Login string `json:"login"`
						ID    int64  `json:"id"`
					} `json:"user"`
				}{
					Body: "Just a regular comment mentioning nobody",
				}
				ev.Comment.User.Login = "user1"
				ev.Comment.User.ID = 100
				return ev
			}(),
			expectedStatus: http.StatusOK,
			expectedBody:   "Ignored comment without mention",
			expectTask:     false,
		},
		{
			name: "Processed comment with bot mention",
			payload: func() GitHubWebhookEvent {
				var ev GitHubWebhookEvent
				ev.Action = "created"
				ev.Issue.Number = 42
				ev.Issue.Title = "Test Title"
				ev.Issue.Body = "Test Body"
				ev.Comment = &struct {
					Body string `json:"body"`
					User struct {
						Login string `json:"login"`
						ID    int64  `json:"id"`
					} `json:"user"`
				}{
					Body: "Hello @my-test-bot, build this!",
				}
				ev.Comment.User.Login = "user1"
				ev.Comment.User.ID = 100
				return ev
			}(),
			mockComments: []*github.IssueComment{
				{Author: "user1", Body: "Hello @my-test-bot, build this!"},
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   "Created task task-42-",
			expectTask:     true,
			expectedPrompt: "Issue Title: Test Title\n\nIssue Body:\nTest Body\n\nComments:\n- [user1]: Hello @my-test-bot, build this!\n" + enhancedCommentPrompt,
		},
		{
			name: "Ignored comment when limit exceeded (>30 comments)",
			payload: func() GitHubWebhookEvent {
				var ev GitHubWebhookEvent
				ev.Action = "created"
				ev.Issue.Number = 42
				ev.Issue.Title = "Test Title"
				ev.Issue.Body = "Test Body"
				ev.Comment = &struct {
					Body string `json:"body"`
					User struct {
						Login string `json:"login"`
						ID    int64  `json:"id"`
					} `json:"user"`
				}{
					Body: "Hello @my-test-bot, build this!",
				}
				ev.Comment.User.Login = "user1"
				ev.Comment.User.ID = 100
				return ev
			}(),
			mockHasMore:    true,
			expectedStatus: http.StatusOK,
			expectedBody:   "The agent does not support issues with more than 30 comments.",
			expectTask:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := k8sfake.NewSimpleClientset()
			gvrToListKind := map[schema.GroupVersionResource]string{
				agentTaskGVR: "AgentTaskList",
			}
			dynClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
			ghClient := &github.MockClient{}
			if tt.mockComments != nil || tt.mockHasMore {
				ghClient.SetIssueComments(42, tt.mockComments, tt.mockHasMore)
			}
			tokenStore := NewInMemoryTokenStore()

			server := NewListenerServer(k8sClient, dynClient, ghClient, "test-namespace", nil, tokenStore)

			bodyBytes, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("failed to marshal payload: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if !strings.Contains(w.Body.String(), tt.expectedBody) {
				t.Errorf("expected body to contain %q, got %q", tt.expectedBody, w.Body.String())
			}

			// Verify dynamic client task creation
			list, err := dynClient.Resource(agentTaskGVR).Namespace("test-namespace").List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatalf("failed to list tasks: %v", err)
			}

			if tt.expectTask {
				if len(list.Items) != 1 {
					t.Fatalf("expected 1 task to be created, got %d", len(list.Items))
				}
				task, err := v1alpha1.FromUnstructured(&list.Items[0])
				if err != nil {
					t.Fatalf("failed to parse unstructured task: %v", err)
				}
				if task.Spec.Prompt != tt.expectedPrompt {
					t.Errorf("expected prompt:\n%q\ngot:\n%q", tt.expectedPrompt, task.Spec.Prompt)
				}
			} else {
				if len(list.Items) != 0 {
					t.Errorf("expected 0 tasks to be created, got %d", len(list.Items))
				}
			}
		})
	}
}

func TestHandleWebhook_Labeled(t *testing.T) {
	scheme := runtime.NewScheme()

	tests := []struct {
		name           string
		payload        GitHubWebhookEvent
		mockComments   []*github.IssueComment
		mockHasMore    bool
		expectedStatus int
		expectedBody   string
		expectTask     bool
		expectedPrompt string
	}{
		{
			name: "Ignored non-agent label",
			payload: func() GitHubWebhookEvent {
				var ev GitHubWebhookEvent
				ev.Action = "labeled"
				ev.Issue.Number = 43
				ev.Issue.Title = "Test Labeled Title"
				ev.Issue.Body = "Test Labeled Body"
				ev.Label = &struct {
					Name string `json:"name"`
				}{
					Name: "some-other-label",
				}
				return ev
			}(),
			expectedStatus: http.StatusOK,
			expectedBody:   "Ignored non-cloud-agent label event",
			expectTask:     false,
		},
		{
			name: "Processed agent label <= 30 comments",
			payload: func() GitHubWebhookEvent {
				var ev GitHubWebhookEvent
				ev.Action = "labeled"
				ev.Issue.Number = 43
				ev.Issue.Title = "Test Labeled Title"
				ev.Issue.Body = "Test Labeled Body"
				ev.Label = &struct {
					Name string `json:"name"`
				}{
					Name: "cloud-agent",
				}
				return ev
			}(),
			mockComments: []*github.IssueComment{
				{Author: "user1", Body: "Initial comment"},
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   "Created task task-43-",
			expectTask:     true,
			expectedPrompt: "Issue Title: Test Labeled Title\n\nIssue Body:\nTest Labeled Body\n\nComments:\n- [user1]: Initial comment\n" + enhancedLabelPrompt,
		},
		{
			name: "PR labeled not supported",
			payload: func() GitHubWebhookEvent {
				var ev GitHubWebhookEvent
				ev.Action = "labeled"
				ev.Issue.Number = 43
				ev.Issue.Title = "Test Labeled Title"
				ev.Issue.Body = "Test Labeled Body"
				ev.Issue.PullRequest = map[string]interface{}{"url": "http://api.github.com/pulls/43"}
				ev.Label = &struct {
					Name string `json:"name"`
				}{
					Name: "cloud-agent",
				}
				return ev
			}(),
			expectedStatus: http.StatusOK,
			expectedBody:   "Adding label on a PR is not supported.",
			expectTask:     false,
		},
		{
			name: "Agent label over limit (>30 comments)",
			payload: func() GitHubWebhookEvent {
				var ev GitHubWebhookEvent
				ev.Action = "labeled"
				ev.Issue.Number = 43
				ev.Issue.Title = "Test Labeled Title"
				ev.Issue.Body = "Test Labeled Body"
				ev.Label = &struct {
					Name string `json:"name"`
				}{
					Name: "cloud-agent",
				}
				return ev
			}(),
			mockHasMore:    true,
			expectedStatus: http.StatusOK,
			expectedBody:   "The agent does not support issues with more than 30 comments.",
			expectTask:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := k8sfake.NewSimpleClientset()
			gvrToListKind := map[schema.GroupVersionResource]string{
				agentTaskGVR: "AgentTaskList",
			}
			dynClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
			ghClient := &github.MockClient{}
			if tt.mockComments != nil || tt.mockHasMore {
				ghClient.SetIssueComments(43, tt.mockComments, tt.mockHasMore)
			}
			tokenStore := NewInMemoryTokenStore()

			server := NewListenerServer(k8sClient, dynClient, ghClient, "test-namespace", nil, tokenStore)

			bodyBytes, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("failed to marshal payload: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if !strings.Contains(w.Body.String(), tt.expectedBody) {
				t.Errorf("expected body to contain %q, got %q", tt.expectedBody, w.Body.String())
			}

			// Verify dynamic client task creation
			list, err := dynClient.Resource(agentTaskGVR).Namespace("test-namespace").List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatalf("failed to list tasks: %v", err)
			}

			if tt.expectTask {
				if len(list.Items) != 1 {
					t.Fatalf("expected 1 task to be created, got %d", len(list.Items))
				}
				task, err := v1alpha1.FromUnstructured(&list.Items[0])
				if err != nil {
					t.Fatalf("failed to parse unstructured task: %v", err)
				}
				if task.Spec.Prompt != tt.expectedPrompt {
					t.Errorf("expected prompt:\n%q\ngot:\n%q", tt.expectedPrompt, task.Spec.Prompt)
				}
			} else {
				if len(list.Items) != 0 {
					t.Errorf("expected 0 tasks to be created, got %d", len(list.Items))
				}
			}
		})
	}
}
