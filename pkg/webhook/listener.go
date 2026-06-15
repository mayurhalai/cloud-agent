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
	"os"
	"strings"

	"github.com/gin-gonic/gin"
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

func NewListenerServer(k8sClient kubernetes.Interface, dynClient dynamic.Interface, ghClient github.Client, namespace string, webhookSecret []byte, tokenStore TokenStore) *gin.Engine {
	s := &ListenerServer{
		k8sClient:     k8sClient,
		dynClient:     dynClient,
		ghClient:      ghClient,
		namespace:     namespace,
		webhookSecret: webhookSecret,
		tokenStore:    tokenStore,
	}

	r := gin.Default()
	r.POST("/webhook", s.handleWebhook)
	r.POST("/callback", s.handleCallback)
	r.POST("/task/:taskID/tokens", s.handleGenerateTokens)
	return r
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

const (
	enhancedCommentPrompt = "\n\nPlease **answer** based on the above issue and comments."
	enhancedLabelPrompt   = "\n\nPlease **make changes** on the repository based on the above issue and comments."
)

func (s *ListenerServer) handleWebhook(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.String(http.StatusBadRequest, "Failed to read body")
		return
	}
	defer func() {
		_ = c.Request.Body.Close()
	}()

	if len(s.webhookSecret) > 0 {
		signature := c.GetHeader("X-Hub-Signature-256")
		if signature == "" {
			c.String(http.StatusUnauthorized, "Missing X-Hub-Signature-256 header")
			return
		}

		if !validateSignature(s.webhookSecret, body, signature) {
			c.String(http.StatusUnauthorized, "Invalid webhook signature")
			return
		}
	}

	var event GitHubWebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		c.String(http.StatusBadRequest, "Invalid JSON payload")
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
			c.String(http.StatusBadRequest, "Missing comment object for created action")
			return
		}
		botName := getAgentName()
		if !strings.Contains(strings.ToLower(event.Comment.Body), "@"+strings.ToLower(botName)) {
			c.String(http.StatusOK, "Ignored comment without mention")
			return
		}
		taskType = "comment"
		comments, hasMore, err := s.ghClient.GetIssueComments(c.Request.Context(), repoOwner, repoName, event.Issue.Number)
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to fetch issue comments: %v", err)
			return
		}
		if hasMore {
			_ = s.ghClient.PostComment(repoOwner, repoName, event.Issue.Number, "The agent does not support issues with more than 30 comments.")
			c.String(http.StatusOK, "The agent does not support issues with more than 30 comments.")
			return
		}
		prompt = formatPrompt(event.Issue.Title, event.Issue.Body, comments)
		// Enhance prompt
		prompt += enhancedCommentPrompt
		taskOwner = event.Comment.User.Login
		taskOwnerEmail = fmt.Sprintf("%d+%s@users.noreply.github.com", event.Comment.User.ID, event.Comment.User.Login)
	case "labeled":
		agentLabel := getAgentLabel()
		if event.Label == nil || event.Label.Name != agentLabel {
			c.String(http.StatusOK, fmt.Sprintf("Ignored non-%s label event", agentLabel))
			return
		}
		// Check if it is a Pull Request
		if event.Issue.PullRequest != nil {
			err = s.ghClient.PostComment(repoOwner, repoName, event.Issue.Number, "Adding label on a PR is not supported.")
			if err != nil {
				c.String(http.StatusInternalServerError, "Failed to post comment to GitHub: %v", err)
				return
			}
			c.String(http.StatusOK, "Adding label on a PR is not supported.")
			return
		}
		taskType = "pr"
		comments, hasMore, err := s.ghClient.GetIssueComments(c.Request.Context(), repoOwner, repoName, event.Issue.Number)
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to fetch issue comments: %v", err)
			return
		}
		if hasMore {
			_ = s.ghClient.PostComment(repoOwner, repoName, event.Issue.Number, "The agent does not support issues with more than 30 comments.")
			c.String(http.StatusOK, "The agent does not support issues with more than 30 comments.")
			return
		}
		prompt = formatPrompt(event.Issue.Title, event.Issue.Body, comments)
		// Enhance prompt
		prompt += enhancedLabelPrompt
		taskOwner = event.Sender.Login
		taskOwnerEmail = fmt.Sprintf("%d+%s@users.noreply.github.com", event.Sender.ID, event.Sender.Login)
	default:
		c.String(http.StatusOK, "Ignored unsupported action")
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
			Prompt:          prompt,
			SandboxTemplate: sandboxTemplate,
			TaskOwner:       taskOwner,
			TaskOwnerEmail:  taskOwnerEmail,
			RepoOwner:       repoOwner,
			RepoName:        repoName,
			IssueNumber:     event.Issue.Number,
			TaskType:        taskType,
		},
		Status: v1alpha1.AgentTaskStatus{
			State: v1alpha1.StatePending,
		},
	}

	uTask, err := v1alpha1.ToUnstructured(agentTask)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to serialize AgentTask: %v", err)
		return
	}

	_, err = s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).Create(c.Request.Context(), uTask, metav1.CreateOptions{})
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to create AgentTask CRD: %v", err)
		return
	}

	c.String(http.StatusCreated, "Created task %s", taskID)
}

