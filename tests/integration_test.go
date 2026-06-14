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
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	"github.com/mayurhalai/cloud-agent/pkg/github"
	"github.com/mayurhalai/cloud-agent/pkg/orchestrator"
	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
	"github.com/mayurhalai/cloud-agent/pkg/webhook"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	agentsv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	agentsfake "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/fake"
	extensionsfake "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/fake"
	extv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
)

var agentTaskGVR = schema.GroupVersionResource{
	Group:    "cloudagent.mayurhalai.github.com",
	Version:  "v1alpha1",
	Resource: "agenttasks",
}

type testEnv struct {
	ctx              context.Context
	cancel           context.CancelFunc
	k8sClient        *k8sfake.Clientset
	dynClient        *dynfake.FakeDynamicClient
	agentsClient     *agentsfake.Clientset
	extensionsClient *extensionsfake.Clientset
	ghClient         *github.MockClient
	tokenStore       *webhook.InMemoryTokenStore
	listenerURL      string
	listenerServer   *httptest.Server
	sandboxServer    *httptest.Server
	sandboxURL       string
	orchestrator     *orchestrator.Orchestrator
	namespace        string
	onSubmitTask     func(req sandbox.TaskRequest)
}

func setupTestEnv(t *testing.T, secret []byte) *testEnv {
	ctx, cancel := context.WithCancel(context.Background())
	namespace := "cloud-agent"

	k8sClient := k8sfake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		agentTaskGVR: "AgentTaskList",
	}
	dynClient := dynfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)

	agentsClient := agentsfake.NewSimpleClientset()         //nolint:staticcheck
	extensionsClient := extensionsfake.NewSimpleClientset() //nolint:staticcheck
	ghClient := &github.MockClient{}
	tokenStore := webhook.NewInMemoryTokenStore()

	var nameCounter int64
	generateRandomSuffix := func() string {
		val := atomic.AddInt64(&nameCounter, 1)
		return fmt.Sprintf("%d", val)
	}

	extensionsClient.PrependReactor("create", "sandboxclaims", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		createAction := action.(clienttesting.CreateAction)
		claim := createAction.GetObject().(*extv1alpha1.SandboxClaim)
		if claim.Name == "" && claim.GenerateName != "" {
			claim.Name = claim.GenerateName + generateRandomSuffix()
		}
		return false, nil, nil
	})

	k8sClient.PrependReactor("create", "events", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		createAction := action.(clienttesting.CreateAction)
		event := createAction.GetObject().(*corev1.Event)
		if event.Name == "" && event.GenerateName != "" {
			event.Name = event.GenerateName + generateRandomSuffix()
		}
		return false, nil, nil
	})

	// Webhook Listener Server
	gin.SetMode(gin.TestMode)
	listenerRouter := webhook.NewListenerServer(k8sClient, dynClient, ghClient, namespace, secret, tokenStore)
	listenerServer := httptest.NewServer(listenerRouter)

	env := &testEnv{
		ctx:              ctx,
		cancel:           cancel,
		k8sClient:        k8sClient,
		dynClient:        dynClient,
		agentsClient:     agentsClient,
		extensionsClient: extensionsClient,
		ghClient:         ghClient,
		tokenStore:       tokenStore,
		listenerURL:      listenerServer.URL,
		listenerServer:   listenerServer,
		namespace:        namespace,
	}

	// Mock Sandbox Server
	sandboxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"success"}`))
			return
		}
		if r.URL.Path == "/task" && r.Method == http.MethodPost {
			var req sandbox.TaskRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"success","message":"Task started"}`))
			if env.onSubmitTask != nil {
				go env.onSubmitTask(req)
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	env.sandboxServer = sandboxServer
	env.sandboxURL = sandboxServer.URL

	t.Setenv("WEBHOOK_LISTENER_URL", listenerServer.URL)
	t.Setenv("TEST_SANDBOX_API_URL", sandboxServer.URL)
	t.Setenv("KUBE_NAMESPACE", namespace)

	// Sandbox Client
	sbHelper := &sandbox.K8sHelper{
		AgentsClient:     agentsClient.AgentsV1alpha1(),
		ExtensionsClient: extensionsClient.ExtensionsV1alpha1(),
		DynamicClient:    dynClient,
		CoreClient:       k8sClient.CoreV1(),
		Log:              testr.New(t),
	}
	sbClient, err := sandbox.NewClient(sandbox.Options{
		APIURL:    sandboxServer.URL,
		K8sHelper: sbHelper,
	})
	if err != nil {
		t.Fatalf("failed to create sandbox client: %v", err)
	}

	// Orchestrator
	env.orchestrator = orchestrator.NewOrchestrator(k8sClient, dynClient, sbClient, namespace)

	// Start micro-reconciler for sandbox claims
	go runMicroReconciler(ctx, namespace, extensionsClient, agentsClient, k8sClient)

	t.Cleanup(func() {
		cancel()
		listenerServer.Close()
		sandboxServer.Close()
	})

	return env
}

