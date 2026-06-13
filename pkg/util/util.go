package util

import (
	"fmt"
	"os"
)

func GetWebhookListenerURL(namespace string) string {
	return GetEnvWithDefault("WEBHOOK_LISTENER_URL", fmt.Sprintf("http://webhook-listener.%s.svc.cluster.local:8080", namespace))
}

func GetEnvWithDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
