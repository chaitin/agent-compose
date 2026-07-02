# agent-compose Refactor Architecture Plan

## Purpose

This document defines a structure-only refactor plan for the current
agent-compose daemon and CLI codebase.

The refactor goal is not to redesign the product, protocol, runtime behavior,
or persistence model. The goal is to make the existing code easier to
understand, change, and test by moving responsibilities into clearer Go
packages.

Hard requirements:

- Public Connect APIs remain unchanged.
- CLI commands, flags, environment variables, and default behavior remain
  unchanged.
- Existing business logic, state transitions, scheduler behavior, persistence
  formats, runtime-driver behavior, proxy behavior, and error semantics remain
  unchanged unless a later change explicitly documents and tests a behavior
  change.
- Existing proto files and generated clients remain compatible.
- Refactor steps must be small enough that each step can be validated by the
  existing test suite.

Non-goals:

- No DDD rewrite.
- No new storage engine.
- No new runtime abstraction beyond moving current abstractions to clearer
  packages.
- No API version changes.
- No large behavior cleanup hidden inside package moves.

## Current Problems

The largest structural issue is that `pkg/agentcompose` is a single broad Go
package containing most daemon responsibilities. It currently includes API
handlers, application orchestration, domain rules, persistence, runtime
coordination, loader scheduling, image management, LLM facade code, workspace
routes, webhook handling, proxying, and test support in one package.

Observed symptoms:

- `pkg/agentcompose` contains roughly one hundred Go files.
- Several files are over one thousand lines, including `project_service.go`,
  `service.go`, `loader_engine.go`, `loader_store.go`, `loader_manager.go`,
  `llm_config.go`, `project_store.go`, and `exec.go`.
- `Service` in `pkg/agentcompose/service.go` implements many unrelated Connect
  service handlers and holds most daemon dependencies directly.
- Business use cases are coupled to Connect request/response types, storage
  details, runtime details, and response mapping.
- Store types contain domain records, normalization helpers, SQL scanning,
  stable ID generation, and repository behavior in the same files.
- Project application logic directly reconciles managed agent definitions,
  managed schedulers, loader records, image availability, and project run
  state.
- Loader logic mixes scheduling, event handling, runtime execution, project-run
  integration, persistence, and API conversion.
- Many identifiers are unexported only because they live in the same large
  package; package privacy is not expressing architectural boundaries.

The result is low local reasoning. A change to one feature often requires
reading unrelated areas because package boundaries do not show ownership.

## Go Layout Position: `pkg` vs `internal`

In Go, `pkg` is only a convention. It does not restrict imports. Putting code
under `pkg` suggests that other modules may import and rely on it as a public
library API. `internal` is enforced by the Go toolchain and is the better
default for application-owned implementation code.

agent-compose is primarily an executable service and CLI with generated API
clients. Its stable external contracts are:

- Connect/protobuf APIs under `proto/`.
- The TypeScript generated client package under `proto-client/`.
- The CLI behavior from `cmd/agent-compose`.
- Runtime guest SDKs and scripts under `runtime/`.
- Compose file behavior.

Most daemon implementation packages are not intended as stable Go libraries.
Therefore, most current code under `pkg/agentcompose` should move to
`internal/agentcompose`.

Recommended policy:

- Use `internal/` for daemon and CLI implementation details.
- Use `pkg/` only for packages intentionally maintained as reusable Go
  libraries with a stable-ish import surface.
- Keep generated protocol packages outside `internal` because they are an
  integration contract.
- Avoid using `pkg` as a dumping ground for shared code. Shared implementation
  code should usually live under `internal/shared` or a bounded internal
  package.

### Package Classification

Recommended final classification:

| Current package | Recommended location | Reason |
| --- | --- | --- |
| `pkg/agentcompose` | `internal/agentcompose/...` | Daemon control-plane implementation, not a public Go API. |
| `pkg/auth` | `internal/auth` or `internal/agentcompose/transport/auth` | HTTP middleware/login implementation for this daemon. |
| `pkg/config` | `internal/config` | Process/env configuration for this binary. |
| `pkg/dbo` | `internal/dbo` | Database wiring implementation detail. |
| `pkg/health` | `internal/health` | Service health endpoint implementation. |
| `pkg/driver` | `internal/driver` initially | Runtime driver implementations are daemon internals unless explicitly supported as a library. |
| `pkg/capproxy` | `internal/capproxy` initially | Capability proxy server implementation detail. |
| `pkg/imagecache` | `internal/imagecache` initially, or keep `pkg/imagecache` if deliberately reusable | OCI image cache could become reusable, but current usage appears daemon-owned. |
| `pkg/compose` | `pkg/compose` or `internal/compose` | Keep in `pkg` only if compose parsing/normalization is intended for external Go users. Otherwise move internal. |
| `pkg/capability` | `pkg/capability` or `internal/capability` | Keep in `pkg` only if capability catalog/client types are an extension API. |
| `pkg/fxgo` | `internal/fxgo` or eliminate over time | Framework glue and response helpers are implementation details. |
| `proto/...` | unchanged | Generated API contract. |

Conservative migration path:

1. Move only `pkg/agentcompose` into `internal/agentcompose/...` first.
2. Leave other `pkg/*` packages in place temporarily to reduce blast radius.
3. After the core split stabilizes, decide package-by-package whether each
   remaining `pkg/*` package is truly public or should move under `internal`.

## Target Architecture

The target architecture is now domain-first. It still borrows useful DDD ideas,
but it does not prioritize broad horizontal directories such as
`domain/application/infrastructure`. In a Go service, package names should
express business capability, and code for one capability should stay close
unless there is a strong reason to separate it.

Core principles:

- Split by business domain first, then split files inside that domain.
- Keep `transport` and `bootstrap` as cross-domain adapter layers.
- Let domains such as `project`, `loader`, `session`, and `run` own their
  models, services, ports, and repository implementations.
- Do not scatter one business capability across distant horizontal directories
  just to satisfy a layered pattern.
- Most daemon implementation code should end up under
  `internal/agentcompose/<domain>`, not public `pkg/<domain>` packages.

Target dependency direction:

```text
cmd/agent-compose
  -> internal/agentcompose/bootstrap
    -> internal/agentcompose/transport
      -> internal/agentcompose/<domain service>
    -> internal/agentcompose/<domain>
      -> internal/agentcompose/shared
    -> internal/driver, internal/imagecache, internal/config, proto/...
```

Allowed dependency rules:

- A domain package may contain models, services, ports, repository
  implementations, and mappers, but file responsibilities must stay clear.
- Domain model and pure-rule files must not import Connect, Echo, SQL, Docker
  clients, runtime drivers, or process config packages.
- Domain services may depend on same-domain repository interfaces, narrow
  interfaces exposed by other domains, and necessary infrastructure adapters.
- `transport` may import proto/connect/echo and domain services, but must not
  contain business orchestration, SQL, or direct runtime-driver calls.
- `bootstrap` wires dependencies and registers handlers.
- Cross-domain collaboration should go through narrow interfaces or explicit
  value objects, not arbitrary access to another domain's persistence structs.

## Proposed Directory Structure

```text
internal/
  agentcompose/
    bootstrap/
      register.go
      background.go
      wiring.go

    transport/
      connectv1/
        session_handler.go
        kernel_handler.go
        agent_handler.go
        agent_definition_handler.go
        llm_handler.go
        config_handler.go
        loader_handler.go
        dashboard_handler.go
        capability_handler.go
        mapper.go
      connectv2/
        project_handler.go
        run_handler.go
        exec_handler.go
        image_handler.go
        mapper.go
      http/
        proxy_routes.go
        webhook_routes.go
        workspace_routes.go
        runtime_llm_facade_routes.go

    session/
      model.go
      service.go
      repository.go
      sqlite.go
      stream.go
      reconcile.go
      proto_mapper.go

    project/
      model.go
      service.go
      validate.go
      apply.go
      build.go
      reconcile_agents.go
      reconcile_schedulers.go
      dryrun.go
      repository.go
      sqlite.go
      proto_mapper.go

    loader/
      model.go
      service.go
      manager.go
      engine.go
      executor.go
      schedule.go
      event_dispatcher.go
      repository.go
      sqlite.go
      proto_mapper.go

    run/
      model.go
      service.go
      coordinator.go
      preparation.go
      proto_mapper.go

    exec/
      service.go
      proto_mapper.go

    agent/
      definition.go
      service.go
      repository.go
      sqlite.go
      proto_mapper.go

    config/
      env.go
      workspace.go
      service.go
      repository.go
      sqlite.go
      proto_mapper.go

    image/
      model.go
      service.go
      ensure.go
      docker_backend.go
      oci_backend.go
      auto_backend.go
      proto_mapper.go

    llm/
      service.go
      client.go
      config.go
      facade.go
      runtime_config.go
      proto_mapper.go

    capability/
      model.go
      service.go
      gateway.go
      provider.go
      proxy.go
      repository.go
      sqlite.go
      proto_mapper.go

    workspace/
      model.go
      service.go
      routes_mapper.go

    events/
      model.go
      dispatcher.go
      topic_store.go
      repository.go
      sqlite.go

    dashboard/
      overview.go
      aggregator.go
      hub.go

    runtime/
      provider.go
      driver_adapter.go

    shared/
      ids/
      jsontime/
      errors/
      response/
```

