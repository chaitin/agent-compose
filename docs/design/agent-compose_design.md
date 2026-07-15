# agent-compose Architecture

Chinese version: [../zh-CN/design/agent-compose_design.md](../zh-CN/design/agent-compose_design.md)

This document describes the agent-compose architecture currently implemented in
the codebase, including the daemon and CLI design that has already landed.
Earlier refactoring plans, phase plans, and acceptance checklists are no longer
kept as design documents.

The current code facts are anchored by these entry points:

- CLI and daemon entrypoint: `cmd/agent-compose/main.go`
- Daemon service registration: `pkg/agentcompose/app/app.go`
- Compose parsing and normalization: `pkg/compose/`
- v2 API: `proto/agentcompose/v2/agentcompose.proto`
- Project/run persistence: `pkg/storage/configstore/project_store.go`,
  `pkg/storage/configstore/run_coordinator_store.go`; shared storage helpers in
  `pkg/storage/`
- Jupyter proxy: `pkg/agentcompose/proxy/proxy.go`
- Loader runtime and scheduling: owner helpers in `pkg/loaders/`; daemon
  orchestration in `pkg/agentcompose/app/loader_controller.go` and
  `pkg/agentcompose/adapters/loader_session_runner.go`
- Domain model helpers: `pkg/model/`
- Project/run owner helpers: `pkg/projects/` and `pkg/runs/`
- Sandbox execution owner helpers: `pkg/sessions/` compatibility lifecycle
  package and `pkg/execution/`
- Standalone frontend image: `agent-compose-ui` repository

## Architecture Goals

agent-compose is an agent/sandbox control plane. Its shape is similar to Docker
Engine + CLI + Compose, while keeping its own domain model for agents,
scheduler, workspaces, runtime drivers, and notebook proxying.

Core boundaries:

- The daemon is the state authority. It owns persistence, scheduler execution,
  runtime lifecycle, Connect APIs, HTTP APIs, and Jupyter proxying.
- The CLI is a daemon client. It reads local `agent-compose.yml`, performs local
  syntax validation and normalization, calls daemon APIs, and renders output.
- `agent-compose.yml` describes projects and agent definitions. It does not
  describe an already running sandbox.
- Web/UI is no longer built into the daemon image or hosted by the daemon
  process. It is deployed as an independent frontend service.
- The v1 session-centric API remains available for the existing Web/UI and
  compatibility clients. The v2 API is the primary path for the CLI and newer
  clients.

```text
CLI / Web / Connect clients
  |
  | Unix socket or HTTP/Connect
  v
agent-compose daemon
  |
  | v1/v2 Connect handlers, HTTP routes, scheduler, store
  v
project / run / loader / sandbox control plane
  |
  | runtime driver
  v
boxlite / docker / microsandbox runtime
  |
  v
guest Jupyter + agent runtime
```

## Process And Transport

`cmd/agent-compose/main.go` uses Cobra to provide a single binary with multiple
subcommands. Running the binary without a subcommand still starts the daemon,
but explicit daemon startup is recommended:

```bash
agent-compose daemon
```

Daemon construction has been split into testable app construction:

- Load `.env` and environment configuration.
- Initialize Echo, structured logging, and DI.
- Register `/api/version`, v1/v2 Connect handlers, webhook/event routes,
  workspace HTTP routes, and Jupyter proxy routes.
- Register the service graph through `agentcompose.Register(di)`.
- Start the loader manager, event dispatcher, capability proxy, and startup
  sandbox reconciliation through `agentcompose.StartBackground(di)`.
- On graceful shutdown, close all listeners and remove the Unix socket file.

The daemon listens on a Unix socket by default:

- `AGENT_COMPOSE_SOCKET` is used when explicitly set.
- Otherwise `$XDG_RUNTIME_DIR/agent-compose.sock` is preferred.
- Otherwise `/var/run/agent-compose.sock` is used.

A TCP HTTP/Connect listener is enabled only when `HTTP_LISTEN` is explicitly
set. CLI connection priority is `--host`, then `AGENT_COMPOSE_HOST`, then the
default Unix socket. `HTTP_LISTEN` is the daemon internal API entrypoint, not
the public browser entrypoint. When it binds a non-loopback address,
configuration loading logs a warning that the listener should be exposed only
on a trusted network or behind the agent-compose-ui server.

```bash
HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon
agent-compose --host http://127.0.0.1:7410 status
```

## CLI Semantics

The CLI does not directly manipulate runtime state, sandbox files, or SQLite
reconciliation logic. It reads and normalizes the local compose file, then calls
daemon v2 APIs.

Current main commands:

- `config`: parse and normalize local `agent-compose.yml`; supports `--json`
  and `--quiet`; does not connect to the daemon.
- `up`: call `ProjectService.ApplyProject`; create or update the project,
  revision, managed agent definitions, and scheduler/loader; does not directly
  create a run or sandbox.
- `down`: call `ProjectService.RemoveProject`; disable managed
  scheduler/loader and stop running sandboxes for the project; preserves project,
  run, and sandbox history by default.
- `ps`: query project, agent, latest run, and running sandbox state.
- `run <agent>`: call `RunService.RunAgentStream` for a manual agent run;
  creates a new sandbox by default, supports reusing an existing sandbox with
  `--sandbox`, stops the runtime after completion by default, and can keep it
  running with `--keep-running`.
- `logs`: inspect run output by project, agent, run id, or sandbox id; supports
  `--follow`.
- `exec`: call `ExecService.ExecStream` inside a running sandbox; target the
  sandbox with positional `<sandbox>`.
- `images`, `image ls`, `pull`, `image pull`, `rmi`, `image rm`,
  `image inspect`: call `ImageService` to manage the daemon image store. The
  default store is selected by daemon `IMAGE_STORE_MODE`.
- `cache ls`, `cache inspect`, `cache prune`, `cache rm`: call `CacheService`
  to list, inspect, dry-run, and explicitly remove daemon runtime cache items.
  The CLI never reads or deletes daemon cache paths directly.
