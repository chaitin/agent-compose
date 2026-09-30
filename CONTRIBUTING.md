# Contributing

Thanks for considering a contribution to agent-compose.

The project is still in preview. Please keep changes focused, explain behavior
changes clearly, and include tests for user-visible behavior.

## Development Setup

### Prerequisites

- **Go 1.26.2**, matching the `go` directive in [`go.mod`](go.mod). Install it
  from the [official Go downloads](https://go.dev/dl/), or use an existing Go
  installation with automatic toolchain downloads enabled (`GOTOOLCHAIN=auto`).
  Distribution packages may be older than the version required by this
  repository.
- **Node.js 20 or newer** and npm, matching the `engines.node` requirement in
  each npm package. Install a supported release from the
  [Node.js download page](https://nodejs.org/en/download) or with a Node version
  manager. In particular, Ubuntu 24.04's default Node.js 18 package is not
  supported.
- **Task v3** for the documented `task ...` commands. With the required Go
  toolchain installed, install it with:

  ```bash
  go install github.com/go-task/task/v3/cmd/task@latest
  export PATH="$(go env GOPATH)/bin:$PATH"
  ```

  Prebuilt packages and other installation methods are available in the
  [Task installation guide](https://taskfile.dev/docs/installation).
- **Docker Engine** for Docker-backed workflows and Linux full-binary artifact
  preparation. Install it from the
  [official Docker Engine documentation](https://docs.docker.com/engine/install/).
  Deployment workflows and deterministic Compose checks also require the
  [Docker Compose plugin](https://docs.docker.com/compose/install/). A working
  Docker daemon is required to:

  - Run sandboxes with the default `docker` runtime driver;
  - Build the guest image (`task image:agent-compose-guest`);
  - Build daemon images (`task image:agent-compose` or `task all`); and
  - Export BoxLite/Microsandbox development artifacts used by Linux
    `task build`, daemon image builds, and runtime smoke tests.

  Lint and unit-style test commands remain isolated and do not start Docker
  workloads. The full `task test` harness also runs deterministic deployment
  contract checks, which require the Docker Compose CLI and `jq` but do not
  contact the Docker daemon. On Linux, `task build` selects the full
  Docker/BoxLite/Microsandbox binary profile; its native artifact preparation
  uses Docker when matching artifacts are not already present. The Darwin
  binary profile compiles only Docker support. Real BoxLite and Microsandbox
  runtime smoke tests additionally require a prepared Linux host with usable
  KVM access.

Verify the required versions before installing dependencies:

```bash
go version       # go1.26.2
node --version   # v20 or newer
npm --version
task --version   # Task v3
```

From the repository root, install the Go development tools and dependencies for
both runtime npm packages (`runtime/agent-compose-runtime-sdk` and
`runtime/javascript`):

```bash
task prepare
```

Build and test from the repository root:

```bash
task lint
task build
task test
```

These tasks regenerate protobuf message sources from `proto/**/*.proto` when
needed. The generated `*.pb.go` message and Connect sources are tracked; commit
them together with the `.proto` change that produced them.

For smaller loops:

```bash
go test ./cmd/... ./pkg/...
cd runtime/agent-compose-runtime-sdk && npm test
cd runtime/javascript && npm run test:unit
```

## Pull Requests

Draft PRs skip the PR checks. Mark a PR as **Ready for review** to run Fast CI;
subsequent commits run it while the PR is ready. Fast CI covers generated-source
validation, lint, compilation, regular tests, SDK tests, and binary checks. The
coverage gate, full driver race tests, and image builds are reserved for Full
CI: add the `ci:full` label when the PR is ready for complete validation. Adding
or removing that label reruns the workflows, and a new commit while the label
is present validates the new head. Image checks continue to follow their path
filters.

The `Full PR validation` check is the stable required-check candidate for
branch protection. It succeeds without running the expensive jobs when
`ci:full` is absent, and reflects the coverage and driver-race results when the
label is present. Main and release pushes keep their existing complete checks.

- Keep PRs scoped to one change.
- Include a clear problem statement and solution summary.
- Update documentation when behavior, configuration, or user workflows change.
- Add or update tests for bug fixes and new functionality.
- Avoid committing generated runtime state, local data, credentials, or private
  infrastructure configuration.

## Code Style

- Follow existing Go package patterns.
- Prefer small, local changes over broad refactors.
- Keep API handlers thin where possible and put reusable behavior in domain
  helpers.
- Use structured configuration and existing helper APIs instead of ad hoc
  parsing.

## Security

Do not include secrets, private registry endpoints, internal certificates,
tokens, or personal local state in commits.

Report suspected vulnerabilities through the process in [SECURITY.md](SECURITY.md).
