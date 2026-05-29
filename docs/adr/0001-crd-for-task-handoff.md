# 0001: CRD for Task Handoff

We will use a Kubernetes Custom Resource Definition (CRD), provisionally called `AgentTask`, to handle the handoff between the GitHub webhook listener and the orchestrator. This leverages the cluster's native state management and allows the orchestrator to act as a standard Kubernetes operator watching for new tasks, rather than building a custom queue or synchronous HTTP API.
