## What to build

Implement a real `AppClient` for the Webhook Listener that authenticates using a GitHub App installation token. This will replace the mock client currently used in production, allowing the listener to interact with real GitHub repositories on behalf of the installed App.

## Acceptance criteria

- [ ] `pkg/github/app_client.go` is created, implementing the `Client` interface.
- [ ] The implementation uses `github.com/google/go-github` and `github.com/bradleyfalzon/ghinstallation` for token minting and API requests.
- [ ] `cmd/webhook-listener/main.go` is updated to instantiate `AppClient` using environment variables (e.g., `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY_PATH`).
- [ ] Existing tests continue to use `MockClient`.

## Blocked by

None - can start immediately
