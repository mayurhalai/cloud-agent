## What to build

Add a new HTTP endpoint to the Webhook Listener (e.g., `POST /task/{id}/tokens`) that generates both the GitHub Auth Token and Result Callback Token on-demand. When called, this endpoint should generate the tokens, store the Result Callback Token in Redis, and return both tokens in the JSON response. Stop the Webhook Listener from generating these tokens upfront when the `AgentTask` is initially created.

## Acceptance criteria

- [ ] Webhook Listener exposes a new endpoint to request tokens for a specific task.
- [ ] Calling the endpoint generates a short-lived GitHub Auth Token and a Result Callback Token.
- [ ] The endpoint stores the Result Callback Token in Redis and returns both tokens in the response.
- [ ] The Webhook Listener no longer generates tokens or writes to Redis when an `AgentTask` is first created.

## Blocked by

- docs/issues/01-redis-infrastructure-and-callback-verification.md