- `inspect <project|agent|run|sandbox>`: inspect project-related objects.
  `inspect session` remains a deprecated compatibility alias.

## `agent-compose.yml` Model

Compose parsing lives in `pkg/compose`. The normalized output is used for local
`config` output, spec hashing, and daemon apply.

Example:

```yaml
name: review-project

variables:
  OPENAI_API_KEY:
    value: ${OPENAI_API_KEY}
    secret: true

workspace:
  provider: git
  url: https://github.com/org/repo.git
  branch: main

agents:
  reviewer:
    provider: codex
    model: gpt-5
    image: ghcr.io/org/agent-runtime:latest
    driver:
      boxlite:
        kernel: s3://bucket/kernel
    env:
      REVIEW_MODE: strict
    scheduler:
      enabled: true
      sandbox_policy: sticky
      triggers:
        - name: hourly
          cron: "0 * * * *"
          prompt: "Review the latest workspace state."
        - event:
            topic: git.push
          prompt: "Review changes from the incoming event."
          sandbox_policy: new

```

Docker projects can opt into isolated named bridge networks:

```yaml
name: networked-project

networks:
  frontend: {}
  backend:
    driver: bridge

agents:
  api:
    networks: [frontend, backend]
    expose: ["8080"]
  web:
    networks: [frontend]
    ports: ["127.0.0.1:18080:8080"]
  worker:
    networks: [backend]
```

Named networks are supported only by Docker agents and currently use the
`bridge` driver. Agents can resolve peers by agent name or by the unique
`<agent>-<sandbox-short-id>` alias. An agent without an explicit attachment is
placed on an implicit project `default` network. The first attachment is the
primary network and supplies the default gateway.

`expose` writes Docker port metadata; it is not a firewall rule and does not
publish a host port. `ports` explicitly publishes TCP ports and defaults
short-form bindings to `127.0.0.1`; an omitted host port asks Docker to allocate
one dynamically. Port-only configuration without named networks continues to
use the existing single bridge.

Any effective named-network attachment adds a project bridge beyond the
daemon/sandbox baseline. This multi-network mode requires a local Linux Docker
Engine over a Unix socket and an agent-compose daemon running natively or in a
container with `network_mode: host`. The default bridge-mode Compose deployment
can still validate and store such a project, but starting its sandbox fails
before any project network or container is created. Projects without named
networks preserve the existing shared single-bridge behavior. The legacy
singular `network` field has been removed; `networks` is the only project
network configuration entry point.

The same scheduler can also declare a loader script directly with inline QJS:

```yaml
agents:
  reviewer:
    provider: codex
    image: ghcr.io/org/agent-runtime:latest
    scheduler:
      script: |
        scheduler.interval("hourly-review", function hourlyReview() {
          return scheduler.agent("Review the latest workspace state.");
        }, 3600000);

        function main(payload) {
          return { ok: true, payload };
        }
```

For CLI-authored compose files, the script may instead use an explicit source:

```yaml
agents:
  reviewer:
    scheduler:
      script:
        url: ./scripts/reviewer-scheduler.js
```

Scheme-less relative and absolute paths, `file://`, `http://`, and `https://`
are supported. `config` and `up` resolve the source on the CLI host and replace
it with an inline snapshot before hashing or sending the v2 request. The daemon,
v2 API, stored revisions, and loader runtime continue to accept script text
only; a URL is not a runtime import and is fetched again only by a later
`config` or `up` invocation.

Normalization rules:

- If `name` is empty, it is derived from the compose file directory.
- Agent map keys must be stable identifiers. Output is sorted by agent name.
- Driver is a one-of shape: `boxlite`, `docker`, or `microsandbox`. When
  omitted, the default is `docker`.
- `firecracker` may appear in the schema, but current normalization returns
  unsupported.
- Triggers support `cron`, `interval`, `timeout`, and `event`. Each trigger must
  specify exactly one type.
- `scheduler.sandbox_policy` accepts `new` or `sticky` and defaults to `new`.
  A trigger may set `sandbox_policy` to override the scheduler default.
- Sticky scheduler runs are scoped by loader and trigger. Repeated runs of one
  trigger reuse its sandbox, while different triggers do not share sandboxes.
  Scheduler script calls outside a trigger callback use the loader-level sticky
  sandbox. Inline scripts may continue to override individual calls with
  `scheduler.agent(prompt, { sandboxPolicy: "..." })`.
- `scheduler.script` is either an inline QJS scalar or an explicit mapping with
  the single non-empty field `url`. URL content is normalized into the same
  inline managed-loader `script` snapshot. Blank inline scripts are unset;
  blank URL content is an error.
- `scheduler.script` and non-empty `scheduler.triggers` are mutually exclusive.
  Scalar values are never auto-detected as URLs. `scheduler.script_file`,
  `import` / `require`, bundling, authentication headers, and background refresh
  are not supported.
- URL fetches have a 10-second total timeout, at most five redirects, and a
  1 MiB decoded-content limit. Files must resolve to regular files and content
  must be UTF-8. HTTP(S) requires 2xx and rejects userinfo, unsupported redirect
  schemes, and HTTPS-to-HTTP downgrade.
- `${NAME}` is read from the CLI process environment or explicitly injected
  environment. Missing variables produce field-path errors. Empty variable values
  are valid.
- Values marked `secret: true` participate in the normalized spec and hash, but
  are redacted in YAML/JSON display output.
- The spec hash is computed from canonical JSON and is insensitive to YAML/JSON
  field ordering.

Workspace providers currently supported during project run preparation:

- `local`: materialize a relative path under the project source path into a file
  workspace snapshot.
- `git`: generate a Git workspace snapshot that is cloned during the sandbox's
  one-time workspace provisioning.

## API Boundaries

### v1 Connect API

