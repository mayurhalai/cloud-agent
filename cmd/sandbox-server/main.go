package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/mayurhalai/cloud-agent/pkg/sandbox"
)

func main() {
	port := getEnvWithDefault("PORT", "8888")
	log.Printf("Sandbox Server: Starting HTTP daemon listening on port %s", port)

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.Any("/task", sandbox.TaskHandler)
	r.Any("/health", sandbox.HealthCheckHandler)

	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Sandbox Server: HTTP server failed: %v", err)
	}
}

func getEnvWithDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
