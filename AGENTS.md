# AGENTS

## Project Map

This repo contains the agent-compose sandbox control plane. It creates, resumes, stops, and proxies isolated notebook runtimes, and exposes agent, loader, LLM, configuration, and workspace APIs.

- `cmd/agent-compose`: CLI, process startup, dependency composition, and shutdown.
- `pkg/agentcompose/app`: use-case and component lifecycle orchestration.
- `pkg/agentcompose/api`: Connect transport handlers and transport/domain mapping.
- `pkg/agentcompose/adapters`, `pkg/agentcompose/proxy`, `pkg/storage`, and `pkg/driver`: external and infrastructure boundaries.
- `pkg/loaders`, `pkg/projects`, `pkg/runs`, and `pkg/sessions`: domain owners. Other `pkg` packages provide focused capabilities; location under `pkg` alone does not make a package a domain owner.
- `proto`: API sources and generated clients. Do not edit generated files manually.
- `proto-client`: published TypeScript protocol client; the web UI is maintained in the separate `agent-compose-ui` repository.
- `runtime`: JavaScript runtime and runtime SDK packages.

## Code Organization

These rules apply to new and modified handwritten code. Existing large files are legacy constraints, not examples to follow; a focused change does not require an unrelated wholesale refactor, but it must not make an already oversized or mixed-responsibility file worse.

### Package ownership and dependency direction

- Put business concepts and rules in the owning domain package under `pkg/` (for example, runs in `pkg/runs`, projects in `pkg/projects`, and sessions in `pkg/sessions`). Do not add domain logic to `cmd/agent-compose`, `pkg/agentcompose/api`, or a generic `util`, `common`, or `helpers` package.
- `cmd/agent-compose` is a composition and process-boundary layer only: CLI parsing, dependency wiring, server startup, and shutdown. Move reusable behavior out of `main.go` into an owning package.
- `pkg/agentcompose/api` translates transport requests and responses and calls domain/application services. It must not own persistence, runtime-driver policy, or reusable business rules.
- `pkg/agentcompose/app` coordinates use cases and component lifecycles. Keep domain rules in domain packages and infrastructure implementations in adapters or storage packages.
- `pkg/agentcompose/adapters`, `pkg/agentcompose/proxy`, and `pkg/storage/*` contain boundary-specific implementations. Domain packages must not import `pkg/agentcompose/*`; dependencies point from entrypoints and adapters toward domain packages, not back toward transport or wiring code.
- Add code to an existing package only when that package clearly owns the concept. Create a focused package when a cohesive group of behavior has an independent responsibility or stable boundary that no existing package clearly owns; having a model, store, or external call by itself is not sufficient reason to create a package.
- Before creating a new package, check that its responsibility can be stated in one sentence and that it owns meaningful behavior rather than merely grouping types. Do not create one-package-per-type trees, speculative layers, or catch-all packages.
- Keep declarations unexported by default. Export only the smallest API required by real cross-package consumers, and expose capabilities rather than internal storage representations or implementation steps.
- Do not use re-exports, type aliases, forwarding wrappers, duplicated types, global callbacks, or a new `common` package to conceal an incorrect dependency direction or import cycle. Revisit ownership and move the boundary instead.

### File boundaries

- Each production file must have one primary responsibility that can be described by its filename. Split unrelated models, handlers, stores, protocol conversion, lifecycle management, and platform implementations into separate files.
- Organize files by capability or responsibility, not by arbitrary size alone. Names such as `run_handler.go`, `run_mapper.go`, and `run_store.go` are examples, not a required layer template; do not create trivial files or abstractions merely to match them. Avoid numbered parts and generic names such as `helpers.go` and `types.go`.
- Related private types, constants, errors, interfaces, and small helpers may remain beside the behavior that owns them. Do not require one type, function, or interface per file; extract them only when they form a separate responsibility or are coherently shared.
- Do not use a large existing file as justification for adding another responsibility to it. When changing such a file, place substantial new behavior in a focused file and leave only the necessary integration at the original call site.
- As a review trigger, a handwritten production file approaching 500 lines should be split unless keeping it together materially improves cohesion. A new or modified file over 800 lines requires an explicit rationale in the change description. Generated code, generated fixtures, and tightly coupled platform-specific variants are exempt.
- Keep interfaces close to the consumer that needs them, and keep implementations close to the state or external system they own. Avoid central files that collect unrelated interfaces, constants, errors, or helper functions for an entire repository.
- Use standard filename suffixes and build constraints for platform-specific code. Keep shared behavior in common files, avoid copying it across platform variants, and keep supported variants semantically consistent.
- Unit test files should usually follow the production responsibility (`run_handler.go` with `run_handler_test.go`), but may be grouped by observable behavior when that is clearer. Do not enforce a mechanical one-to-one mapping. Split broad test files by behavior or component; shared setup may live in a clearly named test helper file.

