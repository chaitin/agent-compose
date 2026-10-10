# Control-plane transport contract

## Scope

Connect RPC is the only control-plane transport for operations performed by the
CLI or Web UI against a running daemon. Local compose and `.env` authoring,
daemon process lifecycle, and the data-plane and bootstrap exceptions below do
not violate this rule.

The authoritative RPC inventory is
`agentcompose.v2.File_agentcompose_v2_agentcompose_proto`. The transport
contract test enumerates that descriptor, starts an in-process daemon, invokes
every procedure over a real Connect transport, and rejects the generated
`UnimplementedXServiceHandler` error. It covers unary, server-streaming, and
bidirectional-streaming methods with bounded contexts. Consequently, the
service and method counts in this document are informational rather than a
second source of truth. At the time this contract was introduced the descriptor
contained 12 services and 73 methods.

An RPC may deliberately return `CodeUnimplemented` for an unsupported compiled
or runtime capability. That is a business result from a concrete handler and is
distinct from the generated fallback error `<fully-qualified RPC> is not
implemented`, which fails the contract test.

## CLI command to RPC matrix

Aliases have the same transport behavior as their canonical command. A
fallback listed below is an RPC-to-RPC compatibility fallback; none accesses a
daemon controller or store directly.

| CLI operation | Connect RPC(s) | Stream | Fallback or local responsibility |
| --- | --- | --- | --- |
| `project up`, `up` | `ProjectService.ApplyProject` | no | Reads compose and `.env` locally before RPC |
| `project ls` | `ProjectService.ListProjects` | no | none |
| `project down`, `down` | `ProjectService.RemoveProject` | no | local compose only selects the project; the server-side removal stops sandboxes and disables schedulers |
| `agent ls`, `ls` | `ProjectService.GetProject` | no | none |
| `run` | `ResourceService.ResolveID`, `RunService.RunAgent`, `StreamAgentRun`, or `AttachAgentRun` | server or bidi | streaming mode selects the matching RPC |
| `exec` | `ResourceService.ResolveID`, `ExecService.Exec`, `StreamExec`, or `AttachExec` | server or bidi | streaming mode selects the matching RPC |
| `logs` | `GetProject`, `ListRuns`, `ListRunEvents`, `FollowRunLogs` | optional server | non-follow compatibility stays on listed RPCs |
| `scheduler ls`, `inspect` | `GetProject`, `GetScheduler`, `ListSchedulers`, `ListSchedulerEvents`, `GetSchedulerRun` | no | reference resolution uses `ResourceService.ResolveID` where required |
| `scheduler invoke`, `trigger` | `InvokeScheduler`, `RunScheduler`, `StartSchedulerRun` | no | none |
| `scheduler runs`, `logs` | `ListSchedulerRuns`, `ListSchedulerEvents`, `StreamSchedulerRuns`, `StreamProjectSchedulerEvents` | optional server | non-follow compatibility stays on listed RPCs |
| `scheduler stop`, `prune` | `StopSchedulerRun`, `PruneSchedulerRuns` | no | none |
| `ps`, `sandbox ls` | `SandboxService.ListSandboxes` | no | none |
| `stop`, `resume`, `rm` and `sandbox` equivalents | `GetSandbox`, `StopSandbox`, `ResumeSandbox`, `RemoveSandbox` | no | none |
| `sandbox prune` | `SandboxService.PruneSandboxes` | no | older-daemon unsupported is reported to the user |
| `stats` | `SandboxService.GetSandboxStats` | no | runtime capability unsupported is a concrete-handler result |
| `images`, `pull`, `build`, `rmi` and `image` equivalents | `ImageService.ListImages`, `PullImage`, `BuildImage`, `RemoveImage`, `InspectImage` | build is server-streaming | none |
| `cache ls`, `inspect`, `prune`, `rm` | matching `CacheService` RPC | no | none |
| `volume ls`, `create`, `inspect`, `prune`, `rm` | matching `VolumeService` RPC | no | none |
| generic `inspect` | `ResourceService.ResolveID` then the matching project/run/sandbox/image/cache/volume RPC | no | none |
| `config` | none | no | local authoring: parse, validate, normalize, and redact compose data |
| `daemon`, `version`, `status`, `auth` | none | no | process/bootstrap responsibility; `status` and auth discovery use approved bootstrap HTTP endpoints |

## Web operation to RPC matrix

Browser clients use the generated Connect-Web client for these control-plane
capabilities. Page layout is intentionally not part of this stable contract.

| Web operation | Connect service and methods |
| --- | --- |
| Project validation/apply/list/get/remove/watch | `ProjectService.ValidateProject`, `ApplyProject`, `ListProjects`, `GetProject`, `RemoveProject`, `WatchProject` |
| Run start/stream/attach/stop/log/events | `RunService.RunAgent`, `StartAgentRun`, `StreamAgentRun`, `AttachAgentRun`, `StopRun`, `FollowRunLogs`, `ListRunEvents`, `ListSandboxRunEvents` |
| Scheduler configuration, invocation, runs, events, and streams | the scheduler methods on `ProjectService` |
| Sandbox list/get/history/watch/lifecycle/stats | `SandboxService` |
| Interactive execution | `ExecService.AttachExec`; non-interactive execution uses `Exec` or `StreamExec` |
| Image/cache/volume management | `ImageService`, `CacheService`, `VolumeService` |
| Settings and workspace presets | `SettingsService` |
| Capability status and catalog | `CapabilityService` |
| Engine driver/provider capability matrix | `EngineService.GetCapabilities` |
| Dashboard overview and watch | `DashboardService` |
| Control-plane LLM generation | `LLMService.Generate` |
| Polymorphic identifier lookup | `ResourceService.ResolveID` |

## Approved non-Connect HTTP routes

The exact route set is executable policy in `TestDaemonHTTPRouteAllowlist`.
Adding a route requires updating that test and architecture review of its
category. The approved categories are:

| Category | Routes | Reason |
| --- | --- | --- |
| Bootstrap | `GET /api/version`, `GET /api/null`, Health Connect service | daemon discovery, compatibility, and health before a control client is established |
| Webhook/event boundary | `/api/webhooks/:topic`, `/api/webhook-sources*`, `/api/events*` | inbound integration and its event-source/query compatibility surface; it must not grow into general project/run control |
| Jupyter proxy | configured Jupyter base path under `/jupyter/:sandbox` | protocol and browser proxy data plane |
| Workspace file proxy | `/api/agent-compose/workspaces/:workspace/files`, `/upload`, `/download` | binary/file data plane |
| Runtime LLM facade | `/api/runtime/sandboxes/:sandbox/llm/openai/v1/*`, `/anthropic/v1/messages` | provider-compatible workload API, not daemon control |

Static browser entry points may be added only at the process/static-serving
boundary and must be explicitly allowlisted. A project, run, scheduler,
sandbox, image, cache, volume, settings, capability, or dashboard operation is
not eligible for this exception merely because it is called from a browser.

## Change gate

- A proto change is picked up automatically by descriptor traversal; a new RPC
  fails if its daemon route or concrete handler is missing.
- A new HTTP route fails the exact allowlist test.
- A new daemon-facing CLI or Web operation must update the matrices above and
  use a generated Connect client. Reviews should reject imports of daemon
  controllers or stores from `cmd/agent-compose` or browser code.
- Streaming checks use per-RPC bounded contexts and protocol handshakes; they do
  not use sleeps.
