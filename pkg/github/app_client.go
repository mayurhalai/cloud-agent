package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v60/github"
)

// AppClient implements the Client interface for a real GitHub App.
type AppClient struct {
	appID          int64
	privateKeyPath string
	baseURL        string // non-exported, populated during unit tests
}

// NewAppClient instantiates a new AppClient with the given App ID and private key file path.
func NewAppClient(appID int64, privateKeyPath string) (*AppClient, error) {
	if appID == 0 {
		return nil, fmt.Errorf("appID cannot be zero")
	}
	if privateKeyPath == "" {
		return nil, fmt.Errorf("privateKeyPath cannot be empty")
	}
	return &AppClient{
		appID:          appID,
		privateKeyPath: privateKeyPath,
	}, nil
}

// getInstallationTransport creates and returns a ghinstallation.Transport for the repository's installation.
func (c *AppClient) getInstallationTransport(owner, repo string) (*ghinstallation.Transport, error) {
	tr := http.DefaultTransport
	atr, err := ghinstallation.NewAppsTransportKeyFromFile(tr, c.appID, c.privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create apps transport: %w", err)
	}

	if c.baseURL != "" {
		atr.BaseURL = c.baseURL
	}

	appClient := github.NewClient(&http.Client{Transport: atr})
	if c.baseURL != "" {
		parsedURL, err := url.Parse(c.baseURL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse base URL: %w", err)
		}
		appClient.BaseURL = parsedURL
		appClient.UploadURL = parsedURL
	}

	installation, _, err := appClient.Apps.FindRepositoryInstallation(context.Background(), owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to find repository installation for %s/%s: %w", owner, repo, err)
	}

	itr := ghinstallation.NewFromAppsTransport(atr, installation.GetID())
	if c.baseURL != "" {
		itr.BaseURL = c.baseURL
	}

	return itr, nil
}

// getInstallationClient retrieves an authenticated GitHub client for the repository's installation.
func (c *AppClient) getInstallationClient(owner, repo string) (*github.Client, error) {
	itr, err := c.getInstallationTransport(owner, repo)
	if err != nil {
		return nil, err
	}

	client := github.NewClient(&http.Client{Transport: itr})
	if c.baseURL != "" {
		parsedURL, err := url.Parse(c.baseURL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse base URL: %w", err)
		}
		client.BaseURL = parsedURL
		client.UploadURL = parsedURL
	}

	return client, nil
}

// PostComment posts a comment to the specified issue or pull request.
func (c *AppClient) PostComment(owner, repo string, issueNumber int, body string) error {
	client, err := c.getInstallationClient(owner, repo)
	if err != nil {
		return err
	}

	comment := &github.IssueComment{
		Body: github.String(body),
	}

	_, _, err = client.Issues.CreateComment(context.Background(), owner, repo, issueNumber, comment)
	if err != nil {
		return fmt.Errorf("failed to create comment: %w", err)
	}

	return nil
}

// GetFile downloads and returns the content of the specified file in the repository.
func (c *AppClient) GetFile(owner, repo, path string) ([]byte, error) {
	client, err := c.getInstallationClient(owner, repo)
	if err != nil {
		return nil, err
	}

	rc, _, err := client.Repositories.DownloadContents(context.Background(), owner, repo, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to download contents: %w", err)
	}
	defer func() {
		_ = rc.Close()
	}()

	content, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("failed to read downloaded contents: %w", err)
	}

	return content, nil
}

// MintInstallationToken retrieves a short-lived token for the repository's installation.
func (c *AppClient) MintInstallationToken(owner, repo string) (string, error) {
	itr, err := c.getInstallationTransport(owner, repo)
	if err != nil {
		return "", err
	}

	token, err := itr.Token(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to mint installation token: %w", err)
	}

	return token, nil
}

// GetIssueComments retrieves issue comments up to 30 and reports if there are more comments.
func (c *AppClient) GetIssueComments(ctx context.Context, owner, repo string, issueNumber int) ([]*IssueComment, bool, error) {
	client, err := c.getInstallationClient(owner, repo)
	if err != nil {
		return nil, false, err
	}

	opts := &github.IssueListCommentsOptions{
		ListOptions: github.ListOptions{
			PerPage: 30,
		},
	}

	comments, resp, err := client.Issues.ListComments(ctx, owner, repo, issueNumber, opts)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list comments: %w", err)
	}

	hasMore := resp.NextPage != 0
	var result []*IssueComment
	for _, comment := range comments {
		author := ""
		if comment.User != nil {
			author = comment.User.GetLogin()
		}
		result = append(result, &IssueComment{
			Author: author,
			Body:   comment.GetBody(),
		})
	}

	return result, hasMore, nil
}
