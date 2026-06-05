package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/mayurhalai/cloud-agent/pkg/apis/cloudagent/v1alpha1"
	"github.com/mayurhalai/cloud-agent/pkg/github"
	yaml "gopkg.in/yaml.v3"
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
	k8sClient     kubernetes.Interface
	dynClient     dynamic.Interface
	ghClient      github.Client
	namespace     string
	webhookSecret []byte
	tokenStore    TokenStore
}

func NewListenerServer(k8sClient kubernetes.Interface, dynClient dynamic.Interface, ghClient github.Client, namespace string, webhookSecret []byte, tokenStore TokenStore) *ListenerServer {
	return &ListenerServer{
		k8sClient:     k8sClient,
		dynClient:     dynClient,
		ghClient:      ghClient,
		namespace:     namespace,
		webhookSecret: webhookSecret,
		tokenStore:    tokenStore,
	}
}

type GitHubWebhookEvent struct {
	Action string `json:"action"` // "created" or "labeled"
	Issue  struct {
		Number      int                    `json:"number"`
		Title       string                 `json:"title"`
		Body        string                 `json:"body"`
		PullRequest map[string]interface{} `json:"pull_request,omitempty"`
	} `json:"issue"`
	Comment *struct {
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
			ID    int64  `json:"id"`
		} `json:"user"`
	} `json:"comment,omitempty"`
	Label *struct {
		Name string `json:"name"`
	} `json:"label,omitempty"`
	Sender struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	} `json:"sender"`
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

	if strings.HasPrefix(u.Path, "/task/") && strings.HasSuffix(u.Path, "/tokens") {
		parts := strings.Split(u.Path, "/")
		if len(parts) == 4 && parts[1] == "task" && parts[3] == "tokens" {
			taskID := parts[2]
			s.handleGenerateTokens(w, r, taskID)
			return
		}
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

	if len(s.webhookSecret) > 0 {
		signature := r.Header.Get("X-Hub-Signature-256")
		if signature == "" {
			http.Error(w, "Missing X-Hub-Signature-256 header", http.StatusUnauthorized)
			return
		}

		if !validateSignature(s.webhookSecret, body, signature) {
			http.Error(w, "Invalid webhook signature", http.StatusUnauthorized)
			return
		}
	}

	var event GitHubWebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	var taskType string
	var prompt string
	var taskOwner string
	var taskOwnerEmail string

	repoOwner := event.Repository.Owner.Login
	repoName := event.Repository.Name

	switch event.Action {
	case "created":
		if event.Comment == nil {
			http.Error(w, "Missing comment object for created action", http.StatusBadRequest)
			return
		}
		taskType = "comment"
		prompt = event.Comment.Body
		taskOwner = event.Comment.User.Login
		taskOwnerEmail = fmt.Sprintf("%d+%s@users.noreply.github.com", event.Comment.User.ID, event.Comment.User.Login)
	case "labeled":
		if event.Label == nil || event.Label.Name != "cloud-agent" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Ignored non-cloud-agent label event"))
			return
		}
		// Check if it is a Pull Request
		if event.Issue.PullRequest != nil {
			err = s.ghClient.PostComment(repoOwner, repoName, event.Issue.Number, "Adding label on a PR is not supported.")
			if err != nil {
				http.Error(w, fmt.Sprintf("Failed to post comment to GitHub: %v", err), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Adding label on a PR is not supported."))
			return
		}
		taskType = "pr"
		if event.Issue.Body != "" {
			prompt = event.Issue.Title + "\n\n" + event.Issue.Body
		} else {
			prompt = event.Issue.Title
		}
		taskOwner = event.Sender.Login
		taskOwnerEmail = fmt.Sprintf("%d+%s@users.noreply.github.com", event.Sender.ID, event.Sender.Login)
	default:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Ignored unsupported action"))
		return
	}

	// Webhook Listener fetches `.cloud-agent.yaml` from the repository to read the `SandboxTemplate`
	var sandboxTemplate string

	systemAgent := os.Getenv("SYSTEM_AGENT")
	switch systemAgent {
	case "pi":
		sandboxTemplate = "pi-sandbox-template"
	case "opencode":
		sandboxTemplate = "opencode-sandbox-template"
	default:
		sandboxTemplate = "pi-sandbox-template"
	}

	configBytes, err := s.ghClient.GetFile(repoOwner, repoName, ".cloud-agent.yaml")
	if err == nil {
		var config struct {
			SandboxTemplate string `yaml:"sandboxTemplate"`
		}
		if err := yaml.Unmarshal(configBytes, &config); err == nil && config.SandboxTemplate != "" {
			sandboxTemplate = config.SandboxTemplate
		}
	}

	taskID := fmt.Sprintf("task-%d-%s", event.Issue.Number, generateRandomHex(4))

	// Parse issue comment/label event information to populate the AgentTask CRD
	agentTask := &v1alpha1.AgentTask{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudagent.mayurhalai.github.com/v1alpha1",
			Kind:       "AgentTask",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskID,
			Namespace: s.namespace,
		},
		Spec: v1alpha1.AgentTaskSpec{
			Prompt:                 prompt,
			SandboxTemplate:        sandboxTemplate,
			TaskOwner:              taskOwner,
			TaskOwnerEmail:         taskOwnerEmail,
			GitHubTokenSecretRef:   "",
			CallbackTokenSecretRef: "",
			RepoOwner:              repoOwner,
			RepoName:               repoName,
			IssueNumber:            event.Issue.Number,
			TaskType:               taskType,
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

	authHeader := r.Header.Get("Authorization")
	var callbackToken string
	if after, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		callbackToken = after
	} else if authHeader != "" {
		callbackToken = authHeader
	} else {
		callbackToken = req.CallbackToken
	}

	if callbackToken == "" {
		http.Error(w, "Missing callback token", http.StatusUnauthorized)
		return
	}

	// Verify token against TokenStore
	valid, err := s.tokenStore.VerifyToken(r.Context(), req.TaskName, callbackToken)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to verify callback token: %v", err), http.StatusInternalServerError)
		return
	}
	if !valid {
		http.Error(w, "Invalid callback token", http.StatusUnauthorized)
		return
	}

	// Invalidate/delete callback token from TokenStore
	err = s.tokenStore.DeleteToken(r.Context(), req.TaskName)
	if err != nil {
		http.Error(w, "Failed to invalidate callback token", http.StatusInternalServerError)
		return
	}

	// Retrieve repository details from spec
	repoOwner := task.Spec.RepoOwner
	repoName := task.Spec.RepoName
	issueNumber := task.Spec.IssueNumber

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

func validateSignature(secret []byte, payload []byte, signatureHeader string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	hexSig := strings.TrimPrefix(signatureHeader, prefix)
	sigBytes, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	expectedMAC := mac.Sum(nil)

	return hmac.Equal(sigBytes, expectedMAC)
}

type TokenResponse struct {
	GitHubToken   string `json:"gitHubToken"`
	CallbackToken string `json:"callbackToken"`
}

func (s *ListenerServer) handleGenerateTokens(w http.ResponseWriter, r *http.Request, taskID string) {
	// Fetch AgentTask
	uTask, err := s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).Get(r.Context(), taskID, metav1.GetOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("AgentTask %s not found", taskID), http.StatusNotFound)
		return
	}

	task, err := v1alpha1.FromUnstructured(uTask)
	if err != nil {
		http.Error(w, "Failed to parse AgentTask", http.StatusInternalServerError)
		return
	}

	// Retrieve repository details from spec
	repoOwner := task.Spec.RepoOwner
	repoName := task.Spec.RepoName
	if repoOwner == "" || repoName == "" {
		http.Error(w, "Missing repository owner or name in spec", http.StatusBadRequest)
		return
	}

	// Mint GitHub installation token using App private key
	githubToken, err := s.ghClient.MintInstallationToken(repoOwner, repoName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to mint github token: %v", err), http.StatusInternalServerError)
		return
	}

	// Generate Result Callback Token
	callbackToken := generateRandomHex(16)

	// Write Result Callback Token to Redis/TokenStore
	err = s.tokenStore.StoreToken(r.Context(), taskID, callbackToken)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to store callback token: %v", err), http.StatusInternalServerError)
		return
	}

	resp := TokenResponse{
		GitHubToken:   githubToken,
		CallbackToken: callbackToken,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
