# Custom Sandbox Client and Sandbox-Router Task Execution

We decided to replace the external `sigs.k8s.io/agent-sandbox/clients/go/sandbox` client SDK with a custom, native implementation in `pkg/sandbox`. This custom client supports creating `SandboxClaim`s and waiting for the `Sandbox` ready status via a simple polling mechanism, and it updates `ExecuteTask` to natively route HTTP requests through the `sandbox-router`.

This decision simplifies the Orchestrator by eliminating the need to look up Pod IPs in Kubernetes to route tasks directly. Instead, we delegate routing to the `sandbox-router` using the required `X-Sandbox-ID`, `X-Sandbox-Namespace`, and `X-Sandbox-Port` headers. Implementing our own client also removes the port-forwarding and gateway discovery complexity of the upstream SDK, reducing external dependencies and improving testability under local environments where a simple HTTP server mock is used.
