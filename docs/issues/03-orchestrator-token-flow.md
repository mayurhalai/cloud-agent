## What to build

Update the Orchestrator workflow to utilize the new Webhook Listener token API. When the Orchestrator picks up a pending `AgentTask`, it should first transition the task status to `started`. It must then call the Webhook Listener's API to fetch the GitHub Auth Token and Result Callback Token. Upon receiving the tokens, it transitions the task status to `running` and passes the tokens directly to the Sandbox Server via the HTTP POST payload. All Orchestrator logic related to fetching tokens from Kubernetes Secrets should be removed.

## Acceptance criteria

- [ ] Orchestrator transitions `AgentTask` to `started` before requesting tokens.
- [ ] Orchestrator calls the Webhook Listener API to retrieve the Task Tokens.
- [ ] Orchestrator transitions `AgentTask` to `running` after receiving the tokens.
- [ ] Tokens are passed to the Sandbox Server in the HTTP request payload.
- [ ] Orchestrator no longer reads Kubernetes Secrets to fetch tokens.

## Blocked by

- docs/issues/02-jit-token-generation-api.md
