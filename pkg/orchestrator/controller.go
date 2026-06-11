package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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
	k8sClient     kubernetes.Interface
	dynClient     dynamic.Interface
	namespace     string
	sbClient      *sandbox.Client
	maxRetries    int // maximum number of times to retry the agent task
	activeTasks   map[string]context.CancelFunc
	activeTasksMu sync.Mutex
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
		k8sClient:   k8sClient,
		dynClient:   dynClient,
		sbClient:    sbClient,
		namespace:   namespace,
		maxRetries:  maxRetries,
		activeTasks: make(map[string]context.CancelFunc),
	}
}

// Reconcile performs a single synchronization step for the given task.
func (o *Orchestrator) Reconcile(ctx context.Context, taskName string) error {
	uTask, err := o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get AgentTask: %v", err)
	}

	task, err := v1alpha1.FromUnstructured(uTask)
	if err != nil {
		return fmt.Errorf("failed to parse AgentTask: %v", err)
	}

	state := task.Status.State
	if state == "" {
		state = v1alpha1.StatePending
		if err := o.updateTaskState(ctx, task, v1alpha1.StatePending); err != nil {
			return fmt.Errorf("failed to initialize task state to Pending: %v", err)
		}
	}

	switch state {
	case v1alpha1.StatePending:
		err = o.validateTask(task)
		if err != nil {
			log.Printf("Validation failed for task %s: %v", task.Name, err)
			o.recordEvent(ctx, task, "Warning", "ValidationFailed", err.Error())
			return nil
		}
		// Transition task to Started
		if err := o.updateTaskState(ctx, task, v1alpha1.StateStarted); err != nil {
			return fmt.Errorf("failed to update task state to Started: %v", err)
		}
		return nil

	case v1alpha1.StateStarted:
		o.activeTasksMu.Lock()
		_, active := o.activeTasks[task.Name]
		o.activeTasksMu.Unlock()
		if !active {
			runCtx, cancel := context.WithCancel(context.Background())
			o.activeTasksMu.Lock()
			o.activeTasks[task.Name] = cancel
			o.activeTasksMu.Unlock()

			go o.runSandboxSubmission(runCtx, task.Name)
		}
		return nil

	case v1alpha1.StateRunning:
		if task.Status.PodName == "" {
			return nil
		}
		pod, err := o.k8sClient.CoreV1().Pods(o.namespace).Get(ctx, task.Status.PodName, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				log.Printf("Pod %s not found for running task %s", task.Status.PodName, task.Name)
				o.recordEvent(ctx, task, "Warning", "PodNotFound", fmt.Sprintf("Sandbox pod %s was not found", task.Status.PodName))
				if err := o.updateTaskState(ctx, task, v1alpha1.StateFailed); err != nil {
					return fmt.Errorf("failed to update task status to Failed: %v", err)
				}
				return nil
			}
			return fmt.Errorf("failed to get pod %s: %v", task.Status.PodName, err)
		}

		terminated := false
		var exitCode int32
		for _, status := range pod.Status.ContainerStatuses {
			if status.State.Terminated != nil {
				terminated = true
				exitCode = status.State.Terminated.ExitCode
				break
			}
		}

		if terminated {
			if exitCode == 0 {
				log.Printf("Pod %s terminated successfully with exit code 0", task.Status.PodName)
				if err := o.updateTaskState(ctx, task, v1alpha1.StateCompleted); err != nil {
					return fmt.Errorf("failed to update task %s state to Completed: %v", task.Name, err)
				}
			} else {
				log.Printf("Pod %s terminated with exit code %d", task.Status.PodName, exitCode)
				o.recordEvent(ctx, task, "Warning", "PodTerminatedWithFailure", fmt.Sprintf("Sandbox pod terminated with exit code %d", exitCode))
				if err := o.updateTaskState(ctx, task, v1alpha1.StateFailed); err != nil {
					return fmt.Errorf("failed to update task %s state to Failed: %v", task.Name, err)
				}
			}
		} else if pod.Status.Phase == corev1.PodSucceeded {
			log.Printf("Pod %s succeeded", task.Status.PodName)
			if err := o.updateTaskState(ctx, task, v1alpha1.StateCompleted); err != nil {
				return fmt.Errorf("failed to update task %s state to Completed: %v", task.Name, err)
			}
		} else if pod.Status.Phase == corev1.PodFailed {
			log.Printf("Pod %s failed", task.Status.PodName)
			o.recordEvent(ctx, task, "Warning", "PodFailed", "Sandbox pod failed")
			if err := o.updateTaskState(ctx, task, v1alpha1.StateFailed); err != nil {
				return fmt.Errorf("failed to update task %s state to Failed: %v", task.Name, err)
			}
		}
		return nil

	case v1alpha1.StateCompleted:
		if task.Status.SandboxClaimName != "" {
			sb := o.sbClient.GetSandbox(task.Status.SandboxClaimName, task.Status.SandboxName, task.Status.PodName, task.Namespace)
			if err := sb.Close(ctx); err != nil {
				log.Printf("Failed to close Sandbox for task %s: %v", task.Name, err)
				o.recordEvent(ctx, task, "Warning", "SandboxCloseFailed", err.Error())
				return fmt.Errorf("failed to close sandbox: %v", err)
			}
		}
		log.Printf("Deleting completed task %s", task.Name)
		err := o.dynClient.Resource(agentTaskGVR).Namespace(task.Namespace).Delete(ctx, task.Name, metav1.DeleteOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete completed task: %v", err)
		}
		return nil

	case v1alpha1.StateFailed:
		if task.Status.SandboxClaimName != "" {
			sb := o.sbClient.GetSandbox(task.Status.SandboxClaimName, task.Status.SandboxName, task.Status.PodName, task.Namespace)
			if err := sb.Close(ctx); err != nil {
				log.Printf("Failed to close Sandbox for failed task %s: %v", task.Name, err)
				o.recordEvent(ctx, task, "Warning", "SandboxCloseFailed", err.Error())
				return fmt.Errorf("failed to close sandbox: %v", err)
			}
			task.Status.SandboxClaimName = ""
			task.Status.SandboxName = ""
			task.Status.PodName = ""
			if err := o.updateTaskStatus(ctx, task); err != nil {
				return fmt.Errorf("failed to update status after closing sandbox: %v", err)
			}
		}

		if task.Status.Retries < o.maxRetries {
			task.Status.Retries++
			task.Status.State = v1alpha1.StateStarted
			log.Printf("Retrying failed task %s (attempt %d/%d)", task.Name, task.Status.Retries, o.maxRetries)
			if err := o.updateTaskStatus(ctx, task); err != nil {
				return fmt.Errorf("failed to transition task to Started state: %v", err)
			}
		} else {
			log.Printf("Task %s failed after exhausting all retries", task.Name)
			o.recordEvent(ctx, task, "Warning", "RetriesExhausted", fmt.Sprintf("Task failed after exhausting %d retries", o.maxRetries))
		}
		return nil
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

