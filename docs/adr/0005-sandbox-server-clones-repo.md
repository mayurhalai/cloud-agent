# 0005: Sandbox Server Clones Repository

Because the system uses a warm pool for faster agent startup, sandboxes are created before tasks are known. This prevents us from using standard Kubernetes InitContainers to clone the target repository. Instead, the Sandbox Server is responsible for performing a dynamic `git clone` upon receiving the task payload from the Orchestrator, right before invoking the coding agent.
