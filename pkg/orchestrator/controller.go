package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
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
		// Check if Pod already exists
		podName := fmt.Sprintf("sandbox-%s", task.Name)
		_, err := o.k8sClient.CoreV1().Pods(o.namespace).Get(ctx, podName, metav1.GetOptions{})
		if err == nil {
			// Pod already exists, update state to Running
			return o.updateTaskState(ctx, task, v1alpha1.StateRunning)
		}

		// Create sandbox Pod
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: o.namespace,
				Labels: map[string]string{
					"cloudagent.mayurhalai.github.com/task-id": task.Name,
					"app": "cloud-agent-sandbox",
				},
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{
					{
						Name:  "sandbox",
						Image: "cloud-agent-sandbox:latest",
						Env: []corev1.EnvVar{
							{
								Name:  "TASK_NAME",
								Value: task.Name,
							},
							{
								Name:  "PROMPT",
								Value: task.Spec.Prompt,
							},
							{
								Name:  "TASK_OWNER",
								Value: task.Spec.TaskOwner,
							},
							{
								Name:  "TASK_OWNER_EMAIL",
								Value: task.Spec.TaskOwnerEmail,
							},
							{
								Name:  "REPO_OWNER",
								Value: task.Annotations["cloudagent.mayurhalai.github.com/repo-owner"],
							},
							{
								Name:  "REPO_NAME",
								Value: task.Annotations["cloudagent.mayurhalai.github.com/repo-name"],
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "callback-token-volume",
								MountPath: "/etc/cloud-agent",
								ReadOnly:  true,
							},
							{
								Name:      "github-token-volume",
								MountPath: "/etc/github-token",
								ReadOnly:  true,
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "callback-token-volume",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: task.Spec.CallbackTokenSecretRef,
								Items: []corev1.KeyToPath{
									{
										Key:  "token",
										Path: "callback-token",
									},
								},
							},
						},
					},
					{
						Name: "github-token-volume",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: task.Spec.GitHubTokenSecretRef,
								Items: []corev1.KeyToPath{
									{
										Key:  "token",
										Path: "github-token",
									},
								},
							},
						},
					},
				},
			},
		}

		_, err = o.k8sClient.CoreV1().Pods(o.namespace).Create(ctx, pod, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create sandbox Pod: %v", err)
		}

		return o.updateTaskState(ctx, task, v1alpha1.StateRunning)

	case v1alpha1.StateCompleted:
		// Delete Pod
		podName := fmt.Sprintf("sandbox-%s", task.Name)
		err := o.k8sClient.CoreV1().Pods(o.namespace).Delete(ctx, podName, metav1.DeleteOptions{})
		if err != nil {
			// If Pod not found, that is fine
			if !strings.Contains(err.Error(), "not found") {
				return fmt.Errorf("failed to delete sandbox Pod: %v", err)
			}
		}
		// Mark state as Deleted
		return o.updateTaskState(ctx, task, v1alpha1.StateDeleted)
	}

	return nil
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
