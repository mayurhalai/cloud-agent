package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mayurhalai/cloud-agent/pkg/orchestrator"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "Path to a kubeconfig file")
	namespace := flag.String("namespace", "default", "Kubernetes namespace to watch")
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		if config, err = clientcmd.BuildConfigFromFlags("", ""); err != nil {
			log.Fatalf("Error building kubeconfig: %s", err.Error())
		}
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error building dynamic client: %s", err.Error())
	}

	orch := orchestrator.NewOrchestrator(k8sClient, dynClient, *namespace)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("Starting Cloud Agent Orchestrator in namespace %s", *namespace)
	if err := orch.Start(ctx); err != nil {
		log.Fatalf("Orchestrator stopped with error: %s", err.Error())
	}
}
