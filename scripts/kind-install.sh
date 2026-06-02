#!/bin/bash

# Replace "vX.Y.Z" with a specific version tag (e.g., "v0.1.0") from
# https://github.com/kubernetes-sigs/agent-sandbox/releases
export VERSION="v0.4.6"

# To install only the core components:
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${VERSION}/manifest.yaml

# To install the extensions components:
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${VERSION}/extensions.yaml

# Create namespace
kubectl create namespace cloud-agent

# Create secret
kubectl apply -f /Users/mayurhalai/repos/crap/secrets/github-app-secret.yaml -n cloud-agent

# Deploy pi agent sandbox
kubectl apply -k k8s/pi
