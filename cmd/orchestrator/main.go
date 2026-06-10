package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-logr/logr"
	"github.com/mayurhalai/cloud-agent/pkg/orchestrator"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"
)

func main() {
	namespace := flag.String("namespace", "cloud-agent", "Kubernetes namespace to watch")
	flag.Parse()

	// In cluster kubeconfig
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Error building kubeconfig: %s", err.Error())
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error building dynamic client: %s", err.Error())
	}

	k8sHelper, err := sandbox.NewK8sHelper(config, logr.Discard())
	if err != nil {
		log.Fatalf("Error building k8s helper: %s", err.Error())
	}
	sandboxClient, err := sandbox.NewClient(context.Background(), sandbox.Options{
		Namespace:    *namespace,
		K8sHelper:    k8sHelper,
		TemplateName: "pi-sandbox-template",                                                   // This template is not actually in use, but client initialization fails without it
		APIURL:       "http://sandbox-router-svc.agent-sandbox-system.svc.cluster.local:8080", // Sandbox Router service url
	})
	if err != nil {
		log.Fatalf("Error building sandbox client: %s", err.Error())
	}

	orch := orchestrator.NewOrchestrator(k8sClient, dynClient, sandboxClient, *namespace)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("Starting Cloud Agent Orchestrator in namespace %s", *namespace)
	if err := orch.Start(ctx); err != nil {
		log.Fatalf("Orchestrator stopped with error: %s", err.Error())
	}
}
