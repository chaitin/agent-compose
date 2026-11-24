# Repository Guidelines

## Guiding Principles

Write clean, maintainable code that follows the surrounding codebase conventions.

Prioritize simplicity, correctness, and clear ownership. Avoid speculative abstractions, unnecessary indirection, excessive file splitting, and unrelated refactoring.

When rules conflict, prioritize correctness, security, compatibility, clear boundaries, and the simplest maintainable design. Use judgment rather than applying rules mechanically.

## Simplicity

- Avoid trivial wrappers, one-off helpers, and abstractions that add indirection without improving clarity.
- Prefer concrete types and functions. Introduce interfaces when a real consumer boundary requires substitution or multiple implementations justify the abstraction.
- Extract functions when they improve readability, reuse, or testability, not merely to shorten a function.
- Reuse the standard library and existing dependencies rather than reinventing functionality.
- Avoid speculative layers, registries, plugin systems, and catch-all packages.

## Project Map

This repository contains the agent-compose daemon and CLI control plane. It applies declarative agent projects, runs AI coding agents in isolated sandboxes, schedules work, and exposes Connect/HTTP, runtime LLM facade, and Jupyter proxy APIs.

| Location | Responsibility |
|---|---|
| `cmd/agent-compose` | CLI, composition, startup, and shutdown |
| `pkg/agentcompose/app` | Use-case and component lifecycle orchestration |
| `pkg/agentcompose/api` | Connect handlers and transport mapping |
| `pkg/agentcompose/adapters`, `pkg/agentcompose/proxy`, `pkg/storage`, `pkg/driver` | Infrastructure and external boundaries |
| `pkg/schedulers`, `pkg/runs`, `pkg/sandboxes`, `internal/projects` | Domain ownership |
| `pkg/compose` | Compose schema, validation, normalization, and source loading |
| `pkg/resources`, `pkg/cache`, `pkg/skills` | Resource resolution, runtime cache policy, and skill resolution |
| `ent` | ent schema (the Go model of the database), generated DAO, codegen, and transaction helper |
| `proto` | API definitions and generated Go/Connect clients |
| `runtime` | Guest JavaScript runtime, SDK, and Jupyter kernel metadata |
| `test/e2e` | Opt-in host-daemon and image lifecycle tests |
| `docs/spec`, `docs/pages` | Architecture specifications and published documentation source |

Package location alone does not establish domain ownership. Follow existing ownership boundaries.

## Design Documents

- Design documents are informative, not authoritative. Source code and tests define current behavior.
- Do not treat proposed, planned, or documented architecture as existing requirements unless explicitly requested.
- Verify documented assumptions against the implementation before acting.
- Do not expand the task to implement design proposals or resolve unrelated discrepancies.

## Code Organization

### Package ownership and dependencies

- Put business logic in the package that owns the concept. Do not put domain logic in CLI, transport, or generic utility packages.
- `cmd/agent-compose` is a composition boundary. Keep reusable behavior in its owning package.
- `pkg/agentcompose/api` translates transport requests and responses; it must not own business rules, persistence, or driver policy.
- `pkg/agentcompose/app` coordinates use cases and lifecycle, not domain rules or infrastructure implementations.
- Infrastructure implementations belong in adapters, storage, proxy, or driver packages as appropriate.
- Create a new package only when it owns a cohesive responsibility not adequately owned by an existing package.
- Keep declarations unexported by default. Expose only the API required by actual consumers.
- Keep interfaces close to their consumers and implementations close to the state or external system they own.
- Dependencies should point toward domain and capability packages, not toward transport or composition code. Domain packages must not import `pkg/agentcompose/*`.

### Files and functions

- Organize files by cohesive responsibility, not arbitrary size or one-type-per-file rules.
- Keep related private types, constants, errors, and helpers alongside the behavior they support.
- Avoid catch-all files such as `helpers.go`, `types.go`, or `misc.go`.
- Do not add unrelated responsibilities to oversized files. Extract substantial new behavior into focused files.
- Around 500 lines is a review trigger for handwritten production files. Files exceeding 800 lines require justification. Line count alone is not a reason to split cohesive code.
- Functions approaching 80 lines or exceeding three levels of control-flow nesting are review triggers, not hard limits.
- Prefer guard clauses and clear sequential flows over unnecessary nesting and indirection.

