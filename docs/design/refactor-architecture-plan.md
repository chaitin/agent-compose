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

The target architecture borrows the useful parts of DDD without enforcing a
strict layered framework. The key idea is to make package boundaries match
business ownership and dependency direction.

Target dependency direction:

```text
cmd/agent-compose
  -> internal/agentcompose/bootstrap
    -> internal/agentcompose/transport
      -> internal/agentcompose/application
        -> internal/agentcompose/domain
    -> internal/agentcompose/infrastructure
      -> internal/agentcompose/application ports
      -> internal/agentcompose/domain
```

Allowed dependency rules:

- `domain` must not import Connect, Echo, proto generated packages, SQL,
  Docker clients, runtime drivers, or process config packages.
- `application` may import `domain` and define ports/interfaces needed for use
  cases.
- `transport` may import proto/connect/echo and `application`, but should not
  contain domain rules or SQL.
- `infrastructure` may import external libraries and implement application
  ports.
- `bootstrap` wires dependencies and registers handlers.
- Cross-domain communication should go through application-level interfaces or
  explicit domain value objects, not by reaching into another package's
  persistence structs.

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

    application/
      session/
        service.go
        reconcile.go
        stream.go
        ports.go
      project/
        service.go
        validate.go
        apply.go
        reconcile_agents.go
        reconcile_schedulers.go
        dryrun.go
        ports.go
      run/
        service.go
        coordinator.go
        preparation.go
        ports.go
      loader/
        service.go
        manager.go
        engine.go
        executor.go
        dispatcher.go
        ports.go
      exec/
        service.go
        ports.go
      config/
        service.go
        ports.go
      image/
        service.go
        ensure.go
        ports.go
      llm/
        service.go
        facade.go
        runtime_config.go
        ports.go
      capability/
        service.go
        gateway.go
        ports.go
      dashboard/
        overview.go
        ports.go

    domain/
      session/
        model.go
        status.go
        tags.go
        events.go
      project/
        model.go
        source.go
        revision.go
        agent.go
        scheduler.go
        validation.go
      run/
        model.go
        status.go
        transition.go
      loader/
        model.go
        trigger.go
        run.go
        event.go
        schedule.go
      workspace/
        model.go
      agent/
        definition.go
      image/
        model.go
        policy.go
      capability/
        model.go
      llm/
        model.go

    infrastructure/
      persistence/
        sqlite/
          db.go
          migrations.go
          session_repository.go
          project_repository.go
          loader_repository.go
          config_repository.go
          agent_definition_repository.go
          topic_event_repository.go
      runtime/
        provider.go
        driver_adapter.go
      image/
        docker_backend.go
        oci_backend.go
        auto_backend.go
      llm/
        client.go
        config_loader.go
      capability/
        provider.go
        proxy.go
      eventbus/
        loader_bus.go
        event_dispatcher.go

    shared/
      ids/
      jsontime/
      errors/
      response/
