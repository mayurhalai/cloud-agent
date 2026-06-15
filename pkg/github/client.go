package github

import (
	"context"
	"fmt"
	"sync"
)

type IssueComment struct {
	Author string
	Body   string
}

type Client interface {
	PostComment(owner, repo string, issueNumber int, body string) error
	GetFile(owner, repo, path string) ([]byte, error)
	MintInstallationToken(owner, repo string) (string, error)
	GetIssueComments(ctx context.Context, owner, repo string, issueNumber int) ([]*IssueComment, bool, error)
}

type MockClient struct {
	mu               sync.Mutex
	Comments         []MockComment
	Files            map[string]map[string]map[string][]byte // owner -> repo -> path -> content
	MintedToken      string
	MintedTokenErr   error
	GetFileErr       error
	IssueComments    map[int][]*IssueComment
	IssueCommentsErr error
	HasMoreComments  bool
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

func (m *MockClient) GetFile(owner, repo, path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetFileErr != nil {
		return nil, m.GetFileErr
	}
	if m.Files == nil {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	repoFiles, ok := m.Files[owner]
	if !ok {
		return nil, fmt.Errorf("repository not found: %s/%s", owner, repo)
	}
	files, ok := repoFiles[repo]
	if !ok {
		return nil, fmt.Errorf("repository not found: %s/%s", owner, repo)
	}
	content, ok := files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	return content, nil
}

func (m *MockClient) MintInstallationToken(owner, repo string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.MintedTokenErr != nil {
		return "", m.MintedTokenErr
	}
	if m.MintedToken != "" {
		return m.MintedToken, nil
	}
	return "mock-github-installation-token", nil
}

func (m *MockClient) SetFile(owner, repo, path string, content []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Files == nil {
		m.Files = make(map[string]map[string]map[string][]byte)
	}
	if _, ok := m.Files[owner]; !ok {
		m.Files[owner] = make(map[string]map[string][]byte)
	}
	if _, ok := m.Files[owner][repo]; !ok {
		m.Files[owner][repo] = make(map[string][]byte)
	}
	m.Files[owner][repo][path] = content
}

func (m *MockClient) SetMintedToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.MintedToken = token
}

func (m *MockClient) GetIssueComments(ctx context.Context, owner, repo string, issueNumber int) ([]*IssueComment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.IssueCommentsErr != nil {
		return nil, false, m.IssueCommentsErr
	}
	comments, ok := m.IssueComments[issueNumber]
	if !ok {
		return nil, false, nil
	}
	return comments, m.HasMoreComments, nil
}

func (m *MockClient) SetIssueComments(issueNumber int, comments []*IssueComment, hasMore bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.IssueComments == nil {
		m.IssueComments = make(map[int][]*IssueComment)
	}
	m.IssueComments[issueNumber] = comments
	m.HasMoreComments = hasMore
}