### State and dependencies

- Avoid mutable package-level or process-global business state.
- Create runtime dependencies during composition and pass them explicitly through constructors or function parameters.
- Package-level constants, sentinel errors, compile-time interface assertions, and safe initialization-once read-only values are allowed.
- Make time, randomness, ID generation, environment lookup, and other nondeterministic dependencies explicit when they affect domain behavior or testability.
- Prefer pure functions for deterministic transformations such as validation, normalization, mapping, comparison, and planning.
- Keep I/O and other side effects at explicit boundaries.
- Do not mutate caller-owned inputs or shared collections unless ownership is explicit.

## Correctness and Reliability

### Errors and boundaries

- Handle, return, or deliberately ignore errors with a clear justification.
- Wrap errors with context using `%w`; classify them with `errors.Is` or `errors.As`, not error-message strings.
- Convert domain errors to HTTP or Connect status codes at the transport boundary.
- Log errors at the boundary that handles or terminates them. Avoid duplicate logging.
- Never expose secrets, credentials, authorization headers, or unfiltered environment values in errors or logs.
- Keep transport, persistence, and runtime-driver types at their respective boundaries. Domain behavior must not depend directly on these representations.
- Explicitly handle missing values, unknown enums, validation, and compatibility defaults when mapping boundary types.
- Validate configuration during startup or component construction. Define defaults in one place.
- Changes to persisted data, public APIs, protobuf fields, or enum values must consider backward compatibility.

### Context, concurrency, and cleanup

- I/O, blocking, and long-running operations must accept and propagate `context.Context`. Do not replace an active request context with `context.Background()`.
- Every goroutine must have a clear owner, termination condition, cancellation path, and error-handling path.
- Components that start background work must provide a way to stop and wait for it during shutdown.
- Make shared-state synchronization explicit. Do not assume execution is serialized.
- Do not hold locks during unbounded I/O.
- Release resources, subscriptions, response bodies, files, and timers on every exit path.

## Change Scope

- Keep changes focused on the requested task.
- Do not refactor unrelated code or introduce unrelated behavior and API changes.
- Preserve existing behavior and compatibility unless the task requires otherwise.
- Do not overwrite or discard existing user changes.
- Report adjacent issues rather than expanding the scope unnecessarily.

## Tests

- Bug fixes must include regression tests that fail without the fix.
- Test observable behavior, including meaningful validation, failure, cancellation, and timeout cases as applicable.
- Tests must be deterministic. Avoid test-order dependencies, brittle sleeps, precise timing assumptions, public network dependencies, and developer-machine state.
- Prefer small fakes and stubs over interaction-heavy mocks. Mock interactions only when they are part of the contract.
- Use table-driven tests when cases share the same behavior and assertion structure.
- Use `t.Helper()`, `t.TempDir()`, and `t.Cleanup()` where appropriate.
- Run focused tests during development and integration tests for boundaries crossed.
- Run `go test -race` for relevant concurrency or shared-state changes.
- Follow `TESTING.md` for test shapes, coverage, and integration/E2E requirements.

Choose tests based on the behavior changed and boundaries crossed. Do not run expensive or environment-dependent tests without a reasonable need.

## Build and Deployment

- Build targets, compiled capabilities, runtime defaults, and deployment configuration are defined by source code and build/deployment files. Inspect them rather than relying on hardcoded documentation values.
- Use repository-defined Task targets rather than constructing build commands manually.
- A compiled capability does not imply runtime availability or health.
- Unsupported capabilities must fail explicitly without creating or modifying runtime state.
- Keep the base Compose configuration deployable with published images and separate local development configuration into `docker-compose.override.yml`.
- Keep deployment-specific configuration in Compose and environment configuration rather than duplicating application defaults.
- Changes to Compose topology, installer behavior, images, volumes, networking, or runtime deployment must preserve existing deployment compatibility unless explicitly intended.
- Follow existing Docker and KVM deployment boundaries. Do not introduce privileged access, host mounts, or networking changes without understanding their security and lifecycle implications.
- Verify deployment changes against the actual Compose files, installer, and deployment tests.

