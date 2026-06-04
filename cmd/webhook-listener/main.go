package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/mayurhalai/cloud-agent/pkg/github"
	"github.com/mayurhalai/cloud-agent/pkg/webhook"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	namespace := flag.String("namespace", "cloud-agent", "Kubernetes namespace to run in")
	flag.Parse()

	// Load kubeconfig
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Error building kubeconfig: %s", err.Error())
	}

	// Create clients
	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error building dynamic client: %s", err.Error())
	}

	// Check environment variables to instantiate either the real AppClient or MockClient
	var ghClient github.Client
	appIDStr := os.Getenv("GITHUB_APP_ID")
	privateKeyPath := os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")

	if appIDStr != "" && privateKeyPath != "" {
		appID, err := strconv.ParseInt(appIDStr, 10, 64)
		if err != nil {
			log.Fatalf("Invalid GITHUB_APP_ID: %v", err)
		}
		ghClient, err = github.NewAppClient(appID, privateKeyPath)
		if err != nil {
			log.Fatalf("Failed to create GitHub App client: %v", err)
		}
		log.Printf("Using real GitHub App client (App ID: %d)", appID)
	} else {
		log.Printf("Warning: GITHUB_APP_ID or GITHUB_APP_PRIVATE_KEY_PATH not set. Falling back to MockClient.")
		ghClient = &github.MockClient{}
	}

	redisURL := os.Getenv("REDIS_URL")
	var tokenStore webhook.TokenStore
	if redisURL != "" {
		var err error
		tokenStore, err = webhook.NewRedisTokenStore(redisURL)
		if err != nil {
			log.Fatalf("Failed to initialize Redis token store: %v", err)
		}
		log.Printf("Using Redis token store connected to %s", redisURL)
	} else {
		log.Printf("Warning: REDIS_URL not set. Falling back to InMemoryTokenStore.")
		tokenStore = webhook.NewInMemoryTokenStore()
	}

	webhookSecret := os.Getenv("GITHUB_APP_WEBHOOK_SECRET")
	server := webhook.NewListenerServer(k8sClient, dynClient, ghClient, *namespace, []byte(webhookSecret), tokenStore)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting Webhook Listener on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatalf("HTTP server failed: %s", err.Error())
	}
}
