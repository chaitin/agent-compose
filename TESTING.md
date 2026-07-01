# Testing Standards

This project uses three test shapes: unit tests, integration tests, and
end-to-end tests. The release-blocking coverage gate is `task test`, implemented
by `scripts/test-coverage.sh`.

The current coverage scope is:
- Go control-plane code under `./cmd/...` and `./pkg/...`.
- The guest scheduler runtime under `runtime/javascript`.

The runtime SDK package under `runtime/agent-compose-runtime-sdk` and the
generated TypeScript Connect client under `proto-client` are validated by
package build/test gates, not by the combined coverage calculation.

## Test Shapes

### Unit Tests

Unit tests verify isolated functions, types, and small modules without requiring
external services, runtime sandboxes, Docker, network access, or persistent
shared state.

Unit tests should:
- be deterministic and fast
- use fakes, stubs, or in-memory stores where practical
- cover edge cases, validation, serialization, scheduling logic, and error paths
- avoid depending on test execution order

Run unit tests with:

```bash
task test:unit
```

### Integration Tests

Integration tests verify collaboration between multiple project components or
between project code and controlled local dependencies.

Integration tests may use:
- local files, temporary databases, and temporary session roots
- in-process HTTP or Connect handlers
- local runtime-driver adapters when they can be exercised deterministically
- controlled Docker or sandbox dependencies when explicitly marked and isolated

Integration tests should prove that service boundaries, persistence, proxying,
configuration, loader scheduling, and runtime-driver interactions work together.

Run integration tests with:

```bash
task test:integration
```

### E2E Tests

E2E tests verify complete user-facing workflows through the deployed service or
a production-like local service instance.

E2E tests should cover critical workflows such as:
- creating, resuming, stopping, and proxying sessions
- executing notebook or kernel actions through the public API surface
- loader trigger and run workflows
- frontend flows that depend on generated protocol clients
- authentication and configuration workflows where applicable

E2E tests should be isolated, repeatable, and explicit about required runtime
dependencies.

Run E2E tests with:

```bash
task test:e2e
```

## Quality Gate

`task test` is the project quality gate for tests.

It calculates and prints:
- unit-test coverage
- integration-test coverage
- E2E-test coverage
- total combined coverage

Coverage output must be visible in normal task output and suitable for CI logs.
The task should fail when any required coverage baseline is not met.

Combined coverage is the merged coverage achieved by all three test shapes over
the same project coverage scope. It must not be calculated as a simple average
of unit, integration, and E2E percentages. When a line, branch, function, or
statement is covered by more than one test shape, it should count once in the
combined Go coverage result. JavaScript combined coverage is produced by a
single all-shapes Vitest coverage run.

Generated protocol clients, vendored code, build artifacts, and runtime-driver
code that requires host-specific sandboxes are excluded from the coverage gate.
The default exclusions live in `scripts/test-coverage.sh` as
`AGENT_COMPOSE_GO_COVER_EXCLUDE_REGEX`.

Coverage artifacts are written to `.cache/coverage` by default. Override the
location with `COVERAGE_DIR` or `AGENT_COMPOSE_COVERAGE_DIR`.

Go test shape selection is based on test names:
- unit: test names without `Integration` or `E2E`
- integration: test names containing `Integration`
- E2E: test names containing `E2E`, plus `./test/e2e`

JavaScript runtime tests use the `TEST_SHAPE` environment variable consumed by
`runtime/javascript/vitest.config.ts`.

## Coverage Baselines

Minimum required coverage:
- unit tests: at least 60%
- integration tests: at least 60%
- E2E tests: at least 60%
- total combined coverage: at least 70%

Recommended coverage targets:
- unit tests: at least 80%
- integration tests: at least 70%
- E2E tests: at least 60%
- total combined coverage: at least 70%

The required baselines are release-blocking. The recommended targets are the
preferred engineering standard for new and substantially changed code.

## Build And Package Gates

Use these package gates when touching their areas:

```bash
task build
task build:runtime
task build:runtime-sdk
task build:proto-client
```

`task build:proto-client` requires `protoc` on `PATH` because it regenerates the
published TypeScript client before compiling it.

Runtime smoke tests are opt-in because they require real runtime dependencies:

```bash
task test:runtime-smoke
SMOKE_RUNTIME_DRIVERS=boxlite task test:runtime-smoke
SMOKE_RUNTIME_DRIVERS=microsandbox task test:runtime-smoke
```

## Reporting Expectations

Coverage reports should make it clear which test shape produced each coverage
number and which source-code scope was measured. When coverage cannot be
calculated for a test shape, `task test` should fail rather than silently omit
that number.

When adding a feature or fixing a bug, choose the narrowest test shape that
proves the behavior, then add broader integration or E2E coverage when the
change crosses service boundaries, persistence boundaries, runtime-driver
behavior, or user-facing workflows.
