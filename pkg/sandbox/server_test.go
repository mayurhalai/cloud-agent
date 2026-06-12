package sandbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunner_ModelResolution(t *testing.T) {
	// 1. Create a temporary directory for everything
	tmpDir, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	// Create mock Git repo
	gitRepoDir := filepath.Join(tmpDir, "git-repo")
	if err := os.MkdirAll(gitRepoDir, 0755); err != nil {
		t.Fatalf("failed to create git repo dir: %v", err)
	}

	runCmd := func(dir string, name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to run %s %v: %v", name, args, err)
		}
	}
	runCmd(gitRepoDir, "git", "init")
	runCmd(gitRepoDir, "git", "config", "user.name", "Test")
	runCmd(gitRepoDir, "git", "config", "user.email", "test@example.com")
	_ = os.WriteFile(filepath.Join(gitRepoDir, "README.md"), []byte("Hello"), 0644)
	runCmd(gitRepoDir, "git", "add", "README.md")
	runCmd(gitRepoDir, "git", "commit", "-m", "Initial commit")
	runCmd(gitRepoDir, "git", "config", "receive.denyCurrentBranch", "ignore")

	t.Setenv("GIT_REMOTE_URL", gitRepoDir)

	// Create a mock agent script that writes its arguments to a file
	agentScript := `#!/bin/sh
echo "$@" > args.txt
`
	agentPath := filepath.Join(tmpDir, "mock-agent.sh")
	if err := os.WriteFile(agentPath, []byte(agentScript), 0755); err != nil {
		t.Fatalf("failed to write mock agent script: %v", err)
	}
	t.Setenv("AGENT_BIN", agentPath)

	// Setup mock HTTP callback server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	defer ts.Close()

	// 2. Setup models dir
	modelsDir := filepath.Join(tmpDir, "models")
	if err := os.MkdirAll(modelsDir, 0755); err != nil {
		t.Fatalf("failed to create models dir: %v", err)
	}
	t.Setenv("MODELS_DIR", modelsDir)

	tests := []struct {
		name          string
		taskType      string
		prModel       string
		generalModel  string
		expectedModel string
	}{
		{
			name:          "PR task uses pr model",
			taskType:      "pr",
			prModel:       "gemini-2.5-pro",
			generalModel:  "gemini-2.5-flash",
			expectedModel: "gemini-2.5-pro",
		},
		{
			name:          "PR task falls back to general model if pr model not set",
			taskType:      "pr",
			prModel:       "",
			generalModel:  "gemini-2.5-flash",
			expectedModel: "gemini-2.5-flash",
		},
		{
			name:          "General task uses general model",
			taskType:      "comment",
			prModel:       "gemini-2.5-pro",
			generalModel:  "gemini-2.5-flash",
			expectedModel: "gemini-2.5-flash",
		},
		{
			name:          "No model passed if model files are missing",
			taskType:      "comment",
			prModel:       "",
			generalModel:  "",
			expectedModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write model files
			prFile := filepath.Join(modelsDir, "model.pr")
			generalFile := filepath.Join(modelsDir, "model.general")

			_ = os.Remove(prFile)
			_ = os.Remove(generalFile)

			if tt.prModel != "" {
				_ = os.WriteFile(prFile, []byte(tt.prModel+"\n"), 0644)
			}
			if tt.generalModel != "" {
				_ = os.WriteFile(generalFile, []byte(tt.generalModel), 0644)
			}

			workspaceDir := filepath.Join(tmpDir, "workspace-"+strings.ReplaceAll(tt.name, " ", "-"))
			_ = os.RemoveAll(workspaceDir)

			runner := NewRunner(
				"test-task",
				ts.URL,
				"token",
				"ghtoken",
				"owner",
				"repo",
				"owner",
				"owner@example.com",
				workspaceDir,
				tt.taskType,
				"prompt text",
				nil,
			)

			exitCode, err := runner.Run(context.Background())
			if err != nil {
				t.Fatalf("Runner failed: %v", err)
			}
			if exitCode != 0 {
				t.Fatalf("Runner exited with code %d", exitCode)
			}

			// Read args.txt written by the mock agent script
			argsFile := filepath.Join(workspaceDir, "args.txt")
			argsData, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("failed to read args file: %v", err)
			}
			argsStr := strings.TrimSpace(string(argsData))

			if tt.expectedModel != "" {
				expectedArg := "--model " + tt.expectedModel
				if !strings.Contains(argsStr, expectedArg) {
					t.Errorf("Expected arguments to contain %q, got: %q", expectedArg, argsStr)
				}
			} else {
				if strings.Contains(argsStr, "--model") {
					t.Errorf("Expected arguments to not contain '--model', got: %q", argsStr)
				}
			}
		})
	}
}
