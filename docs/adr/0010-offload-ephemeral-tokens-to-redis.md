# Offload Ephemeral Tokens to Redis

We decided to move the storage of the **Result Callback Token** from Kubernetes Secrets (as established in ADR 0008) to a dedicated Redis deployment. The Webhook Listener now writes this token directly to Redis upon creation and passes the ephemeral tokens back to the Orchestrator. 

This avoids constantly creating and deleting short-lived Secrets in the Kubernetes API server for every single `AgentTask`, reducing API churn and providing a more appropriate state store for high-throughput ephemeral session data. A simple Redis deployment in `k8s/base` provides self-contained development defaults, which can be overridden to managed Redis clusters in production.
