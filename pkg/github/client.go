package github

import "sync"

type Client interface {
	PostComment(owner, repo string, issueNumber int, body string) error
}

type MockClient struct {
	mu       sync.Mutex
	Comments []MockComment
}

type MockComment struct {
	Owner       string
	Repo        string
	IssueNumber int
	Body        string
}

func (m *MockClient) PostComment(owner, repo string, issueNumber int, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Comments = append(m.Comments, MockComment{
		Owner:       owner,
		Repo:        repo,
		IssueNumber: issueNumber,
		Body:        body,
	})
	return nil
}

func (m *MockClient) GetComments() []MockComment {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]MockComment, len(m.Comments))
	copy(res, m.Comments)
	return res
}
