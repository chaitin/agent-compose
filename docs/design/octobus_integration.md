# OctoBus Integration Implementation Spec

Chinese version: [../zh-CN/design/octobus_integration.md](../zh-CN/design/octobus_integration.md)

OctoBus repository: [chaitin/OctoBus](https://github.com/chaitin/OctoBus).

agent-compose integrates published OctoBus capability sets, injects selected
capability sets into work sessions and automation tasks, and provides capability
call entry points to guests. agent-compose is the only integration boundary:
frontend and guest do not connect to OctoBus directly.

## Architecture

Two paths:

```text
Control plane (frontend read, Connect/HTTP): frontend -> agent-compose CapabilityService -> OctoBus /admin/v1/*
Data plane (agent call, gRPC):              guest agent -> agent-compose capproxy -> OctoBus daemon gRPC
```

- The control plane is read-only: connection status, capability set list, and
  capability catalog.
- The data plane exposes only gRPC: guest calls capabilities through
  agent-compose transparent proxy. MCP / REST are not exposed to the guest.
- Both the control-plane provider and data-plane capproxy dynamically read
  OctoBus connection config from `ConfigStore`.

UI shows only product concepts:

| OctoBus concept | agent-compose UI name |
| --- | --- |
| capset | Capability set |
| method | Callable capability |
| service | Integration source |
| instance | Connection instance |

## Configuration

### OctoBus Connection

Page-configured, stored in DB, dynamically read at runtime:

- Table `capability_gateway`, single row: `addr`, `token` (secret).
- `ConfigService` provides `GetCapabilityGatewayConfig` /
  `UpdateCapabilityGatewayConfig`; token is redacted when read back.
- Non-empty `addr` means enabled.

```proto
message CapabilityGatewayConfig {   // read response, never returns token
  string addr = 1;
  bool token_set = 2;               // whether token is set
}

message UpdateCapabilityGatewayConfigRequest {
  string addr = 1;
  string token = 2;                 // empty string means clear
}
```

When backend accesses OctoBus, it injects `Authorization: Bearer <token>` if
token exists. Token stays server-side only; it is not returned to frontend in
plaintext, written into session metadata, injected into guest env, or logged.

### Data-Plane Proxy Entry

Deployment-fixed, bound once at startup, not page-configured:

- Proxy bind address: listen address of agent-compose internal transparent gRPC
  proxy server.
- Guest-reachable proxy address: `CAP_GRPC_TARGET` injected into sessions,
  determined by container / network mapping.
- Runtime gRPC capability calls require both `CAP_GRPC_LISTEN` and
  `CAP_GRPC_TARGET` to be set when the daemon starts. `CAP_GRPC_LISTEN` starts
  the local capability gRPC proxy; `CAP_GRPC_TARGET` is the guest-reachable
  address injected into new sessions. If either is missing, the control plane
  can still show OctoBus as connected, but sessions with selected capsets will
  not receive usable runtime capability connection variables. Restart
  agent-compose and create a new session after changing these values.

## Control-Plane CapabilityService

```proto
service CapabilityService {
  rpc GetCapabilityStatus(GetCapabilityStatusRequest) returns (CapabilityStatusResponse);
  rpc ListCapabilitySets(ListCapabilitySetsRequest) returns (ListCapabilitySetsResponse);
  rpc GetCapabilityCatalog(GetCapabilityCatalogRequest) returns (GetCapabilityCatalogResponse);
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
```

Backend behavior:

| RPC | OctoBus API | Handling |
| --- | --- | --- |
| `GetCapabilityStatus` | `GET /admin/v1/status` | Return `configured` / `ok` / `status` / `service_count` |
| `ListCapabilitySets` | `GET /admin/v1/capsets` | Normalize to UI capability set list |
| `GetCapabilityCatalog` | `GET /admin/v1/catalog/{capset_id}?all=true` | Backend performs URL escaping and normalizes the three protocol entries |

The same catalog endpoint also provides `?format=md` (`text/markdown`, rendered
by OctoBus `RenderCatalogMarkdown`). During session injection, agent-compose
uses `?format=md&grpc=true` to render capability instructions into the guest
(see [session / loader injection](#session--loader-injection)).

OctoBus catalog structure: `?all=true` returns three parallel arrays: `grpc`,
`mcp`, and `connect_rpc`; each method appears once in each array. gRPC entries
include a complete metadata triple: `x-octobus-capset`,
`x-octobus-service`, and `x-octobus-instance`. agent-compose merges entries by
join key `(service_id, instance_id, method_full_name)` into
`CapabilityMethod`, using `endpoints` to represent gRPC / MCP / Connect entry
types. `endpoints` are for UI display only and do not include the OctoBus
address.

## Data-Plane Forwarding: gRPC Only

Guest connects to `CAP_GRPC_TARGET`, makes gRPC calls by `method_full_name`,
carries session credential metadata, and includes the target instance from the
injected capability guide markdown:

```text
x-capability-session-token: <CAP_TOKEN>
x-octobus-service: <service_id>     # provided by guest
x-octobus-instance: <instance_id>   # provided by guest
```

Boundary: **capset is the session-level isolation boundary enforced by
capproxy; service / instance is routing inside the capset and is selected by the
guest.** capproxy handles each stream:

```text
1. Look up in-memory index by token -> (session, allowed_capsets)
2. Validate guest-provided x-octobus-capset is in the session's allowed_capsets
3. Reflection methods (grpc.reflection.*): require only x-octobus-capset and pass through
4. Business methods:
     - guest already provides x-octobus-service / x-octobus-instance -> pass both through
     - guest does not provide them -> look up catalog by (capset, method_full_name):
       unique match -> fill automatically; zero matches -> NotFound; multiple matches -> FailedPrecondition
     - inject OctoBus token read from ConfigStore
     - forward to OctoBus daemon
```

OctoBus business methods strictly require all three metadata values:
`x-octobus-capset` / `x-octobus-service` / `x-octobus-instance`
(`findGRPCExposedMethod`). capset is enforced by capproxy, while service /
instance are provided by the guest or filled by capproxy.

Implementation notes:

- gRPC server uses `UnknownServiceHandler` + raw passthrough codec and streams
  frames bidirectionally to OctoBus daemon (`grpc.NewClient` + raw codec).
- token -> session binding uses in-memory index
  `token -> (session_id, capset_ids)`: rebuilt from existing sessions at
  startup, incrementally maintained on session create/stop.
- capproxy validates `x-octobus-capset` belongs to the session binding set,
  injects OctoBus token, and passes through guest `x-octobus-service` /
  `x-octobus-instance`.
- Normalized catalog used for filling missing routing metadata is cached by
  capset with TTL; after expiry, the next resolution pulls again.
- OctoBus addr / token are read from `ConfigStore` during forwarding.
- Auth and isolation: capset set is bound to the session; guest can choose only
  within the bound set. service / instance is routing inside a capset and may be
  specified by the guest. `CAP_TOKEN` is an agent-compose-issued session
  credential used only to resolve session -> capset binding. It cannot access
  OctoBus. OctoBus token stays server-side and does not enter the guest.

## Session / Loader Injection

Capability injection has two steps because lifecycle timing differs: env/tags
must be merged into the create request before DB creation, while capability
guide markdown (MPI catalog) can be written only after the session directory
exists. Both steps are driven by `capset_ids` and are used by both work sessions
and loader runs.

**Capability injection is best-effort. Any step failure must not block session /
loader creation or execution.** Capabilities are additive and should not couple
session/loader survival to OctoBus availability, especially for automatic loader
scheduling. Failures are recorded as session events + logs, and capability
problems are left to runtime where capproxy forwards gRPC errors to the agent.
Capset validity is checked on the control plane, where frontend uses
`ListCapabilitySets` for selection, not during creation.

**Step 1: `buildCapabilityGatewaySessionVars(capset_ids)` before DB creation**
locally generates env items and tags without calling OctoBus or validating
capsets:

```text
CAP_GRPC_TARGET=<deployment-fixed guest-reachable proxy address>
CAP_TOKEN=<new uuid per session>   # secret
```

Tag: one `capset=<capset_id>` per capability set. Preconditions are only that at
least one capset is selected and `CAP_GRPC_TARGET` is configured. If the latter
is missing, capability injection is skipped and a warning is recorded; creation
is not blocked.

**Step 2: `writeCapabilityGuide(session, capset_ids)` after
`prepareSessionWorkspace` and before `StartSessionVM`, best-effort.** For each
capset, call OctoBus
`GET /admin/v1/catalog/{capset_id}?format=md&grpc=true` to render capability
guide markdown, then write it to the **session MPI catalog**
`<sessionDir>/runtime/mpi/catalog.md` (mounted in the guest as
`/data/runtime/mpi/catalog.md`). `agent-compose-runtime-js`
(`runtime/javascript`) `readMpiContext` reads this catalog and injects it as
**high-priority context** into the agent system prompt: Codex receives it through
`config.developer_instructions`; Claude receives it through `systemPrompt`
(preset `claude_code` + `append`). Therefore, once a session is created, the
agent knows available capabilities as soon as it starts without having to cat
the file itself. Rendered content includes each gRPC method, its `x-octobus-*`
metadata (capset / service / instance), and guidance to use server reflection to
obtain descriptors. The guest uses this to include `x-octobus-capset`,
`x-octobus-service`, and `x-octobus-instance` when calling. It does not include
OctoBus address or token and uses only the `grpc` section. **If OctoBus is
unreachable or rendering fails, record an event and continue; session/loader
starts normally.**

Coverage: Codex and Claude receive `mpiContext` in system prompt. Gemini runner
does not currently consume `mpiContext`; this is a known gap and is out of scope
for this phase.

Timing constraint: env injection runs before `Store.CreateSession` and returns
values that are merged into the create request, when session directory does not
exist yet. Markdown writing must wait until after `CreateSession` creates the
directory and before `StartSessionVM` mounts it; otherwise the path may not
exist or the file may miss the mount.

Proto field:

```proto
message CreateSessionRequest {
  repeated string capset_ids = 10;
}
```

Agent definition, session creation, and loader all save capability set
selection. `capset_ids` is added to `AgentDefinition`,
`CreateAgentSessionRequest`, `CreateLoaderRequest`, `UpdateLoaderRequest`,
`LoaderSummary`, and `LoaderDetail`, and is persisted as
`agent_definition.capset_ids` / `loader.capset_ids`.

Injection chain:

| Stage | Responsibility |
| --- | --- |
| `SessionRPCBridge.createSession` / `loader_manager.go` loader run | Receive `capset_ids`; call step 1 before DB creation and step 2 after DB creation |
| `buildCapabilityGatewaySessionVars` (step 1) | Locally generate `CAP_GRPC_TARGET` / `CAP_TOKEN` env items + `capset` tags, without calling OctoBus |
| `Store.CreateSession` | Persist merged `EnvItems` and tags, create session directory |
| `prepareSessionWorkspace` | Populate workspace with git clone / file copy |
| `writeCapabilityGuide` (step 2) | Render capability guide markdown into session MPI catalog `runtime/mpi/catalog.md` (guest `/data/runtime/mpi/catalog.md`) |
| runtime driver | Inject `session.EnvItems` into guest and mount workspace/runtime directories |
| `agent-compose-runtime-js` (guest) | `readMpiContext` reads catalog and injects Codex / Claude system prompt |

## Frontend

Settings page "Capability Gateway":

- `GetCapabilityGatewayConfig` / `UpdateCapabilityGatewayConfig` edit
  `addr` / `token`.
- `GetCapabilityStatus` probes connection status and capability count.

Session creation and loader:

- Use `ListCapabilitySets` to select capability sets and submit `capsetIds`.

## Error Handling

| Scenario | Behavior |
| --- | --- |
| OctoBus not configured (`addr` empty) | `GetCapabilityStatus` returns `configured=false` |
| OctoBus connection failure | Return `ok=false` and error summary |
| Control-plane OctoBus returns non-2xx | Return Connect error with HTTP status |
| Control-plane `GetCapabilityCatalog` capset not found | not found / invalid argument |
| Injection-stage OctoBus unreachable / markdown render failure | **Does not block**: record session event + log; session/loader is still created and runs best-effort |
| Data-plane method not in capset, when guest did not specify instance and fill lookup misses | gRPC `NotFound` |
| Data-plane guest did not specify instance and method has multiple instances | gRPC `FailedPrecondition`; guest must include `x-octobus-service` / `x-octobus-instance` |
| Data-plane OctoBus returns gRPC status | Status code / message are passed through |

Errors returned to frontend must not leak private network parameters. HTTP
client uses timeout.

## Implementation Tasks

Backend:

1. Add `capability_gateway` table to `ConfigStore` with single row `addr` and
   `token`, plus `Get` / `Save`.
2. Proto: add `GetCapabilityGatewayConfig` /
   `UpdateCapabilityGatewayConfig` to `ConfigService`; add `capset_ids` to
   `CreateSessionRequest`, agent definition, and loader messages; add three
   `CapabilityService` RPCs. Regenerate Go / TS.
3. Control-plane provider depends on `ConfigStore` and reads `addr` / `token`
   on every call.
4. Data-plane capproxy: read OctoBus addr / token from `ConfigStore`; maintain
   token -> session in-memory index; validate guest `x-octobus-capset` belongs
   to session binding; pass through guest `x-octobus-service` /
   `x-octobus-instance`; when missing, fill from normalized catalog cache by
   capset; run a dedicated gRPC listener.
5. Two-step injection shared by work sessions and loader runs:
   `buildCapabilityGatewaySessionVars` before DB creation to generate
   `CAP_GRPC_TARGET` / `CAP_TOKEN` env + `capset` tags; `writeCapabilityGuide`
   after DB creation and before VM start to render capability guide markdown
   with `?format=md&grpc=true` into session MPI catalog
   `runtime/mpi/catalog.md`, which `agent-compose-runtime-js` injects into Codex
   / Claude system prompt.

Frontend:

6. Wire settings page to `GetCapabilityGatewayConfig`,
   `UpdateCapabilityGatewayConfig`, and `GetCapabilityStatus`.
7. Wire session creation, agent definition, and loader to
   `ListCapabilitySets`, submitting `capsetIds`.

Tests:

8. Control plane: unconfigured, connection failure, capsets normalization,
   catalog normalization, capset not found.
9. Data plane: validate guest capset belongs to session binding; pass through
   guest service / instance; fill unique service / instance when missing;
   reflection stream validates capset; inject OctoBus token; method not in
   capset -> `NotFound`; missing instance with multiple matches ->
   `FailedPrecondition`.
10. Injection consistency and tolerance: loader and work session share the same
    injection result; capability guide markdown is written into MPI catalog
    (`runtime/mpi/catalog.md`, not workspace) and includes method service /
    instance; **when OctoBus is unreachable or markdown render fails, session
    and loader still create and run successfully (best-effort, non-blocking).**