The v1 API is the stable interface for the existing Web/UI and compatibility
clients. The daemon currently registers:

- `SessionService`
- `KernelService`
- `AgentService`
- `AgentDefinitionService`
- `LLMService`
- `ConfigService`
- `LoaderService`
- `DashboardService`
- `CapabilityService`

v1 still covers session, cell, agent event, global env, workspace config,
loader, dashboard overview, and capability management.

### v2 Connect API

The v2 API is for project/run/image/exec workflows:

- `ProjectService`
  - `ValidateProject`
  - `ApplyProject`
  - `GetProject`
  - `ListProjects`
  - `RemoveProject`
  - `WatchProject` is currently covered only by an unimplemented handler.
- `RunService`
  - `RunAgent`
  - `RunAgentStream`
  - `GetRun`
  - `ListRuns`
  - `StopRun`
- `ExecService`
  - `Exec`
  - `ExecStream`
- `ImageService`
  - `ListImages`
  - `PullImage`
  - `InspectImage`
  - `RemoveImage`
- `CacheService`
  - `ListCaches`
  - `InspectCache`
  - `PruneCaches`
  - `RemoveCache`

`RemoveProject(remove_history=true)` currently returns unimplemented. The
default `down` semantics preserve history. `ImageService` supports both Docker
daemon store and OCI cache store; when request store is `UNSPECIFIED`, the
daemon image store mode selects the backend. `CacheService` is the explicit
cross-sandbox cache lifecycle boundary for OCI physical storage, materialized
images, runtime-derived shared images, and skill artifacts. Sandbox-owned
runtime state belongs to `SandboxService`.

v2 `ProjectSpec` is the wire shape used by CLI and API clients to pass the
current compose state. `AgentSpec.scheduler` contains:

- `enabled`
- declarative `triggers`
- inline QJS `script` (including URL sources already snapshotted by the CLI)

URL authoring syntax is deliberately absent from this wire shape. When the
server receives a v2 `ProjectSpec`, it first converts it back to the
compose YAML shape, then runs the same parse/normalize rules in `pkg/compose`.
`ProjectSpecResponse` also returns the normalized `scheduler.script` to CLI and
API responses. This keeps local `config`, CLI `up`, `ValidateProject`, and direct
v2 API calls on the same field rules, mutual-exclusion rules, and spec hash
calculation.

### HTTP Routes

Besides Connect APIs, the daemon registers these HTTP routes:

- `/api/version`
- webhook / event ingress: `/api/webhooks/:topic`, `/api/events...`
- file workspace helper routes:
  `/api/agent-compose/workspaces/:workspaceID/files`, `upload`, and `download`
- Jupyter proxy: `<JupyterProxyBasePath>/:sessionID` and
  `<JupyterProxyBasePath>/:sessionID/*`. The default base path is `/jupyter`.

The Jupyter proxy implementation lives in `pkg/agentcompose/proxy/proxy.go`.
`GetSessionProxy` returns only proxy entry information; actual HTTP/WebSocket
forwarding is handled by the HTTP routes above. When a sandbox is created
through the v1-compatible API,
`Config.JupyterProxyBasePath` is written into `proxyPath`; the current code
default is `/jupyter`.

## Project Apply And Scheduling

A project is the persisted daemon-side instance of `agent-compose.yml`. Project
id, managed agent id, scheduler id, loader id, and run id are generated by
stable rules.

Current `ApplyProject` behavior:

- Validate and normalize the v2 `ProjectSpec`.
- Persist project revisions as a monotonically increasing sequence. Applying the
  same spec repeatedly without an intervening spec change reuses the current
  revision; returning to a previously seen spec hash after another revision
  creates a new revision.
- Write `project_agent`.
- Reconcile each agent spec into a managed `AgentDefinition`, isolated from
  manual agent definitions by `managed_project_id`, `managed_project_revision`,
  and `managed_agent_name`.
- Compile scheduler definitions into managed Loader/Trigger records.
  Declarative `scheduler.triggers` generate a managed loader script. Inline
  `scheduler.script` is used directly as the managed loader script, and triggers
  returned from loader validation are written to `loader_trigger` and
  `ProjectScheduler.trigger_count`.
- Delete or disable schedulers removed from the spec, then refresh the loader
  manager.
- Do not directly create runs or sandboxes.
- Return `issues` on reconcile failure and avoid leaving half-created enabled
  schedulers that would continue triggering broken agents.

Managed resources modify only agent definitions, loaders, and triggers that
carry managed metadata. Manual resources with the same names are not overwritten
or deleted.

`ValidateProject` and `ApplyProject` use the same scheduler construction path.
Declarative schedulers only receive compose and loader trigger structure
validation. Inline QJS schedulers call existing
`LoaderManager.Validate(ctx, "scheduler", script)`, where the QJS loader engine
evaluates the script and collects triggers registered through
`scheduler.interval`, `scheduler.timeout`, `scheduler.on`, and
`scheduler.cron`. Syntax errors, duplicate trigger names, and invalid
timer/cron/event parameters are converted into project validation issues at the
path `agents.<name>.scheduler.script`.

Reconcile order is conservative: stage `ProjectScheduler` and managed `Loader`
as disabled, replace loader triggers, then enable the loader and scheduler. If
trigger replacement or enablement fails, cleanup runs to avoid leaving an
enabled scheduler whose trigger/script state is inconsistent.

## Run Execution Pipeline

A run is a single agent execution record. It can come from CLI manual run,
scheduler trigger, or future API clients.

`RunService.RunAgent` and `RunAgentStream` share the same coordinator path:

1. Resolve project agent and managed agent definition by project id + agent
   name.
2. Create a pending `project_run` record, storing source, scheduler/trigger,
   prompt, driver, image, and related metadata.
3. Merge runtime environment. Priority from low to high is global env, project
   variables, agent env, then run request env.