func runMicroReconciler(ctx context.Context, namespace string, extensionsClient *extensionsfake.Clientset, agentsClient *agentsfake.Clientset, k8sClient *k8sfake.Clientset) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 1. Process SandboxClaims
			claims, err := extensionsClient.ExtensionsV1alpha1().SandboxClaims(namespace).List(ctx, metav1.ListOptions{})
			if err == nil {
				for _, claim := range claims.Items {
					if claim.Status.SandboxStatus.Name == "" {
						claim.Status.SandboxStatus.Name = "sb-" + claim.Name
						_, _ = extensionsClient.ExtensionsV1alpha1().SandboxClaims(namespace).UpdateStatus(ctx, &claim, metav1.UpdateOptions{})
					}
				}
			}

			// 2. Process Sandboxes
			claims, err = extensionsClient.ExtensionsV1alpha1().SandboxClaims(namespace).List(ctx, metav1.ListOptions{})
			if err == nil {
				for _, claim := range claims.Items {
					sbName := claim.Status.SandboxStatus.Name
					if sbName != "" {
						_, err := agentsClient.AgentsV1alpha1().Sandboxes(namespace).Get(ctx, sbName, metav1.GetOptions{})
						if err != nil { // Create if it doesn't exist
							sbObj := &agentsv1alpha1.Sandbox{
								ObjectMeta: metav1.ObjectMeta{
									Name:      sbName,
									Namespace: namespace,
									Annotations: map[string]string{
										"agents.x-k8s.io/pod-name": "pod-" + sbName,
									},
								},
								Status: agentsv1alpha1.SandboxStatus{
									Conditions: []metav1.Condition{
										{
											Type:   "Ready",
											Status: metav1.ConditionStatus(metav1.ConditionTrue),
										},
									},
									PodIPs: []string{"127.0.0.1"},
								},
							}
							_, _ = agentsClient.AgentsV1alpha1().Sandboxes(namespace).Create(ctx, sbObj, metav1.CreateOptions{})
						}
					}
				}
			}

			// 3. Process Pods
			sbs, err := agentsClient.AgentsV1alpha1().Sandboxes(namespace).List(ctx, metav1.ListOptions{})
			if err == nil {
				for _, sbObj := range sbs.Items {
					podName := sbObj.Annotations["agents.x-k8s.io/pod-name"]
					if podName != "" {
						_, err := k8sClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
						if err != nil { // Create if it doesn't exist
							podObj := &corev1.Pod{
								ObjectMeta: metav1.ObjectMeta{
									Name:      podName,
									Namespace: namespace,
								},
								Status: corev1.PodStatus{
									Phase: corev1.PodRunning,
									PodIP: "127.0.0.1",
									ContainerStatuses: []corev1.ContainerStatus{
										{
											Name: "agent",
											State: corev1.ContainerState{
												Running: &corev1.ContainerStateRunning{
													StartedAt: metav1.Now(),
												},
											},
										},
									},
								},
							}
							_, _ = k8sClient.CoreV1().Pods(namespace).Create(ctx, podObj, metav1.CreateOptions{})
						}
					}
				}
			}
		}
	}
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

