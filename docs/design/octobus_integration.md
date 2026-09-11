# OctoBus Integration Implementation Spec

Project-scoped extensions are documented separately in
[Project-scoped OctoBus servers](project_octobus_servers_design.md).

OctoBus repository: [chaitin/OctoBus](https://github.com/chaitin/OctoBus).

agent-compose integrates published OctoBus capability sets, injects selected
capability sets into work sandboxes and automation tasks, and provides capability
call entry points to guests. agent-compose is the only integration boundary:
frontend and guest do not connect to OctoBus directly.

This document describes the original daemon-wide gateway and the protocol shared
by all OctoBus routes. Projects may additionally declare named servers and use
qualified capset declarations. The project-scoped configuration, resolution,
compatibility, and re-apply rules are specified in
[Project-scoped OctoBus servers](project_octobus_servers_design.md). Unqualified
capset IDs continue to use the daemon-wide gateway described here.

## Architecture

Three paths:

```text
Control plane (frontend read, Connect/HTTP):   frontend -> agent-compose CapabilityService -> OctoBus /admin/v1/*
Business invocation (Connect/HTTP):            business service -> agent-compose CapabilityService -> OctoBus Connect RPC
Agent data plane (gRPC):                       guest agent -> agent-compose capproxy -> OctoBus daemon gRPC
```

- The control plane provides read-only connection status, capability set list,
  and capability catalog.
- Business invocation is deterministic and forwards an explicit
  `capset/instance/service/method/payload` request without involving an agent
  or model.
- The data plane exposes only gRPC: guest calls capabilities through
  agent-compose transparent proxy. MCP / REST are not exposed to the guest.
- Both paths resolve an OctoBus target at call time. Unqualified capsets
  dynamically read the daemon-wide connection from `ConfigStore`; qualified
  capsets resolve the named server from the current managed agent definition.
  Target selection does not change the upstream OctoBus protocol.

UI shows only product concepts:

| OctoBus concept | agent-compose UI name |
| --- | --- |
| capset | Capability set |
| method | Callable capability |
| service | Integration source |
| instance | Connection instance |

## Configuration

### Daemon-wide OctoBus Connection

Page-configured, stored in DB, dynamically read at runtime:

- Table `capability_gateway`, single row: `addr`, `token` (capset invocation
  secret), and `admin_token` (OctoBus admin API secret).
- `ConfigService` provides `GetCapabilityGatewayConfig` /
  `UpdateCapabilityGatewayConfig`; both tokens are redacted when read back.
- Non-empty `addr` means enabled.

```proto
message CapabilityGatewayConfig {   // read response, never returns token
  string addr = 1;
  bool token_set = 2;               // whether the capset token is set
  bool admin_token_set = 3;         // whether the admin token is set
}

message UpdateCapabilityGatewayConfigRequest {
  optional string addr = 1;
  optional string token = 2;        // capset token; empty string means clear
  optional string admin_token = 3;  // admin token; empty string means clear
}
```

Admin API reads (`/admin/v1/status`, capsets, and catalog) use `admin_token`.
When `admin_token` is empty, the legacy `token` value is used only as a
compatibility fallback. Business `InvokeCapability` calls always use `token`.
Both values stay server-side only; neither is returned to the frontend in
plaintext, written into sandbox metadata, injected into guest env, or logged.

This singleton remains the compatibility route for every unqualified
`capset_ids` entry. Merely declaring project `octobus_servers` does not override
or disable it. A qualified declaration such as `internal/dev` uses the project
server named `internal`, while the upstream still receives `dev` in
`x-octobus-capset`. Project tokens have the same daemon-only boundary.

### Data-Plane Proxy Entry

Deployment-fixed, bound once at startup, not page-configured:

- Proxy bind address: listen address of agent-compose internal transparent gRPC
  proxy server.
- Guest-reachable proxy address: `CAP_GRPC_TARGET` injected into sandboxes,
  determined by container / network mapping.
- Runtime gRPC capability calls require both `CAP_GRPC_LISTEN` and
  `CAP_GRPC_TARGET` to be set when the daemon starts. `CAP_GRPC_LISTEN` starts
  the local capability gRPC proxy; `CAP_GRPC_TARGET` is the guest-reachable
  address injected into new sandboxes. If either is missing, the control plane
  can still show OctoBus as connected, but sandboxes with selected capsets will
  not receive usable runtime capability connection variables. Restart
  agent-compose and create a new sandbox after changing these values.

## Control-Plane CapabilityService

```proto
service CapabilityService {
  rpc GetCapabilityStatus(GetCapabilityStatusRequest) returns (CapabilityStatusResponse);
  rpc ListCapabilitySets(ListCapabilitySetsRequest) returns (ListCapabilitySetsResponse);
  rpc GetCapabilityCatalog(GetCapabilityCatalogRequest) returns (GetCapabilityCatalogResponse);
  rpc InvokeCapability(InvokeCapabilityRequest) returns (InvokeCapabilityResponse);
}

message GetCapabilityStatusRequest {}
message ListCapabilitySetsRequest {}

message CapabilityStatusResponse {
  bool configured = 1;
  bool ok = 2;
  string status = 3;
  uint32 service_count = 4;
  string error = 5;
  bool runtime_configured = 6;
  bool proxy_listen_configured = 7;
  bool proxy_target_configured = 8;
}

message CapabilitySet {
  string id = 1;
  string name = 2;
  string description = 3;
  bool enabled = 4;
}
message ListCapabilitySetsResponse { repeated CapabilitySet capsets = 1; }

message GetCapabilityCatalogRequest { string capset_id = 1; }

message CapabilityEndpoint {
  string protocol = 1;
  string endpoint = 2;
  string method_path = 3;
  map<string, string> metadata = 4;
  string tool_name = 5;
  string procedure = 6;
  string http_method = 7;
  repeated string content_types = 8;
}
message CapabilityMethod {
  string service_id = 1;
  string instance_id = 2;
  string runtime_mode = 3;
  string method_full_name = 4;
  string request_message_full_name = 5;
  string response_message_full_name = 6;
  string backend_instance_status = 7;
  repeated CapabilityEndpoint endpoints = 8;
}
message GetCapabilityCatalogResponse {
  string capset_id = 1;
  string name = 2;
  string description = 3;
  repeated CapabilityMethod methods = 4;
}

message InvokeCapabilityRequest {
  string capset_id = 1;
  string instance_id = 2;
  string service_id = 3;
  string method = 4;
  string payload_json = 5;
}
message InvokeCapabilityResponse {
  string result_json = 1;
}
```

Backend behavior:

| RPC | OctoBus API | Handling |
| --- | --- | --- |
| `GetCapabilityStatus` | `GET /admin/v1/status` | Return `configured` / `ok` / `status` / `service_count` |
| `ListCapabilitySets` | `GET /admin/v1/capsets` | Normalize to UI capability set list |
| `GetCapabilityCatalog` | `GET /admin/v1/catalog/{capset_id}?all=true` | Backend performs URL escaping and normalizes the three protocol entries |
| `InvokeCapability` | `POST /capsets/{capset_id}/connect/{instance_id}/{service_id}/{method}` | Forward the JSON request to OctoBus and return the JSON response |

The same catalog endpoint also provides `?format=md` (`text/markdown`, rendered
by OctoBus `RenderCatalogMarkdown`). During sandbox injection, agent-compose
uses `?format=md&grpc=true` to render capability instructions into the guest
(see [sandbox / scheduler injection](#sandbox--scheduler-injection)).

OctoBus catalog structure: `?all=true` returns three parallel arrays: `grpc`,
`mcp`, and `connect_rpc`; each method appears once in each array. gRPC entries
include routing metadata for `x-octobus-capset` and `x-octobus-instance`.
`service_id` remains a catalog/UI field, but it is not part of the current
OctoBus gRPC routing metadata. agent-compose merges entries by join key
`(service_id, instance_id, method_full_name)` into `CapabilityMethod`, using
`endpoints` to represent gRPC / MCP / Connect entry types. `endpoints` are for
UI display only and do not include the OctoBus address.

## Data-Plane Forwarding: gRPC Only

Guest connects to `CAP_GRPC_TARGET`, makes gRPC calls by `method_full_name`,
carries sandbox credential metadata, and includes the target instance from the
injected capability guide markdown:

```text
x-capability-sandbox-token: <CAP_TOKEN>
x-octobus-instance: <instance_id>   # provided by guest
```

The deprecated `x-capability-session-token` metadata key remains accepted only
as a compatibility fallback.

Boundary: **capset is the sandbox-level isolation boundary enforced by
capproxy; instance is routing inside the capset and is selected by the guest.**
OctoBus instance ids are globally unique and already identify the service.
capproxy handles each stream:

```text
1. Look up in-memory index by token -> (sandbox, allowed_capsets, managed scope)
2. Validate the complete guest-provided x-octobus-capset declaration is in the sandbox's allowed_capsets
3. Resolve the daemon-wide target for an unqualified declaration, or the named project target for a qualified declaration; strip the qualifier before forwarding
4. Reflection methods (grpc.reflection.*): require only x-octobus-capset and pass through
5. Business methods:
     - require x-octobus-instance from the guest
     - inject the token from the selected OctoBus target
     - forward to OctoBus daemon
```

OctoBus business methods require `x-octobus-capset` and `x-octobus-instance`
(`findGRPCExposedMethod`). capset is enforced by capproxy, while instance is
provided by the guest from the injected capability guide.

Implementation notes:

- gRPC server uses `UnknownServiceHandler` + raw passthrough codec and streams
  frames bidirectionally to OctoBus daemon (`grpc.NewClient` + raw codec).
- token -> sandbox binding uses in-memory index
  `token -> (sandbox_id, capset_ids)`: rebuilt from existing sandboxes at
  startup, incrementally maintained on sandbox create/stop.
- capproxy validates the complete `x-octobus-capset` declaration belongs to the sandbox binding set,
  requires guest `x-octobus-instance` for business calls, and injects the
  OctoBus token.
- For unqualified capsets, OctoBus addr / token are read from `ConfigStore`
  during forwarding. For qualified capsets, the current managed agent
  definition supplies the selected project server URL / token. In both cases,
  only the real capset ID is forwarded upstream.
- Auth and isolation: capset set is bound to the sandbox; guest can choose only
  within the bound set. instance is routing inside a capset and must be
  specified by the guest for business calls. `CAP_TOKEN` is an
  agent-compose-issued sandbox credential used only to resolve sandbox ->
  capset binding. It cannot access OctoBus. OctoBus token stays server-side and
  does not enter the guest.

## Sandbox / Scheduler Injection

Capability injection has two steps because lifecycle timing differs: env/tags
must be merged into the create request before DB creation, while capability
guide markdown (MPI catalog) can be written only after the sandbox directory
exists. Both steps are driven by `capset_ids` and are used by both work sandboxes
and scheduler runs.

**Capability injection is best-effort. Any step failure must not block sandbox /
scheduler creation or execution.** Capabilities are additive and should not couple
sandbox/scheduler survival to OctoBus availability, especially for automatic
scheduler runs. Failures are recorded as sandbox events + logs, and capability
problems are left to runtime where capproxy forwards gRPC errors to the agent.
Capset validity is checked on the control plane, where frontend uses
`ListCapabilitySets` for selection, not during creation.

**Step 1: `BuildGatewaySandboxVars(capset_ids)` before DB creation**
locally generates env items and tags without calling OctoBus or validating
capsets:

```text
CAP_GRPC_TARGET=<deployment-fixed guest-reachable proxy address>
CAP_TOKEN=<new uuid per sandbox>   # secret
```

Tag: one `capset=<capset_id>` per capability set. Preconditions are only that at
least one capset is selected and `CAP_GRPC_TARGET` is configured. If the latter
is missing, capability injection is skipped and a warning is recorded; creation
is not blocked.

**Step 2: `writeCapabilityGuide(sandbox, capset_ids)` after the shared
Workspace Provisioner has established `ready` and before the selected runtime
driver starts,
best-effort.** For each
capset, call OctoBus
`GET /admin/v1/catalog/{capset_id}?format=md&grpc=true` to render capability
guide markdown, then write it to the **sandbox MPI catalog**
`<sandboxDir>/runtime/mpi/catalog.md` (mounted in the guest as
`/data/runtime/mpi/catalog.md`). `agent-compose-runtime`
(`runtime/javascript`) `readMpiContext` reads this catalog and injects it as
**high-priority context** into the agent system prompt: Codex receives it through
`config.developer_instructions`; Claude receives it through `systemPrompt`
(preset `claude_code` + `append`). Therefore, once a sandbox is created, the
agent knows available capabilities as soon as it starts without having to cat
the file itself. Rendered content includes each gRPC method, its `x-octobus-*`
metadata (capset / instance), and guidance to use server reflection to obtain
descriptors. The guest uses this to include `x-octobus-capset` and
`x-octobus-instance` when calling. It does not include OctoBus address or token
and uses only the `grpc` section. **If OctoBus is
unreachable or rendering fails, record an event and continue; sandbox/scheduler
starts normally.**

Coverage: all five current guest runners receive the composed `systemContext`,
which contains the MPI catalog. Codex and Claude use native system/developer
context channels; Gemini and OpenCode prepend it to the user prompt, and Pi
passes it through an appended system-prompt file.

Timing constraint: env injection runs before `Store.CreateSandbox` and returns
values that are merged into the create request, when sandbox directory does not
exist yet. Markdown writing must wait until after `CreateSandbox` creates the
directory and before the selected runtime driver mounts it; otherwise the path may not
exist or the file may miss the mount.

The current v2 `AgentSpec.capset_ids` field carries these declarations through
project revision, managed agent definition, scheduler execution, and sandbox
creation. There is no live v1 `CreateSessionRequest` control-plane message.

Agent definitions, sandbox creation, and schedulers all retain capability set
selection. `AgentSpec.capset_ids` is persisted in project revision and
`project_agent.spec_json` data, copied into the derived `AgentDefinition` and
`SchedulerSummary`, and then written into sandbox bindings during creation.

`capset_ids` may contain both unqualified entries such as `dev` and qualified
entries such as `internal/dev`. The sandbox persists the complete declarations
as its authorization set. Project re-apply does not add or remove authorization
from a running sandbox, but subsequent calls resolve a qualified server from the
current managed agent definition, so URL and token updates take effect without
recreating that sandbox. This matches the managed configuration behavior used
for project MCP servers. See the project-scoped design for the complete failure
and compatibility semantics.

Injection chain:

| Stage | Responsibility |
| --- | --- |
| `SandboxRPCBridge.createSandbox` / `SchedulerSessionRunner.Ensure` | Receive `capset_ids`; call step 1 before DB creation and step 2 after DB creation |
| `BuildGatewaySandboxVars` (step 1) | Locally generate `CAP_GRPC_TARGET` / `CAP_TOKEN` env items + `capset` tags, without calling OctoBus |
| `Store.CreateSandbox` | Persist merged `EnvItems` and tags, create sandbox directory |
| shared Workspace Provisioner | Establish `ready`; initial provisioning may clone/copy, while a ready resume leaves the workspace untouched |
| `writeCapabilityGuide` (step 2) | Render capability guide markdown into sandbox MPI catalog `runtime/mpi/catalog.md` (guest `/data/runtime/mpi/catalog.md`) |
| runtime driver | Inject `sandbox.EnvItems` into guest and mount workspace/runtime directories |
| `agent-compose-runtime` (guest) | `readMpiContext` reads catalog and injects Codex / Claude system prompt |

## Frontend

Settings page "Capability Gateway":

- `GetCapabilityGatewayConfig` / `UpdateCapabilityGatewayConfig` edit
  `addr`, the capset invocation token, and the OctoBus admin token.
- `GetCapabilityStatus` probes connection status and capability count.

Sandbox creation and scheduler:

- Use `ListCapabilitySets` to select capability sets and submit `capsetIds`.

## Error Handling

| Scenario | Behavior |
| --- | --- |
| OctoBus not configured (`addr` empty) | `GetCapabilityStatus` returns `configured=false` |
| OctoBus connection failure | Return `ok=false` and error summary |
| Control-plane OctoBus returns non-2xx | Return Connect error with HTTP status |
| Control-plane `GetCapabilityCatalog` capset not found | not found / invalid argument |
| Injection-stage OctoBus unreachable / markdown render failure | **Does not block**: record sandbox event + log; sandbox/scheduler is still created and runs best-effort |
| Data-plane business call missing `x-octobus-instance` | gRPC `FailedPrecondition`; guest must include `x-octobus-instance` |
| Data-plane method / instance not exposed by capset | OctoBus gRPC status is passed through |
| Data-plane OctoBus returns gRPC status | Status code / message are passed through |

Errors returned to frontend must not leak private network parameters. HTTP
client uses timeout.

## Implementation Tasks

Backend:

1. Add `capability_gateway` table to `ConfigStore` with single row `addr`,
   `token`, and `admin_token`, plus `Get` / `Save`.
2. Proto: add `GetCapabilityGatewayConfig` /
   `UpdateCapabilityGatewayConfig` to `SettingsService`; add `capset_ids` to
   `AgentSpec` and sandbox request shapes; add three
   `CapabilityService` RPCs. Regenerate Go / TS.
3. Control-plane provider depends on `ConfigStore` and reads `addr` /
   `admin_token` on every call.
4. Data-plane capproxy: read OctoBus addr / token from `ConfigStore`; maintain
   token -> sandbox in-memory index; validate guest `x-octobus-capset` belongs
   to sandbox binding; require guest `x-octobus-instance` for business calls;
   run a dedicated gRPC listener.
5. Two-step injection shared by work sandboxes and scheduler runs:
   `BuildGatewaySandboxVars` before DB creation to generate
   `CAP_GRPC_TARGET` / `CAP_TOKEN` env + `capset` tags; `writeCapabilityGuide`
   after DB creation and before VM start to render capability guide markdown
   with `?format=md&grpc=true` into sandbox MPI catalog
   `runtime/mpi/catalog.md`, which `agent-compose-runtime` injects into Codex
   / Claude system prompt.

Frontend:

6. Wire settings page to `GetCapabilityGatewayConfig`,
   `UpdateCapabilityGatewayConfig`, and `GetCapabilityStatus`.
7. Wire sandbox creation, agent definition, and scheduler to
   `ListCapabilitySets`, submitting `capsetIds`.

Tests:

8. Control plane: unconfigured, connection failure, capsets normalization,
   catalog normalization, capset not found.
9. Data plane: validate guest capset belongs to sandbox binding; require and
   pass through guest instance; reflection stream validates capset; inject
   OctoBus token; missing instance ->
   `FailedPrecondition`; OctoBus routing errors are passed through.
10. Injection consistency and tolerance: scheduler and work sandbox share the same
    injection result; capability guide markdown is written into MPI catalog
    (`runtime/mpi/catalog.md`, not workspace) and includes method instance
    routing metadata; **when OctoBus is unreachable or markdown render fails, sandbox
    and scheduler still create and run successfully (best-effort, non-blocking).**
