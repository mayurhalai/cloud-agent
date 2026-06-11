package tests

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	"github.com/mayurhalai/cloud-agent/pkg/github"
	"github.com/mayurhalai/cloud-agent/pkg/orchestrator"
	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
	"github.com/mayurhalai/cloud-agent/pkg/webhook"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	clientgotesting "k8s.io/client-go/testing"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	agentsfake "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/fake"
	agentsv1alpha1 "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/typed/api/v1alpha1"
	extensionsfake "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/fake"
	extensionsclientv1alpha1 "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/typed/api/v1alpha1"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
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
	fakeAgentsCS := agentsfake.NewSimpleClientset() //nolint:staticcheck
	var createdClaims []*extensionsv1alpha1.SandboxClaim
	var claimsMu sync.Mutex

	fakeExtensionsCS := extensionsfake.NewSimpleClientset() //nolint:staticcheck
	fakeExtensionsCS.PrependReactor("create", "sandboxclaims", func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
		createAction := action.(clientgotesting.CreateAction)
		claim := createAction.GetObject().(*extensionsv1alpha1.SandboxClaim)
		if claim.Name == "" && claim.GenerateName != "" {
			claim.Name = claim.GenerateName + "abcde"
		}
		claimsMu.Lock()
		createdClaims = append(createdClaims, claim)
		claimsMu.Unlock()
		return false, nil, nil
	})

	testStore := webhook.NewInMemoryTokenStore()

	fakeDyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		{
			Group:    "cloudagent.mayurhalai.github.com",
			Version:  "v1alpha1",
			Resource: "agenttasks",
		}: "AgentTaskList",
		{
			Group:    "extensions.agents.x-k8s.io",
			Version:  "v1alpha1",
			Resource: "sandboxclaims",
		}: "SandboxClaimList",
	})

	fakeDyn.PrependReactor("create", "agenttasks", func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
		return false, nil, nil
	})
	mockGh := &github.MockClient{}
	mockGh.SetFile("mayurhalai", "cloud-agent", ".cloud-agent.yaml", []byte("sandboxTemplate: custom-template"))

	// Start fake sandbox controller
	startFakeSandboxController(ctx, fakeAgentsCS.AgentsV1alpha1(), fakeExtensionsCS.ExtensionsV1alpha1(), fakeK8s.CoreV1(), namespace)

	// Create temp directories
	tmpDir, err := os.MkdirTemp("", "sandbox-secret")
	if err != nil {
		t.Fatalf("Failed to create tmp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

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
	t.Setenv("WORKSPACE_DIR", filepath.Join(tmpDir, "workspace"))

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
	t.Setenv("AGENT_BINARY", agentPath)

	// Set up mock Sandbox runtime HTTP server
	mockSandboxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upload" {
			err := r.ParseMultipartForm(10 << 20)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer func() { _ = file.Close() }()

			outPath := filepath.Join(tmpDir, header.Filename)
			outFile, err := os.Create(outPath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer func() { _ = outFile.Close() }()
			_, _ = io.Copy(outFile, file)

			w.WriteHeader(http.StatusOK)
			return
		}

		if r.URL.Path == "/task" {
			var req sandbox.TaskRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			// Validate required fields
			if req.TaskName == "" || req.CallbackURL == "" || req.RepoOwner == "" || req.RepoName == "" || req.TaskOwner == "" || req.TaskOwnerEmail == "" || req.Prompt == "" {
				http.Error(w, "Missing required fields in TaskRequest", http.StatusBadRequest)
				return
			}

			// Serialize back to JSON for the handler
			updatedBody, err := json.Marshal(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			newReq := r.Clone(r.Context())
			newReq.Body = io.NopCloser(bytes.NewReader(updatedBody))
			newReq.ContentLength = int64(len(updatedBody))

			sandbox.TaskHandler(w, newReq)
			return
		}

		if r.URL.Path == "/execute" {
			var req struct {
				Command string `json:"command"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			absBinPath, _ := filepath.Abs("../bin")
			pathEnv := fmt.Sprintf("PATH=%s:%s", absBinPath, os.Getenv("PATH"))

			cmd := exec.Command("sh", "-c", req.Command)
			cmd.Dir = tmpDir
			cmd.Env = append(os.Environ(), pathEnv)

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			exitCode := 0
			if err := cmd.Run(); err != nil {
				if exitError, ok := err.(*exec.ExitError); ok {
					exitCode = exitError.ExitCode()
				} else {
					exitCode = 1
				}
			}

			resp := map[string]interface{}{
				"stdout":    stdout.String(),
				"stderr":    stderr.String(),
				"exit_code": exitCode,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		http.Error(w, "Not found", http.StatusNotFound)
	}))
	defer mockSandboxServer.Close()

	t.Setenv("TEST_SANDBOX_API_URL", mockSandboxServer.URL)

	// 2. Set up Webhook Listener HTTP Server
	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace, nil, testStore)
	server := httptest.NewServer(listener)
	defer server.Close()

	t.Setenv("WEBHOOK_LISTENER_URL", server.URL)

	mokeK8sHelper := &sandbox.K8sHelper{
		AgentsClient:     fakeAgentsCS.AgentsV1alpha1(),
		ExtensionsClient: fakeExtensionsCS.ExtensionsV1alpha1(),
		DynamicClient:    fakeDyn,
		CoreClient:       fakeK8s.CoreV1(),
		Log: funcr.New(func(prefix, args string) {
			t.Logf("[SDK] %s: %s", prefix, args)
		}, funcr.Options{}),
	}

	mockSbClient, err := sandbox.NewClient(sandbox.Options{
		K8sHelper: mokeK8sHelper,
		APIURL:    mockSandboxServer.URL,
	})
	if err != nil {
		t.Fatalf("Failed to create sandbox client: %v", err)
	}

	// 3. Start Orchestrator watch loop
	orch := orchestrator.NewOrchestrator(fakeK8s, fakeDyn, mockSbClient, namespace)

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

	// 6. Verify SandboxClaim was created
	var claim *extensionsv1alpha1.SandboxClaim
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		claimsMu.Lock()
		defer claimsMu.Unlock()
		if len(createdClaims) == 1 {
			claim = createdClaims[0]
			return true
		}
		return false
	})
	if err != nil {
		t.Fatalf("SandboxClaim not created in time: %v", err)
	}

	if claim.Spec.TemplateRef.Name != "custom-template" {
		t.Errorf("Expected SandboxClaim templateRef name 'custom-template', got %s", claim.Spec.TemplateRef.Name)
	}

	// 7. Verify the Callback completed successfully
	// - comment was posted
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		return len(mockGh.GetComments()) == 1
	})
	if err != nil {
		t.Fatalf("Comment not posted on GitHub: %v", err)
	}

	comments := mockGh.GetComments()
	if comments[0].Body != "Mock Agent Response" {
		t.Errorf("Expected comment body 'Mock Agent Response', got '%s'", comments[0].Body)
	}

	// - callback token was deleted from TokenStore (invalidated)
	if _, ok := testStore.GetToken(task.Name); ok {
		t.Errorf("Expected callback token to be deleted/invalidated from TokenStore")
	}

	// 8. Verify the task resource was deleted from the API server
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		_, err := fakeDyn.Resource(agentTaskGVR).Namespace(namespace).Get(ctx, task.Name, metav1.GetOptions{})
		return k8serrors.IsNotFound(err)
	})
	if err != nil {
		t.Fatalf("Task was not deleted in time: %v", err)
	}

	// 9. Verify the SandboxClaim was deleted (Closed)
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		claims, err := fakeExtensionsCS.ExtensionsV1alpha1().SandboxClaims(namespace).List(ctx, metav1.ListOptions{})
		return err == nil && len(claims.Items) == 0
	})
	if err != nil {
		t.Fatalf("SandboxClaim was not deleted: %v", err)
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
		{
			Group:    "cloudagent.mayurhalai.github.com",
			Version:  "v1alpha1",
			Resource: "agenttasks",
		}: "AgentTaskList",
	})
	mockGh := &github.MockClient{}

	testStore := webhook.NewInMemoryTokenStore()
	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace, nil, testStore)
	server := httptest.NewServer(listener)
	defer server.Close()

	// 1. Create a dummy AgentTask and Secrets directly to test callback API
	taskID := "test-callback-task"
	callbackToken := "super-secret-token"

	err := testStore.StoreToken(ctx, taskID, callbackToken)
	if err != nil {
		t.Fatalf("Failed to store token: %v", err)
	}

	agentTask := &v1alpha1.AgentTask{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudagent.mayurhalai.github.com/v1alpha1",
			Kind:       "AgentTask",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskID,
			Namespace: namespace,
		},
		Spec: v1alpha1.AgentTaskSpec{
			Prompt:          "test prompt",
			SandboxTemplate: "default",
			RepoOwner:       "mayurhalai",
			RepoName:        "cloud-agent",
			IssueNumber:     123,
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
	fakeAgentsCS := agentsfake.NewSimpleClientset() //nolint:staticcheck
	var createdClaimsPR []*extensionsv1alpha1.SandboxClaim
	var claimsMuPR sync.Mutex

	fakeExtensionsCS := extensionsfake.NewSimpleClientset() //nolint:staticcheck
	fakeExtensionsCS.PrependReactor("create", "sandboxclaims", func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
		createAction := action.(clientgotesting.CreateAction)
		claim := createAction.GetObject().(*extensionsv1alpha1.SandboxClaim)
		if claim.Name == "" && claim.GenerateName != "" {
			claim.Name = claim.GenerateName + "abcde"
		}
		claimsMuPR.Lock()
		createdClaimsPR = append(createdClaimsPR, claim)
		claimsMuPR.Unlock()
		return false, nil, nil
	})

	testStorePR := webhook.NewInMemoryTokenStore()

	fakeDyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		schema.GroupVersionResource{
			Group:    "cloudagent.mayurhalai.github.com",
			Version:  "v1alpha1",
			Resource: "agenttasks",
		}: "AgentTaskList",
		schema.GroupVersionResource{
			Group:    "extensions.agents.x-k8s.io",
			Version:  "v1alpha1",
			Resource: "sandboxclaims",
		}: "SandboxClaimList",
	})

	fakeDyn.PrependReactor("create", "agenttasks", func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
		return false, nil, nil
	})
	mockGh := &github.MockClient{}
	mockGh.SetFile("mayurhalai", "cloud-agent", ".cloud-agent.yaml", []byte("sandboxTemplate: custom-template"))

	// Start fake sandbox controller
	startFakeSandboxController(ctx, fakeAgentsCS.AgentsV1alpha1(), fakeExtensionsCS.ExtensionsV1alpha1(), fakeK8s.CoreV1(), namespace)

	// Create temp directories
	tmpDir, err := os.MkdirTemp("", "sandbox-secret-pr")
	if err != nil {
		t.Fatalf("Failed to create tmp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

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
	t.Setenv("WORKSPACE_DIR", filepath.Join(tmpDir, "workspace"))

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
	t.Setenv("AGENT_BINARY", agentPath)

	// Set up mock Sandbox runtime HTTP server
	mockSandboxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upload" {
			err := r.ParseMultipartForm(10 << 20)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer func() { _ = file.Close() }()

			outPath := filepath.Join(tmpDir, header.Filename)
			outFile, err := os.Create(outPath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer func() { _ = outFile.Close() }()
			_, _ = io.Copy(outFile, file)

			w.WriteHeader(http.StatusOK)
			return
		}

		if r.URL.Path == "/task" {
			var req sandbox.TaskRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			// Validate required fields
			if req.TaskName == "" || req.CallbackURL == "" || req.RepoOwner == "" || req.RepoName == "" || req.TaskOwner == "" || req.TaskOwnerEmail == "" || req.Prompt == "" {
				http.Error(w, "Missing required fields in TaskRequest", http.StatusBadRequest)
				return
			}

			// Serialize back to JSON for the handler
			updatedBody, err := json.Marshal(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			newReq := r.Clone(r.Context())
			newReq.Body = io.NopCloser(bytes.NewReader(updatedBody))
			newReq.ContentLength = int64(len(updatedBody))

			sandbox.TaskHandler(w, newReq)
			return
		}

		if r.URL.Path == "/execute" {
			var req struct {
				Command string `json:"command"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			absBinPath, _ := filepath.Abs("../bin")
			pathEnv := fmt.Sprintf("PATH=%s:%s", absBinPath, os.Getenv("PATH"))

			cmd := exec.Command("sh", "-c", req.Command)
			cmd.Dir = tmpDir
			cmd.Env = append(os.Environ(), pathEnv)

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			exitCode := 0
			if err := cmd.Run(); err != nil {
				if exitError, ok := err.(*exec.ExitError); ok {
					exitCode = exitError.ExitCode()
				} else {
					exitCode = 1
				}
			}

			resp := map[string]interface{}{
				"stdout":    stdout.String(),
				"stderr":    stderr.String(),
				"exit_code": exitCode,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		http.Error(w, "Not found", http.StatusNotFound)
	}))
	defer mockSandboxServer.Close()

	t.Setenv("TEST_SANDBOX_API_URL", mockSandboxServer.URL)

	// 2. Set up Webhook Listener HTTP Server
	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace, nil, testStorePR)
	server := httptest.NewServer(listener)
	defer server.Close()

	t.Setenv("WEBHOOK_LISTENER_URL", server.URL)

	mokeK8sHelper := &sandbox.K8sHelper{
		AgentsClient:     fakeAgentsCS.AgentsV1alpha1(),
		ExtensionsClient: fakeExtensionsCS.ExtensionsV1alpha1(),
		DynamicClient:    fakeDyn,
		CoreClient:       fakeK8s.CoreV1(),
		Log: funcr.New(func(prefix, args string) {
			t.Logf("[SDK] %s: %s", prefix, args)
		}, funcr.Options{}),
	}

	mockSbClient, err := sandbox.NewClient(sandbox.Options{
		K8sHelper: mokeK8sHelper,
		APIURL:    mockSandboxServer.URL,
	})
	if err != nil {
		t.Fatalf("Failed to create sandbox client: %v", err)
	}

	// 3. Start Orchestrator watch loop
	orch := orchestrator.NewOrchestrator(fakeK8s, fakeDyn, mockSbClient, namespace)

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

	if task.Spec.TaskType != "pr" {
		t.Errorf("Expected task type 'pr', got '%s'", task.Spec.TaskType)
	}

	// 6. Verify SandboxClaim was created
	var claim *extensionsv1alpha1.SandboxClaim
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		claimsMuPR.Lock()
		defer claimsMuPR.Unlock()
		if len(createdClaimsPR) == 1 {
			claim = createdClaimsPR[0]
			return true
		}
		return false
	})
	if err != nil {
		t.Fatalf("SandboxClaim not created in time: %v", err)
	}

	if claim.Spec.TemplateRef.Name != "custom-template" {
		t.Errorf("Expected SandboxClaim templateRef name 'custom-template', got %s", claim.Spec.TemplateRef.Name)
	}

	// 7. Verify the Callback reported the PR link
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		return len(mockGh.GetComments()) == 1
	})
	if err != nil {
		t.Fatalf("PR link comment not posted on GitHub: %v", err)
	}

	comments := mockGh.GetComments()
	if comments[0].Body != "https://github.com/mayurhalai/cloud-agent/pull/42" {
		t.Errorf("Expected comment body 'https://github.com/mayurhalai/cloud-agent/pull/42', got '%s'", comments[0].Body)
	}

	// Verify commit logs in the workspace directory!
	workspaceTempDir := filepath.Join(tmpDir, "workspace")
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

	testStore := webhook.NewInMemoryTokenStore()
	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace, nil, testStore)
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

