# Stateless Agent Retries

We decided to restart interrupted Agent tasks from scratch (stateless retries) rather than attempting to checkpoint and resume their state, up to a configurable retry limit.

While a PodDisruptionBudget protects active tasks from routine cluster maintenance, involuntary interruptions (like OOMKilled, or node crashes) will still happen. Since third-party CLI agents (like opencode or pi) typically do not natively support checkpointing their LLM context or file system state to disk, building a custom state-resumption wrapper around them would be highly complex and brittle. Restarting the task from scratch trades computational efficiency (cost of re-running the prompt) for architectural simplicity and reliability.
