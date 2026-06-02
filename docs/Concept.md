# Cloud Agent

In most enterprises environment there are restrictions on keeping a laptop running when the employee is AFK. This makes it difficult to run AFK coding agent tasks. Most often employee has to stare at the screen while coding agent is working on AFK tasks. We want to solve this by deploying coding agents on Kubernetes cluster so employees can delegate AFK tasks to coding agents on the cloud.

## Overview

The focus of this solution is to provide an enterprise teams with a way to delegate coding tasks to coding agents on the cloud. We will use GitHub as orchestration platform. User can tag a GitHub App on a issue to get answer, add GitHub App label to create a PR for the issue resolution.

## Supported scenarios

We have identified following scenarios:

### GitHub App mention

User can mention the app in an issue/PR comment to get an answer.

Mention on an issue comment:
Use issue title, description and previous comments as context to answer the query for which the GitHub App is tagged. Answer should be posted as a comment on the issue.

Mention on a PR comment:
Use PR title, description and previous comments as context to answer the query for which the GitHub App is tagged. Answer should be posted as a comment on the PR.

In both case if mention was without a question, then default query should be to recommend based on context.

### GitHub App label

User can add a label to an issue to create a PR for the issue resolution.
Use Issue title, description and all comments as context to create PR. The PR should propose changes required to resolve the issue.
Assume fix can be applied on the same repository where issue is present.

**Note:** adding label on a PR is not valid case and system should just post a comment to the PR that this action is not supported.

## Architecture

On the front, it is a github application that listens to events on GitHub. In the background the system starts a coding agent to complete task. How a task should be completed depends on the task.
System components:
- GitHub application web-hook listner
- Agent sandbox orchestration controller
- Agent sandbox server

### GitHub application web-hook listner

The GitHub App is the entry point for all interactions with the system. It will be responsible for receiving web-hook events from GitHub and processing them.

Responsibilities:
- Receives web-hook events from GitHub and filter out events that are not related to the system.
- Based on event type it will gather required content and ask Agent orchestrator to start the task.
- Generate least privilege token for the Agent sandbox for pushing code.
- Expose callback endpoint for agent sandbox server to send back result to post on issue/PR.

### Agent sandbox orchestration controller (or agent-sandbox client)

The Agent sandbox orchestration controller is responsible for managing the lifecycle of agent sandboxes. It will create and destroy agent sandboxes based on request from GitHub App. The controller will use sigs.k8s.io/agent-sandbox/clients/go/sandbox to create and manage sandboxes.

To speed up execution, we should keep SandboxWarmPool for all kind of required languages.

Need to know:
- Prompt to be given to the agent.
- SandboxTemplate so it claims from appropriate pool.

Responsibilities:
- Claim sandbox from warm pool.
- For PR task, decide template based on `.cloud-agent.yaml` file. Default to plain agent image, the same image that is used to answer on GitHub App mention workflow.
- Pass on the prompt to agent sandbox server.
- Delete sandbox when notified by agent sandbox server that task is completed.

### Agent sandbox server

The Agent sandbox server will be running inside the agent sandbox container. It will be responsible for executing an agent.

Need to know:
- Whether task is to answer back or create PR

Responsibilities:
- Pass on the prompt to the agent.
- Get response back from agent.
- Send response back via callback, if task is to answer back.
- Notify agent sandbox orchestration controller that task is completed.
- When successfully notified agent sandbox orchestration controller about completion, exit with status code 0.

#### Invoking agent

We will create a server that will accept a prompt. This server will be responsible for executing an agent. This server now needs to be baked in all images all agent sandbox images.

#### Build language images

We will create a base image with such server and use `ONBUILD` instruction to copy the server and set an entrypoint to run the server.
We will use following flow to achieve language specific agent sandbox images:
Server base image -> Agent specific base image -> Lanugage specific image
Final language specific image will be used to create sandbox for PR, and agent sandbox for answering back.

## PR ownership

Since a GitHub App is creating PR on behalf of user, we need to make sure that PR is reviewed and approved by a different user. To achieve this, the webhook listner will mint a **Scoped Agent Token** with least privilege (only allowing code push and PR creation) and provide it to the Agent sandbox. The Agent will use this scoped token to perform the `git push`, but will use git config to set the `user.name` and `user.email` to the user who triggered the task, with the GitHub App as a co-author. This guarantees GitHub records the user as the author. Because the user is the author, standard repository rules will prevent them from approving their own PR and bypassing the code review process.

Since PR is being raised for an issue, the authorship of the commits will be assigned to the **Task Owner** — the user who triggered the Agent by adding the label. This ensures that the user requesting the work is held responsible for the code, and standard repository rules will prevent them from approving the resulting PR.

## Coding agents

We will support opencode and pi coding agents to begin with. But we want to expand with more coding agents in future. Keep in mind that we mostly will support cli coding agents.