4. Prepare local/Git workspace snapshot from project/agent workspace spec.
5. Create a new sandbox or reuse an existing sandbox with `--sandbox`.
6. Write project, agent, run_id, scheduler_id, source, and related tags to the
   sandbox.
7. Mark run as running and call the existing agent executor.
8. Stream start/output/completed events for streaming requests.
9. Persist terminal run state for success, failure, cancellation, workspace
  preparation failure, sandbox startup failure, agent execution failure, and
   stream send failure.
10. Stop the runtime by default while preserving sandbox/run history. The
    `KEEP_RUNNING` cleanup policy keeps the sandbox running.

State queries primarily use project/run relationships in SQLite. Sandbox tags
are used for compatibility queries, `down` stopping project sandboxes, and
file-level debugging.

### Agent system prompt (Phase 1)

`AgentDefinition.system_prompt` is persisted on agent definitions (manual and
managed) and exposed through v1/v2 APIs and the Agents UI. At execution time the
host resolves this field and materializes agent identity for the guest runtime.

Layered prompt model:

1. **Agent Identity** — per-agent `system_prompt` (omitted when empty)
2. **Capabilities (MPI)** — OctoBus capset catalog under `runtime/mpi/catalog.md`
3. **Per-turn task** — user message in `--message-file` (never mixed with identity)

Transport uses a **fixed convention path** under the sandbox state tree:

```text
<sandbox>/state/agents/system-prompts/system-prompt.txt  ->  guest /data/state/agents/system-prompts/system-prompt.txt
```

Resolution paths:

- Managed project runs: `RunService` passes `run.ManagedAgentID` into
  `ExecuteAgentRequest`
- Loader runs: `loaderRunHost.Agent` passes the loader-bound agent definition id
- v1 session chat compatibility path: sandbox tags `source=agent` and `agent_id`

The guest JS runtime (`runtime/javascript`) reads the convention file from
`--state-root`, composes identity + MPI via `buildSystemContext`, and injects the
result into Codex `developer_instructions`, Claude `systemPrompt.append`, or
Gemini user prompt prepend.

See [agent_system_prompt_design.md](agent_system_prompt_design.md) and
[agent-compose-runtime_contract.md](agent-compose-runtime_contract.md) for
the full contract.

## Command Execution And Images

`ExecService` does not create sandboxes. It executes commands only inside an
existing running sandbox. Target lookup can use:

- explicit `sandbox_id`
- explicit `run_id`, then the associated run sandbox
- project/agent selector, which must uniquely match a running sandbox

The default cwd is the guest workspace path `/workspace`, and requests may
override it.

`ImageService` currently has three backend entry points:

- `ListImages` supports reference query, `--all`, and pagination.
- `PullImage` supports platform.
- `InspectImage` returns image details.
- `RemoveImage` supports force and prune children.

Store selection rules:

- Request store `DOCKER_DAEMON` forces Docker daemon.
- Request store `OCI_CACHE` forces daemonless OCI cache.
- Request store `UNSPECIFIED` uses `IMAGE_STORE_MODE`: `docker` forces Docker,
  `oci` forces OCI cache, and `auto` briefly probes Docker daemon, using Docker
  when available and OCI cache otherwise.

OCI cache uses `pkg/imagecache` and go-containerregistry to pull images from a
registry. It does not depend on dockerd, containerd, or Podman. `PullImage` uses
go-containerregistry `remote.Image`, the default keychain, platform selector,
and configured insecure registry list. When platform is not specified, daemon
platform is used. OCI cache stores metadata, OCI Image Layout, BoxLite
materialized layout, and Microsandbox rootfs. OCI image proto fields include
`Store=OCI_CACHE`, `Oci` metadata, repo tags/digests, manifest/config digest,
platform, size, labels, and store status.

OCI cache query and deletion semantics keep the v2 API shape consistent with the
Docker backend, but status comes from cache metadata:

- `ListImages` query matches requested ref, normalized ref, repo tag, repo
  digest, manifest digest, config digest, and cache key, and supports substring
  filtering.
- `InspectImage` uses the same lookup keys. Digest lookup ignores differences in
  `sha256:` prefix form.
- `RemoveImage` deletes only the matched metadata ref by default. When the same
  image identity has multiple refs, `force` is required. `prune_children` does
  not remove blobs in OCI cache and returns a warning. The OCI cache inventory
  treats remaining metadata manifests as required references; explicit cache
  prune can reclaim an unreferenced manifest and then mark/sweep unreachable
  blobs while preserving shared layers. `RemoveImage` and CLI `rmi` do not
  delete materialized or runtime-derived caches.
- Not found, invalid reference, conflict, internal, and unavailable errors map
  to stable Connect codes. Error messages retain operation, image ref, and cache
  endpoint.

## Sandbox and Cache Lifecycle

The ownership boundary is whether an artifact can outlive and be reused by a
sandbox. `SandboxService` owns runtime instances and sandbox-local state;
`CacheService` owns only cross-sandbox reusable artifacts. Volumes, external
workspace sources, run artifacts, and logical image refs remain in their own
domains.

Sandbox ownership is journaled atomically in
`<SANDBOX_ROOT>/.lifecycle/<sandbox-id>.json` before external runtime creation.
The record contains only driver/runtime identity, sandbox-owned paths/resources,
shared cache dependencies, lifecycle state, and completed deletion stages; it
does not persist environment values, tokens, or credentials. Startup safely
backfills missing records for valid historical sandboxes and resumes only
journals already marked `deleting`. Corrupt, unsupported, or path-escaping
ownership is never replaced or guessed during removal.

Removal is serialized with resume, proxy-start, existing-sandbox run binding,
and exec work. It persists deletion intent and the `DELETING` state, optionally
stops a running runtime when force was requested, uses the driver's official
remove API, releases accessories, removes sandbox data/metadata, and finally
removes the journal. Every completed stage is durable and retry-safe. `stop`
preserves resumable state; `rm` removes all sandbox-owned state. Daemon-wide
orphan inventory is opt-in through `sandbox prune --include-orphans`, and any
resource associated with a sandbox record in any project is excluded. Unsafe
or incomplete ownership is display-only even when forced.