func TestIntegration_E2EHelloWorld(t *testing.T) {
	env := setupTestEnv(t, nil)

	env.ghClient.SetIssueComments(1, []*github.IssueComment{{Author: "user", Body: "Hello @cloud-agent"}}, false)

	// Webhook request payload
	payload := map[string]interface{}{
		"action": "created",
		"issue": map[string]interface{}{
			"number": 1,
			"title":  "Hello Title",
			"body":   "Hello Body",
		},
		"comment": map[string]interface{}{
			"body": "Hello @cloud-agent",
			"user": map[string]interface{}{
				"login": "user",
				"id":    100,
			},
		},
		"sender": map[string]interface{}{
			"login": "user",
			"id":    100,
		},
		"repository": map[string]interface{}{
			"name": "testrepo",
			"owner": map[string]interface{}{
				"login": "testowner",
			},
		},
	}
	bodyBytes, _ := json.Marshal(payload)

	cbDone := make(chan struct{})
	env.onSubmitTask = func(req sandbox.TaskRequest) {
		defer close(cbDone)
		// Introduce a sleep to prevent dynamic client status update race condition
		time.Sleep(100 * time.Millisecond)
		callbackURL := fmt.Sprintf("%s/callback", env.listenerURL)
		cbReq := map[string]string{
			"taskName":      req.TaskName,
			"callbackToken": req.CallbackToken,
			"response":      "Hello World!",
		}
		cbBytes, _ := json.Marshal(cbReq)
		httpReq, _ := http.NewRequest(http.MethodPost, callbackURL, bytes.NewBuffer(cbBytes))
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+req.CallbackToken)

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			t.Errorf("failed callback: %v", err)
			return
		}
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("callback returned status %d: %s", resp.StatusCode, string(body))
		}
	}

	// 1. Post to Webhook
	resp, err := http.Post(env.listenerURL+"/webhook", "application/json", bytes.NewBuffer(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to post webhook: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 201, got %d. Body: %s", resp.StatusCode, string(respBody))
	}

	// 2. Poll Dynamic Client for created task
	var taskName string
	err = pollWithTimeout(5*time.Second, 100*time.Millisecond, func() (bool, error) {
		list, err := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		if len(list.Items) == 1 {
			taskName = list.Items[0].GetName()
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for AgentTask creation: %v", err)
	}

	// 3. Keep reconciling the task
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-env.ctx.Done():
				return
			case <-ticker.C:
				_ = env.orchestrator.Reconcile(env.ctx, taskName)
			}
		}
	}()

	// 4. Wait for callback to finish and task to become Completed
	select {
	case <-cbDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for callback execution")
	}

	// 5. Verify the task state transitions to Completed, and then gets deleted
	err = pollWithTimeout(5*time.Second, 100*time.Millisecond, func() (bool, error) {
		list, err := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		return len(list.Items) == 0, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for AgentTask deletion: %v", err)
	}

	// 6. Verify comments posted on GitHub mock
	comments := env.ghClient.GetComments()
	if len(comments) != 1 {
		t.Fatalf("Expected exactly 1 comment posted to GitHub, got %d: %+v", len(comments), comments)
	}
	if comments[0].Body != "Hello World!" {
		t.Errorf("Expected comment body 'Hello World!', got %q", comments[0].Body)
	}
}

func TestIntegration_CallbackAuthentication(t *testing.T) {
	env := setupTestEnv(t, nil)

	// Create an AgentTask CRD
	task := &v1alpha1.AgentTask{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudagent.mayurhalai.github.com/v1alpha1",
			Kind:       "AgentTask",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-auth-test",
			Namespace: env.namespace,
		},
		Spec: v1alpha1.AgentTaskSpec{
			Prompt:          "test prompt",
			SandboxTemplate: "template",
			RepoOwner:       "owner",
			RepoName:        "repo",
			IssueNumber:     1,
			TaskType:        "comment",
		},
	}
	uTask, _ := v1alpha1.ToUnstructured(task)
	_, err := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Create(context.Background(), uTask, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create agent task: %v", err)
	}

	// Store a token
	validToken := "valid-secret-token"
	err = env.tokenStore.StoreToken(context.Background(), task.Name, validToken)
	if err != nil {
		t.Fatalf("Failed to store token: %v", err)
	}

	callbackURL := env.listenerURL + "/callback"

	// 1. Without Token
	cbReq := map[string]string{
		"taskName": task.Name,
		"response": "Success",
	}
	cbBytes, _ := json.Marshal(cbReq)
	resp, err := http.Post(callbackURL, "application/json", bytes.NewBuffer(cbBytes))
	if err != nil {
		t.Fatalf("Failed request: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected 401 for missing token, got %d", resp.StatusCode)
	}

	// 2. With Invalid Token
	cbReq["callbackToken"] = "invalid-token"
	cbBytes, _ = json.Marshal(cbReq)
	resp, err = http.Post(callbackURL, "application/json", bytes.NewBuffer(cbBytes))
	if err != nil {
		t.Fatalf("Failed request: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected 401 for invalid token, got %d", resp.StatusCode)
	}

	// 3. With Valid Token
	cbReq["callbackToken"] = validToken
	cbBytes, _ = json.Marshal(cbReq)
	resp, err = http.Post(callbackURL, "application/json", bytes.NewBuffer(cbBytes))
	if err != nil {
		t.Fatalf("Failed request: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 for valid token, got %d", resp.StatusCode)
	}

	// 4. Repeated call with same token (should be deleted/invalidated)
	resp, err = http.Post(callbackURL, "application/json", bytes.NewBuffer(cbBytes))
	if err != nil {
		t.Fatalf("Failed request: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected 401 for reuse of callback token, got %d", resp.StatusCode)
	}
}