func getAgentLabel() string {
	agentLabel := os.Getenv("AGENT_LABEL")
	if agentLabel == "" {
		agentLabel = "cloud-agent"
	}
	return agentLabel
}

func getAgentName() string {
	agentName := os.Getenv("AGENT_GITHUB_NAME")
	if agentName == "" {
		agentName = "cloud-agent"
	}
	return agentName
}

func formatPrompt(title, body string, comments []*github.IssueComment) string {
	var sb strings.Builder
	sb.WriteString("Issue Title: ")
	sb.WriteString(title)
	sb.WriteString("\n\nIssue Body:\n")
	sb.WriteString(body)
	sb.WriteString("\n\nComments:\n")
	for _, c := range comments {
		_, _ = fmt.Fprintf(&sb, "- [%s]: %s\n", c.Author, c.Body)
	}
	return sb.String()
}

func (s *ListenerServer) handleCallback(c *gin.Context) {
	var req CallbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.String(http.StatusBadRequest, "Invalid JSON payload: %v", err)
		return
	}

	// Fetch AgentTask
	uTask, err := s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).Get(c.Request.Context(), req.TaskName, metav1.GetOptions{})
	if err != nil {
		c.String(http.StatusNotFound, "AgentTask %s not found", req.TaskName)
		return
	}

	task, err := v1alpha1.FromUnstructured(uTask)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to parse AgentTask")
		return
	}

	authHeader := c.GetHeader("Authorization")
	var callbackToken string
	if after, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		callbackToken = after
	} else if authHeader != "" {
		callbackToken = authHeader
	} else {
		callbackToken = req.CallbackToken
	}

	if callbackToken == "" {
		c.String(http.StatusUnauthorized, "Missing callback token")
		return
	}

	// Verify token against TokenStore
	valid, err := s.tokenStore.VerifyToken(c.Request.Context(), req.TaskName, callbackToken)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to verify callback token: %v", err)
		return
	}
	if !valid {
		c.String(http.StatusUnauthorized, "Invalid callback token")
		return
	}

	// Invalidate/delete callback token from TokenStore
	err = s.tokenStore.DeleteToken(c.Request.Context(), req.TaskName)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to invalidate callback token")
		return
	}

	// Retrieve repository details from spec
	repoOwner := task.Spec.RepoOwner
	repoName := task.Spec.RepoName
	issueNumber := task.Spec.IssueNumber

	// Post response comment back to GitHub
	err = s.ghClient.PostComment(repoOwner, repoName, issueNumber, req.Response)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to post comment to GitHub: %v", err)
		return
	}

	// Update status to Completed
	task.Status.State = v1alpha1.StateCompleted
	uTaskUpdated, err := v1alpha1.ToUnstructured(task)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to serialize updated AgentTask: %v", err)
		return
	}

	_, err = s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).UpdateStatus(c.Request.Context(), uTaskUpdated, metav1.UpdateOptions{})
	if err != nil {
		// Try full update if UpdateStatus fails
		_, err = s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).Update(c.Request.Context(), uTaskUpdated, metav1.UpdateOptions{})
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to update AgentTask state to Completed: %v", err)
			return
		}
	}

	c.String(http.StatusOK, "Callback successful, task marked as completed")
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

func (s *ListenerServer) handleGenerateTokens(c *gin.Context) {
	taskID := c.Param("taskID")

	// Fetch AgentTask
	uTask, err := s.dynClient.Resource(agentTaskGVR).Namespace(s.namespace).Get(c.Request.Context(), taskID, metav1.GetOptions{})
	if err != nil {
		c.String(http.StatusNotFound, "AgentTask %s not found", taskID)
		return
	}

	task, err := v1alpha1.FromUnstructured(uTask)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to parse AgentTask")
		return
	}

	// Retrieve repository details from spec
	repoOwner := task.Spec.RepoOwner
	repoName := task.Spec.RepoName
	if repoOwner == "" || repoName == "" {
		c.String(http.StatusBadRequest, "Missing repository owner or name in spec")
		return
	}

	// Mint GitHub installation token using App private key
	githubToken, err := s.ghClient.MintInstallationToken(repoOwner, repoName)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to mint github token: %v", err)
		return
	}

	// Generate Result Callback Token
	callbackToken := generateRandomHex(16)

	// Write Result Callback Token to Redis/TokenStore
	err = s.tokenStore.StoreToken(c.Request.Context(), taskID, callbackToken)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to store callback token: %v", err)
		return
	}

	resp := TokenResponse{
		GitHubToken:   githubToken,
		CallbackToken: callbackToken,
	}

	c.JSON(http.StatusOK, resp)
}
