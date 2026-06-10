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

# Install sandbox router
AGENT_SANDBOX_REPO_LOCATION="../../kubernetes-sigs/agent-sandbox"
pushd "${AGENT_SANDBOX_REPO_LOCATION}/clients/python/agentic-sandbox-client/sandbox-router"
SANDBOX_ROUTER_IMAGE="sandbox-router:latest"
docker build -t $SANDBOX_ROUTER_IMAGE .
kind load docker-image $SANDBOX_ROUTER_IMAGE --name desktop
# sed -i "s|\${ROUTER_IMAGE}|${SANDBOX_ROUTER_IMAGE}|g" sandbox_router.yaml
kubectl apply -n cloud-agent -f sandbox_router.yaml
popd

# Create secret
kubectl apply -f /Users/mayurhalai/repos/crap/secrets/cloud-agent-secret.yaml -n cloud-agent

# Load images
kind load docker-image webhook-listener:latest --name desktop
kind load docker-image orchestrator:latest --name desktop
kind load docker-image agent-pi:latest --name desktop

# Deploy pi agent sandbox
kubectl apply -k k8s/pi