func (o *Orchestrator) recordEvent(ctx context.Context, task *v1alpha1.AgentTask, eventType, reason, message string) {
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: task.Name + "-event-",
			Namespace:    task.Namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "cloudagent.mayurhalai.github.com/v1alpha1",
			Kind:       "AgentTask",
			Name:       task.Name,
			Namespace:  task.Namespace,
			UID:        task.UID,
		},
		Reason:  reason,
		Message: message,
		Type:    eventType,
		Source: corev1.EventSource{
			Component: "cloud-agent-orchestrator",
		},
		FirstTimestamp: metav1.Now(),
		LastTimestamp:  metav1.Now(),
		Count:          1,
	}
	_, err := o.k8sClient.CoreV1().Events(task.Namespace).Create(ctx, event, metav1.CreateOptions{})
	if err != nil {
		log.Printf("Failed to record event for task %s: %v", task.Name, err)
	}
}

func (o *Orchestrator) runSandboxSubmission(ctx context.Context, taskName string) {
	defer func() {
		o.activeTasksMu.Lock()
		if cancel, ok := o.activeTasks[taskName]; ok {
			cancel()
			delete(o.activeTasks, taskName)
		}
		o.activeTasksMu.Unlock()
	}()

	backoff := 1 * time.Second
	maxBackoff := 10 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		uTask, err := o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).Get(ctx, taskName, metav1.GetOptions{})
		if err != nil {
			log.Printf("Failed to get task %s in submission loop: %v", taskName, err)
			return
		}
		task, err := v1alpha1.FromUnstructured(uTask)
		if err != nil {
			log.Printf("Failed to parse task %s in submission loop: %v", taskName, err)
			return
		}

		if task.Status.State != v1alpha1.StateStarted {
			return
		}

		sandboxClaimName, sandboxName, podName, err := o.trySubmitToSandbox(ctx, task)
		if err == nil {
			task.Status.State = v1alpha1.StateRunning
			task.Status.SandboxClaimName = sandboxClaimName
			task.Status.SandboxName = sandboxName
			task.Status.PodName = podName
			if err := o.updateTaskStatus(ctx, task); err != nil {
				log.Printf("Failed to update task %s status to Running: %v", taskName, err)
			}
			return
		}

		log.Printf("Sandbox submission failed for task %s: %v", taskName, err)
		o.recordEvent(ctx, task, "Warning", "SandboxSubmissionFailed", err.Error())

		jitter := time.Duration(rand.Int63n(int64(backoff)))
		sleepTime := backoff + jitter
		if sleepTime > maxBackoff {
			sleepTime = maxBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleepTime):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (o *Orchestrator) trySubmitToSandbox(ctx context.Context, task *v1alpha1.AgentTask) (string, string, string, error) {
	sb, err := o.sbClient.CreateSandbox(ctx, task.Spec.SandboxTemplate, o.namespace)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to create sandbox: %w", err)
	}

	closeFailed := false
	defer func() {
		if err != nil {
			if closeErr := sb.Close(ctx); closeErr != nil {
				closeFailed = true
				log.Printf("Failed to close sandbox during cleanup: %v", closeErr)
			}
		}
	}()

	tokenURL := o.getTokenURL(task.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to create token request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to request tokens: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return "", "", "", fmt.Errorf("token API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		return "", "", "", fmt.Errorf("failed to decode token response: %w", err)
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

	if err := sb.SubmitTask(ctx, taskReq); err != nil {
		if closeFailed {
			o.recordEvent(ctx, task, "Warning", "SandboxCloseFailed", "Failed to close sandbox during cleanup")
		}
		return "", "", "", fmt.Errorf("failed to execute task inside sandbox: %w", err)
	}

	return sb.ClaimName(), sb.SandboxName(), sb.PodName(), nil
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

// Start starts the orchestrator controller loop watching for AgentTasks and Pods
func (o *Orchestrator) Start(ctx context.Context) error {
	watcher, err := o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	defer watcher.Stop()

	podWatcher, err := o.k8sClient.CoreV1().Pods(o.namespace).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	defer podWatcher.Stop()

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
		case event, ok := <-podWatcher.ResultChan():
			if !ok {
				return fmt.Errorf("pod watch channel closed")
			}
			if event.Type == watch.Added || event.Type == watch.Modified || event.Type == watch.Deleted {
				pod, ok := event.Object.(*corev1.Pod)
				if !ok {
					continue
				}
				list, err := o.dynClient.Resource(agentTaskGVR).Namespace(o.namespace).List(ctx, metav1.ListOptions{})
				if err != nil {
					log.Printf("Failed to list AgentTasks for pod event: %v", err)
					continue
				}
				for _, item := range list.Items {
					task, err := v1alpha1.FromUnstructured(&item)
					if err != nil {
						continue
					}
					if task.Status.PodName == pod.Name && task.Status.State == v1alpha1.StateRunning {
						if err := o.Reconcile(ctx, task.Name); err != nil {
							log.Printf("Reconcile error for %s on pod event: %v", task.Name, err)
						}
					}
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