Cache lifecycle is explicit and daemon-authoritative. `pkg/cache` owns the
inventory model, filters, reference policy, TTL classification, path safety,
and dry-run/remove rules without importing Connect. Every remover re-inventories
by cache ID under its source lock and validates root containment/symlinks instead
of trusting a request path.

The daemon composes `cache.Source` implementations for OCI physical storage,
materialized images, skill artifacts, and compiled runtime drivers. Ordinary
CGO does not implicitly add a native driver capability. Microsandbox shared
images are inventoried and removed through the SDK. BoxLite v0.9.7 image cache
is inventory-only and `unknown`, because the ABI does not expose a safe image
remove/prune operation; agent-compose never deletes BoxLite internal image
directories directly.

Cache domains:

- `oci-image-store`: OCI image metadata/layout owned by image cache.
- `materialized-image-cache`: runtime inputs derived from images, such as
  BoxLite OCI layouts and Microsandbox rootfs directories under
  `<DATA_ROOT>/image-cache`.
- `runtime-derived-cache`: runtime-driver artifacts under driver homes, such as
  shared runtime images.
- `skill-artifact-cache`: completed content-addressed skill artifacts and
  interrupted temporary/lock entries.

OCI inventory walks the manifest graph, including nested indexes and shared
config/layer blobs. Image metadata is a `REQUIRED` manifest reference. Manifest
removal updates the OCI index first, then mark/sweeps unreachable blobs under
the image-cache lock, so an interrupted sweep leaves discoverable orphan blobs
rather than a dangling index. Materialized image metadata and configured skill
specs are `ADVISORY`; actual dependencies recorded by running or stopped
sandbox journals are `REQUIRED`. Skill resolve uses a root shared lock and
per-artifact lock, while prune uses the root exclusive lock and artifact lock.
Artifact manifests contain source type, content/commit identity, created time,
and last-used time but no source credentials.

Protection is conservative:

- `active`, `referenced`, and `unknown` items are never removed. An unspecified
  reference policy is conservatively `REQUIRED`; `ADVISORY` references do not
  block removal.
- `unused`, `expired`, and `orphaned` items are removed only when the request is
  forced.
- `cache prune` and `cache rm` default to dry-run. Real deletion requires
  `--force`, which authorizes execution but cannot bypass reference protection.

`CACHE_TTL` defaults to `168h`; `0` disables expiration classification and a
negative or invalid value fails startup validation. TTL changes inventory state
only and never causes background/startup deletion. Operators explicitly run
`cache prune --expired --force`; `--older-than` remains an independent filter.

For `up/run`, the `docker` driver ensures the required image is available.
`boxlite` and `microsandbox` project/run preparation does not fail just because
Docker daemon is unavailable. When starting runtimes, they use Docker-first image
resolution: when Docker daemon is available, local Docker materialization is
reused; when Docker is unavailable or the Docker image is missing, OCI cache is
used. BoxLite consumes OCI layout and Microsandbox consumes extracted rootfs.
Docker runtime does not consume OCI cache directly.

## Storage Model

Default data root:

- If `DATA_ROOT` is empty, `$XDG_DATA_HOME/agent-compose` is used.
- If `XDG_DATA_HOME` is empty, `$HOME/.local/share/agent-compose` is used.
- `SANDBOX_ROOT` defaults to `<DATA_ROOT>/sandboxes`. For compatibility, when
  neither root environment variable is set and `<DATA_ROOT>/sessions` is a
  non-empty directory, the daemon uses that directory and emits a warning.
- If `IMAGE_CACHE_ROOT` is empty, it is `<DATA_ROOT>/images`.

Image store configuration:

| Environment variable | Default | Description |
| --- | --- | --- |
| `IMAGE_STORE_MODE` | `auto` | Default store selection mode for `UNSPECIFIED` ImageService requests. Valid values: `auto`, `docker`, `oci`. |
| `IMAGE_CACHE_ROOT` | `<DATA_ROOT>/images` | Daemonless OCI cache root. Stores metadata and OCI Image Layout. Runtime materialization directories live beside this root under `image-cache/`. |
| `CACHE_TTL` | `168h` | Shared cache expiration classification threshold. `0` disables expiration; invalid or negative values fail startup. It never triggers automatic deletion. |
| `IMAGE_INSECURE_REGISTRIES` | empty | Insecure registry host list for OCI cache pulls. Supports comma, semicolon, or newline separators and trims each item. |
| `IMAGE_REGISTRY` | `docker.io` | Default registry for unqualified image references. Also used by runtime smoke default image resolution. |

Typical layout:

```text
data/agent-compose/
├── data.db
├── images/
│   ├── metadata.json
│   └── oci/
├── image-cache/<image-id>/
│   ├── oci/
│   └── rootfs/
├── skills/<content-id>/
└── sandboxes/
    ├── .lifecycle/<sandbox-id>.json
    └── <sandbox_id>/
        ├── metadata.json
        ├── workspace/
        ├── context/
        ├── home/
        ├── runtime/
        ├── state/
        │   ├── cells.json
        │   ├── events.jsonl
        │   └── workspace-provisioning/
        │       └── attempt-<id>/
        ├── logs/
        ├── vm/
        │   └── runtime.json
        └── proxy/
            └── jupyter.json
```

The sandbox directory stores sandbox metadata, workspace, home backing, runtime
shared directory, cell/event timeline, VM state, and proxy state. By default,
`images/` is the OCI cache root; `image-cache/<image-id>/oci` is the BoxLite
materialized OCI layout, and `image-cache/<image-id>/rootfs` is the Microsandbox
materialized rootfs.

`DATA_ROOT/data.db` currently stores:

- global env
- workspace config
- agent definition
- loader / loader trigger / loader binding
- loader run / loader event
- webhook topic event
- project / project_revision / project_agent / project_scheduler / project_run

Project-related tables:

- `project`
- `project_revision`: append-only project spec history keyed by
  `(project_id, revision)`. `spec_hash` identifies content and is indexed for
  lookup, but it is not unique because different revisions may intentionally
  contain identical spec content.
- `project_agent`
- `project_scheduler`
- `project_run`

Managed agent definitions and loaders are isolated through managed metadata
columns on existing tables:

- `managed_project_id`
- `managed_project_revision`
- `managed_agent_name`
- `managed_scheduler_id`, loader only

## Sandbox And Runtime

Sandbox is the low-level runtime lifecycle unit. Three runtime drivers are
currently supported:

- `boxlite`
- `docker`
- `microsandbox`

The default driver is controlled by `RUNTIME_DRIVER`; when empty, it is
`docker`. The default guest image is `debian:bookworm-slim`.

### Compiled Driver Capability

Driver names describe product support, while compiled capability belongs to a
specific binary or image:

| Artifact/profile | Compiled drivers |
| --- | --- |
| macOS native binary (`darwin-docker`) | `docker` |
| Linux native binary (`linux-full`) | `docker`, `boxlite`, `microsandbox` |
| Linux daemon image (`linux-full`; `linux/amd64`, `linux/arm64`) | `docker`, `boxlite`, `microsandbox` |

The platform Task entry dispatches to the macOS Docker-only build on Darwin and
the Linux full build on Linux. `scripts/build-agent-compose-binary.sh` is the
single owner of profile CGO settings, build tags, target metadata, and
`BuildVersion` linker flags; Task, both Dockerfiles, and CI select a profile
instead of rebuilding those arguments. The Linux profile fails its artifact
preflight unless the required BoxLite headers, libraries, and runtime
executables and the Microsandbox executables and shared libraries are present;
the Go build does not start after a preflight failure. The legacy
`build:agent-compose:boxlite` task remains only as a deprecated alias of the
Linux full build.

Native binaries are local and CI verification artifacts; GitHub Release does
not publish standalone per-platform binaries and continues to publish the
installer assets that refer to the multi-architecture images.

`agent-compose --json version` has the stable shape `version`, `os`, `arch`, and
`compiled_drivers`; `/api/version` adds the same build fields to its legacy
envelope, and `status --json` preserves the complete response. The text form of
`agent-compose version` and the status table remain backward compatible.
`CompiledRuntimeDrivers` owns the ordered driver list. It reports build
capability only and does not probe Docker daemon reachability, `/dev/kvm`,
runtime libraries or executables, image access, or driver health. The full
image therefore reports all three drivers even when running on macOS Docker
Desktop without KVM, while its default runtime remains Docker.

Compiled capability is validated before persistence or runtime side effects.
A daemon whose configured default driver is absent from its binary fails during
service graph construction. Create/apply/scheduler entry points reject an
uncompiled selected driver as unsupported before writing project, agent, run,
or sandbox state. Historical objects remain readable, but runtime operations on
an unavailable compiled driver return the same unsupported classification
without rewriting their stored driver or runtime state. Connect surfaces map
this condition to `CodeUnimplemented`, and the CLI preserves its unsupported
exit-code classification. Runtime wrappers remain lazy: constructing the
provider does not initialize BoxLite or Microsandbox and does not touch KVM;
actual runtime availability is established only when an operation starts the
selected driver. Pure compose parsing and normalization remain
platform-independent and continue to accept all product-supported driver names;
compiled validation occurs at daemon-side apply/create/run boundaries.

### Workspace Provisioning And Resume

A Workspace Source is the one-time seed for a sandbox workspace. File workspace
content, a project-local file workspace snapshot, or the Git configuration saved
in the sandbox snapshot is consulted and materialized only by initial
provisioning attempts before that sandbox first reaches `ready`. The resulting
`<sandbox>/workspace` is an independent, writable copy; it is not kept
synchronized with its source.

Sandbox metadata persists workspace provisioning version 1 with one of these
states:

- `pending`: a workspace-bearing sandbox exists but its initial provisioning has
  not completed.
- `failed`: the most recent initial provisioning attempt failed before the
  runtime started; a later start can retry it through `pending`.
- `ready`: provisioning completed, or a legacy sandbox was accepted as already
  initialized. This is terminal for the lifetime of the sandbox.

The allowed transitions are `pending -> ready|failed` and `failed -> pending`.
Every successful transition updates the UTC `updated_at` timestamp. Unknown
versions or states fail closed without changing the workspace. The record is an
internal `workspace_provisioning` field in sandbox `metadata.json`; it is not
exposed through `SandboxSummary`, the public API, or list filters.

The process-level `Provisioner` is shared by every lifecycle path that can start
or restart a workspace-backed sandbox: v1 session create/resume,
`sessions.Lifecycle.ResumeLoaded` and Jupyter `EnsureProxyReady`, loader sandbox
create/sticky resume, and project-run sandbox create/reuse. It reloads persisted
metadata and makes the decision from provisioning state, rather than from a
create/resume flag or workspace directory contents. For `pending` and `failed`,
it materializes into a same-filesystem staging directory, promotes the result,
and persists `ready` before the runtime driver is allowed to start. A
provisioning or state-persistence error prevents the driver from starting.

Calls for the same sandbox ID are serialized in-process with `singleflight`.
The shared operation reloads authoritative metadata before deciding what to do,
and every caller reloads it again before returning, so a stale caller cannot
overwrite the final state. Callers waiting on the same attempt may cancel their
own context without canceling the shared operation; different sandboxes can be
provisioned concurrently. This is not a distributed lock for multiple daemon
processes sharing one `SANDBOX_ROOT`.

