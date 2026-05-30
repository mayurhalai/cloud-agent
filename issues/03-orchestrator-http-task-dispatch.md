## What to build

Update the `orchestrator` controller to send task details to the Sandbox Server via an HTTP POST request rather than executing a command-line script inside the sandbox. The orchestrator will serialize the task parameters into a JSON payload and deliver it to the daemon's endpoint.

## Acceptance criteria

- [ ] `pkg/orchestrator/controller.go` is updated to issue an HTTP POST request instead of calling `sb.Run` with CLI arguments.
- [ ] If native HTTP routing is not available via `agent-sandbox`, a tunnel or local `curl` via `sb.Run` is used to deliver the payload.
- [ ] `tests/integration_test.go` mock interactions are updated to assert the HTTP payload is correct instead of asserting on shell execution flags.

## Blocked by

- Issue 1 (01-sandbox-server-http-daemon.md)
- Issue 2 (02-sandbox-server-dockerization.md) (Conceptually, for full e2e deployment)
