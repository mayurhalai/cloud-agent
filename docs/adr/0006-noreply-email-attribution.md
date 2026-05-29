# 0006: Noreply Email Attribution

To ensure that GitHub PRs created by the agent are attributed to the user who triggered the task (the Task Owner), we must configure git with their email address. Because users' real emails are often private and inaccessible via webhook payloads, the Webhook Listener constructs the user's standard GitHub noreply email (`{ID}+{USERNAME}@users.noreply.github.com`) using data readily available in the webhook event. The Sandbox Server uses this for git attribution, guaranteeing GitHub recognizes the author and enforces standard PR approval rules.