func TestIntegration_E2EPRTask(t *testing.T) {
	remoteDir := setupRemoteRepo(t)
	defer os.RemoveAll(remoteDir) //nolint:errcheck

	agentPath := setupMockAgent(t, true)
	defer os.RemoveAll(filepath.Dir(agentPath)) //nolint:errcheck

	ghSrv := mockGitHubServer(t)
	defer ghSrv.Close()

	agentHome, err := os.MkdirTemp("", "agent-home-*")
	if err != nil {
		t.Fatalf("failed to create temp agent home: %v", err)
	}
	defer os.RemoveAll(agentHome) //nolint:errcheck

	t.Setenv("AGENT_HOME_DIR", agentHome)
	t.Setenv("GIT_REMOTE_URL", remoteDir)
	t.Setenv("AGENT_BIN", agentPath)
	t.Setenv("GITHUB_API_URL", ghSrv.URL+"/")

	env := setupTestEnv(t, nil)

	env.ghClient.SetIssueComments(2, []*github.IssueComment{{Author: "user", Body: "initial comment"}}, false)

	// Webhook request payload (labeled event)
	payload := map[string]interface{}{
		"action": "labeled",
		"issue": map[string]interface{}{
			"number": 2,
			"title":  "PR Title",
			"body":   "PR Body",
		},
		"label": map[string]interface{}{
			"name": "cloud-agent",
		},
		"sender": map[string]interface{}{
			"login": "user",
			"id":    100,
		},
		"repository": map[string]interface{}{
			"name": "testrepo",
			"owner": map[string]interface{}{
				"login": "testowner",
			},
		},
	}
	bodyBytes, _ := json.Marshal(payload)

	cbDone := make(chan struct{})
	env.onSubmitTask = func(req sandbox.TaskRequest) {
		defer close(cbDone)
		// Instantiate real sandbox runner inside mock sandbox
		runner := sandbox.NewRunner(
			req.TaskName,
			req.CallbackToken,
			req.GitHubToken,
			req.RepoOwner,
			req.RepoName,
			req.TaskOwner,
			req.TaskOwnerEmail,
			req.TaskType,
			req.Prompt,
			req.IssueNumber,
		)
		exitCode, err := runner.Run(env.ctx)
		if err != nil || exitCode != 0 {
			t.Errorf("Runner.Run failed inside test: exitCode %d, err %v", exitCode, err)
		}
	}

	// 1. Post to Webhook
	resp, err := http.Post(env.listenerURL+"/webhook", "application/json", bytes.NewBuffer(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to post webhook: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d", resp.StatusCode)
	}

	// 2. Poll Dynamic Client for created task
	var taskName string
	err = pollWithTimeout(5*time.Second, 100*time.Millisecond, func() (bool, error) {
		list, err := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		if len(list.Items) == 1 {
			taskName = list.Items[0].GetName()
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for AgentTask creation: %v", err)
	}

	// 3. Keep reconciling the task
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-env.ctx.Done():
				return
			case <-ticker.C:
				_ = env.orchestrator.Reconcile(env.ctx, taskName)
			}
		}
	}()

	// 4. Wait for runner and callback
	select {
	case <-cbDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for runner execution")
	}

	// 5. Verify the task gets completed and deleted
	err = pollWithTimeout(5*time.Second, 100*time.Millisecond, func() (bool, error) {
		list, err := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		return len(list.Items) == 0, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for AgentTask deletion: %v", err)
	}

	// 6. Verify comments posted on GitHub mock
	comments := env.ghClient.GetComments()
	if len(comments) != 1 {
		t.Fatalf("Expected exactly 1 comment posted to GitHub, got %d", len(comments))
	}
	if comments[0].Body != "I have created a PR #42" {
		t.Errorf("Expected comment body 'I have created a PR #42', got %q", comments[0].Body)
	}

	// 7. Verify new branch created in remote repo
	cmd := exec.Command("git", "branch", "--list", "attribution-test-"+taskName)
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to list git branches: %v", err)
	}
	if !strings.Contains(string(out), "attribution-test-"+taskName) {
		t.Errorf("Expected branch to exist in remote repo")
	}
}

