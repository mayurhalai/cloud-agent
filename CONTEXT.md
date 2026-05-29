# Cloud Agent Context

This context describes the components and concepts for the Cloud Agent system, which allows delegating GitHub issues and PRs to coding agents running in Kubernetes.

## Language

**AgentTask**:
A Kubernetes Custom Resource representing a unit of work (e.g., answering a question on an issue, or creating a PR) created by the webhook listener.
_Avoid_: Task, Job, Request

**Webhook Listener**:
The component that receives GitHub events, processes them, and creates AgentTasks.
_Avoid_: GitHub application web-hook listener

**Orchestrator**:
The Kubernetes controller that watches AgentTasks, manages warm pools via `sigs.k8s.io/agent-sandbox`, and assigns tasks to sandboxes.
_Avoid_: Agent sandbox orchestration controller, Agent orchestrator, agent-sandbox client

**Sandbox Server**:
The server process running inside the sandbox container that receives the prompt, invokes the coding agent, and reports completion.
_Avoid_: Agent sandbox server

**SandboxTemplate**:
A string identifier (e.g., `go`, `python`) defined in a repository's `.cloud-agent.yaml` that determines which container image and warm pool the Orchestrator uses for a task.

**Task Owner**:
The GitHub user who triggered the AgentTask (e.g., by adding a label to an issue). Commits made by the agent are attributed to this user so they cannot approve their own PRs.

**Coding Agent**:
The underlying CLI tool (e.g., `opencode`, `pi`) invoked by the Sandbox Server to execute the prompt. The specific tool used is a system-wide default set by the administrator.