For `pending` and retrying `failed` sandboxes, providers receive only a staging
workspace under
`<sandbox>/state/workspace-provisioning/attempt-<id>`. The Provisioner removes
only stale `attempt-*` entries, materializes the complete source there, and
promotes it to the authoritative `<sandbox>/workspace` with a same-filesystem
rename. It requires the stored workspace path to equal that authoritative path
and rejects symlinked sandbox, state, or provisioning roots. Provider code
therefore cannot write the formal workspace before promotion. Sandbox metadata
updates use a synced temporary file and same-directory rename, so a failed save
does not truncate the previous metadata.

Materialization, staging, or promotion failures persist `failed`; a later start
first transitions it back to `pending` and retries from a fresh attempt. If
persisting either the failure state or `ready` also fails, the returned error
preserves the state-write failure, and the runtime still does not start. A
runtime-driver failure after `ready` is different: provisioning remains
`ready`, and the next resume retries only runtime startup.

For `ready`, the Provisioner returns without resolving the workspace config,
reading or inspecting the Workspace Source or sandbox workspace, or invoking a
provider materializer. Stop/resume and daemon restart with the same data and
sandbox roots therefore preserve edits, deletions, generated files, symlinks,
and other workspace state. Later source or config changes, source deletion, and
Git remote changes do not refresh an existing sandbox. Sandbox changes are not
copied back to file/local sources and are not automatically committed or pushed
to Git.

A legacy workspace-bearing sandbox whose metadata has no provisioning state is
migrated directly to `ready` before runtime start. Migration does not resolve
its config, inspect workspace contents, infer from runtime status, or attempt to
rebuild it. New sandboxes remain independent and use the latest source captured
by their current snapshot or config for their own initial provisioning. Source
changes therefore affect newly created sandboxes, not existing `ready`
sandboxes. Removing a sandbox deletes its sandbox directory, including the
writable workspace, provisioning metadata, and any provisioning staging
attempts.

Current sandbox startup flow, used by v1-compatible `CreateSession` and the
other creation paths:

1. Resolve env, tags, workspace id, driver, and guest image from the request.
2. Merge global env and request env.
3. Create the sandbox directory and initialize metadata, VM state, and proxy
   state. A workspace id or snapshot initializes provisioning as `pending`.
4. Run the shared Provisioner. It either establishes and persists `ready`,
   migrates legacy metadata, or returns an error before runtime startup.
5. Prepare managed capability, runtime, state, and home resources.
6. Start runtime through the driver.
7. Mark sandbox as `RUNNING`.
8. Record sandbox-created state. Loader lifecycle events use
   `loader.sandbox.*`; the historical `agent-compose.session.*` topic prefix is
   retained only where the v1 compatibility event bus still emits it.

`ResumeSession` is the v1-compatible resume method. It loads the same sandbox,
runs the shared Provisioner, and starts its runtime; a `ready` workspace is left
untouched. `StopSession` stops runtime and marks the sandbox `STOPPED` without
changing its workspace or provisioning state.

Startup reconciles persisted sandbox runtime state. `GetSession`,
`ListSessions`, and `StopSession` also trigger reconciliation logic.

Default guest paths:

| Host path | Guest path | Purpose |
| --- | --- | --- |
| `<sandbox>/workspace` | `/workspace` | Jupyter root, cell/agent/command cwd |
| `<sandbox>/state` | `/data/state` | Cell artifacts, agent prompt, provider state |
| `<sandbox>/runtime` | `/data/runtime` | Runtime shared resources |
| `<sandbox>/logs` | `/data/logs` | Jupyter logs |
| `<sandbox>/home` or child paths | `/root` or child paths | Tool config and state for Codex, Claude, Gemini, git, and related tools |

For the more detailed mount manifest design, see
[runtime_mount_manifest_design.md](runtime_mount_manifest_design.md) and
[runtime_mount_manifest_driver_specific_design.md](runtime_mount_manifest_driver_specific_design.md).

## Loader Runtime

The current loader runtime is `scheduler`, supporting:

- `interval`
- `timeout`
- `event`
- `cron`

Project compose `scheduler.script` uses the same runtime. Scripts are evaluated
during validate/apply to collect triggers. APIs with side effects or host
dependencies, such as `scheduler.agent`, `scheduler.llm`, `scheduler.exec`,
`scheduler.shell`, `scheduler.event.publish`, and the v1-compatible session RPC
bridge, should be used in
`main()` or trigger callbacks.

`scheduler` is the only product-level global object in the loader QJS
environment. Its responsibilities are trigger registration, lightweight state,
event publishing, and delegating work that needs sandbox capabilities to runtime
sandboxes. The QJS layer is not intended to host complex Node.js workflows, npm
dependencies, or long-running business logic.

When full Node.js capabilities are needed, the current implementation calls
workspace scripts inside the loader sandbox through `scheduler.exec` /
`scheduler.shell`, or uses existing agent and LLM capabilities through
`scheduler.agent` / `scheduler.llm`. Standalone `scheduler.run(file, input,
options)`, runtime workflow context, workflow bridge token, and an
`agent-compose-runtime workflow` subcommand are not part of the current API
contract. Design documents should not present those draft interfaces as
implemented capabilities.

`LoaderManager.Start()` starts the schedule loop and event loop during daemon
background startup.

Main JavaScript APIs:

- `scheduler.log(message, payload)`
- `scheduler.agent(prompt, options)`
- `scheduler.llm(prompt, options)`
- `scheduler.state.get(key)`
- `scheduler.state.set(key, value)`
- `scheduler.state.delete(key)`
- `scheduler.exec(request)`
- `scheduler.shell(script, options)`
- `scheduler.event.publish(topic, payload)`
- `scheduler.interval(...)`
- `scheduler.timeout(...)`
- `scheduler.on(...)`
- `scheduler.cron(...)`

`scheduler.agent` and `scheduler.llm` support `outputSchema` / `schema`. Passing
a `scheduler.z` schema generates JSON Schema and validates the returned value
locally. Passing plain JSON Schema performs JSON parsing.