### Change checklist

Before finishing a code change, verify:

- every new type and function is in the package that owns its behavior;
- transport, orchestration, domain, persistence, and runtime-driver concerns are not mixed in one file;
- no new catch-all package or generic dumping-ground file was introduced;
- substantial additions to an oversized file were extracted into a cohesive file or package;
- focused tests for the changed behavior pass, and the applicable quality gates below have been run or explicitly reported as not run with a reason.

## Code Style

- The organization rules apply to all handwritten code in the repository. Go-specific rules apply to all handwritten Go files; runtime JavaScript/TypeScript must follow the same ownership, cohesion, explicit-dependency, and side-effect-boundary principles using language-appropriate tooling.
- All handwritten Go code must be formatted with `gofmt` and pass the configured lint checks. Do not manually edit generated protobuf, Connect, or other generated files; change their source and regenerate them.
- Keep functions focused on one operation. A function approaching 80 lines or exceeding three levels of control-flow nesting is a review trigger: simplify or extract cohesive behavior, or explain why keeping it together is clearer. Do not create tiny pass-through functions solely to satisfy a size target.
- Prefer guard clauses and early returns over deeply nested conditionals. Keep the successful path easy to follow and make exceptional paths explicit.
- Choose names that express domain intent. Avoid vague names such as `Manager`, `Processor`, `Helper`, `Data`, or `Util` unless the complete name identifies a specific responsibility.
- Package names must be short, lowercase, and describe the capability they own. Avoid names that repeat their parent package or implementation detail without adding meaning.
- Define interfaces near the consumers that require substitution, keep them small, and return concrete types by default. Do not introduce an interface only to mirror a single implementation or speculate about future implementations.
- Comments must explain intent, invariants, compatibility decisions, or non-obvious tradeoffs rather than restate the code. Exported APIs must use idiomatic Go documentation comments.
- Avoid boolean parameters whose meaning is unclear at the call site. Use a domain-specific type or named options when a function selects between distinct behaviors.
- Use one term consistently for the same concept across APIs, domain models, persistence, configuration, and logs. Preserve established initialism spelling such as `ID`, `HTTP`, `API`, and `LLM`.

### State and functional design

- Do not introduce mutable package-level or process-global business state. Runtime state must normally have an explicit owner, be created during composition, and be passed through constructors or function parameters.
- Do not use package-level singleton clients, stores, service locators, caches, configuration, clocks, random generators, or mutable collections as implicit dependencies. This keeps tests and concurrent instances isolated.
- Package-level constants, sentinel errors, compile-time interface assertions, and initialization-once read-only values such as compiled regular expressions are allowed. A read-only reference value must not be mutated after initialization, expose a mutable backing value, or be unsafe for concurrent reads.
- Linker-injected values and global state required by cgo or an external library are allowed only when the technical constraint is documented, access is encapsulated, and synchronization and lifecycle are explicit. Do not treat these exceptions as a pattern for application state.
- Prefer functional code where it fits naturally; this is a design preference, not a requirement to make every component purely functional. Validation, normalization, mapping, comparison, planning, and other deterministic transformations should usually be pure functions whose results depend only on their inputs.
- Keep side effects at explicit boundaries such as handlers, application orchestration, adapters, and stores when practical. Separating decisions from I/O is encouraged when it makes behavior clearer and independently testable, but do not add layers or abstractions solely to appear functional.
- Do not mutate caller-owned input values or returned shared collections unless the API explicitly documents ownership transfer. Copy before mutation when ownership is ambiguous.
- Make nondeterministic or external dependencies such as time, ID generation, randomness, environment lookup, and filesystem access explicit when they affect domain decisions or test control. Boundary code that owns the side effect may access it directly; do not thread dependencies through unrelated layers or wrap ordinary deterministic standard-library functions merely for stylistic purity.
- Methods and stateful components are appropriate when behavior owns lifecycle, mutable state, caching, concurrency, or I/O resources. When no such ownership exists, prefer a focused function over a stateless service object or namespace-like type.

## Correctness and Reliability

### Errors

