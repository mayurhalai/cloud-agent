package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	"github.com/mayurhalai/cloud-agent/pkg/github"
	"github.com/mayurhalai/cloud-agent/pkg/orchestrator"
	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
	"github.com/mayurhalai/cloud-agent/pkg/webhook"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

var agentTaskGVR = schema.GroupVersionResource{
	Group:    "cloudagent.mayurhalai.github.com",
	Version:  "v1alpha1",
	Resource: "agenttasks",
}

func TestEndToEndHelloWorld(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	namespace := "default"
	scheme := runtime.NewScheme()

	// 1. Instantiate fake clients and mock github client
	fakeK8s := kubernetesfake.NewSimpleClientset()
	fakeDyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		schema.GroupVersionResource{
			Group:    "cloudagent.mayurhalai.github.com",
			Version:  "v1alpha1",
			Resource: "agenttasks",
		}: "AgentTaskList",
	})
	mockGh := &github.MockClient{}
	mockGh.SetFile("mayurhalai", "cloud-agent", ".cloud-agent.yaml", []byte("sandboxTemplate: custom-template"))

	// 2. Set up Webhook Listener HTTP Server
	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace)
	server := httptest.NewServer(listener)
	defer server.Close()

	// 3. Start Orchestrator watch loop
	orch := orchestrator.NewOrchestrator(fakeK8s, fakeDyn, namespace)
	go func() {
		if err := orch.Start(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("Orchestrator error: %v", err)
		}
	}()

	// 4. Send GitHub issue comment webhook event to /webhook
	payload := map[string]interface{}{
		"action": "created",
		"issue": map[string]interface{}{
			"number": 1,
			"title":  "Test Issue",
		},
		"comment": map[string]interface{}{
			"body": "cloud-agent answer",
			"user": map[string]interface{}{
				"login": "test-owner-user",
				"id":    123456,
			},
		},
		"repository": map[string]interface{}{
			"name": "cloud-agent",
			"owner": map[string]interface{}{
				"login": "mayurhalai",
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Failed to marshal webhook payload: %v", err)
	}

	resp, err := http.Post(server.URL+"/webhook", "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		t.Fatalf("Failed to POST webhook: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status code 201, got %d", resp.StatusCode)
	}

	// 5. Verify the AgentTask CRD was created and is monitored by the Orchestrator
	var list *unstructured.UnstructuredList
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		list, err = fakeDyn.Resource(agentTaskGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		return err == nil && len(list.Items) == 1
	})
	if err != nil {
		t.Fatalf("AgentTask not created in time: %v", err)
	}

	taskObj := list.Items[0]
	task, err := v1alpha1.FromUnstructured(&taskObj)
	if err != nil {
		t.Fatalf("Failed to parse AgentTask: %v", err)
	}

	if task.Spec.Prompt != "cloud-agent answer" {
		t.Errorf("Expected prompt 'cloud-agent answer', got %s", task.Spec.Prompt)
	}

	if task.Spec.TaskOwnerEmail != "123456+test-owner-user@users.noreply.github.com" {
		t.Errorf("Expected TaskOwnerEmail '123456+test-owner-user@users.noreply.github.com', got %s", task.Spec.TaskOwnerEmail)
	}

	if task.Spec.SandboxTemplate != "custom-template" {
		t.Errorf("Expected sandboxTemplate 'custom-template', got %s", task.Spec.SandboxTemplate)
	}

	// 6. Wait until the Orchestrator detects the task and provisions a Pod
	podName := fmt.Sprintf("sandbox-%s", task.Name)
	var pod *corev1.Pod
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		pod, err = fakeK8s.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		return err == nil
	})
	if err != nil {
		t.Fatalf("Pod was not created by orchestrator in time: %v", err)
	}

	// Verify Pod environment variables
	var foundOwnerEmail, foundRepoOwner, foundRepoName bool
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "TASK_OWNER_EMAIL" && env.Value == "123456+test-owner-user@users.noreply.github.com" {
			foundOwnerEmail = true
		}
		if env.Name == "REPO_OWNER" && env.Value == "mayurhalai" {
			foundRepoOwner = true
		}
		if env.Name == "REPO_NAME" && env.Value == "cloud-agent" {
			foundRepoName = true
		}
	}
	if !foundOwnerEmail {
		t.Errorf("TASK_OWNER_EMAIL environment variable not found or incorrect on Pod")
	}
	if !foundRepoOwner {
		t.Errorf("REPO_OWNER environment variable not found or incorrect on Pod")
	}
	if !foundRepoName {
		t.Errorf("REPO_NAME environment variable not found or incorrect on Pod")
	}

	// Verify the Volume mount config
	if len(pod.Spec.Volumes) != 2 {
		t.Errorf("Expected 2 Pod volumes, got %d: %+v", len(pod.Spec.Volumes), pod.Spec.Volumes)
	}
	var callbackVolumeFound, githubVolumeFound bool
	for _, vol := range pod.Spec.Volumes {
		if vol.Name == "callback-token-volume" {
			callbackVolumeFound = true
		}
		if vol.Name == "github-token-volume" {
			githubVolumeFound = true
		}
	}
	if !callbackVolumeFound {
		t.Errorf("Missing callback-token-volume in Pod Spec")
	}
	if !githubVolumeFound {
		t.Errorf("Missing github-token-volume in Pod Spec")
	}

	// Verify task state transitioned to Running
	var updatedTaskObj *unstructured.Unstructured
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		updatedTaskObj, err = fakeDyn.Resource(agentTaskGVR).Namespace(namespace).Get(ctx, task.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		updatedTask, _ := v1alpha1.FromUnstructured(updatedTaskObj)
		return updatedTask.Status.State == v1alpha1.StateRunning
	})
	if err != nil {
		t.Fatalf("Task did not transition to Running state in time: %v", err)
	}

	// 7. Simulating Sandbox Server Execution
	// Read the minted callback token from the created Secret
	secret, err := fakeK8s.CoreV1().Secrets(namespace).Get(ctx, task.Spec.CallbackTokenSecretRef, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to retrieve callback secret: %v", err)
	}
	var token string
	if len(secret.Data["token"]) > 0 {
		token = string(secret.Data["token"])
	} else {
		token = secret.StringData["token"]
	}

	// Write token to a temporary file (to simulate secret volume mount in pod)
	tmpDir, err := os.MkdirTemp("", "sandbox-secret")
	if err != nil {
		t.Fatalf("Failed to create tmp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	tokenFile := filepath.Join(tmpDir, "callback-token")
	if err := os.WriteFile(tokenFile, []byte(token), 0644); err != nil {
		t.Fatalf("Failed to write token file: %v", err)
	}

	// Create a local git repository to act as target remote
	srcDir, err := os.MkdirTemp("", "git-src")
	if err != nil {
		t.Fatalf("Failed to create src temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(srcDir)
	}()

	runCmd := func(dir string, name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to run %s %v: %v", name, args, err)
		}
	}

	runCmd(srcDir, "git", "init")
	runCmd(srcDir, "git", "config", "user.name", "Test Src")
	runCmd(srcDir, "git", "config", "user.email", "src@example.com")

	dummyFile := filepath.Join(srcDir, "README.md")
	_ = os.WriteFile(dummyFile, []byte("Hello Remote"), 0644)
	runCmd(srcDir, "git", "add", "README.md")
	runCmd(srcDir, "git", "commit", "-m", "Initial commit")
	runCmd(srcDir, "git", "config", "receive.denyCurrentBranch", "ignore")

	t.Setenv("GIT_REMOTE_URL", srcDir)

	// Set up temporary workspace directory for cloning
	workspaceTempDir, err := os.MkdirTemp("", "sandbox-workspace")
	if err != nil {
		t.Fatalf("Failed to create workspace temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(workspaceTempDir)
	}()

	// Write dummy github token
	githubTokenFile := filepath.Join(tmpDir, "github-token")
	if err := os.WriteFile(githubTokenFile, []byte("dummy-gh-token"), 0644); err != nil {
		t.Fatalf("Failed to write github token: %v", err)
	}

	// Write mock agent script
	mockAgentScript := `#!/bin/sh
if [ "$TASK_TYPE" = "pr" ]; then
  echo "Attribution verification" > pr-test.txt
  git add pr-test.txt
  git commit -m "Agent commit"
  git push origin HEAD
  echo "https://github.com/mayurhalai/cloud-agent/pull/42"
else
  echo "Mock Agent Response"
fi
`
	agentPath := filepath.Join(tmpDir, "mock-agent")
	if err := os.WriteFile(agentPath, []byte(mockAgentScript), 0755); err != nil {
		t.Fatalf("Failed to write mock agent script: %v", err)
	}

	// Start Sandbox Runner pointing to local server callback
	runner := sandbox.NewRunner(
		task.Name,
		server.URL+"/callback",
		tokenFile,
		githubTokenFile,
		"mayurhalai",
		"cloud-agent",
		task.Spec.TaskOwner,
		task.Spec.TaskOwnerEmail,
		workspaceTempDir,
		"comment",
		agentPath,
		task.Spec.Prompt,
		nil,
	)
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Sandbox runner returned error: %v", err)
	}

	// 8. Verify the Callback completed successfully
	// - comment was posted
	comments := mockGh.GetComments()
	if len(comments) != 1 {
		t.Errorf("Expected 1 comment on GitHub, got %d", len(comments))
	} else {
		if comments[0].Body != "Mock Agent Response" {
			t.Errorf("Expected comment body 'Mock Agent Response', got '%s'", comments[0].Body)
		}
		if comments[0].IssueNumber != 1 {
			t.Errorf("Expected issue 1, got %d", comments[0].IssueNumber)
		}
	}

	// - callback token secret was deleted (invalidated)
	_, err = fakeK8s.CoreV1().Secrets(namespace).Get(ctx, task.Spec.CallbackTokenSecretRef, metav1.GetOptions{})
	if err == nil {
		t.Errorf("Expected callback token secret to be deleted/invalidated")
	}

	// 9. Verify Orchestrator cleanup deletes the Pod
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		_, err = fakeK8s.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		return err != nil && strings.Contains(err.Error(), "not found")
	})
	if err != nil {
		t.Fatalf("Pod was not deleted by orchestrator in time: %v", err)
	}

	// Verify the final task status transitioned to Deleted
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		finalTaskObj, err := fakeDyn.Resource(agentTaskGVR).Namespace(namespace).Get(ctx, task.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		finalTask, _ := v1alpha1.FromUnstructured(finalTaskObj)
		return finalTask.Status.State == v1alpha1.StateDeleted
	})
	if err != nil {
		t.Fatalf("Task did not transition to Deleted state in time: %v", err)
	}
}

