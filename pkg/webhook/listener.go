package webhook

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	"github.com/mayurhalai/cloud-agent/pkg/github"
	yaml "gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var agentTaskGVR = schema.GroupVersionResource{
	Group:    "cloudagent.mayurhalai.github.com",
	Version:  "v1alpha1",
	Resource: "agenttasks",
}

type ListenerServer struct {
	k8sClient kubernetes.Interface
	dynClient dynamic.Interface
	ghClient  github.Client
	namespace string
}

func NewListenerServer(k8sClient kubernetes.Interface, dynClient dynamic.Interface, ghClient github.Client, namespace string) *ListenerServer {
	return &ListenerServer{
		k8sClient: k8sClient,
		dynClient: dynClient,
		ghClient:  ghClient,
		namespace: namespace,
	}
}

type GitHubIssueCommentEvent struct {
	Action string `json:"action"`
	Issue  struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
			ID    int64  `json:"id"`
		} `json:"user"`
	} `json:"comment"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

type CallbackRequest struct {
	TaskName      string `json:"taskName"`
	CallbackToken string `json:"callbackToken"`
	Response      string `json:"response"`
}

func (s *ListenerServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	u, err := url.Parse(r.URL.Path)
	if err != nil {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	switch u.Path {
	case "/webhook":
		s.handleWebhook(w, r)
		return
	case "/callback":
		s.handleCallback(w, r)
		return
	}

	http.Error(w, "Not found", http.StatusNotFound)
}

func (s *ListenerServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer func() {
		_ = r.Body.Close()
	}()

	var event GitHubIssueCommentEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	// Basic validation: only handle comment creation
	if event.Action != "created" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Ignored non-create action"))
		return
	}

	// Webhook Listener fetches `.cloud-agent.yaml` from the repository to read the `SandboxTemplate`
	sandboxTemplate := "default"
	repoOwner := event.Repository.Owner.Login
	repoName := event.Repository.Name
	configBytes, err := s.ghClient.GetFile(repoOwner, repoName, ".cloud-agent.yaml")
	if err == nil {
		var config struct {
			SandboxTemplate string `yaml:"sandboxTemplate"`
		}
		if err := yaml.Unmarshal(configBytes, &config); err == nil && config.SandboxTemplate != "" {
			sandboxTemplate = config.SandboxTemplate
		}
	}

	// Listener mints a GitHub installation token using its App private key
	githubToken, err := s.ghClient.MintInstallationToken(repoOwner, repoName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to mint github token: %v", err), http.StatusInternalServerError)
		return
	}

	taskID := fmt.Sprintf("task-%d-%s", event.Issue.Number, generateRandomHex(4))

	callbackToken := generateRandomHex(16)

	// Create Callback Token Secret
	cbSecretName := fmt.Sprintf("%s-callback-token", taskID)
	cbSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cbSecretName,
			Namespace: s.namespace,
			Labels: map[string]string{
				"cloudagent.mayurhalai.github.com/task-id": taskID,
			},
		},
		StringData: map[string]string{
			"token": callbackToken,
		},
	}
	_, err = s.k8sClient.CoreV1().Secrets(s.namespace).Create(r.Context(), cbSecret, metav1.CreateOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create callback secret: %v", err), http.StatusInternalServerError)
		return
	}

	// Create GitHub Token Secret
	ghSecretName := fmt.Sprintf("%s-github-token", taskID)
	ghSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ghSecretName,
			Namespace: s.namespace,
			Labels: map[string]string{
				"cloudagent.mayurhalai.github.com/task-id": taskID,
			},
		},
		StringData: map[string]string{
			"token": githubToken,
		},
	}
	_, err = s.k8sClient.CoreV1().Secrets(s.namespace).Create(r.Context(), ghSecret, metav1.CreateOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create github token secret: %v", err), http.StatusInternalServerError)
		return
	}

	// Parse issue comment event information to populate the AgentTask CRD
	agentTask := &v1alpha1.AgentTask{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudagent.mayurhalai.github.com/v1alpha1",
			Kind:       "AgentTask",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskID,
			Namespace: s.namespace,
			Annotations: map[string]string{
				"cloudagent.mayurhalai.github.com/issue-number": fmt.Sprintf("%d", event.Issue.Number),
				"cloudagent.mayurhalai.github.com/repo-owner":   event.Repository.Owner.Login,
				"cloudagent.mayurhalai.github.com/repo-name":    event.Repository.Name,
			},
		},
		Spec: v1alpha1.AgentTaskSpec{
			Prompt:                 event.Comment.Body,
			SandboxTemplate:        sandboxTemplate,
			TaskOwner:              event.Comment.User.Login,
			TaskOwnerEmail:         fmt.Sprintf("%d+%s@users.noreply.github.com", event.Comment.User.ID, event.Comment.User.Login),
			GitHubTokenSecretRef:   ghSecretName,
			CallbackTokenSecretRef: cbSecretName,
		},
		Status: v1alpha1.AgentTaskStatus{
			State: v1alpha1.StatePending,
		},
	}

	uTask, err := v1alpha1.ToUnstructured(agentTask)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to serialize AgentTask: %v", err), http.StatusInternalServerError)
		return
	}

	_, err = s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).Create(r.Context(), uTask, metav1.CreateOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create AgentTask CRD: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(w, "Created task %s", taskID)
}

func (s *ListenerServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer func() {
		_ = r.Body.Close()
	}()

	var req CallbackRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	// Fetch AgentTask
	uTask, err := s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).Get(r.Context(), req.TaskName, metav1.GetOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("AgentTask %s not found", req.TaskName), http.StatusNotFound)
		return
	}

	task, err := v1alpha1.FromUnstructured(uTask)
	if err != nil {
		http.Error(w, "Failed to parse AgentTask", http.StatusInternalServerError)
		return
	}

	// Fetch Callback Token Secret to verify
	cbSecret, err := s.k8sClient.CoreV1().Secrets(s.namespace).Get(r.Context(), task.Spec.CallbackTokenSecretRef, metav1.GetOptions{})
	if err != nil {
		http.Error(w, "Callback token secret not found or already verified", http.StatusUnauthorized)
		return
	}

	authHeader := r.Header.Get("Authorization")
	var callbackToken string
	if strings.HasPrefix(authHeader, "Bearer ") {
		callbackToken = strings.TrimPrefix(authHeader, "Bearer ")
	} else if authHeader != "" {
		callbackToken = authHeader
	} else {
		callbackToken = req.CallbackToken
	}

	if callbackToken == "" {
		http.Error(w, "Missing callback token", http.StatusUnauthorized)
		return
	}

	var storedToken string
	if len(cbSecret.Data["token"]) > 0 {
		storedToken = string(cbSecret.Data["token"])
	} else {
		storedToken = cbSecret.StringData["token"]
	}

	if storedToken != callbackToken {
		http.Error(w, "Invalid callback token", http.StatusUnauthorized)
		return
	}

	// Invalidate/delete callback token Secret per ADR 0008
	err = s.k8sClient.CoreV1().Secrets(s.namespace).Delete(r.Context(), task.Spec.CallbackTokenSecretRef, metav1.DeleteOptions{})
	if err != nil {
		http.Error(w, "Failed to invalidate callback token secret", http.StatusInternalServerError)
		return
	}

	// Retrieve repository details from annotations
	repoOwner := task.Annotations["cloudagent.mayurhalai.github.com/repo-owner"]
	repoName := task.Annotations["cloudagent.mayurhalai.github.com/repo-name"]
	var issueNumber int
	_, err = fmt.Sscanf(task.Annotations["cloudagent.mayurhalai.github.com/issue-number"], "%d", &issueNumber)
	if err != nil {
		http.Error(w, "Invalid issue number annotation", http.StatusInternalServerError)
		return
	}

	// Post response comment back to GitHub
	err = s.ghClient.PostComment(repoOwner, repoName, issueNumber, req.Response)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to post comment to GitHub: %v", err), http.StatusInternalServerError)
		return
	}

	// Update status to Completed
	task.Status.State = v1alpha1.StateCompleted
	uTaskUpdated, err := v1alpha1.ToUnstructured(task)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to serialize updated AgentTask: %v", err), http.StatusInternalServerError)
		return
	}

	_, err = s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).UpdateStatus(r.Context(), uTaskUpdated, metav1.UpdateOptions{})
	if err != nil {
		// Try full update if UpdateStatus fails
		_, err = s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).Update(r.Context(), uTaskUpdated, metav1.UpdateOptions{})
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to update AgentTask state to Completed: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Callback successful, task marked as completed"))
}

func generateRandomHex(n int) string {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "abcd"
	}
	return hex.EncodeToString(bytes)
}