- Every error must be handled, returned, or deliberately ignored with a comment explaining why. Do not discard errors only to make a code path continue.
- Wrap errors with operation context using `%w`, and use `errors.Is` or `errors.As` for classification. Do not branch on error-message strings.
- Convert domain errors to Connect or HTTP status codes only at the transport boundary. Domain and storage packages must not depend on transport-specific error types.
- Log an error at the boundary that handles or terminates it; do not log and return the same error at every layer. Error messages and logs must not expose secrets, authorization headers, credentials, or unfiltered environment values.

### Context, concurrency, and cleanup

- Operations that perform I/O, wait, or may block must accept and propagate `context.Context`; it is the first parameter and must not be replaced with `context.Background()` inside an active request path.
- Every goroutine must have an identifiable owner, termination condition, cancellation path, and error-handling path. Components that start background work must also provide and use a way to stop and wait for it during shutdown.
- Make shared-state synchronization explicit through ownership, locks, atomics, or channels. Do not rely on an assumption that a path is currently called serially.
- Do not hold a lock while performing network, disk, runtime-driver, or other unbounded work. Ensure acquired resources, subscriptions, response bodies, files, and timers are released on every exit path.

### Boundaries and data

- Keep protobuf and HTTP types at transport boundaries, database records at persistence boundaries, and runtime-driver types at adapter boundaries. Domain behavior must not depend directly on those representations.
- Mapping between boundary and domain types must explicitly handle missing values, unknown enums, validation, and compatibility defaults. Do not silently turn malformed external input into a valid zero value.
- Validate required configuration at startup or component construction. Define a default in one place, and do not scatter `os.Getenv` calls through business logic.
- Changes to persisted data, public APIs, protobuf fields, or enum values must account for backward compatibility and existing data. Compatibility code belongs at a named boundary rather than being spread through domain logic.

### Tests for changed behavior

- A bug fix must include a regression test that fails without the fix. New behavior must cover its success path and meaningful validation, failure, cancellation, or timeout paths as applicable.
- Tests must assert observable behavior and remain deterministic. They must not depend on test order, brittle sleeps, precise wall-clock timing, public network services, or developer-machine state unless explicitly classified and isolated as integration or E2E tests. Prefer controllable clocks, synchronization signals, and eventual assertions; when real time is unavoidable, use bounded waits with generous deadlines.
- Every test must contain meaningful assertions that can detect incorrect behavior. Do not add tests that merely execute lines for coverage, duplicate implementation logic, or assert only that a mock was called when an observable result is available.
- Prefer small fakes or stubs over interaction-heavy mocks; use mocks when the interaction itself is the contract. Do not add mutable package-level function variables as test seams—inject replaceable dependencies through the owning component or function boundary.
- Use table-driven tests when cases share the same behavior and assertion structure, not as a default for unrelated scenarios. Mark test helpers with `t.Helper()` and manage temporary resources with `t.TempDir()`, `t.Cleanup()`, or equivalent lifecycle mechanisms.
- During development, run focused tests for every changed package and the narrowest integration tests that cover crossed boundaries. For changes to synchronization, goroutine lifecycle, or shared state, also run the relevant tests with `go test -race`.
- Follow `TESTING.md` for test shapes, coverage requirements, and when a change requires integration or E2E coverage.

## Deployment Configuration

- Keep `docker-compose.yml` deployable on its own with published images. A remote deployment should only need `docker-compose.yml` plus a user-created `.env`.
- Use `docker-compose.override.yml` for local development behavior such as `build:`, locally built image tags, local-only build args, or other settings that should not affect remote deployments.
- Keep defaults in the application or image rather than duplicating them in Compose. Expose only deployment knobs in `.env.example`, grouped by purpose, and use commented examples for optional settings.
- Keep secrets and required deployment credentials in `.env.example` empty unless a safe example value exists, and document that operators must set them before exposing a deployment.

## Quality Gates

Testing standards and coverage requirements are defined in `TESTING.md`.

Primary commands:
```bash
task lint
task build
task test
```

`Taskfile.yml` is the source of truth for lint scope and options. Use `task lint` rather than reproducing its package list or flags manually.

Before final handoff, run `task lint`, `task build`, and `task test` when the change and environment make them applicable. Focused package tests are sufficient during iteration; boundary-crossing changes require the relevant integration or E2E tests described in `TESTING.md`. If a gate cannot be run because of environment, dependency, or scope constraints, report exactly which command was not run and why—do not imply it passed.
