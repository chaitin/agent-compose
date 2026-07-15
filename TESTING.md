# Testing Standards

This project measures test coverage through three complementary test shapes:
unit tests, integration tests, and end-to-end tests. Coverage standards apply to
project code across the Go services, runtime components, and frontend/runtime
JavaScript or TypeScript code.

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

The real host-daemon Docker Jupyter lifecycle E2E is opt-in because it requires
a local Docker Engine and a guest image containing JupyterLab. Run it with:

```bash
task test:e2e:docker-jupyter
```

The task uses `agent-compose-guest:latest` by default. Set
`AGENT_COMPOSE_E2E_DOCKER_JUPYTER_IMAGE` to test another compatible image. The
test starts an isolated host daemon, creates a Docker sandbox through the public
API, verifies the unified Jupyter endpoint, then stops and resumes the sandbox
with deliberately stale persisted port state to verify inspect-based recovery.

The real host-daemon Docker file-workspace restart/resume E2E is also opt-in. It
requires a reachable local Docker Engine, the Docker CLI, and a compatible local
guest image. Build the default image and run it with:

```bash
task image:agent-compose-guest
task test:e2e:docker-workspace-resume
```

The task uses `agent-compose-guest:latest` by default. To use another existing
local image, run:

```bash
AGENT_COMPOSE_E2E_DOCKER_WORKSPACE_IMAGE=example/agent-compose-guest:tag \
  task test:e2e:docker-workspace-resume
```

The test uses public APIs and a real Docker runtime to verify that a daemon
restart preserves an existing sandbox's workspace and container identity, while
a new sandbox receives the latest Workspace Source without state leaking back
to that source. It also checks resource cleanup. Inspect verbose test output and
daemon logs when it fails.

This focused task is intentionally absent from `task test` and GitHub Actions
because GitHub-hosted CI does not provide its prebuilt guest-image prerequisite.

The real Docker scheduler script E2E is also opt-in because it starts a Docker
sandbox and waits for a scheduler trigger. Run it with:

```bash
task test:e2e:docker-scheduler
```

The task uses `agent-compose-guest:latest` by default. Set
`AGENT_COMPOSE_E2E_GUEST_IMAGE` to use another compatible image. The test is
compiled only with the `docker_e2e` build tag, so the ordinary `task test`
coverage gate does not include this scheduler Docker E2E or create its runtime
containers.

The full daemon image Docker lifecycle E2E is opt-in because it starts the
daemon image and Docker sandbox containers through a local Docker socket. Run
it after building both local images:

```bash
task image:agent-compose
task image:agent-compose-guest
task test:e2e:image-docker
```

The task uses `agent-compose:latest`, `agent-compose-guest:latest`, and
`/var/run/docker.sock` by default. Override them with
`AGENT_COMPOSE_E2E_DAEMON_IMAGE`, `AGENT_COMPOSE_E2E_GUEST_IMAGE`, and
`AGENT_COMPOSE_E2E_DOCKER_SOCKET`. The startup case runs the shipped daemon
image with no Docker socket, privilege, device, or `/dev/kvm` and verifies
`/api/version`. The lifecycle case mounts the selected socket, then uses the
public APIs to create, exec, stop, resume, exec, and remove a Docker sandbox.
Both cases use isolated resources and fail if their labeled containers,
network, or volumes remain after cleanup. The task also installs a
fixture-scoped interrupt/exit cleanup trap; a hard-killed run reports any stale
resource IDs on the next invocation instead of touching unrelated containers.
Ordinary `task test` runs only the deterministic task contract and compiles the
environment-gated runtime cases without contacting the Docker daemon.

## Quality Gate

`task test` is the project quality gate for tests.

Before coverage collection, it runs `task test:deploy` to validate the
installer state machine, base and KVM Compose rendering, and the exact installer
release-asset set. These deterministic checks require the Docker CLI with
Compose v2 and `jq`, but do not require a running Docker daemon, KVM, network
access, or runtime sandboxes. They do not change or contribute to the coverage
baselines below.

The `test` task in `Taskfile.yml` must calculate and print:
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
combined coverage result.

Generated protocol clients, vendored code, build artifacts, and test fixtures
should be excluded from coverage calculations unless the project intentionally
treats them as maintained source code. Any exclusions must be documented in the
coverage tooling or the `test` task implementation.

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

## Reporting Expectations

Coverage reports should make it clear which test shape produced each coverage
number and which source-code scope was measured. When coverage cannot be
calculated for a test shape, `task test` should fail rather than silently omit
that number.

When adding a feature or fixing a bug, choose the narrowest test shape that
proves the behavior, then add broader integration or E2E coverage when the
change crosses service boundaries, persistence boundaries, runtime-driver
behavior, or user-facing workflows.
