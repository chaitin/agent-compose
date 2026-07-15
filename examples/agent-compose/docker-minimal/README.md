# agent-compose Docker minimal example

Languages: English | [中文](README.zh-CN.md)

This example shows the smallest useful `agent-compose.yml` for running an
agent-compose project with the Docker runtime driver.

It is intentionally minimal:

- one project
- one agent
- Docker runtime driver
- explicit guest image
- no scheduler
- no model or API key requirement for `config`, `up`, and `ps`

## Prerequisites

- Docker daemon is running.
- The `agent-compose` daemon is already running.
- Docker can pull `ghcr.io/chaitin/agent-compose-guest:latest`, or it already exists locally.

Pull the exact image referenced by this example if needed:

```bash
docker pull ghcr.io/chaitin/agent-compose-guest:latest
```

If you have an installed `agent-compose` binary in `PATH`, use:

```bash
agent-compose status
```

When working from the source tree, you can run the CLI directly:

```bash
go run ./cmd/agent-compose status
```

## Compose file

This directory contains the minimal Docker-backed project:

```yaml
name: docker-minimal

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
    driver:
      docker: {}
```

The important part is:

```yaml
driver:
  docker: {}
```

If the agent omits `driver`, the compose normalizer defaults to `docker`.
This example sets `docker: {}` explicitly to document the intended runtime.

## Run the example

From this directory:

```bash
agent-compose config
agent-compose up
agent-compose ps
```

From the repository root without installing the binary:

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml ps
```

Expected result:

- `config` prints a normalized project with `driver.name: docker`.
- `up` creates or updates the project and managed agent definition.
- `ps` shows the `reviewer` agent using Docker and `ghcr.io/chaitin/agent-compose-guest:latest`.

## Optional run test

To start a runtime session and keep it alive:

```bash
agent-compose run reviewer --keep-running --prompt "hello from docker minimal example"
```

A real agent run requires a working guest runtime and provider configuration in
the daemon. Long-lived credentials stay in the daemon; the guest Codex CLI uses
the sandbox-scoped LLM facade variables injected by agent-compose.

If the runtime session is alive, you can run commands in it:

```bash
agent-compose exec <sandbox-id> -- pwd
agent-compose exec <sandbox-id> -- env
```

Clean up running project sessions:

```bash
agent-compose down
```

Replace `<sandbox-id>` with the sandbox id returned by `run` or shown by
`agent-compose ps --all`. `down` stops project sandboxes; use
`agent-compose rm <sandbox-id>` when you also want to delete one.

## What to verify

The real-daemon Docker E2E runs this example. For a manual check, verify:

- `config` reports `driver.name: docker` and the published guest image.
- `up` reports one applied project and one agent.
- `run reviewer --command "printf 'docker minimal ok\\n'"` succeeds without
  provider credentials.
- a prompt run succeeds only when the daemon has a working LLM provider.
- a kept run has a non-empty sandbox id in `ps --all`.
- `exec <sandbox-id> -- pwd` returns the guest working directory.

Project, revision, run, and sandbox IDs are generated per environment and are
intentionally not hard-coded.
