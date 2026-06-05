package v1alpha1

import (
	"encoding/json"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type AgentTaskState string

const (
	StatePending   AgentTaskState = "Pending"
	StateStarted   AgentTaskState = "Started"
	StateRunning   AgentTaskState = "Running"
	StateCompleted AgentTaskState = "Completed"
	StateFailed    AgentTaskState = "Failed"
	StateDeleted   AgentTaskState = "Deleted"
)

type AgentTaskSpec struct {
	Prompt                 string `json:"prompt,omitempty"`
	SandboxTemplate        string `json:"sandboxTemplate,omitempty"`
	TaskOwner              string `json:"taskOwner,omitempty"`
	TaskOwnerEmail         string `json:"taskOwnerEmail,omitempty"`
	GitHubTokenSecretRef   string `json:"githubTokenSecretRef,omitempty"`
	CallbackTokenSecretRef string `json:"callbackTokenSecretRef,omitempty"`
	RepoOwner              string `json:"repoOwner,omitempty"`
	RepoName               string `json:"repoName,omitempty"`
	IssueNumber            int    `json:"issueNumber,omitempty"`
	TaskType               string `json:"taskType,omitempty"`
}

type AgentTaskStatus struct {
	State   AgentTaskState `json:"state,omitempty"`
	Retries int            `json:"retries,omitempty"`
}

type AgentTask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentTaskSpec   `json:"spec,omitempty"`
	Status AgentTaskStatus `json:"status,omitempty"`
}

// FromUnstructured converts an unstructured object to an AgentTask.
func FromUnstructured(u *unstructured.Unstructured) (*AgentTask, error) {
	var task AgentTask
	data, err := u.MarshalJSON()
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &task)
	return &task, err
}

// ToUnstructured converts an AgentTask to an unstructured object.
func ToUnstructured(task *AgentTask) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	var u unstructured.Unstructured
	err = u.UnmarshalJSON(data)
	if err != nil {
		return nil, err
	}
	// Ensure TypeMeta is set correctly
	u.SetAPIVersion("cloudagent.mayurhalai.github.com/v1alpha1")
	u.SetKind("AgentTask")
	return &u, nil
}
