# agent-compose Docker minimal example

Languages: English | [中文](README.zh-CN.md)

This example is the smallest useful `agent-compose.yml` for a project using
the Docker runtime driver: one enabled agent, an explicit guest image, and no
scheduler.

`config`, `up`, and `ls` exercise only the control plane and do not require a
model or API key.

## Prerequisites

- The `agent-compose` daemon is running.
- Docker is required only when starting a real agent sandbox.
- `agent-compose-guest:latest` exists locally before a real run.

From the repository root, build the guest image if needed:

```bash
task image:agent-compose-guest
```

Check the daemon before continuing:

```bash
agent-compose status
```

When working from the source tree, replace `agent-compose` with
`go run ./cmd/agent-compose`.

## Compose file

```yaml
name: docker-minimal

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    driver:
      docker: {}
```

The agent defaults to `enabled: true`. The default driver is also Docker, but
this example keeps `docker: {}` explicit so its runtime requirement is clear.

## Validate and apply

From this directory:

```bash
agent-compose config
agent-compose up
agent-compose ls
```

From the repository root without installing the binary:

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml ls
```

Expected result:

- `config` prints `enabled: true` and `driver.name: docker` after normalization.
- `up` creates or updates the project and its `reviewer` agent.
- `ls` shows the agent using Docker and `agent-compose-guest:latest`, with its
  scheduler set to `false`.

Representative normalized output:

```yaml
name: docker-minimal
agents:
    - name: reviewer
      enabled: true
      provider: codex
      image: agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
```

Representative control-plane output (IDs depend on the local compose path):

```console
$ agent-compose up
ID            NAME            TYPE     ACTION
<project-id>  docker-minimal  project  created
<agent-id>    reviewer        agent    created

$ agent-compose ls
AGENT     PROVIDER  MODEL  IMAGE                       DRIVER  SCHEDULER
reviewer  codex            agent-compose-guest:latest  docker  false
```

## Optional real run

A real agent run requires Docker, a compatible guest image, and working Codex
credentials or API access in the guest environment:

```bash
agent-compose run reviewer --keep-running --prompt "hello from docker minimal example"
```

List the running sandbox, then pass its ID positionally to `exec`:

```bash
agent-compose ps
agent-compose exec <sandbox-id> -- pwd
agent-compose exec <sandbox-id> -- env
```

The old agent-name target flag is no longer supported by `exec`.

Clean up the project and any running project sandboxes:

```bash
agent-compose down
```
