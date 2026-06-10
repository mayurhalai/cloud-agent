package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var agentTaskGVR = schema.GroupVersionResource{
	Group:    "cloudagent.mayurhalai.github.com",
	Version:  "v1alpha1",
	Resource: "agenttasks",
}

type Orchestrator struct {
	k8sClient  kubernetes.Interface
	dynClient  dynamic.Interface
	namespace  string
	sbClient   *sandbox.Client
	maxRetries int // maximum number of times to retry the agent task
}

func NewOrchestrator(k8sClient kubernetes.Interface, dynClient dynamic.Interface, sbClient *sandbox.Client, namespace string) *Orchestrator {
	// Set Agent max retries
	maxRetries := 0
	if retryStr := os.Getenv("AGENT_RETRY_COUNT"); retryStr != "" {
		if val, err := strconv.Atoi(retryStr); err == nil && val >= 0 {
			maxRetries = val
		} else {
			log.Printf("Invalid AGENT_RETRY_COUNT value '%s', defaulting to 0 retries", retryStr)
		}
	}
	return &Orchestrator{
		k8sClient:  k8sClient,
		dynClient:  dynClient,
		sbClient:   sbClient,
		namespace:  namespace,
		maxRetries: maxRetries,
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
		// Transition task to Pending
		if err := o.updateTaskState(ctx, task, v1alpha1.StatePending); err != nil {
			return fmt.Errorf("failed to update task state to Pending: %v", err)
		}
		err = o.validateTask(task)
		if err != nil {
			log.Printf("Validation failed for task %s: %v", task.Name, err)
			// TODO: update task with event
			return nil
		}
		// Transition task to Running
		if err := o.updateTaskState(ctx, task, v1alpha1.StateRunning); err != nil {
			return fmt.Errorf("failed to update task state to Running: %v", err)
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

func (o *Orchestrator) validateTask(task *v1alpha1.AgentTask) error {
	// Validate required fields
	var missing []string
	if task.Spec.Prompt == "" {
		missing = append(missing, "prompt")
	}
	if task.Spec.RepoOwner == "" {
		missing = append(missing, "repoOwner")
	}
	if task.Spec.RepoName == "" {
		missing = append(missing, "repoName")
	}
	if task.Spec.SandboxTemplate == "" {
		missing = append(missing, "sandboxTemplate")
	}
	if task.Spec.IssueNumber == 0 {
		missing = append(missing, "issueNumber")
	}
	if task.Spec.TaskType == "" {
		missing = append(missing, "taskType")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %v", missing)
	}
	return nil
}

type tokenResponse struct {
	GitHubToken   string `json:"gitHubToken"`
	CallbackToken string `json:"callbackToken"`
}

func (o *Orchestrator) getTokenURL(taskID string) string {
	urlVal := o.getWebhookListenerURL()
	return fmt.Sprintf("%s/task/%s/tokens", strings.TrimSuffix(urlVal, "/"), taskID)
}

func (o *Orchestrator) executeTask(task *v1alpha1.AgentTask) {
	ctx := context.Background()
	log.Printf("Starting execution for task %s", task.Name)

	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		task.Status.Retries = attempt
		if attempt > 0 {
			log.Printf("Retrying task %s (attempt %d/%d)", task.Name, attempt, o.maxRetries)
			if err := o.updateTaskStatus(ctx, task); err != nil {
				log.Printf("Failed to update retry status for task %s: %v", task.Name, err)
			}
		}

		agentFailed, err := o.executeAttempt(ctx, task)
		if err == nil {
			log.Printf("Task %s submitted successfully", task.Name)
			return
		}

		if !agentFailed {
			log.Printf("Task %s failed due to infrastructure error (will not retry): %v", task.Name, err)
			_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
			return
		}

		log.Printf("Task %s execution failed (agent failure): %v", task.Name, err)
	}

	log.Printf("Task %s failed after exhausting all retries", task.Name)
	_ = o.updateTaskState(ctx, task, v1alpha1.StateFailed)
}

func (o *Orchestrator) executeAttempt(ctx context.Context, task *v1alpha1.AgentTask) (bool, error) {
	sb, err := o.sbClient.CreateSandbox(ctx, task.Spec.SandboxTemplate, o.namespace)
	if err != nil {
		return false, fmt.Errorf("failed to create Sandbox instance: %v", err)
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
		return false, fmt.Errorf("failed to create token request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to request tokens: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return false, fmt.Errorf("token API returned non-OK status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		return false, fmt.Errorf("failed to decode token response: %v", err)
	}

	// Transition task to StateRunning after receiving tokens
	if err := o.updateTaskState(ctx, task, v1alpha1.StateRunning); err != nil {
		return false, fmt.Errorf("failed to update task state to Running: %v", err)
	}

	taskReq := &sandbox.TaskRequest{
		TaskName:       task.Name,
		CallbackURL:    o.getWebhookListenerURL() + "/callback",
		CallbackToken:  tokResp.CallbackToken,
		GitHubToken:    tokResp.GitHubToken,
		RepoOwner:      task.Spec.RepoOwner,
		RepoName:       task.Spec.RepoName,
		TaskOwner:      task.Spec.TaskOwner,
		TaskOwnerEmail: task.Spec.TaskOwnerEmail,
		WorkspaceDir:   getEnvWithDefault("WORKSPACE_DIR", "/workspace"),
		TaskType:       task.Spec.TaskType,
		AgentBinary:    getEnvWithDefault("AGENT_BINARY", "opencode"),
		Prompt:         task.Spec.Prompt,
	}

	log.Printf("Executing sandbox-server inside sandbox for task %s", task.Name)
	if err := sb.SubmitTask(ctx, taskReq); err != nil {
		return true, fmt.Errorf("failed to execute task inside sandbox: %v", err)
	}

	return false, nil
}

func (o *Orchestrator) updateTaskState(ctx context.Context, task *v1alpha1.AgentTask, state v1alpha1.AgentTaskState) error {
	task.Status.State = state
	return o.updateTaskStatus(ctx, task)
}

func (o *Orchestrator) updateTaskStatus(ctx context.Context, task *v1alpha1.AgentTask) error {
	uTask, err := v1alpha1.ToUnstructured(task)
	if err != nil {
		return err
	}
	updatedUTask, err := o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).UpdateStatus(ctx, uTask, metav1.UpdateOptions{})
	if err != nil {
		updatedUTask, err = o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).Update(ctx, uTask, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}
	updatedTask, err := v1alpha1.FromUnstructured(updatedUTask)
	if err != nil {
		return err
	}
	*task = *updatedTask
	return nil
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
			if event.Type == watch.Added || event.Type == watch.Modified {
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

func (o *Orchestrator) getWebhookListenerURL() string {
	return getEnvWithDefault("WEBHOOK_LISTENER_URL", fmt.Sprintf("http://webhook-listener.%s.svc.cluster.local:8080", o.namespace))
}

func getEnvWithDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
