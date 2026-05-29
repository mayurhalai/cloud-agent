# 0008: Callback Endpoint Authentication

To secure the Webhook Listener's callback endpoint (used by the Sandbox Server to post results to GitHub), the Listener generates a unique, one-time Callback Token when creating an `AgentTask`. This token is stored in a dedicated Kubernetes Secret (separate from the GitHub installation token) and mounted into the sandbox. The Sandbox Server must include this token to authorize its callback. Once verified, the token is invalidated. This prevents malicious code executed in the sandbox from arbitrarily posting comments as the GitHub App.
