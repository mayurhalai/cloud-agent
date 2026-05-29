# 0004: Webhook Listener Parses Sandbox Configuration

The Webhook Listener is responsible for fetching and parsing the `.cloud-agent.yaml` file from the target repository. It extracts the required `SandboxTemplate` and passes it directly into the `AgentTask` CRD. This keeps the Orchestrator completely decoupled from GitHub APIs and prevents it from needing GitHub credentials to determine how to run a task.