## Database and ORM

- SQLite (default) and PostgreSQL are both first-class backends. The physical
  schema is owned solely by the released, paired SQL migration chains under
  `pkg/storage/sqlite/migrations` and `pkg/storage/postgres/migrations`.
- ent is the only ORM for application data access. `ent/schema` mirrors the
  released schema one-to-one; `ent/dao` is generated from it. Read `ent/README.md`
  before changing persistence code.
- Never edit `ent/dao` or any other generated file by hand. Change `ent/schema`,
  run `task generate:ent`, and commit the regenerated DAO. `task check:ent` fails
  when the committed DAO is stale.
- A model change that adds, removes, or retypes a column or index also needs
  paired SQLite/PostgreSQL migrations with the same version and name, and must
  keep existing data roots upgradable in place. Never edit a released migration.
- Use the generated ent client for single-table reads, writes, upserts, and
  deletes. Raw SQL is allowed only where ent cannot express the semantics
  (read-model joins/aggregates/CTEs, row locks, migrations, bulk copy); mark each
  such statement with `// Kept as SQL: <reason>`.
- For entities whose physical primary key is composite, never call
  `UpdateOneID`/`DeleteOneID`: constrain every key column with predicates. The
  schema comments identify those entities.
- Do not place ent's automatic migration or `Client.Schema.Create` in the
  runtime path. Existing databases are migrated only by the released SQL chains.
- Keep `pkg/storage/database/ent_schema_test.go` green: it verifies the ent
  schema against the migrated SQLite schema.

## Documentation and Generated Sources

- Edit public manuals under `docs/pages`. Keep English and `docs/pages/zh-CN` aligned.
- Run `task docs:build`. Never edit `build/pages` directly.
- Changes to `pkg/compose/spec.go` must update the YAML manuals and pass the documentation build.
- Change API definitions in `proto/**/*.proto` and regenerate with `task generate` or `task generate:proto`. Never edit generated files manually.
- Change the data model in `ent/schema` and regenerate with `task generate:ent`. Never edit `ent/dao` manually.
- Change protobuf contracts and their implementations in the same PR. Follow the existing Proto module and release workflow.

## Commits and Pull Requests

- Create PRs as drafts using `gh pr create --draft`. Keep them in draft until ready for review and CI.
- Draft PRs skip CI; marking them ready triggers applicable checks.
- Follow scoped Conventional Commit style, e.g. `feat(cli): load project environment files`.
- Keep PRs focused. Include a problem/solution summary, test commands, and documentation/test checklist.
- Update documentation for behavior, configuration, API, or workflow changes.
- Link relevant issues when available.
- Never commit credentials, private endpoints, certificates, local runtime data, or repository-local build artifacts.

## Quality Gates

- Follow `TESTING.md` for test shapes and coverage requirements.
- Use the standard Go testing package and Vitest for runtime npm packages.
- Use `task lint` rather than reproducing its package list or flags. `Taskfile.yml` is the source of truth.
- Use repository-defined Task targets for build, test, deployment validation, and documentation generation.

### Primary commands

```bash
task prepare
task lint
task build
task test
task test:deploy
task docs:build
```

Use focused tests during iteration. Boundary-crossing changes require the relevant integration or E2E tests. Run `task test:runtime-smoke` only on a prepared Linux/KVM host when real BoxLite or Microsandbox runtime coverage is required.

Before handoff, run `task lint`, `task build`, and `task test` when applicable, plus `task docs:build` for public documentation or Compose schema changes.

If a gate cannot be run, report the exact command and reason. Never imply an unrun check passed.