func TestWebhookSignatureValidation(t *testing.T) {
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
	mockGh.SetFile("mayurhalai", "cloud-agent", ".cloud-agent.yaml", []byte("sandboxTemplate: default"))

	webhookSecret := []byte("my-super-secret-key")
	testStore := webhook.NewInMemoryTokenStore()
	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace, webhookSecret, testStore)
	server := httptest.NewServer(listener)
	defer server.Close()

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

	// 1. Missing signature header -> StatusUnauthorized (401)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/webhook", bytes.NewBuffer(payloadBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to POST webhook: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status code 401 for missing signature, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "Missing X-Hub-Signature-256 header") {
		t.Errorf("Expected response body to mention missing header, got %q", string(body))
	}

	// 2. Invalid signature header -> StatusUnauthorized (401)
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/webhook", bytes.NewBuffer(payloadBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid-signature-hex-1234")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to POST webhook: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status code 401 for invalid signature, got %d", resp.StatusCode)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "Invalid webhook signature") {
		t.Errorf("Expected response body to mention invalid signature, got %q", string(body))
	}

	// 3. Valid signature header -> StatusCreated (201)
	mac := hmac.New(sha256.New, webhookSecret)
	mac.Write(payloadBytes)
	validSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/webhook", bytes.NewBuffer(payloadBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", validSig)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to POST webhook: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Expected status code 201 for valid signature, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func startFakeSandboxController(ctx context.Context, agentsClient agentsv1alpha1.AgentsV1alpha1Interface, extensionsClient extensionsclientv1alpha1.ExtensionsV1alpha1Interface, coreClient corev1client.CoreV1Interface, namespace string) {
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				claims, err := extensionsClient.SandboxClaims(namespace).List(ctx, metav1.ListOptions{})
				if err != nil {
					continue
				}
				for _, claim := range claims.Items {
					if claim.Status.SandboxStatus.Name == "" {
						claim.Status.SandboxStatus.Name = "mock-sandbox-" + claim.Name
						_, _ = extensionsClient.SandboxClaims(namespace).UpdateStatus(ctx, &claim, metav1.UpdateOptions{})
					}
					sbName := claim.Status.SandboxStatus.Name
					if sbName != "" {
						_, err := agentsClient.Sandboxes(namespace).Get(ctx, sbName, metav1.GetOptions{})
						if err != nil && k8serrors.IsNotFound(err) {
							sb := &sandboxv1alpha1.Sandbox{
								ObjectMeta: metav1.ObjectMeta{
									Name:      sbName,
									Namespace: namespace,
									Annotations: map[string]string{
										sandbox.PodNameAnnotation: "pod-" + sbName,
									},
								},
							}
							_, _ = agentsClient.Sandboxes(namespace).Create(ctx, sb, metav1.CreateOptions{})

							pod := &corev1.Pod{
								ObjectMeta: metav1.ObjectMeta{
									Name:      "pod-" + sbName,
									Namespace: namespace,
								},
								Status: corev1.PodStatus{
									Phase: corev1.PodRunning,
								},
							}
							_, _ = coreClient.Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
						}
					}
				}

				sandboxes, err := agentsClient.Sandboxes(namespace).List(ctx, metav1.ListOptions{})
				if err != nil {
					continue
				}
				for _, sb := range sandboxes.Items {
					isReady := false
					for _, cond := range sb.Status.Conditions {
						if cond.Type == string(sandboxv1alpha1.SandboxConditionReady) && cond.Status == metav1.ConditionTrue {
							isReady = true
							break
						}
					}
					if !isReady {
						sb.Status.Conditions = []metav1.Condition{
							{
								Type:   string(sandboxv1alpha1.SandboxConditionReady),
								Status: metav1.ConditionTrue,
							},
						}
						_, _ = agentsClient.Sandboxes(namespace).UpdateStatus(ctx, &sb, metav1.UpdateOptions{})
					}
				}
			}
		}
	}()
}

