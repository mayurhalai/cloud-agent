# 0003: CRD-Driven Sandbox Cleanup

The Sandbox Server communicates only with the Webhook Listener when a task is finished. It does not notify the Orchestrator directly. Instead, the Webhook Listener updates the `AgentTask` CRD status to `Completed`. The Orchestrator, which watches the CRD, reacts to this status change by deleting the sandbox. This simplifies the Sandbox Server's responsibilities and leverages Kubernetes native state management for cleanup.
