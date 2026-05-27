# Cloud Agent

In most enterprises environment there are restrictions on keeping a laptop running when the employee is AFK. This makes it difficult to run AFK coding agent tasks. Most often employee has to stare at the screen while coding agent is working on AFK tasks. We want to solve this by deploying coding agents on Kubernetes cluster so employees can delegate AFK tasks to coding agents on the cloud.

## Overview

The focus of this solution is to provide an enterprise teams with a way to delegate coding tasks to coding agents on the cloud. We will use GitHub as orchestration platform. User can tag a bot on a issue to get answer, add bot label to create a PR for the issue resolution.

## Architecture

On the front, it is a github application that listens to events on GitHub. In the background the system starts a coding agent to complete task. How a task should be completed depends on the task.
System components:
- GitHub application web-hook listner
- Agent sandbox orchestration controller

### GitHub application web-hook listner

The GitHub App is the entry point for all interactions with the system. It will be responsible for receiving web-hook events from GitHub and processing them.

### Agent sandbox orchestration controller (or agent-sandbox client)

The Agent sandbox orchestration controller is responsible for managing the lifecycle of agent sandboxes. It will create and destroy agent sandboxes based on request from GitHub App. The controller will use sigs.k8s.io/agent-sandbox/clients/go/sandbox to create and manage sandboxes.

To speed up execution, we should keep SandboxWarmPool for all kind of required languages. 

### Authentication & Security
We will use a centralized GitHub App with installation tokens to authenticate with GitHub. The credentials will be stored as a Kubernetes secret. An administrator is responsible for creating this secret prior to installing the Helm chart, and the Helm chart will provide an option to reference this existing secret.

### Resilience and Fault Tolerance
To ensure our system is resilient to failures, specifically voluntary disruptions like node drains or cluster upgrades, we will configure a Shared PodDisruptionBudget (PDB) for the agent sandboxes. This will protect active agent tasks from being interrupted by routine cluster maintenance.

## Deployment

The deployment of this entire solution should be done via Helm chart. Helm chart would provide easy way to configure the setup. Like which coding agent to be deployed, parameters configuration for agents and agent sandbox environment configuration.

## Agent sandboxing

We will use agent-sandbox.sigs.k8s.io for sandboxing. Since every project is different, we need to have a way to configure sandboxing via dockerfile. We will provide few base sandboxes for some language stacks. Example, Go and Python.

## PR ownership

Since a bot is creating PR on behalf of user, we need to make sure that PR is reviewed and approved by a different user. To achieve this, the Controller will mint a **Scoped Agent Token** with least privilege (only allowing code push and PR creation) and provide it to the Agent sandbox. The Agent will use this scoped token to perform the `git push`, but will use git config to set the `user.name` and `user.email` to the user who triggered the task, with the bot as a co-author. This guarantees GitHub records the user as the author. Because the user is the author, standard repository rules will prevent them from approving their own PR and bypassing the code review process.

Since PR is being raised for an issue, the authorship of the commits will be assigned to the **Task Owner** — the user who triggered the Agent by adding the label. This ensures that the user requesting the work is held responsible for the code, and standard repository rules will prevent them from approving the resulting PR.

## Coding agents

We will support opencode and pi coding agents to begin with. But we want to expand with more coding agents in future. Keep in mind that we mostly will support cli coding agents.

## Questions

How to build sandbox image for a language stack?
We need to build language image for various agents we support.

How do we know agent has finished its task?
Option 1: We can exit with status code 0. We set `spec.lifecycle.ttlSecondsAfterFinished` to `0` and let kubernetes garbage collect the sandbox.

How do we input/output from agent?
Option 1: We bake some sort of rest server on agent. We call rest endpoints to give input and get output from it.
Option 2: We can explore sidecar approach (No need to inject, we can add sidecar to sandbox template). The sidecar contains the rest server and somehow communicate with agent container. 

How to pass context to Agent?
Use separate system prompt for issue answer and issue PR.

Agent can push the code to a branch. But how to create a PR from the branch? We can't promt agent and expect it to always follow.

How do we pass the task prompt to the Agent? Problem is, we need to pass it as argument to agent, but that prevents the container from running until sits in a warm pool. Does docker image offer hybrid approach? Sleep until get signal and then run the agent with task prompt as argument?
