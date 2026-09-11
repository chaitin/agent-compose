# Integrating with the agent-compose daemon

The daemon is the control plane for projects, revisions, agents, schedulers,
runs, sandboxes, events, artifacts, images, caches, and volumes. External
clients should use the generated Connect RPC client; the CLI and Web UI use the
same boundary.

## Transport and authentication

The daemon accepts Connect RPC over HTTP. For a TCP deployment, use the
configured base URL and send:

```text
Authorization: Bearer <token>
Content-Type: application/json
```

Unix-socket access is intended for local clients. HTTPS and reverse-proxy
termination belong to the deployment boundary. Do not put bearer tokens in
URLs, compose files committed to source control, prompts, or logs.

Connect procedure paths are generated from the protobuf service and method
names, for example:

```text
/agentcompose.v2.ProjectService/ListProjects
```

Use generated types and clients instead of hand-writing procedure paths.

## Resource lifecycle

The common project flow is:

1. Validate a compose document with `ProjectService.ValidateProject`.
2. Apply it with `ProjectService.ApplyProject`, which creates a revision.
3. Start a run with `RunService.RunAgent` or `StartAgentRun`.
4. Read events with `ListRunEvents` or follow output with
   `FollowRunLogs`/`StreamAgentRun`.
5. Stop a run with `RunService.StopRun` when cancellation is required.

Resource IDs are scoped identifiers, not display names. Resolve user-facing
references through `ResourceService.ResolveID` when a command accepts more
than one resource kind.

## Streaming and cancellation

Unary methods return one response. Server-streaming methods deliver events or
output until completion. Bidirectional methods such as `AttachAgentRun` keep
an interactive attachment open and require a context cancellation path.

Always set a bounded context deadline, propagate cancellation, close the
transport, and handle a terminal status. Reconnecting an interactive run
requires the returned run ID; do not create a second run accidentally.

## Errors and capability boundaries

An RPC may return `CodeUnimplemented` when a concrete runtime capability is not
compiled or available. This is different from the generated
`Unimplemented...ServiceHandler` fallback, which indicates a missing route or
handler. Clients should report the operation, resource, and server error
without exposing credentials.

The control plane is separate from data-plane routes such as workspace file
transfer, Jupyter proxy, webhook ingress, and the runtime LLM facade. Do not
use those routes to manage projects or runs.

For the authoritative CLI-to-RPC mapping and approved HTTP exceptions, see the
[Connect transport matrix](connect-transport-matrix.html).
