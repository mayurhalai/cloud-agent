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
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	kubeconfig := flag.String("kubeconfig", "", "Path to a kubeconfig file")
	namespace := flag.String("namespace", "default", "Kubernetes namespace to run in")
	flag.Parse()

	// Load kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		// Fallback to in-cluster config
		if config, err = clientcmd.BuildConfigFromFlags("", ""); err != nil {
			log.Fatalf("Error building kubeconfig: %s", err.Error())
		}
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

	server := webhook.NewListenerServer(k8sClient, dynClient, ghClient, *namespace)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting Webhook Listener on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatalf("HTTP server failed: %s", err.Error())
	}
}