func TestIntegration_E2ECommentTask(t *testing.T) {
	remoteDir := setupRemoteRepo(t)
	defer os.RemoveAll(remoteDir) //nolint:errcheck

	agentPath := setupMockAgent(t, false)       // Comment task doesn't make git changes
	defer os.RemoveAll(filepath.Dir(agentPath)) //nolint:errcheck

	agentHome, err := os.MkdirTemp("", "agent-home-*")
	if err != nil {
		t.Fatalf("failed to create temp agent home: %v", err)
	}
	defer os.RemoveAll(agentHome) //nolint:errcheck

	t.Setenv("AGENT_HOME_DIR", agentHome)
	t.Setenv("GIT_REMOTE_URL", remoteDir)
	t.Setenv("AGENT_BIN", agentPath)

	env := setupTestEnv(t, nil)

	env.ghClient.SetIssueComments(3, []*github.IssueComment{{Author: "user", Body: "please help me @cloud-agent"}}, false)

	// Webhook request payload
	payload := map[string]interface{}{
		"action": "created",
		"issue": map[string]interface{}{
			"number": 3,
			"title":  "Comment Title",
			"body":   "Comment Body",
		},
		"comment": map[string]interface{}{
			"body": "please help me @cloud-agent",
			"user": map[string]interface{}{
				"login": "user",
				"id":    100,
			},
		},
		"sender": map[string]interface{}{
			"login": "user",
			"id":    100,
		},
		"repository": map[string]interface{}{
			"name": "testrepo",
			"owner": map[string]interface{}{
				"login": "testowner",
			},
		},
	}
	bodyBytes, _ := json.Marshal(payload)

	cbDone := make(chan struct{})
	env.onSubmitTask = func(req sandbox.TaskRequest) {
		defer close(cbDone)
		runner := sandbox.NewRunner(
			req.TaskName,
			req.CallbackToken,
			req.GitHubToken,
			req.RepoOwner,
			req.RepoName,
			req.TaskOwner,
			req.TaskOwnerEmail,
			req.TaskType,
			req.Prompt,
			req.IssueNumber,
		)
		exitCode, err := runner.Run(env.ctx)
		if err != nil || exitCode != 0 {
			t.Errorf("Runner.Run failed: exitCode %d, err %v", exitCode, err)
		}
	}

	// 1. Post webhook
	resp, err := http.Post(env.listenerURL+"/webhook", "application/json", bytes.NewBuffer(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to post webhook: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d", resp.StatusCode)
	}

	// 2. Poll Dynamic Client for created task
	var taskName string
	err = pollWithTimeout(5*time.Second, 100*time.Millisecond, func() (bool, error) {
		list, err := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		if len(list.Items) == 1 {
			taskName = list.Items[0].GetName()
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for AgentTask creation: %v", err)
	}

	// 3. Keep reconciling the task
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-env.ctx.Done():
				return
			case <-ticker.C:
				_ = env.orchestrator.Reconcile(env.ctx, taskName)
			}
		}
	}()

	// 4. Wait for runner and callback
	select {
	case <-cbDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for runner execution")
	}

	// 5. Verify the task gets completed and deleted
	err = pollWithTimeout(5*time.Second, 100*time.Millisecond, func() (bool, error) {
		list, err := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		return len(list.Items) == 0, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for AgentTask deletion: %v", err)
	}

	// 6. Verify comments posted on GitHub mock
	comments := env.ghClient.GetComments()
	if len(comments) != 1 {
		t.Fatalf("Expected exactly 1 comment posted to GitHub, got %d", len(comments))
	}
	if comments[0].Body != "Agent output message" {
		t.Errorf("Expected comment body 'Agent output message', got %q", comments[0].Body)
	}
}

func TestIntegration_LabelOnPRRejected(t *testing.T) {
	env := setupTestEnv(t, nil)

	// Webhook request payload (labeled event where issue.pull_request is NOT nil)
	payload := map[string]interface{}{
		"action": "labeled",
		"issue": map[string]interface{}{
			"number": 4,
			"title":  "PR Label Title",
			"body":   "PR Label Body",
			"pull_request": map[string]interface{}{
				"url": "http://api.github.com/pulls/4",
			},
		},
		"label": map[string]interface{}{
			"name": "cloud-agent",
		},
		"sender": map[string]interface{}{
			"login": "user",
			"id":    100,
		},
		"repository": map[string]interface{}{
			"name": "testrepo",
			"owner": map[string]interface{}{
				"login": "testowner",
			},
		},
	}
	bodyBytes, _ := json.Marshal(payload)

	resp, err := http.Post(env.listenerURL+"/webhook", "application/json", bytes.NewBuffer(bodyBytes))
	if err != nil {
		t.Fatalf("Failed to post webhook: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	// Verify no task is created
	time.Sleep(500 * time.Millisecond)
	list, err := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list agent tasks: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("Expected no task to be created, got %d", len(list.Items))
	}

	// Verify GitHub client posted rejection comment
	comments := env.ghClient.GetComments()
	if len(comments) != 1 {
		t.Fatalf("Expected exactly 1 comment posted to GitHub, got %d", len(comments))
	}
	if comments[0].Body != "Adding label on a PR is not supported." {
		t.Errorf("Expected 'Adding label on a PR is not supported.', got %q", comments[0].Body)
	}
}

func TestIntegration_WebhookSignatureValidation(t *testing.T) {
	secret := []byte("secret-key")
	env := setupTestEnv(t, secret)

	env.ghClient.SetIssueComments(5, []*github.IssueComment{{Author: "user", Body: "Hello @cloud-agent"}}, false)

	payload := map[string]interface{}{
		"action": "created",
		"issue": map[string]interface{}{
			"number": 5,
			"title":  "Hello Title",
			"body":   "Hello Body",
		},
		"comment": map[string]interface{}{
			"body": "Hello @cloud-agent",
			"user": map[string]interface{}{
				"login": "user",
				"id":    100,
			},
		},
		"sender": map[string]interface{}{
			"login": "user",
			"id":    100,
		},
		"repository": map[string]interface{}{
			"name": "testrepo",
			"owner": map[string]interface{}{
				"login": "testowner",
			},
		},
	}
	bodyBytes, _ := json.Marshal(payload)

	// 1. Without Signature Header
	req, _ := http.NewRequest(http.MethodPost, env.listenerURL+"/webhook", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", resp.StatusCode)
	}

	// 2. With Invalid Signature Header
	req, _ = http.NewRequest(http.MethodPost, env.listenerURL+"/webhook", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", "sha256=invalidmac")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", resp.StatusCode)
	}

	// 3. With Valid Signature Header
	mac := hmac.New(sha256.New, secret)
	mac.Write(bodyBytes)
	expectedMAC := mac.Sum(nil)
	signatureHeader := "sha256=" + hex.EncodeToString(expectedMAC)

	req, _ = http.NewRequest(http.MethodPost, env.listenerURL+"/webhook", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", signatureHeader)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", resp.StatusCode)
	}
}

