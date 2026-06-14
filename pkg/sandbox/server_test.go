package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type mockWebhookServer struct {
	server       *httptest.Server
	lastCallback *CallbackRequest
}

type CallbackRequest struct {
	CallbackToken string `json:"callbackToken"`
	TaskName      string `json:"taskName"`
	Response      string `json:"response"`
}

func startMockWebhookServer(t *testing.T) *mockWebhookServer {
	t.Helper()
	m := &mockWebhookServer{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/callback" && r.Method == http.MethodPost {
			var req CallbackRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				m.lastCallback = &req
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	return m
}

func setupRemoteRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "remote-git-repo-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	runGitCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to run git %v in remote: %v", args, err)
		}
	}

	runGitCmd("init")
	runGitCmd("config", "user.name", "Remote Owner")
	runGitCmd("config", "user.email", "remote@example.com")
	runGitCmd("config", "commit.gpgsign", "false")

	initialFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(initialFile, []byte("# Test Repo"), 0644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}

	runGitCmd("add", "README.md")
	runGitCmd("commit", "-m", "Initial commit")
	runGitCmd("branch", "-M", "main")

	return dir
}

func setupMockAgent(t *testing.T, makesChanges bool) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mock-agent-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	agentPath := filepath.Join(dir, "mock-agent")
	var content string
	if makesChanges {
		content = `#!/bin/sh
echo "Agent output message"
echo "agent change" > agent_output.txt
`
	} else {
		content = `#!/bin/sh
echo "Agent output message"
`
	}

	if err := os.WriteFile(agentPath, []byte(content), 0755); err != nil {
		t.Fatalf("failed to write mock agent: %v", err)
	}

	return agentPath
}

func mockGitHubServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/testowner/testrepo/pulls") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number": 42}`))
			return
		}
		if r.URL.Path == "/repos/testowner/testrepo" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"default_branch": "main"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestRunner_Run_PR_WithChanges(t *testing.T) {
	remoteDir := setupRemoteRepo(t)
	defer func() { _ = os.RemoveAll(remoteDir) }()

	agentPath := setupMockAgent(t, true)
	defer func() { _ = os.RemoveAll(filepath.Dir(agentPath)) }()

	mockSrv := startMockWebhookServer(t)
	defer mockSrv.server.Close()

	ghSrv := mockGitHubServer(t)
	defer ghSrv.Close()

	agentHome, err := os.MkdirTemp("", "agent-home-*")
	if err != nil {
		t.Fatalf("failed to create temp agent home: %v", err)
	}
	defer func() { _ = os.RemoveAll(agentHome) }()

	t.Setenv("AGENT_HOME_DIR", agentHome)
	t.Setenv("GIT_REMOTE_URL", remoteDir)
	t.Setenv("AGENT_BIN", agentPath)
	t.Setenv("WEBHOOK_LISTENER_URL", mockSrv.server.URL)
	t.Setenv("GITHUB_API_URL", ghSrv.URL+"/")

	agentHomeDir = getAgentHomeDir()

	runner := NewRunner(
		"task-123",
		"cb-token-xyz",
		"dummy-gh-token",
		"testowner",
		"testrepo",
		"Agent Name",
		"agent@example.com",
		"pr",
		"please create changes",
		123,
	)

	exitCode, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Runner.Run failed: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}

	if mockSrv.lastCallback == nil {
		t.Fatalf("Webhook callback was not received")
	}
	if mockSrv.lastCallback.Response != "I have created a PR #42" {
		t.Errorf("Expected callback response 'I have created a PR #42', got %q", mockSrv.lastCallback.Response)
	}
	if mockSrv.lastCallback.TaskName != "task-123" {
		t.Errorf("Expected taskName 'task-123', got %q", mockSrv.lastCallback.TaskName)
	}

	cmd := exec.Command("git", "branch", "--list", "attribution-test-task-123")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to check branches in remote repo: %v", err)
	}
	if !strings.Contains(string(out), "attribution-test-task-123") {
		t.Errorf("Expected branch 'attribution-test-task-123' to exist in remote repo, but was not found. Branches:\n%s", string(out))
	}

	cmd = exec.Command("git", "log", "-n", "1", "attribution-test-task-123", "--pretty=format:%an|%ae|%s")
	cmd.Dir = remoteDir
	logOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get git log from remote repo: %v", err)
	}
	expectedLog := "Agent Name|agent@example.com|cloud-agent: automated commit for task task-123"
	if string(logOut) != expectedLog {
		t.Errorf("Expected git log %q, got %q", expectedLog, string(logOut))
	}
}

func TestRunner_Run_PR_NoChanges(t *testing.T) {
	remoteDir := setupRemoteRepo(t)
	defer func() { _ = os.RemoveAll(remoteDir) }()

	agentPath := setupMockAgent(t, false)
	defer func() { _ = os.RemoveAll(filepath.Dir(agentPath)) }()

	mockSrv := startMockWebhookServer(t)
	defer mockSrv.server.Close()

	agentHome, err := os.MkdirTemp("", "agent-home-*")
	if err != nil {
		t.Fatalf("failed to create temp agent home: %v", err)
	}
	defer func() { _ = os.RemoveAll(agentHome) }()

	t.Setenv("AGENT_HOME_DIR", agentHome)
	t.Setenv("GIT_REMOTE_URL", remoteDir)
	t.Setenv("AGENT_BIN", agentPath)
	t.Setenv("WEBHOOK_LISTENER_URL", mockSrv.server.URL)

	agentHomeDir = getAgentHomeDir()

	runner := NewRunner(
		"task-456",
		"cb-token-456",
		"dummy-gh-token",
		"testowner",
		"testrepo",
		"Agent Name",
		"agent@example.com",
		"pr",
		"please create changes",
		456,
	)

	exitCode, err := runner.Run(context.Background())
	if err == nil {
		t.Fatalf("Runner.Run failed: Expected error but did not get one")
	}
	if exitCode != 1 {
		t.Errorf("Expected exit code 1, got %d", exitCode)
	}

	if mockSrv.lastCallback != nil {
		t.Fatalf("Webhook callback was not expected but received: %+v", mockSrv.lastCallback)
	}

	cmd := exec.Command("git", "branch", "--list", "attribution-test-task-456")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to check branches in remote repo: %v", err)
	}
	if strings.Contains(string(out), "attribution-test-task-456") {
		t.Errorf("Expected branch 'attribution-test-task-456' to NOT exist in remote repo, but it was found")
	}
}

func TestRunner_Run_CommentTask_NoCommitPush(t *testing.T) {
	remoteDir := setupRemoteRepo(t)
	defer func() { _ = os.RemoveAll(remoteDir) }()

	agentPath := setupMockAgent(t, true)
	defer func() { _ = os.RemoveAll(filepath.Dir(agentPath)) }()

	mockSrv := startMockWebhookServer(t)
	defer mockSrv.server.Close()

	agentHome, err := os.MkdirTemp("", "agent-home-*")
	if err != nil {
		t.Fatalf("failed to create temp agent home: %v", err)
	}
	defer func() { _ = os.RemoveAll(agentHome) }()

	t.Setenv("AGENT_HOME_DIR", agentHome)
	t.Setenv("GIT_REMOTE_URL", remoteDir)
	t.Setenv("AGENT_BIN", agentPath)
	t.Setenv("WEBHOOK_LISTENER_URL", mockSrv.server.URL)

	agentHomeDir = getAgentHomeDir()

	runner := NewRunner(
		"task-789",
		"cb-token-789",
		"dummy-gh-token",
		"testowner",
		"testrepo",
		"Agent Name",
		"agent@example.com",
		"comment",
		"please answer",
		789,
	)

	exitCode, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Runner.Run failed: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}

	if mockSrv.lastCallback == nil {
		t.Fatalf("Webhook callback was not received")
	}

	cmd := exec.Command("git", "branch", "--list", "attribution-test-task-789")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to check branches in remote repo: %v", err)
	}
	if strings.Contains(string(out), "attribution-test-task-789") {
		t.Errorf("Expected branch 'attribution-test-task-789' to NOT exist in remote repo, but it was found")
	}
}
