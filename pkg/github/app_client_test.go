package github

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAppClient(t *testing.T) {
	// 1. Generate RSA key for JWT signing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	pemBytes := pem.EncodeToMemory(pemBlock)

	tmpFile, err := os.CreateTemp("", "github-app-key-*.pem")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	if _, err := tmpFile.Write(pemBytes); err != nil {
		t.Fatalf("failed to write key to temp file: %v", err)
	}
	_ = tmpFile.Close()

	// 2. Setup mock HTTP server
	var requestedPaths []string
	var lastMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("[MOCK SERVER] %s %s", r.Method, r.URL.Path)
		requestedPaths = append(requestedPaths, r.URL.Path)
		lastMethod = r.Method

		switch r.URL.Path {
		case "/repos/my-owner/my-repo/installation":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":123456}`))

		case "/app/installations/123456/access_tokens":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"ghs_mockedtoken123456"}`))

		case "/repos/my-owner/my-repo/contents/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Return a JSON array representing the directory listing
			downloadURL := fmt.Sprintf("http://%s/raw/my-file.txt", r.Host)
			responseJSON := fmt.Sprintf(`[
				{
					"type": "file",
					"name": "my-file.txt",
					"path": "my-file.txt",
					"size": 28,
					"download_url": "%s"
				}
			]`, downloadURL)
			_, _ = w.Write([]byte(responseJSON))

		case "/raw/my-file.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("hello world from mock github"))

		case "/repos/my-owner/my-repo/issues/42/comments":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 999, "body": "test comment"}`))

		default:
			t.Errorf("Unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// 3. Initialize AppClient with base URL pointing to mock server
	client, err := NewAppClient(12345, tmpFile.Name())
	if err != nil {
		t.Fatalf("NewAppClient failed: %v", err)
	}
	// Append trailing slash to server URL to satisfy go-github BaseURL requirement
	client.baseURL = server.URL + "/"

	// 4. Test MintInstallationToken
	t.Run("MintInstallationToken", func(t *testing.T) {
		requestedPaths = nil
		token, err := client.MintInstallationToken("my-owner", "my-repo")
		if err != nil {
			t.Fatalf("MintInstallationToken failed: %v", err)
		}
		if token != "ghs_mockedtoken123456" {
			t.Errorf("expected token ghs_mockedtoken123456, got %q", token)
		}
		if len(requestedPaths) < 2 {
			t.Errorf("expected at least 2 requests, got %v", requestedPaths)
		}
	})

	// 5. Test GetFile
	t.Run("GetFile", func(t *testing.T) {
		requestedPaths = nil
		content, err := client.GetFile("my-owner", "my-repo", "my-file.txt")
		if err != nil {
			t.Fatalf("GetFile failed: %v", err)
		}
		if string(content) != "hello world from mock github" {
			t.Errorf("expected 'hello world from mock github', got %q", string(content))
		}
	})

	// 6. Test PostComment
	t.Run("PostComment", func(t *testing.T) {
		requestedPaths = nil
		err := client.PostComment("my-owner", "my-repo", 42, "hello world comment")
		if err != nil {
			t.Fatalf("PostComment failed: %v", err)
		}
		if lastMethod != "POST" {
			t.Errorf("expected method POST, got %s", lastMethod)
		}
	})
}
