## What to build

Deploy a simple Redis instance to `k8s/base` and configure the Webhook Listener to connect to it via a `REDIS_URL` environment variable. Update the Webhook Listener so that when it generates the Result Callback Token, it stores the token in Redis instead of creating a Kubernetes Secret. Finally, update the Webhook Listener's callback endpoint to verify incoming callback requests against the token stored in Redis rather than looking up a Kubernetes Secret.

## Acceptance criteria

- [ ] A Redis deployment and service exist in `k8s/base`.
- [ ] The Webhook Listener deployment is configured with a `REDIS_URL` pointing to the Redis service.
- [ ] When an `AgentTask` is created, the Result Callback Token is written to Redis instead of a Kubernetes Secret.
- [ ] The callback endpoint successfully authenticates requests by verifying the token against Redis.

## Blocked by

None - can start immediately