func pollUntil(ctx context.Context, interval time.Duration, cond func() bool) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if cond() {
				return nil
			}
		}
	}
}

func TestCallbackEndpointAuthentication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	namespace := "default"
	scheme := runtime.NewScheme()

	fakeK8s := kubernetesfake.NewSimpleClientset()
	fakeDyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		schema.GroupVersionResource{
			Group:    "cloudagent.mayurhalai.github.com",
			Version:  "v1alpha1",
			Resource: "agenttasks",
		}: "AgentTaskList",
	})
	mockGh := &github.MockClient{}

	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace)
	server := httptest.NewServer(listener)
	defer server.Close()

	// 1. Create a dummy AgentTask and Secrets directly to test callback API
	taskID := "test-callback-task"
	callbackToken := "super-secret-token"

	cbSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cb-secret",
			Namespace: namespace,
		},
		StringData: map[string]string{
			"token": callbackToken,
		},
	}
	_, err := fakeK8s.CoreV1().Secrets(namespace).Create(ctx, cbSecret, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create callback secret: %v", err)
	}

	agentTask := &v1alpha1.AgentTask{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudagent.mayurhalai.github.com/v1alpha1",
			Kind:       "AgentTask",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskID,
			Namespace: namespace,
			Annotations: map[string]string{
				"cloudagent.mayurhalai.github.com/issue-number": "123",
				"cloudagent.mayurhalai.github.com/repo-owner":   "mayurhalai",
				"cloudagent.mayurhalai.github.com/repo-name":    "cloud-agent",
			},
		},
		Spec: v1alpha1.AgentTaskSpec{
			Prompt:                 "test prompt",
			SandboxTemplate:        "default",
			GitHubTokenSecretRef:   "dummy-gh-secret",
			CallbackTokenSecretRef: "test-cb-secret",
		},
	}
	uTask, err := v1alpha1.ToUnstructured(agentTask)
	if err != nil {
		t.Fatalf("Failed to serialize AgentTask: %v", err)
	}
	_, err = fakeDyn.Resource(agentTaskGVR).Namespace(namespace).Create(ctx, uTask, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create AgentTask: %v", err)
	}

	// 2. Try callback with no Authorization header -> Should reject
	reqPayload := map[string]string{
		"taskName": taskID,
		"response": "Success",
	}
	bodyBytes, _ := json.Marshal(reqPayload)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/callback", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for missing token, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 3. Try callback with invalid Authorization header -> Should reject
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/callback", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for invalid token, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 4. Try callback with correct token in header -> Should succeed
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/callback", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+callbackToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 for correct token, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 5. Subsequent callback with same token -> Should reject (already invalidated)
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/callback", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+callbackToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for already invalidated token, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestEndToEndPRTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	namespace := "default"
	scheme := runtime.NewScheme()

	// 1. Instantiate fake clients and mock github client
	fakeK8s := kubernetesfake.NewSimpleClientset()
	fakeDyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		schema.GroupVersionResource{
			Group:    "cloudagent.mayurhalai.github.com",
			Version:  "v1alpha1",
			Resource: "agenttasks",
		}: "AgentTaskList",
	})
	mockGh := &github.MockClient{}
	mockGh.SetFile("mayurhalai", "cloud-agent", ".cloud-agent.yaml", []byte("sandboxTemplate: custom-template"))

	// 2. Set up Webhook Listener HTTP Server
	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace)
	server := httptest.NewServer(listener)
	defer server.Close()

	// 3. Start Orchestrator watch loop
	orch := orchestrator.NewOrchestrator(fakeK8s, fakeDyn, namespace)
	go func() {
		if err := orch.Start(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("Orchestrator error: %v", err)
		}
	}()

	// 4. Send GitHub issue label webhook event to /webhook
	payload := map[string]interface{}{
		"action": "labeled",
		"issue": map[string]interface{}{
			"number": 1,
			"title":  "Fix bug",
			"body":   "Fix this issue",
		},
		"label": map[string]interface{}{
			"name": "cloud-agent",
		},
		"sender": map[string]interface{}{
			"login": "test-owner-user",
			"id":    123456,
		},
		"repository": map[string]interface{}{
			"name": "cloud-agent",
			"owner": map[string]interface{}{
				"login": "mayurhalai",
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Failed to marshal webhook payload: %v", err)
	}

	resp, err := http.Post(server.URL+"/webhook", "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		t.Fatalf("Failed to POST webhook: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status code 201, got %d", resp.StatusCode)
	}

	// 5. Verify the AgentTask CRD was created
	var list *unstructured.UnstructuredList
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		list, err = fakeDyn.Resource(agentTaskGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		return err == nil && len(list.Items) == 1
	})
	if err != nil {
		t.Fatalf("AgentTask not created in time: %v", err)
	}

	taskObj := list.Items[0]
	task, err := v1alpha1.FromUnstructured(&taskObj)
	if err != nil {
		t.Fatalf("Failed to parse AgentTask: %v", err)
	}

	if task.Annotations["cloudagent.mayurhalai.github.com/task-type"] != "pr" {
		t.Errorf("Expected task type annotation 'pr', got '%s'", task.Annotations["cloudagent.mayurhalai.github.com/task-type"])
	}

	// Wait until orchestrator provisions Pod
	podName := fmt.Sprintf("sandbox-%s", task.Name)
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		_, err := fakeK8s.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		return err == nil
	})
	if err != nil {
		t.Fatalf("Pod was not created in time: %v", err)
	}

	// Retrieve secret/token
	secret, err := fakeK8s.CoreV1().Secrets(namespace).Get(ctx, task.Spec.CallbackTokenSecretRef, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to retrieve callback secret: %v", err)
	}
	var token string
	if len(secret.Data["token"]) > 0 {
		token = string(secret.Data["token"])
	} else {
		token = secret.StringData["token"]
	}

	tmpDir, err := os.MkdirTemp("", "sandbox-secret-pr")
	if err != nil {
		t.Fatalf("Failed to create tmp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	tokenFile := filepath.Join(tmpDir, "callback-token")
	if err := os.WriteFile(tokenFile, []byte(token), 0644); err != nil {
		t.Fatalf("Failed to write token file: %v", err)
	}

	// Create local target remote git repository
	srcDir, err := os.MkdirTemp("", "git-src-pr")
	if err != nil {
		t.Fatalf("Failed to create src temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(srcDir)
	}()

	runCmd := func(dir string, name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to run %s %v: %v", name, args, err)
		}
	}

	runCmd(srcDir, "git", "init")
	runCmd(srcDir, "git", "config", "user.name", "Test Src")
	runCmd(srcDir, "git", "config", "user.email", "src@example.com")

	dummyFile := filepath.Join(srcDir, "README.md")
	_ = os.WriteFile(dummyFile, []byte("Hello Remote"), 0644)
	runCmd(srcDir, "git", "add", "README.md")
	runCmd(srcDir, "git", "commit", "-m", "Initial commit")
	runCmd(srcDir, "git", "config", "receive.denyCurrentBranch", "ignore")

	t.Setenv("GIT_REMOTE_URL", srcDir)

	// Set up temporary workspace directory for cloning
	workspaceTempDir, err := os.MkdirTemp("", "sandbox-workspace-pr")
	if err != nil {
		t.Fatalf("Failed to create workspace temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(workspaceTempDir)
	}()

	githubTokenFile := filepath.Join(tmpDir, "github-token")
	if err := os.WriteFile(githubTokenFile, []byte("dummy-gh-token"), 0644); err != nil {
		t.Fatalf("Failed to write github token: %v", err)
	}

	// Write mock agent script that performs a commit and pushes it
	mockAgentScript := `#!/bin/sh
echo "attribution-verified" > attribution-test.txt
git add attribution-test.txt > /dev/null 2>&1
git commit -m "Verify git attribution for test-owner-user" > /dev/null 2>&1
git push origin HEAD > /dev/null 2>&1
echo "https://github.com/mayurhalai/cloud-agent/pull/42"
`
	agentPath := filepath.Join(tmpDir, "mock-agent")
	if err := os.WriteFile(agentPath, []byte(mockAgentScript), 0755); err != nil {
		t.Fatalf("Failed to write mock agent script: %v", err)
	}

	// Start Sandbox Runner pointing to local server callback
	runner := sandbox.NewRunner(
		task.Name,
		server.URL+"/callback",
		tokenFile,
		githubTokenFile,
		"mayurhalai",
		"cloud-agent",
		task.Spec.TaskOwner,
		task.Spec.TaskOwnerEmail,
		workspaceTempDir,
		"pr",
		agentPath,
		task.Spec.Prompt,
		nil,
	)
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Sandbox runner returned error: %v", err)
	}

	// Verify commit logs
	cmdLog := exec.Command("git", "log", "-1", "--format=%an|%ae")
	cmdLog.Dir = workspaceTempDir
	logOut, err := cmdLog.Output()
	if err != nil {
		t.Fatalf("Failed to run git log: %v", err)
	}
	logStr := strings.TrimSpace(string(logOut))
	expectedAuthor := fmt.Sprintf("%s|%s", task.Spec.TaskOwner, task.Spec.TaskOwnerEmail)
	if logStr != expectedAuthor {
		t.Errorf("Expected commit author %q, got %q", expectedAuthor, logStr)
	}

	// Verify the Callback reported the PR link
	comments := mockGh.GetComments()
	if len(comments) != 1 {
		t.Errorf("Expected 1 comment on GitHub, got %d", len(comments))
	} else {
		if comments[0].Body != "https://github.com/mayurhalai/cloud-agent/pull/42" {
			t.Errorf("Expected comment body 'https://github.com/mayurhalai/cloud-agent/pull/42', got '%s'", comments[0].Body)
		}
	}
}

func TestLabelOnPRRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	namespace := "default"
	scheme := runtime.NewScheme()

	fakeK8s := kubernetesfake.NewSimpleClientset()
	fakeDyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		schema.GroupVersionResource{
			Group:    "cloudagent.mayurhalai.github.com",
			Version:  "v1alpha1",
			Resource: "agenttasks",
		}: "AgentTaskList",
	})
	mockGh := &github.MockClient{}

	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace)
	server := httptest.NewServer(listener)
	defer server.Close()

	// 1. Simulate a labeled event on a PR
	payload := map[string]interface{}{
		"action": "labeled",
		"issue": map[string]interface{}{
			"number": 100,
			"title":  "PR Title",
			"body":   "PR Body",
			"pull_request": map[string]interface{}{
				"url": "https://api.github.com/repos/mayurhalai/cloud-agent/pulls/100",
			},
		},
		"label": map[string]interface{}{
			"name": "cloud-agent",
		},
		"sender": map[string]interface{}{
			"login": "test-owner-user",
			"id":    123456,
		},
		"repository": map[string]interface{}{
			"name": "cloud-agent",
			"owner": map[string]interface{}{
				"login": "mayurhalai",
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Failed to marshal webhook payload: %v", err)
	}

	resp, err := http.Post(server.URL+"/webhook", "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		t.Fatalf("Failed to POST webhook: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status code 200, got %d", resp.StatusCode)
	}

	// 2. Verify that no AgentTask CRD was created
	list, err := fakeDyn.Resource(agentTaskGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list AgentTasks: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("Expected 0 AgentTasks, got %d", len(list.Items))
	}

	// 3. Verify that a rejection comment was posted on GitHub
	comments := mockGh.GetComments()
	if len(comments) != 1 {
		t.Fatalf("Expected 1 comment on GitHub, got %d", len(comments))
	}
	if comments[0].Body != "Adding label on a PR is not supported." {
		t.Errorf("Expected comment body 'Adding label on a PR is not supported.', got '%s'", comments[0].Body)
	}
	if comments[0].IssueNumber != 100 {
		t.Errorf("Expected comment on issue/PR 100, got %d", comments[0].IssueNumber)
	}
}