This is the target direction, not a single massive patch. Transitional
packages, wrappers, aliases, and same-package file splits are acceptable during
migration when each step is reviewable, testable, and clearly converges toward
domain packages.

## Boundary Details

### Bootstrap

`bootstrap` replaces the current broad `agentcompose.Setup(di)` role.

Responsibilities:

- Register constructors in the dependency injector.
- Build domain services, repositories, transport handlers, and background
  workers.
- Register Connect and HTTP routes on Echo.
- Start background managers.

Compatibility requirement:

- Keep `agentcompose.Setup(di)` as a thin compatibility wrapper until all
  imports are updated:

```go
func Setup(di do.Injector) {
    bootstrap.Setup(di)
}
```

### Transport

Transport packages own external protocol mapping only.

Responsibilities:

- Connect handler structs.
- Echo route registration for HTTP endpoints.
- Request validation that is purely protocol-level.
- Mapping between proto messages and domain service commands/results.
- Mapping domain errors to Connect or HTTP errors.

Transport packages must not:

- Open SQL transactions.
- Call runtime drivers directly.
- Start scheduler runs directly.
- Normalize compose specs except by calling the project domain service.
- Own project/loader/session/run state transitions.

### Domain Packages

Domain packages are the main target of this refactor. Each domain package is
named after a business capability and keeps related code close together.

A domain package may contain:

- `model.go`: domain models, status constants, and value objects.
- `service.go`: primary use-case orchestration for that domain.
- `repository.go`: storage interfaces required by that domain.
- `sqlite.go`: the current SQLite implementation. If it grows too large, it can
  move to a domain-local `sqlite/` subpackage later.
- `proto_mapper.go`: mapping between the domain and proto. If mapping becomes
  complex, it may stay in `transport` instead.
- Use-case files such as `apply.go`, `manager.go`, `coordinator.go`, and
  `schedule.go`.

Domain packages still need file-level boundaries:

- Pure model/rule files must not import proto, connect, echo, SQL, or drivers.
- Service files may orchestrate repositories and other-domain interfaces, but
  must not implement HTTP/Connect behavior.
- SQLite files may import SQL, but must not own business workflows.

### Shared

`shared` is only for small, stable utilities with no clear domain owner:

- ID/hash helpers.
- Time/JSON helpers.
- Error classification.
- Response helpers.

Do not let `shared` become a new dumping ground. Logic with a clear business
owner must stay in that domain.

## Initial File Mapping

The first pass can be mostly mechanical. Suggested mapping:

