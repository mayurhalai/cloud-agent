package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

	// Verify the Volume mount config
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].Name != "callback-token-volume" {
		t.Errorf("Unexpected Pod volumes: %+v", pod.Spec.Volumes)
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
	token := string(secret.Data["token"])

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

	// Start Sandbox Runner pointing to local server callback
	runner := sandbox.NewRunner(task.Name, server.URL+"/callback", tokenFile, nil)
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Sandbox runner returned error: %v", err)
	}

	// 8. Verify the Callback completed successfully
	// - comment was posted
	comments := mockGh.GetComments()
	if len(comments) != 1 {
		t.Errorf("Expected 1 comment on GitHub, got %d", len(comments))
	} else {
		if comments[0].Body != "Hello World" {
			t.Errorf("Expected comment body 'Hello World', got '%s'", comments[0].Body)
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