func TestIntegration_GenerateTokenEndpoint(t *testing.T) {
	env := setupTestEnv(t, nil)

	// 1. Call for non-existent task
	url := fmt.Sprintf("%s/task/non-existent-task/tokens", env.listenerURL)
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", resp.StatusCode)
	}

	// 2. Call for existing task
	task := &v1alpha1.AgentTask{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudagent.mayurhalai.github.com/v1alpha1",
			Kind:       "AgentTask",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-token-test",
			Namespace: env.namespace,
		},
		Spec: v1alpha1.AgentTaskSpec{
			Prompt:          "test prompt",
			SandboxTemplate: "template",
			RepoOwner:       "owner",
			RepoName:        "repo",
			IssueNumber:     5,
			TaskType:        "comment",
		},
	}
	uTask, _ := v1alpha1.ToUnstructured(task)
	_, _ = env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Create(context.Background(), uTask, metav1.CreateOptions{})

	url = fmt.Sprintf("%s/task/%s/tokens", env.listenerURL, task.Name)
	resp, err = http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	var tokResp webhook.TokenResponse
	err = json.NewDecoder(resp.Body).Decode(&tokResp)
	if err != nil {
		t.Fatalf("Failed to decode token response: %v", err)
	}

	if tokResp.GitHubToken != "mock-github-installation-token" {
		t.Errorf("Expected 'mock-github-installation-token', got %q", tokResp.GitHubToken)
	}
	if tokResp.CallbackToken == "" {
		t.Errorf("Expected non-empty CallbackToken")
	}

	// Verify callback token is in token store
	exists, err := env.tokenStore.VerifyToken(context.Background(), task.Name, tokResp.CallbackToken)
	if err != nil {
		t.Fatalf("VerifyToken failed: %v", err)
	}
	if !exists {
		t.Errorf("CallbackToken was not saved in TokenStore")
	}
}

