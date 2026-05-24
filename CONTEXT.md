# Cloud Agent Context

The core domain for the Cloud Agent platform, defining the system that delegates AFK coding tasks to cloud-based agents.

## Language

**Controller**:
The control plane application that listens to GitHub webhooks, orchestrates jobs, and schedules workloads.
_Avoid_: GitHub application, bot, application

**Agent**:
The underlying CLI coding model (e.g., opencode, pi) running inside a sandboxed pod to execute a task.
_Avoid_: bot, coding agent, CLI agent

**Global Agent Configuration**:
The cluster-wide configuration (provided via Helm chart during installation) that strictly defines which single CLI agent (e.g., opencode) is used for the entire organization.
_Avoid_: default agent, global config

**Sandbox Configuration**:
A configuration file (e.g., `.cloud-agent.yaml`) located in the target repository's root, defining only the environment dependencies (e.g., Go, Python, system packages) required for the agent to execute in that specific repository, not the choice of agent itself.
_Avoid_: agent configuration, dockerfile config

**Task Owner**:
The user who triggers the Agent by adding a specific label to the issue. This user's identity is used as the author of the resulting commits.
_Avoid_: assignee, PR author

**Mention Trigger**:
When a user tags the bot (e.g., `@botname`) in an issue comment. This triggers a Q&A modality where the Agent responds with a comment instead of modifying code.
_Avoid_: comment trigger, question mode

**Label Trigger**:
When a user adds a specific label to an issue. This triggers an Implementation modality where the Agent creates a branch, pushes code, and opens a PR.
_Avoid_: PR trigger, action label

**Task Lifecycle**:
The formalized state machine (e.g., Pending, Running, Failed, Completed) managed by the Controller. Interrupted tasks (e.g., node drain, pod crash) are restarted from scratch by the Controller up to a retry limit, and marked as Failed if the limit is exceeded.
_Avoid_: agent state, job status

**Scoped Agent Token**:
A short-lived, repository-specific GitHub authentication token minted by the Controller and injected into the Agent's sandbox. It adheres to the principle of least privilege, allowing only branch creation, code push, and PR creation.
_Avoid_: GitHub App token, global token