| Current file group | Target area |
| --- | --- |
| `service.go` setup/registration | `internal/agentcompose/bootstrap` |
| `transport_handlers.go` and Connect methods | `internal/agentcompose/transport/connectv1` and `connectv2` |
| session/workspace/cell models in `model.go` | `internal/agentcompose/session`, `workspace` |
| `session_*.go` and session parts of `store.go` | `internal/agentcompose/session` |
| `config_store.go` | split by responsibility into `config`, `workspace`, `agent`, and `capability` |
| `project_store.go`, `project_service.go`, `project_*` | `internal/agentcompose/project`; project run state may move to `run` |
| `project_schema.go` | `internal/agentcompose/project/validate.go` |
| `project_agent_runner.go` | `internal/agentcompose/run` or `project`, depending on orchestration ownership |
| `project_down.go` | `internal/agentcompose/project/down.go` |
| `run_coordinator.go`, `run_service.go`, `run_preparation.go` | `internal/agentcompose/run` |
| `exec.go`, `exec_service.go` | `internal/agentcompose/exec` |
| `loader_model.go`, `loader_store.go`, `loader_engine.go`, `loader_manager.go` | `internal/agentcompose/loader` |
| `loader_run_executor.go`, `loader_event_dispatcher.go`, `loader_events.go`, `loader_bus.go` | `internal/agentcompose/loader` or `events`, depending on ownership |
| `webhook*.go` | `transport/http`; event ingestion delegates to `events` or `loader` |
| `proxy.go` | `transport/http`; session proxy logic belongs to `session` |
| `workspace.go`, `workspace_routes.go` | `workspace` and `transport/http` |
| `llm_client.go`, `llm_config.go`, `llm_facade.go`, `llm_runtime_config.go` | `llm`; HTTP facade routes stay in `transport/http` |
| `image_*.go` | `image` |
| `capability_*.go` | `capability` |
| `dashboard_overview.go` | `dashboard` |
| `event_dispatcher.go`, `topic_event_*.go` | `events` |

## Migration Phases

### Phase 0: Guardrails

Before moving code:

- Record the current `main` commit.
- Ensure the working tree is clean except unrelated user changes.
- Run a baseline test command and keep the result in the PR description.
- Add this document and use it as the refactor contract.

Recommended baseline:

```bash
task test
task build
```

If full tests are too slow for intermediate commits, each phase should still
run targeted `go test` commands and the final phase must run the project quality
gate.

### Phase 1: Compatibility Shell

Create `internal/agentcompose/bootstrap` and move only setup/wiring code there.

Keep the old package entrypoint:

- `pkg/agentcompose.Setup(di)` remains available.
- Existing `cmd/agent-compose` imports can remain unchanged initially.

Success criteria:

- No public API changes.
- `cmd/agent-compose` starts through the same path.
- Tests pass.

### Phase 2: Transport Handler Shells

Move Connect methods out of the broad `Service` type into service-specific
handler structs.

At this phase, handlers may still delegate to existing `Service` methods. The
purpose is to break the "one type implements every service" pattern first
without moving business logic in the same step.

Success criteria:

- Generated proto packages unchanged.
- Registered Connect route paths unchanged.
- Existing integration tests still hit the same endpoints.

### Phase 3: Seed Domain Packages

Create real domain packages from low-coupling areas instead of continuing only
same-package file splits.

Priority order:

1. `image`
2. `capability`
3. `dashboard`
4. `events`
5. `loader`
6. `project`
7. `session`

Avoid changing JSON tags, status strings, stable IDs, hash inputs, or timestamp
semantics.

Success criteria:

- Each new domain package has a clear public surface.
- Transport depends on domain services or mappers only.
- Pure rule files do not import proto/connect/echo/SQL/drivers.

### Phase 4: Move High-Complexity Domains

Move complex domains such as `loader`, `project`, `session`, and `run`.

Requirements:

- Move models and pure rules first, then service orchestration, then repository
  implementations.
- Each PR moves one responsibility slice from one domain.
- For hotspot files such as `project_service.go`, `loader_manager.go`, and
  `loader_engine.go`, same-package file splits may be used first to reduce
  file size before moving code into domain packages.

Success criteria:

- `pkg/agentcompose` is no longer the main business implementation package.
- Large files keep shrinking.
- Use cases can be tested without Connect request/response wrappers.

### Phase 5: Move Persistence Into Domains

Move SQL-backed store methods into the domain that owns the aggregate.

Important: this is a package and file organization change, not a database
schema change.

Keep:

- Existing DB path behavior.
- Existing table names.
- Existing migrations.
- Existing JSON columns and encoding.
- Existing stable IDs.

Success criteria:

- Existing data directories remain readable.
- Store migration tests pass unchanged.
- Integration tests using temporary DBs pass.

### Phase 6: Move from `pkg/agentcompose` to `internal/agentcompose`

After internal package boundaries are stable, update imports so the daemon uses
`internal/agentcompose`.

Possible compatibility options:

- Delete `pkg/agentcompose` if no external Go imports are supported.
- Keep a temporary deprecated wrapper package if needed for local migration.