func TestGenerateTokensEndpoint(t *testing.T) {
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

	testStore := webhook.NewInMemoryTokenStore()
	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace, nil, testStore)
	server := httptest.NewServer(listener)
	defer server.Close()

	taskID := "test-jit-task"

	// 1. Try JIT token generation for non-existent task -> 404
	resp, err := http.Post(server.URL+"/task/"+taskID+"/tokens", "application/json", nil)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404 for non-existent task, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2. Create the task in dynamic client (missing repo fields in spec)
	agentTask := &v1alpha1.AgentTask{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudagent.mayurhalai.github.com/v1alpha1",
			Kind:       "AgentTask",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskID,
			Namespace: namespace,
		},
		Spec: v1alpha1.AgentTaskSpec{
			Prompt: "test",
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

	// JIT call for task with missing spec fields -> 400 Bad Request
	resp, err = http.Post(server.URL+"/task/"+taskID+"/tokens", "application/json", nil)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status 400 for missing spec fields, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 3. Update task with valid spec fields
	agentTask.Spec.RepoOwner = "mayurhalai"
	agentTask.Spec.RepoName = "cloud-agent"
	uTaskUpdated, _ := v1alpha1.ToUnstructured(agentTask)
	_, err = fakeDyn.Resource(agentTaskGVR).Namespace(namespace).Update(ctx, uTaskUpdated, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Failed to update AgentTask: %v", err)
	}

	// 4. JIT call for valid task -> 200 OK and returns tokens
	resp, err = http.Post(server.URL+"/task/"+taskID+"/tokens", "application/json", nil)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var tokenResp webhook.TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("Failed to decode TokenResponse: %v", err)
	}
	_ = resp.Body.Close()

	if tokenResp.GitHubToken == "" {
		t.Errorf("Expected non-empty GitHubToken")
	}
	if tokenResp.CallbackToken == "" {
		t.Errorf("Expected non-empty CallbackToken")
	}

	// Verify the callback token was stored in TokenStore
	ok, err := testStore.VerifyToken(ctx, taskID, tokenResp.CallbackToken)
	if err != nil {
		t.Fatalf("Failed to verify token: %v", err)
	}
	if !ok {
		t.Errorf("CallbackToken not stored in TokenStore")
	}
}

func TestOrchestratorRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	namespace := "default"
	scheme := runtime.NewScheme()

	fakeK8s := kubernetesfake.NewSimpleClientset()
	t.Setenv("AGENT_RETRY_COUNT", "2")

	fakeAgentsCS := agentsfake.NewSimpleClientset() //nolint:staticcheck
	var createdClaims []*extensionsv1alpha1.SandboxClaim
	var claimsMu sync.Mutex

	fakeExtensionsCS := extensionsfake.NewSimpleClientset() //nolint:staticcheck
	fakeExtensionsCS.PrependReactor("create", "sandboxclaims", func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
		createAction := action.(clientgotesting.CreateAction)
		claim := createAction.GetObject().(*extensionsv1alpha1.SandboxClaim)
		if claim.Name == "" && claim.GenerateName != "" {
			claim.Name = claim.GenerateName + "abcde"
		}
		claimsMu.Lock()
		createdClaims = append(createdClaims, claim)
		claimsMu.Unlock()
		return false, nil, nil
	})

	testStore := webhook.NewInMemoryTokenStore()

	fakeDyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		schema.GroupVersionResource{
			Group:    "cloudagent.mayurhalai.github.com",
			Version:  "v1alpha1",
			Resource: "agenttasks",
		}: "AgentTaskList",
		schema.GroupVersionResource{
			Group:    "extensions.agents.x-k8s.io",
			Version:  "v1alpha1",
			Resource: "sandboxclaims",
		}: "SandboxClaimList",
	})

	mockGh := &github.MockClient{}
	mockGh.SetFile("mayurhalai", "cloud-agent", ".cloud-agent.yaml", []byte("sandboxTemplate: custom-template"))

	startFakeSandboxController(ctx, fakeAgentsCS.AgentsV1alpha1(), fakeExtensionsCS.ExtensionsV1alpha1(), fakeK8s.CoreV1(), namespace)

	// Set up mock Sandbox runtime HTTP server that always returns 200 OK for /task
	var executeAttempts int
	var execMu sync.Mutex
	mockSandboxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/task" {
			execMu.Lock()
			executeAttempts++
			execMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "Task accepted"})
			return
		}
		http.Error(w, "Not found", http.StatusNotFound)
	}))
	defer mockSandboxServer.Close()

	t.Setenv("TEST_SANDBOX_API_URL", mockSandboxServer.URL)

	// background goroutine to simulate pod termination with non-zero exit code
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				list, err := fakeDyn.Resource(agentTaskGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
				if err != nil {
					continue
				}
				for _, item := range list.Items {
					task, err := v1alpha1.FromUnstructured(&item)
					if err != nil {
						continue
					}
					if task.Status.State == v1alpha1.StateRunning && task.Status.PodName != "" {
						pod, err := fakeK8s.CoreV1().Pods(namespace).Get(ctx, task.Status.PodName, metav1.GetOptions{})
						if err == nil && len(pod.Status.ContainerStatuses) == 0 {
							pod.Status.ContainerStatuses = []corev1.ContainerStatus{
								{
									Name: "pi-agent",
									State: corev1.ContainerState{
										Terminated: &corev1.ContainerStateTerminated{
											ExitCode: 1,
										},
									},
								},
							}
							_, _ = fakeK8s.CoreV1().Pods(namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{})
							// Trigger a modified event on the pod by updating it
							_, _ = fakeK8s.CoreV1().Pods(namespace).Update(ctx, pod, metav1.UpdateOptions{})
						}
					}
				}
			}
		}
	}()

	// Set up Webhook Listener HTTP Server
	listener := webhook.NewListenerServer(fakeK8s, fakeDyn, mockGh, namespace, nil, testStore)
	server := httptest.NewServer(listener)
	defer server.Close()

	t.Setenv("WEBHOOK_LISTENER_URL", server.URL)

	mokeK8sHelper := &sandbox.K8sHelper{
		AgentsClient:     fakeAgentsCS.AgentsV1alpha1(),
		ExtensionsClient: fakeExtensionsCS.ExtensionsV1alpha1(),
		DynamicClient:    fakeDyn,
		CoreClient:       fakeK8s.CoreV1(),
		Log: funcr.New(func(prefix, args string) {
			t.Logf("[SDK] %s: %s", prefix, args)
		}, funcr.Options{}),
	}

	mockSbClient, err := sandbox.NewClient(sandbox.Options{
		K8sHelper: mokeK8sHelper,
		APIURL:    mockSandboxServer.URL,
	})
	if err != nil {
		t.Fatalf("Failed to create sandbox client: %v", err)
	}

	// Start Orchestrator watch loop
	orch := orchestrator.NewOrchestrator(fakeK8s, fakeDyn, mockSbClient, namespace)

	go func() {
		if err := orch.Start(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("Orchestrator error: %v", err)
		}
	}()

	// Send GitHub issue comment webhook event to /webhook
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

	// Verify the AgentTask CRD was created
	var list *unstructured.UnstructuredList
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		list, err = fakeDyn.Resource(agentTaskGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		return err == nil && len(list.Items) == 1
	})
	if err != nil {
		t.Fatalf("AgentTask not created in time: %v", err)
	}

	taskObj := list.Items[0]
	taskName := taskObj.GetName()

	// Wait for the task to reach StateFailed
	err = pollUntil(ctx, 100*time.Millisecond, func() bool {
		tObj, err := fakeDyn.Resource(agentTaskGVR).Namespace(namespace).Get(ctx, taskName, metav1.GetOptions{})
		if err != nil {
			return false
		}
		task, _ := v1alpha1.FromUnstructured(tObj)
		return task.Status.State == v1alpha1.StateFailed
	})
	if err != nil {
		t.Fatalf("Task did not transition to Failed state: %v", err)
	}

	// Fetch final task to verify retries
	tObj, err := fakeDyn.Resource(agentTaskGVR).Namespace(namespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get final task: %v", err)
	}
	finalTask, _ := v1alpha1.FromUnstructured(tObj)

	if finalTask.Status.Retries != 2 {
		t.Errorf("Expected task.Status.Retries to be 2, got %d", finalTask.Status.Retries)
	}

	execMu.Lock()
	attempts := executeAttempts
	execMu.Unlock()
	if attempts != 3 {
		t.Errorf("Expected 3 execution attempts (1 initial + 2 retries), got %d", attempts)
	}

	claimsMu.Lock()
	claimsCount := len(createdClaims)
	claimsMu.Unlock()
	if claimsCount != 3 {
		t.Errorf("Expected 3 SandboxClaims to be created (one for each retry attempt), got %d", claimsCount)
	}
}