func TestIntegration_OrchestratorRetries(t *testing.T) {
	// Set AGENT_RETRY_COUNT to 2
	t.Setenv("AGENT_RETRY_COUNT", "2")

	env := setupTestEnv(t, nil)

	// Create an AgentTask CRD
	task := &v1alpha1.AgentTask{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudagent.mayurhalai.github.com/v1alpha1",
			Kind:       "AgentTask",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-retry-test",
			Namespace: env.namespace,
		},
		Spec: v1alpha1.AgentTaskSpec{
			Prompt:          "test prompt",
			SandboxTemplate: "template",
			RepoOwner:       "owner",
			RepoName:        "repo",
			IssueNumber:     10,
			TaskType:        "comment",
		},
		Status: v1alpha1.AgentTaskStatus{
			State: v1alpha1.StatePending,
		},
	}
	uTask, _ := v1alpha1.ToUnstructured(task)
	_, _ = env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Create(context.Background(), uTask, metav1.CreateOptions{})

	// 1. Reconcile Pending -> Started
	err := env.orchestrator.Reconcile(context.Background(), task.Name)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Fetch task and check state is Started
	uObj, _ := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Get(context.Background(), task.Name, metav1.GetOptions{})
	tTask, _ := v1alpha1.FromUnstructured(uObj)
	if tTask.Status.State != v1alpha1.StateStarted {
		t.Fatalf("Expected StateStarted, got %s", tTask.Status.State)
	}

	// Capture when it tries to submit.
	// Since onSubmitTask is not configured to fail the pod immediately, we can let it transition to Running.
	runningDone := make(chan struct{})
	env.onSubmitTask = func(req sandbox.TaskRequest) {
		close(runningDone)
	}

	// Reconcile Started -> spawns runSandboxSubmission
	_ = env.orchestrator.Reconcile(context.Background(), task.Name)

	// Wait for task to transition to Running
	select {
	case <-runningDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for submission")
	}

	// Ensure State is Running
	err = pollWithTimeout(3*time.Second, 100*time.Millisecond, func() (bool, error) {
		uObj, _ := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Get(context.Background(), task.Name, metav1.GetOptions{})
		tTask, _ = v1alpha1.FromUnstructured(uObj)
		return tTask.Status.State == v1alpha1.StateRunning, nil
	})
	if err != nil {
		t.Fatalf("Failed to transition task to Running state: %v", err)
	}

	// 2. Simulate Pod failure: get the pod, and update its status to terminated with exit code 1
	podName := tTask.Status.PodName
	pod, err := env.k8sClient.CoreV1().Pods(env.namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to find pod %s: %v", podName, err)
	}

	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name: "agent",
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 1,
				},
			},
		},
	}
	_, _ = env.k8sClient.CoreV1().Pods(env.namespace).UpdateStatus(context.Background(), pod, metav1.UpdateOptions{})

	// Reconcile Running -> Failed (Attempt 1 / retry count 1)
	err = env.orchestrator.Reconcile(context.Background(), task.Name)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	// Reconcile Failed -> Started
	_ = env.orchestrator.Reconcile(context.Background(), task.Name)

	// Reconcile loop in Orchestrator handles StateFailed -> transitions back to StateStarted and increments Retries to 1.
	uObj, _ = env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Get(context.Background(), task.Name, metav1.GetOptions{})
	tTask, _ = v1alpha1.FromUnstructured(uObj)
	if tTask.Status.State != v1alpha1.StateStarted || tTask.Status.Retries != 1 {
		t.Fatalf("Expected StateStarted and Retries = 1, got State=%s Retries=%d", tTask.Status.State, tTask.Status.Retries)
	}

	// Trigger second run
	runningDone2 := make(chan struct{})
	env.onSubmitTask = func(req sandbox.TaskRequest) {
		close(runningDone2)
	}

	// Reconcile Started -> spawns runSandboxSubmission
	_ = env.orchestrator.Reconcile(context.Background(), task.Name)

	select {
	case <-runningDone2:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for submission 2")
	}

	// Wait for State to become Running again
	err = pollWithTimeout(3*time.Second, 100*time.Millisecond, func() (bool, error) {
		uObj, _ := env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Get(context.Background(), task.Name, metav1.GetOptions{})
		tTask, _ = v1alpha1.FromUnstructured(uObj)
		return tTask.Status.State == v1alpha1.StateRunning, nil
	})
	if err != nil {
		t.Fatalf("Failed to transition task to Running state on retry 1: %v", err)
	}

	// Simulate second Pod failure
	podName2 := tTask.Status.PodName
	pod2, _ := env.k8sClient.CoreV1().Pods(env.namespace).Get(context.Background(), podName2, metav1.GetOptions{})
	pod2.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name: "agent",
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 1,
				},
			},
		},
	}
	_, _ = env.k8sClient.CoreV1().Pods(env.namespace).UpdateStatus(context.Background(), pod2, metav1.UpdateOptions{})

	// Reconcile Running -> Failed (Attempt 2 / retry count 2)
	_ = env.orchestrator.Reconcile(context.Background(), task.Name)
	// Reconcile Failed -> Started
	_ = env.orchestrator.Reconcile(context.Background(), task.Name)

	// Reconcile loop in Orchestrator handles StateFailed -> transitions back to StateStarted and increments Retries to 2.
	uObj, _ = env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Get(context.Background(), task.Name, metav1.GetOptions{})
	tTask, _ = v1alpha1.FromUnstructured(uObj)
	if tTask.Status.State != v1alpha1.StateStarted || tTask.Status.Retries != 2 {
		t.Fatalf("Expected StateStarted and Retries = 2, got State=%s Retries=%d", tTask.Status.State, tTask.Status.Retries)
	}

	// Trigger third run
	runningDone3 := make(chan struct{})
	env.onSubmitTask = func(req sandbox.TaskRequest) {
		close(runningDone3)
	}

	_ = env.orchestrator.Reconcile(context.Background(), task.Name)

	select {
	case <-runningDone3:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for submission 3")
	}

	// Wait for State to become Running again
	err = pollWithTimeout(3*time.Second, 100*time.Millisecond, func() (bool, error) {
		uObj, _ = env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Get(context.Background(), task.Name, metav1.GetOptions{})
		tTask, _ = v1alpha1.FromUnstructured(uObj)
		return tTask.Status.State == v1alpha1.StateRunning, nil
	})
	if err != nil {
		t.Fatalf("Failed to transition task to Running state on retry 2: %v", err)
	}

	// Simulate third Pod failure
	podName3 := tTask.Status.PodName
	pod3, _ := env.k8sClient.CoreV1().Pods(env.namespace).Get(context.Background(), podName3, metav1.GetOptions{})
	pod3.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name: "agent",
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 1,
				},
			},
		},
	}
	_, _ = env.k8sClient.CoreV1().Pods(env.namespace).UpdateStatus(context.Background(), pod3, metav1.UpdateOptions{})

	// Reconcile Running -> Failed
	_ = env.orchestrator.Reconcile(context.Background(), task.Name)

	// Since Retries == 2 and AGENT_RETRY_COUNT == 2, it should remain in StateFailed and not retry.
	uObj, _ = env.dynClient.Resource(agentTaskGVR).Namespace(env.namespace).Get(context.Background(), task.Name, metav1.GetOptions{})
	tTask, _ = v1alpha1.FromUnstructured(uObj)
	if tTask.Status.State != v1alpha1.StateFailed {
		t.Errorf("Expected StateFailed after exhausting all retries, got %s", tTask.Status.State)
	}
	if tTask.Status.Retries != 2 {
		t.Errorf("Expected Retries to remain 2, got %d", tTask.Status.Retries)
	}
}

func pollWithTimeout(timeout, interval time.Duration, condition func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, err := condition()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("timeout exceeded")
}
