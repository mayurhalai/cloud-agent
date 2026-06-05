package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	agentsandbox "sigs.k8s.io/agent-sandbox/clients/go/sandbox"
)

var agentTaskGVR = schema.GroupVersionResource{
	Group:    "cloudagent.mayurhalai.github.com",
	Version:  "v1alpha1",
	Resource: "agenttasks",
}

type Orchestrator struct {
	k8sClient kubernetes.Interface
	dynClient dynamic.Interface
	namespace string
	K8sHelper *agentsandbox.K8sHelper
}

func NewOrchestrator(k8sClient kubernetes.Interface, dynClient dynamic.Interface, namespace string) *Orchestrator {
	return &Orchestrator{
		k8sClient: k8sClient,
		dynClient: dynClient,
		namespace: namespace,
	}
}

// Reconcile performs a single synchronization step for the given task.
func (o *Orchestrator) Reconcile(ctx context.Context, taskName string) error {
	uTask, err := o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get AgentTask: %v", err)
	}

	task, err := v1alpha1.FromUnstructured(uTask)
	if err != nil {
		return fmt.Errorf("failed to parse AgentTask: %v", err)
	}

	state := task.Status.State
	if state == "" {
		state = v1alpha1.StatePending
	}

	switch state {
	case v1alpha1.StatePending:
		// Transition task to StateStarted before requesting tokens
		if err := o.updateTaskState(ctx, task, v1alpha1.StateStarted); err != nil {
			return fmt.Errorf("failed to update task state to Started: %v", err)
		}

		// Spawn background goroutine to execute task
		go o.executeTask(task)
		return nil

	case v1alpha1.StateCompleted:
		// Mark state as Deleted
		return o.updateTaskState(ctx, task, v1alpha1.StateDeleted)
	}

	return nil
}

type tokenResponse struct {
	GitHubToken   string `json:"gitHubToken"`
	CallbackToken string `json:"callbackToken"`
}

func (o *Orchestrator) getTokenURL(taskID string) string {
	if urlVal := os.Getenv("WEBHOOK_LISTENER_URL"); urlVal != "" {
		return fmt.Sprintf("%s/task/%s/tokens", strings.TrimSuffix(urlVal, "/"), taskID)
	}
	callbackURL := getEnvWithDefault("CALLBACK_URL", "http://webhook-listener/callback")
	if u, err := url.Parse(callbackURL); err == nil {
		u.Path = fmt.Sprintf("/task/%s/tokens", taskID)
		return u.String()
	}
	return fmt.Sprintf("http://webhook-listener/task/%s/tokens", taskID)
}

func (o *Orchestrator) executeTask(task *v1alpha1.AgentTask) {
	ctx := context.Background()
	log.Printf("Starting execution for task %s", task.Name)

	opts := agentsandbox.Options{
		TemplateName: task.Spec.SandboxTemplate,
		Namespace:    o.namespace,
	}
	if o.K8sHelper != nil {
		opts.K8sHelper = o.K8sHelper
	}
	if testAPIURL := os.Getenv("TEST_SANDBOX_API_URL"); testAPIURL != "" {
		opts.APIURL = testAPIURL
	}

	sb, err := agentsandbox.New(ctx, opts)
	if err != nil {
		log.Printf("Failed to create Sandbox instance for task %s: %v", task.Name, err)
		_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
		return
	}

	if err := sb.Open(ctx); err != nil {
		log.Printf("Failed to open Sandbox for task %s: %v", task.Name, err)
		_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
		return
	}
	defer func() {
		if err := sb.Close(ctx); err != nil {
			log.Printf("Failed to close Sandbox for task %s: %v", task.Name, err)
		}
	}()

	// Call Webhook Listener JIT API to fetch tokens
	tokenURL := o.getTokenURL(task.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, nil)
	if err != nil {
		log.Printf("Failed to create token request for task %s: %v", task.Name, err)
		_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to request tokens for task %s: %v", task.Name, err)
		_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		log.Printf("Token API returned non-OK status %d for task %s: %s", resp.StatusCode, task.Name, string(bodyBytes))
		_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
		return
	}

	var tokResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		log.Printf("Failed to decode token response for task %s: %v", task.Name, err)
		_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
		return
	}

	// Transition task to StateRunning after receiving tokens
	if err := o.updateTaskState(ctx, task, v1alpha1.StateRunning); err != nil {
		log.Printf("Failed to update task state to Running for task %s: %v", task.Name, err)
		return
	}

	// Determine target URL for task dispatch
	var targetURL string
	if testURL := os.Getenv("TEST_SANDBOX_API_URL"); testURL != "" {
		targetURL = testURL + "/task"
	} else {
		pod, err := o.k8sClient.CoreV1().Pods(o.namespace).Get(ctx, sb.PodName(), metav1.GetOptions{})
		if err != nil {
			log.Printf("Failed to get sandbox pod %s for task %s: %v", sb.PodName(), task.Name, err)
			_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
			return
		}
		targetURL = fmt.Sprintf("http://%s:8080/task", pod.Status.PodIP)
	}

	taskReq := &sandbox.TaskRequest{
		TaskName:          task.Name,
		CallbackURL:       getEnvWithDefault("CALLBACK_URL", "http://webhook-listener/callback"),
		CallbackTokenPath: "callback-token",
		GitHubTokenPath:   "github-token",
		CallbackToken:     tokResp.CallbackToken,
		GitHubToken:       tokResp.GitHubToken,
		RepoOwner:         task.Spec.RepoOwner,
		RepoName:          task.Spec.RepoName,
		TaskOwner:         task.Spec.TaskOwner,
		TaskOwnerEmail:    task.Spec.TaskOwnerEmail,
		WorkspaceDir:      getEnvWithDefault("WORKSPACE_DIR", "/workspace"),
		TaskType:          task.Spec.TaskType,
		AgentBinary:       getEnvWithDefault("AGENT_BINARY", "opencode"),
		Prompt:            task.Spec.Prompt,
	}

	log.Printf("Executing sandbox-server inside sandbox for task %s", task.Name)
	if err := sandbox.ExecuteTask(ctx, targetURL, taskReq); err != nil {
		log.Printf("Failed to execute task %s inside sandbox: %v", task.Name, err)
		_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
		return
	}

	log.Printf("Task %s completed successfully in sandbox", task.Name)
	_ = o.updateTaskState(ctx, task, v1alpha1.StateDeleted)
}

func (o *Orchestrator) updateTaskState(ctx context.Context, task *v1alpha1.AgentTask, state v1alpha1.AgentTaskState) error {
	task.Status.State = state
	uTask, err := v1alpha1.ToUnstructured(task)
	if err != nil {
		return err
	}
	_, err = o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).UpdateStatus(ctx, uTask, metav1.UpdateOptions{})
	if err != nil {
		_, err = o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).Update(ctx, uTask, metav1.UpdateOptions{})
	}
	return err
}

// Start starts the orchestrator controller loop watching for AgentTasks
func (o *Orchestrator) Start(ctx context.Context) error {
	watcher, err := o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}
			if event.Type == "ADDED" || event.Type == "MODIFIED" {
				uObj, ok := event.Object.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				taskName := uObj.GetName()
				if err := o.Reconcile(ctx, taskName); err != nil {
					log.Printf("Reconcile error for %s: %v", taskName, err)
				}
			}
		}
	}
}

func getEnvWithDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