### Daemon LLM client

`scheduler.llm`, `LLMService.Generate`, and SDK `runtime.llm` delegate to
`LLMClient` in the Go daemon. Configuration is daemon-global:

- `LLM_API_ENDPOINT`, `LLM_API_KEY`, `OPENAI_API_KEY`, `LLM_MODEL`,
  `LLM_TIMEOUT`
- `LLM_API_PROTOCOL`: `responses` (default, OpenAI Responses API) or
  `chat_completions` (OpenAI-compatible Chat Completions; aliases: `chat`,
  `chat_completion`)

Global env from the UI/database overrides process environment for these keys.
The `chat_completions` protocol is for unary text generation only. It does not
create workspace-capable agent sandboxes or grant file, command, or MCP tool
access. With `outputSchema`, it uses prompt guidance and `json_object` instead
of Responses API strict JSON Schema.

Guest agent providers (`codex`, `claude`, `gemini`, `opencode`) remain separate CLI runners
inside guest containers with their own API keys and provider-native session
state.

The loader's primary sandbox lifecycle API is:

- `scheduler.sandbox.createSandbox(request)`
- `scheduler.sandbox.resumeSandbox(request)`
- `scheduler.sandbox.stopSandbox(request)`
- `scheduler.sandbox.getSandbox(request)`
- `scheduler.sandbox.listSandboxes()`
- `scheduler.sandbox.getSandboxProxy(request)`

These methods expose sandbox-shaped request and response JSON while currently
bridging to the v1 lifecycle service internally. The loader also retains these
deprecated v1 `SessionService` aliases:

- `scheduler.session.createSession(request)`
- `scheduler.session.resumeSession(request)`
- `scheduler.session.stopSession(request)`
- `scheduler.session.getSession(request)`
- `scheduler.session.listSessions()`
- `scheduler.session.getSessionProxy(request)`

Method names use lower camel case and also retain PascalCase aliases. New
scripts should use `scheduler.sandbox.*`; calls through `scheduler.session.*`
emit deprecation warnings.

## Frontend Service

The daemon does not host Web/UI static assets and no longer supports
`HTTP_ROOT` / `UI_ROOT` static-root configuration. The daemon main process only
registers API, Connect, webhook/workspace, and Jupyter proxy routes.

The current Docker deployment provides an independent frontend service:

- The `agent-compose-ui` repository builds and publishes the frontend image.
- Compose has two services: `agent-compose` daemon and
  `agent-compose-frontend`.
- The daemon starts by default; the `agent-compose-frontend` service starts
  when the `with-ui` profile is enabled.
- The frontend image runs the agent-compose-ui server behind nginx. Nginx owns
  static assets, access logs, body size limits, timeouts, and WebSocket upgrade
  handling. The UI server owns browser authentication, OAuth, cookie sessions,
  and authenticated reverse proxying.
- `/api/auth/*` and `/oauth/*` belong to the UI server and are no longer
  registered by the daemon.
- The UI server proxies daemon v1/v2 Connect APIs, the health API,
  workspace/event/webhook HTTP APIs, `/jupyter/*` or the configured
  `JUPYTER_PROXY_BASE`, and the compatible `/agent-compose/session/*` paths to
  the daemon.
- Browser traffic should be exposed through the agent-compose-ui server port.
  The daemon `HTTP_LISTEN` used by Compose is an internal API reachable from the
  container network and host loopback; direct external use must be protected by
  a trusted network, reverse proxy, VPN, mTLS, or upper-layer machine
  authentication.

Deployment capability is layered rather than inferred from the image's
compiled drivers:

- `docker-compose.yml` is the complete base deployment. It mounts the Docker
  socket for the default Docker driver, but requests neither privileged mode nor
  `/dev/kvm`.
- `docker-compose.kvm.yml` is an explicit overlay for hosts that will run
  BoxLite or Microsandbox. It adds only `privileged: true` and the `/dev/kvm`
  device to the daemon service.
- `docker-compose.override.yml` is reserved for local-development build
  overrides; it is independent of the KVM overlay.
- On a new installation, the installer checks whether `/dev/kvm` exists and
  persists either `docker-compose.yml` or
  `docker-compose.yml:docker-compose.kvm.yml` as `COMPOSE_FILE` in `.env`.
  Existing explicit selections are preserved. Detection selects topology; it
  does not prove KVM permissions or native runtime health.

Consequently, the published full Linux image can follow the base Compose path
in Docker mode without privilege or KVM. Operators opt into the KVM topology
only when they select BoxLite or Microsandbox at runtime.

The local CLI does not go through the UI server. It uses the daemon Unix socket
by default with socket peer credential trust, and reaches the TCP/HTTP daemon
API only when `--host` or `AGENT_COMPOSE_HOST` is set.

Webhook ingress and browser login are separate security boundaries.
`/api/webhooks/*` business handling, source token checks, and provider signature
checks remain in the daemon handler. The UI server only forwards webhook paths
as an HTTP entrypoint and does not convert webhook tokens into browser cookie
sessions.

For shared playground build, deployment, and verification flow, see
[playground_setup.md](playground_setup.md).

## Key Constraints

- The daemon is the state and reconciliation authority. The CLI does not write
  SQLite or sandbox files directly.
- `agents.<name>` is an agent definition, not a resident runtime.
- `up` manages definitions and scheduler. It is not the same as running an
  agent.
- `run` is a one-shot execution. It stops runtime by default after completion.
- `down` disables managed scheduler/loader and stops running project sandboxes.
  It does not delete history by default.
- The v1 API must remain compatible. The v2 API carries the primary
  project/run/exec/image path.
- Web/UI is deployed as an independent service and is not included in the daemon
  Docker image; browser auth/OAuth belongs to the agent-compose-ui server.
- The daemon TCP API should not be used as the public browser entrypoint.
  Browser cookie/OAuth settings do not protect daemon `HTTP_LISTEN`.
