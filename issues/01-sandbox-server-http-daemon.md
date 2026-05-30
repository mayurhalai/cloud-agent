## What to build

Convert the `sandbox-server` Go application from a CLI tool into a long-running HTTP daemon. It should expose a `POST /task` endpoint that accepts a JSON payload of task configuration (TaskName, RepoOwner, Prompt, etc.) and executes the existing agent runner logic.

## Acceptance criteria

- [ ] `cmd/sandbox-server/main.go` initializes an HTTP server listening on a port (e.g., 8080) instead of parsing CLI flags.
- [ ] A `POST /task` handler is implemented to parse task JSON payloads.
- [ ] Local tests verify the endpoint behaves correctly with valid and invalid payloads.

## Blocked by

None - can start immediately
