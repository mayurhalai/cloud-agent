package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	agentsclientset "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned"
	agentsv1alpha1 "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/typed/api/v1alpha1"
	extensionsclientset "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/typed/api/v1alpha1"
	extv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
)

const PodNameAnnotation = "agents.x-k8s.io/pod-name"

// K8sHelper encapsulates all Kubernetes API interactions for sandbox lifecycle management.
type K8sHelper struct {
	AgentsClient     agentsv1alpha1.AgentsV1alpha1Interface
	ExtensionsClient extensionsv1alpha1.ExtensionsV1alpha1Interface
	DynamicClient    dynamic.Interface
	CoreClient       corev1client.CoreV1Interface
	Log              logr.Logger
}

// NewK8sHelper creates a K8sHelper by loading kubeconfig and constructing required clientsets.
func NewK8sHelper(config *rest.Config, log logr.Logger) (*K8sHelper, error) {
	agentsCS, err := agentsclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to create agents clientset: %w", err)
	}

	extensionsCS, err := extensionsclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to create extensions clientset: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to create dynamic client: %w", err)
	}

	coreCS, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to create core clientset: %w", err)
	}

	return &K8sHelper{
		AgentsClient:     agentsCS.AgentsV1alpha1(),
		ExtensionsClient: extensionsCS.ExtensionsV1alpha1(),
		DynamicClient:    dynClient,
		CoreClient:       coreCS.CoreV1(),
		Log:              log,
	}, nil
}

// Options configures a Sandbox instance.
type Options struct {
	APIURL    string
	K8sHelper *K8sHelper
}

// Client manages sandbox lifecycles.
type Client struct {
	opts Options
}

// NewClient creates a Client with shared configuration.
func NewClient(opts Options) (*Client, error) {
	return &Client{opts: opts}, nil
}

// CreateSandbox provisions a new sandbox and returns a managed handle.
func (c *Client) CreateSandbox(ctx context.Context, template, namespace string) (*Sandbox, error) {
	if template == "" {
		return nil, fmt.Errorf("sandbox: template name is required")
	}
	if namespace == "" {
		namespace = "default"
	}

	claim := &extv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "sandbox-claim-",
			Namespace:    namespace,
		},
		Spec: extv1alpha1.SandboxClaimSpec{
			TemplateRef: extv1alpha1.SandboxTemplateRef{
				Name: template,
			},
		},
	}

	created, err := c.opts.K8sHelper.ExtensionsClient.SandboxClaims(namespace).Create(ctx, claim, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to create SandboxClaim: %w", err)
	}

	cleanup := func() {
		delCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.opts.K8sHelper.ExtensionsClient.SandboxClaims(namespace).Delete(delCtx, created.Name, metav1.DeleteOptions{})
	}

	var sandboxName string
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for sandboxName == "" {
		select {
		case <-ctx.Done():
			cleanup()
			return nil, fmt.Errorf("sandbox: timeout waiting for sandbox name resolution: %w", ctx.Err())
		case <-ticker.C:
			claimObj, err := c.opts.K8sHelper.ExtensionsClient.SandboxClaims(namespace).Get(ctx, created.Name, metav1.GetOptions{})
			if err != nil {
				cleanup()
				return nil, fmt.Errorf("sandbox: failed to get SandboxClaim: %w", err)
			}
			if claimObj.Status.SandboxStatus.Name != "" {
				sandboxName = claimObj.Status.SandboxStatus.Name
			}
		}
	}

	var podName string
	var sbObjIPs []string
	for podName == "" {
		select {
		case <-ctx.Done():
			cleanup()
			return nil, fmt.Errorf("sandbox: timeout waiting for sandbox readiness: %w", ctx.Err())
		case <-ticker.C:
			sbObj, err := c.opts.K8sHelper.AgentsClient.Sandboxes(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
			if err != nil {
				cleanup()
				return nil, fmt.Errorf("sandbox: failed to get Sandbox: %w", err)
			}
			isReady := false
			for _, cond := range sbObj.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
					isReady = true
					break
				}
			}
			if isReady {
				sbObjIPs = sbObj.Status.PodIPs
				if sbObj.Annotations != nil {
					podName = sbObj.Annotations[PodNameAnnotation]
				}
				if podName == "" {
					podName = sbObj.Name
				}
			}
		}
	}

	var podIP string
	pod, err := c.opts.K8sHelper.CoreClient.Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err == nil {
		podIP = pod.Status.PodIP
	} else if len(sbObjIPs) > 0 {
		podIP = sbObjIPs[0]
	}

	return &Sandbox{
		client:      c,
		claimName:   created.Name,
		sandboxName: sandboxName,
		podName:     podName,
		namespace:   namespace,
		podIP:       podIP,
	}, nil
}

// Sandbox represents a managed sandbox instance.
type Sandbox struct {
	client      *Client
	claimName   string
	sandboxName string
	podName     string
	namespace   string
	podIP       string
}

// PodName returns the pod name.
func (s *Sandbox) PodName() string {
	return s.podName
}

// SandboxName returns the sandbox name.
func (s *Sandbox) SandboxName() string {
	return s.sandboxName
}

// ClaimName returns the claim name.
func (s *Sandbox) ClaimName() string {
	return s.claimName
}

// Namespace returns the namespace.
func (s *Sandbox) Namespace() string {
	return s.namespace
}

// Close deletes the SandboxClaim to clean up resources.
func (s *Sandbox) Close(ctx context.Context) error {
	if s.claimName == "" {
		return nil
	}
	err := s.client.opts.K8sHelper.ExtensionsClient.SandboxClaims(s.namespace).Delete(ctx, s.claimName, metav1.DeleteOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("sandbox: failed to delete claim %s: %w", s.claimName, err)
	}
	return nil
}

// SubmitTask delivers the task payload to the Sandbox Server either directly or via sandbox-router.
func (s *Sandbox) SubmitTask(ctx context.Context, req *TaskRequest) error {
	payloadBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal task request: %w", err)
	}

	var targetURL string
	var useRouter bool
	if testURL := os.Getenv("TEST_SANDBOX_API_URL"); testURL != "" {
		targetURL = strings.TrimSuffix(testURL, "/") + "/task"
	} else if s.client.opts.APIURL != "" {
		targetURL = strings.TrimSuffix(s.client.opts.APIURL, "/") + "/task"
		useRouter = true
	} else {
		return fmt.Errorf("neither TEST_SANDBOX_API_URL nor client APIURL is configured")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if useRouter {
		httpReq.Header.Set("X-Sandbox-ID", s.sandboxName)
		httpReq.Header.Set("X-Sandbox-Namespace", s.namespace)
		httpReq.Header.Set("X-Sandbox-Port", "8888")
		httpReq.Header.Set("X-Sandbox-Pod-IP", s.podIP)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send task request to %s: %w", targetURL, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("task execution returned unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
