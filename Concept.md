# Cloud Agent

In most enterprises environment there are restrictions on keeping a laptop running when the employee is AFK. This makes it difficult to run AFK coding agent tasks. Most often employee has to stare at the screen while coding agent is working on AFK tasks. We want to solve this by deploying coding agents on Kubernetes cluster so employees can delegate AFK tasks to coding agents on the cloud.

## Overview

The focus of this solution is to provide an enterprise teams with a way to delegate coding tasks to coding agents on the cloud. We will use GitHub and GitLab as orchestration platform. User can tag a bot on a issue to get answer, add bot label to create a PR for the issue resolution.

## Architecture

On the front, it is a github application that listens to events on GitHub. We will add GitLab support later. In the background the application starts a coding agent to complete task. How a task should be completed depends on the task.

### Authentication & Security
We will use a centralized GitHub App with installation tokens to authenticate with GitHub. The credentials will be stored as a Kubernetes secret. An administrator is responsible for creating this secret prior to installing the Helm chart, and the Helm chart will provide an option to reference this existing secret.

### Resilience and Fault Tolerance
To ensure our system is resilient to failures, specifically voluntary disruptions like node drains or cluster upgrades, we will configure a Shared PodDisruptionBudget (PDB) for the agent sandboxes. This will protect active agent tasks from being interrupted by routine cluster maintenance.

## Deployment

The deployment of this entire solution should be done via Helm chart. Helm chart would provide easy way to configure the setup. Like which coding agent to be deployed, parameters configuration for agents and agent sandbox environment configuration.

## Agent sandboxing

We will use agent-sandbox.sigs.k8s.io for sandboxing. Since every project is different, we need to have a way to configure sandboxing via dockerfile. We will provide few base sandboxes for some language stacks. Example, Go and Python.

## PR ownership

Since a bot is creating PR on behalf of user, we need to make sure that PR is reviewed and approved by a different user. To achieve this, the bot will use the GitHub App's installation token to perform the `git push`, but will use git config to set the `user.name` and `user.email` to the user who triggered the task, with the bot as a co-author. This guarantees GitHub records the user as the author. Because the user is the author, standard repository rules will prevent them from approving their own PR and bypassing the code review process.

Since PR is being raised for an issue, we will make the issue assignee as user who authored the PR. If there are multiple assignees, then the first assignee will be considered as an author. We will reject issues with no assignee with a message 'Please assign an issue to yourself or someone else before adding a label'.

## Coding agents

We will support opencode and pi coding agents to begin with. But we want to expand with more coding agents in future. Keep in mind that we mostly will support cli coding agents.
