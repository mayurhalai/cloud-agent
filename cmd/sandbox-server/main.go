package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
)

func main() {
	taskName := flag.String("task-name", os.Getenv("TASK_NAME"), "Name of the AgentTask custom resource")
	callbackURL := flag.String("callback-url", os.Getenv("CALLBACK_URL"), "Callback URL of the webhook listener")
	tokenPath := flag.String("token-path", "/etc/cloud-agent/callback-token", "Path to the callback token secret volume file")
	flag.Parse()

	if *taskName == "" {
		log.Fatal("task-name parameter or TASK_NAME environment variable is required")
	}
	if *callbackURL == "" {
		log.Fatal("callback-url parameter or CALLBACK_URL environment variable is required")
	}

	runner := sandbox.NewRunner(*taskName, *callbackURL, *tokenPath, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("Sandbox Server: Running Hello World answer for task %s", *taskName)
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("Sandbox Server run failed: %s", err.Error())
	}

	log.Println("Sandbox Server: Hello World completed successfully")
}
