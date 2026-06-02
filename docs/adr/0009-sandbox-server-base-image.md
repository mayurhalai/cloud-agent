# Sandbox Server as a Base Image

We have decided to ship the Sandbox Server as a Docker base image rather than a Kubernetes sidecar container, requiring all user-defined `SandboxTemplate` images to inherit from this base image.

This represents a trade-off: it simplifies the runtime architecture inside the sandbox (single container, straightforward IPC, and file sharing with the coding agent) at the cost of coupling the SandboxTemplate images to our release cycle. Users will need to rebuild their templates to pick up new versions of the Sandbox Server binary. We chose this over a sidecar pattern to minimize complexity in volume sharing, permissions, and process coordination during agent execution.
