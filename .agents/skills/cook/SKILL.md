---
name: cook
description: Process an issue or feature request by developing code. This skill leverages Test-Driven Development (TDD) via the existing `tdd` skill and enforces a continuous verification loop to ensure high code quality. Use when user wants to process an issue, implement a feature request, or "cook" some code.
---

# Cook Skill

Turn an issue into a working testable code.

The issue tracker and triage label vocabulary should have been provided to you — run `/setup-matt-pocock-skills` if not.

## Process

### 1. Understand the requirements

If the user passes an issue reference (issue number, URL, or path) as an argument, fetch it from the issue tracker and read its full body and comments.

### 2. Explore the codebase (optional)

If you have not already explored the codebase, do so to understand the current state of the code.

### 3. Implement via TDD

Delegate to `tdd` skill to implement the core logic.

### 4. Verify

Before finishing, run verification check to ensure nothing is broken.
Use the following command:
```bash
make verify
```

### 5. Fix & Iterate

If the verification fails, fix the errors and run verification again.
