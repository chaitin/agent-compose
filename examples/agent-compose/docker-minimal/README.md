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
- The `agent-compose-guest:latest` image exists locally.

From the repository root, build the guest image if needed:

```bash
task image:agent-compose-guest
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
    image: agent-compose-guest:latest
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
- `ps` shows the `reviewer` agent using Docker and `agent-compose-guest:latest`.

## Optional run test

To start a runtime session and keep it alive:

```bash
agent-compose run reviewer --keep-running --prompt "hello from docker minimal example"
```

A real agent run requires a working guest runtime and provider authentication.
For `provider: codex`, configure the required Codex credentials or API key in
the guest environment before expecting model execution to succeed.

If the runtime session is alive, you can run commands in it:

```bash
agent-compose exec --agent reviewer -- pwd
agent-compose exec --agent reviewer -- env
```

Clean up running project sessions:

```bash
agent-compose down
```

## Verification output

Output from a local verification run.

### 1. Config normalization

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml config
name: docker-minimal
agents:
    - name: reviewer
      provider: codex
      image: agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
network:
    mode: default
```

### 2. Apply project

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml up
Project: docker-minimal
ID: project-docker-minimal-ad604c8bf8d3
Revision: 1
Spec: sha256:0e351a523ae793f780fc0933f3b88920501f20dfd4d855654fe711a8a3cb4edd
Status: applied
Agents: 1
Schedulers: 0

ACTION   TYPE              NAME                                                                     ID
created  project           docker-minimal                                                           project-docker-minimal-ad604c8bf8d3
created  project_revision  sha256:0e351a523ae793f780fc0933f3b88920501f20dfd4d855654fe711a8a3cb4edd  project-docker-minimal-ad604c8bf8d3/1
created  project_agent     reviewer                                                                 agent-reviewer-a9f84de36227
created  agent_definition  reviewer                                                                 agent-reviewer-a9f84de36227
```

### 3. Project status

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml ps
AGENT     SCHEDULER  LATEST RUN  RUN STATUS  SESSION  DRIVER  IMAGE
reviewer  disabled   -           -           -        docker  agent-compose-guest:latest
```

### 4. Docker runtime container

```console
$ docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
NAMES                                                IMAGE                        STATUS
agent-compose-8aa2625d-db67-4428-82ae-8bef1a137a2f   agent-compose-guest:latest   Up 14 seconds
```