```

This structure is a target direction, not a single massive patch. During
migration, transitional packages and aliases are acceptable if they keep the
change reviewable.

## Boundary Details

### Bootstrap

`bootstrap` replaces the current broad `agentcompose.Setup(di)` role.

Responsibilities:

- Register constructors in the dependency injector.
- Build repositories, application services, transport handlers, and background
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
- Mapping between proto messages and application commands/results.
- Mapping application/domain errors to Connect or HTTP errors.

Transport packages must not:

- Open SQL transactions.
- Call runtime drivers directly.
- Start scheduler runs directly.
- Normalize compose specs except by calling application services.
- Own project/loader/session state transitions.

### Application

Application packages own use cases. They are the place where current behavior
should be preserved while dependencies become explicit.

Examples:

- `application/project.Service.ApplyProject`
- `application/project.Service.ValidateProject`
- `application/project.Service.DownProject`
- `application/run.Service.RunAgent`
- `application/loader.Manager.RunNow`
- `application/session.Service.CreateSession`
- `application/image.Service.EnsureProjectAgentImages`

Application packages should depend on interfaces defined close to the use case.
For example, `application/project` can define:

```go
type ProjectRepository interface {
    UpsertProject(ctx context.Context, record project.Record) error
    SaveRevision(ctx context.Context, revision project.Revision) error
    ListAgents(ctx context.Context, projectID string) ([]project.Agent, error)
}
```

Infrastructure implements these interfaces. The application should not depend
on SQLite-specific types.

### Domain

Domain packages own business concepts and local rules.

Good candidates:

- Session status constants and transition helpers.
- Project records, revision identity, scheduler/agent model.
- Project run status transitions.
- Loader trigger kinds, run status, concurrency/session policy rules.
- Stable ID and hash rules when they are domain identity rules.

Domain packages should avoid:

- Proto types.
- SQL row scanners.
- Connect errors.
- Echo handlers.
- Docker or sandbox clients.
- Environment variable loading.

### Infrastructure

Infrastructure packages own concrete adapters:

- SQLite repositories.
- Runtime driver provider.
- Docker/OCI image backends.
- LLM client implementation.
- Capability provider/proxy.
- Loader bus and event dispatcher implementations.

Infrastructure may import external packages freely, but it should implement
interfaces needed by application services rather than being called directly
from transport.

## Initial File Mapping

The first pass can be mostly mechanical. Suggested mapping:

| Current file group | Target area |
| --- | --- |
| `service.go` setup/registration | `internal/agentcompose/bootstrap` |
| `service.go` Connect methods | `internal/agentcompose/transport/connectv1` and `connectv2` |
| `model.go` session/workspace/cell models | `domain/session`, `domain/workspace` |
| `session_*.go` | `application/session`, `transport/connectv1`, or `infrastructure/persistence/sqlite` depending on role |
| `store.go` | `infrastructure/persistence/sqlite/session_repository.go` plus session domain models |
| `config_store.go` | split into config, workspace, agent definition, capability repositories |
| `project_store.go` | `domain/project`, `domain/run`, `infrastructure/persistence/sqlite/project_repository.go` |
| `project_service.go` | `transport/connectv2/project_handler.go` plus `application/project/*` |
| `project_schema.go` | `application/project/validate.go` or `domain/project/validation.go` |
| `project_agent_runner.go` | `application/run` or `application/project` depending on orchestration ownership |
| `project_down.go` | `application/project/down.go` plus transport wrapper |
| `run_coordinator.go` | `domain/run/transition.go` plus `application/run/coordinator.go` |
| `run_service.go` | `application/run/service.go` plus `transport/connectv2/run_handler.go` |
| `run_preparation.go` | `application/run/preparation.go` |
| `exec.go`, `exec_service.go` | `application/exec`, `transport/connectv2/exec_handler.go` |
| `loader_model.go` | `domain/loader/model.go` |
| `loader_store.go` | `infrastructure/persistence/sqlite/loader_repository.go` |
| `loader_engine.go` | `application/loader/engine.go` |
| `loader_manager.go` | `application/loader/manager.go` |
| `loader_service.go` | `transport/connectv1/loader_handler.go` plus `application/loader/service.go` |
| `loader_run_executor.go` | `application/loader/executor.go` |
| `loader_event_dispatcher.go`, `loader_events.go`, `loader_bus.go` | `application/loader` and `infrastructure/eventbus` |
| `webhook*.go` | `transport/http` plus `application/loader` event ingestion |
| `proxy.go` | `transport/http/proxy_routes.go` |
| `workspace.go`, `workspace_routes.go` | `domain/workspace`, `application/config`, `transport/http` |
| `llm_client.go`, `llm_config.go`, `llm_facade.go`, `llm_runtime_config.go` | `application/llm`, `infrastructure/llm`, `transport/http` |
| `image_*.go` | `application/image`, `infrastructure/image`, `transport/connectv2/image_handler.go` |
| `capability_*.go` | `domain/capability`, `application/capability`, `infrastructure/capability`, `transport/connectv1` |
| `dashboard_overview.go` | `application/dashboard` |
| `event_dispatcher.go`, `topic_event_*.go` | `domain` event models, `application`, `infrastructure/persistence/sqlite` |

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

### Phase 2: Split Transport Handlers

Move Connect methods out of the broad `Service` type into service-specific
handler structs.

Example:

- `SessionHandler` delegates to `application/session.Service`.
- `ProjectHandler` delegates to `application/project.Service`.
- `LoaderHandler` delegates to `application/loader.Service`.

At this phase, application services may still wrap existing implementations.
The purpose is to break the "one type implements every service" pattern first.

Success criteria:

- Generated proto packages unchanged.
- Registered Connect route paths unchanged.
- Existing integration tests still hit the same endpoints.

### Phase 3: Extract Domain Models and Rules

Move pure models, constants, and state helpers into domain packages.

Start with low-risk areas:

- Loader status/trigger/session policy constants.
- Project run status transition helpers.
- Session status/tag/env value objects.

Avoid changing JSON tags, status strings, stable IDs, hash inputs, or timestamp
semantics.

Success criteria:

- Snapshot or JSON-based tests continue to pass.
- No proto or SQL imports appear in domain packages.

### Phase 4: Extract Application Services

Move use cases out of transport and persistence files.

Priority order:

1. `application/project`
2. `application/loader`
3. `application/run`
4. `application/session`
5. `application/exec`
6. `application/image`
7. `application/llm`
8. `application/config`, `capability`, `dashboard`

This phase should introduce explicit ports where needed, but should avoid
changing behavior.

Success criteria:

- Use cases can be tested without Connect request/response wrappers.
- Transport files contain mapping and delegation, not business orchestration.
- No storage SQL is called directly from transport.

### Phase 5: Split Persistence

Move SQL-backed store methods into repository files grouped by aggregate.

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

- `transport -> application -> domain`
- `infrastructure -> application ports`
- `bootstrap -> all implementation packages`
- `cmd -> bootstrap`
- `application -> domain`

Forbidden:

- `domain -> proto`
- `domain -> connectrpc.com/connect`
- `domain -> github.com/labstack/echo`
- `domain -> database/sql`
- `domain -> pkg/driver` or runtime implementations
- `transport -> infrastructure/persistence/sqlite`
- `transport -> runtime drivers`
- `transport -> image backends`
- `infrastructure -> transport`

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
4. Move one domain model group at a time.
5. Extract one application use case group at a time.
6. Split one repository group at a time.

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
