package main

import (
	"log"
	"net/http"
	"os"

	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
)

func main() {
	port := getEnvWithDefault("PORT", "8888")
	log.Printf("Sandbox Server: Starting HTTP daemon listening on port %s", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/task", sandbox.TaskHandler)
	mux.HandleFunc("/health", sandbox.HealthCheckHandler)

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Sandbox Server: HTTP server failed: %v", err)
	}
}

func getEnvWithDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
