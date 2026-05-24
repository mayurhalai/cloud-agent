---
labels:
  - ready-for-agent
---

# Cloud Agent Platform PRD

## Problem Statement

In enterprise environments, there are often strict policies preventing employees from keeping their laptops running while away from the keyboard (AFK). This makes it difficult or impossible for developers to delegate long-running automated coding tasks to local AI agents, as they must monitor the screen and keep the device awake. As a result, developer productivity is blocked by the inability to offload asynchronous work to agents reliably.

## Solution

The Cloud Agent Platform solves this by deploying coding agents on a Kubernetes cluster. Developers can interact with the platform directly through GitHub issues. By adding specific labels or mentioning the bot, the platform will automatically spin up an agent in the cloud to work on the task asynchronously. Once complete, the agent will push code and create a Pull Request on behalf of the developer, allowing the developer to delegate work without keeping their local machine active.

## User Stories

1. As a developer, I want to tag the agent bot (Mention Trigger) in an issue comment, so that I can get an immediate text-based answer or clarification without leaving GitHub.
2. As a developer, I want to add a specific label (Label Trigger) to an issue I am assigned to, so that the agent automatically begins implementing the feature and creates a Pull Request.
3. As a developer triggering an implementation task, I want the resulting Pull Request to list my identity as the author, so that I am held responsible for the code and standard repository rules prevent me from approving my own PR.
4. As a repository maintainer, I want to specify a `.cloud-agent.yaml` file (Sandbox Configuration) in my repository root, so that the agent runs in an environment with the correct dependencies (e.g., Go, Python) tailored to my project.
5. As a cluster administrator, I want to configure the platform using a Helm chart, so that I can enforce a single organizational CLI agent (Global Agent Configuration) and easily manage infrastructure settings.
6. As a security administrator, I want the platform to authenticate via a centralized GitHub App but mint a Scoped Agent Token for individual tasks, so that the agent sandbox only has least-privilege access (branch creation, code push, PR creation) to the specific repository it is working on.
7. As a platform engineer, I want the system to handle involuntary pod disruptions (e.g., node drains) gracefully via a Task Lifecycle, so that interrupted tasks are automatically retried from scratch and failures are reported to the user.
8. As a developer, I want to receive a comment on the GitHub issue if the agent fails to complete the task after exhausting its retries, so that I am aware the task requires human intervention.
9. As an organization, I want to use standard Kubernetes abstractions like a Shared PodDisruptionBudget, so that active agent tasks are protected from routine voluntary cluster maintenance.
10. As a repository maintainer, I want the platform to reject Label Triggers on unassigned issues, so that it is always clear who the Task Owner is before work begins.

## Implementation Decisions

- **Webhook Receiver Module**: A new HTTP server module that listens to GitHub webhooks. It will parse `issues` and `issue_comment` payloads to detect Mention Triggers and Label Triggers, extracting the necessary context (repo, issue number, triggering user) into a `TaskRequest` event.
- **Controller / Orchestrator Module**: The core workload manager running in Kubernetes. It will consume `TaskRequest` events and manage a formal Task Lifecycle state machine (Pending, Running, Failed, Completed). It will interact with the Kubernetes API to provision pods running the Global Agent Configuration, mounting the repository-specific Sandbox Configuration. It implements stateless retries, restarting tasks from scratch upon involuntary interruptions up to a defined limit.
- **Security Module**: A module responsible for authenticating with the GitHub API using the central GitHub App credentials (stored as a Kubernetes Secret). It will mint short-lived, repository-scoped installation tokens (Scoped Agent Token) to inject into the Agent Sandbox.
- **Task Ownership Enforcement**: The platform will explicitly check that the user adding the label is assigned to the issue. The resulting `git push` inside the sandbox will use the Scoped Agent Token for authentication, but configure `user.name` and `user.email` to match the Task Owner.
- **Helm Deployment**: The entire platform, including the Controller, Webhook Receiver, and Global Agent Configuration parameters, will be packaged and deployed as a Helm chart.

## Testing Decisions

- **Testing Philosophy**: Tests should focus strictly on the external behavior and public interfaces of the modules, avoiding brittle assertions on internal implementation details. We will use black-box testing wherever possible.
- **Webhook Receiver**: Unit tests will inject mock GitHub webhook JSON payloads and assert that the correct `TaskRequest` struct is emitted (or rejected if invalid).
- **Controller / Orchestrator**: Unit tests will validate the Task Lifecycle state machine. We will mock the Kubernetes API client to test that the Controller correctly provisions pods, handles simulated pod crashes by incrementing retry counters, and transitions to Failed or Completed states appropriately.
- **Security Module**: Unit tests will mock the GitHub API responses to ensure the module correctly requests and parses Scoped Agent Tokens, and handles authentication failures gracefully.

## Out of Scope

- **GitLab Support**: While the architecture may eventually support GitLab, V1 is strictly focused on GitHub integrations and terminology.
- **Dynamic Agent Selection**: Per-repository selection of the CLI agent is out of scope. The organization will use a single, globally configured CLI agent.
- **State Checkpointing**: The platform will not attempt to snapshot or resume the internal state/LLM context of the CLI agents if they are interrupted. Retries are strictly stateless.
- **Multi-Context Processing**: Complex task splitting or multi-issue coordination is out of scope. One issue maps to one agent task.

## Further Notes

- Reference ADR `docs/adr/0001-stateless-agent-retries.md` for context on why the stateless retry mechanism was chosen over state checkpointing.
- Ensure the Helm chart values surface the necessary PodDisruptionBudget configurations to align with the resilience strategy.
