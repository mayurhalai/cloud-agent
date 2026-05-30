## What to build

Create a multi-stage Dockerfile for the `sandbox-server` that builds the Go binary and packages it into a minimal base image. The `ENTRYPOINT` of the image should be the long-running HTTP daemon process.

## Acceptance criteria

- [ ] A new `Dockerfile` is added to `cmd/sandbox-server/` or the project root.
- [ ] The Dockerfile uses a multi-stage build (e.g., building in a `golang` image and copying the binary to a smaller base image).
- [ ] The image can be built successfully and run locally exposing the HTTP port.
- [ ] ADR 0009 constraints are respected: this image serves as a base image for SandboxTemplates.

## Blocked by

- Issue 1 (01-sandbox-server-http-daemon.md)