Recommended final state:

- No daemon implementation remains in `pkg/agentcompose`.
- `pkg/agentcompose` is removed or contains only a short deprecation wrapper
  during a transition window.

### Phase 7: Reclassify Other `pkg/*` Packages

Evaluate each remaining `pkg/*` package:

- If it is part of a documented external Go library surface, keep it in `pkg`.
- If it is daemon implementation, move it to `internal`.

This should be a separate phase after the core split because it affects many
imports but should not change behavior.

## Package Dependency Rules

These rules should be enforced by code review, and later by an import linter if
needed.

Allowed:

- `transport -> domain service`
- `domain service -> same-domain repository/interface`
- `domain service -> other-domain narrow interface`
- `domain sqlite/repository implementation -> database/sql`
- `bootstrap -> all implementation packages`
- `cmd -> bootstrap`

Forbidden:

- pure model/rule files -> proto/connect/echo/database/sql/drivers
- `transport -> database/sql`
- `transport -> runtime drivers`
- `transport -> image backends`
- domain packages directly accessing another domain's SQLite implementation
- `shared -> concrete business domains`

## Behavior Preservation Checklist

Every refactor PR should explicitly check:

- Connect route paths are identical.
- Proto request/response messages are unchanged.
- CLI output formats are unchanged, especially `--json` output.
- Status strings are unchanged.
- Error codes and important error messages are unchanged.
- DB schema and migration behavior are unchanged.
- Existing data can still be loaded.
- Loader trigger scheduling behavior is unchanged.
- Project apply dry-run output is unchanged.
- Managed loader/agent reconcile behavior is unchanged.
- Project run lifecycle behavior is unchanged.
- Runtime session creation/resume/stop behavior is unchanged.
- Jupyter proxy paths and headers are unchanged.
- Webhook behavior is unchanged.
- LLM facade request/response behavior is unchanged.
- Image ensure/pull/cache behavior is unchanged.

## Testing Strategy

Use the existing test taxonomy from `TESTING.md`.

Minimum per-phase checks:

- Run package-local unit tests for moved packages.
- Run existing integration tests covering changed boundaries.
- Run `task build` after import-path migrations.
- Run `task test` before merging a phase.

Additional recommended checks for this refactor:

- Add lightweight architecture tests or scripts that fail if domain packages
  import forbidden packages.
- Add route registration tests that assert Connect paths are unchanged.
- Add golden tests for important CLI JSON outputs if not already present.
- Add repository compatibility tests using existing fixture DBs if available.

## Review Strategy

Keep PRs small and mechanical.

Recommended PR shape:

1. Add this design document.
2. Move setup/bootstrap without changing behavior.
3. Split one transport handler group at a time.
4. Seed one low-coupling domain package at a time.
5. Move one responsibility slice from a complex domain at a time.
6. Move repository implementations into the owning domain.

For each PR:

- State whether it is intended to be behavior-preserving.
- List moved files and new packages.
- List test commands run.
- Call out any unavoidable behavior change separately. Behavior changes should
  not be mixed with mechanical package moves.

## Open Decisions

These decisions should be made before Phase 7:

- Whether `pkg/compose` is a supported Go library API or only an internal parser
  for the daemon and CLI.
- Whether `pkg/capability` is an extension API for external integrations.
- Whether `pkg/imagecache` should become a reusable image-cache library or stay
  daemon-owned.
- Whether `pkg/driver` should ever be imported outside this module. If not, it
  should move to `internal/driver`.
- Whether to introduce an import-boundary linter after the package split.

## Recommended First Concrete Step

Start with a low-risk compatibility step:

1. Add `internal/agentcompose/bootstrap`.
2. Move constructor registration, route registration, and background startup
   from `pkg/agentcompose/service.go` into bootstrap functions.
3. Keep `pkg/agentcompose.Setup(di)` as a wrapper.
4. Do not move business logic yet.
5. Run `go test ./pkg/agentcompose ./cmd/agent-compose` and then `task build`.

This creates the first architectural boundary without changing the runtime
surface area.

After the bootstrap and transport shell steps, the next concrete step is to
seed a low-coupling domain package, preferably `image` or `capability`, and
route the existing transport wrapper through that domain package without
changing behavior.
