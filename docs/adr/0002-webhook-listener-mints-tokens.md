# 0002: Webhook Listener Mints GitHub Tokens

The Webhook Listener is responsible for generating short-lived GitHub installation tokens for the agent sandboxes. It stores the token in a Kubernetes Secret and references it in the `AgentTask` CRD. The Orchestrator merely mounts this Secret into the sandbox. This keeps the Orchestrator isolated from GitHub authentication concerns and centralizes credential management in the Listener.
