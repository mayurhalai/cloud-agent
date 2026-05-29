# 0007: System-Wide Coding Agent Selection

The choice of which underlying coding agent to use (e.g., `opencode` or `pi`) is determined system-wide by the cluster administrator at installation time, rather than being configurable per-repository in `.cloud-agent.yaml`. This ensures a consistent, predictable cost and performance profile across all repositories managed by a single Cloud Agent installation.
