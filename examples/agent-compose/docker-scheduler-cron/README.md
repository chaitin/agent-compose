# agent-compose Docker cron scheduler example

Languages: English | [中文](README.zh-CN.md)

This example shows a Docker-backed agent-compose project with a managed cron
scheduler.

It verifies the scheduler control-plane flow:

- parse a cron trigger from `agent-compose.yml`
- apply the project to the daemon
- create a managed project scheduler and loader
- show the scheduler as enabled
- disable the scheduler with `agent-compose down`

The example does not require a model call for `config`, `up`, `ps`, or `down`.
The scheduled run itself still requires a working guest runtime and provider
authentication.

## Prerequisites

- Docker daemon is running.
- The `agent-compose` daemon is already running.
- Docker can access `ghcr.io/chaitin/agent-compose-guest:latest`.

Pull the image referenced by the compose file if needed:

```bash
docker pull ghcr.io/chaitin/agent-compose-guest:latest
```

## Compose file

```yaml
name: docker-scheduler-cron

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
    driver:
      docker: {}
    scheduler:
      enabled: true
      triggers:
        - name: hourly-review
          cron: "0 * * * *"
          prompt: "Review the current project state and summarize any important changes."
```

The trigger uses standard cron syntax. The expression below runs at the top of
every hour:

```yaml
cron: "0 * * * *"
```

## Run the example

From this directory:

```bash
agent-compose config
agent-compose up
agent-compose ps
agent-compose inspect project docker-scheduler-cron
agent-compose down
```

From the repository root without installing the binary:

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml ps
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml inspect project docker-scheduler-cron
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml down
```

Expected result:

- `config` prints the trigger as `kind: cron`.
- `up` creates `project_scheduler` and `loader` resources.
- `ps` shows the scheduler as `enabled`.
- `inspect project` shows `scheduler_count: 1` and `trigger_count: 1`.
- `down` disables the managed scheduler and loader.

## Making the trigger easier to observe

For a local demo where you want the scheduler to fire soon, use an interval
trigger instead of cron:

```yaml
scheduler:
  enabled: true
  triggers:
    - name: every-minute
      interval: 1m
      prompt: "Say hello from the interval trigger."
```

Use cron when you want calendar-based scheduling. Use interval when you want
short local feedback while testing.

## Verification output

Output from a local verification run.

The recorded output below used the equivalent locally built
`agent-compose-guest:latest` image. The committed compose file now uses the
published image; generated IDs, hashes, and timestamps will also differ.

### 1. Config normalization

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml config
name: docker-scheduler-cron
agents:
    - name: reviewer
      provider: codex
      image: agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
      scheduler:
        enabled: true
        triggers:
            - name: hourly-review
              kind: cron
              cron: 0 * * * *
              prompt: Review the current project state and summarize any important changes.
network:
    mode: default
```

### 2. Apply project

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml up
Project: docker-scheduler-cron
ID: project-docker-scheduler-cron-034aaf526f91
Revision: 1
Spec: sha256:93950d90a6dbd56a141cbd0b059c06eb37b4db6bb27860b24cb78bea781536d5
Status: applied
Agents: 1
Schedulers: 1

ACTION   TYPE               NAME                                                                     ID
created  project            docker-scheduler-cron                                                    project-docker-scheduler-cron-034aaf526f91
created  project_revision   sha256:93950d90a6dbd56a141cbd0b059c06eb37b4db6bb27860b24cb78bea781536d5  project-docker-scheduler-cron-034aaf526f91/1
created  project_agent      reviewer                                                                 agent-reviewer-4bff2fb6372a
created  agent_definition   reviewer                                                                 agent-reviewer-4bff2fb6372a
created  project_scheduler  reviewer                                                                 scheduler-reviewer-default-ed0b5bed0daa
created  loader             docker-scheduler-cron/reviewer scheduler                                 loader-reviewer-default-ed0b5bed0daa
```

### 3. Scheduler status

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml ps
AGENT     SCHEDULER  LATEST RUN  RUN STATUS  SESSION  DRIVER  IMAGE
reviewer  enabled    -           -           -        docker  agent-compose-guest:latest
```

### 4. Inspect project

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml inspect project docker-scheduler-cron
{
  "project": {
    "id": "project-docker-scheduler-cron-034aaf526f91",
    "name": "docker-scheduler-cron",
    "current_revision": 1,
    "agent_count": 1,
    "scheduler_count": 1
  },
  "agents": [
    {
      "agent_name": "reviewer",
      "provider": "codex",
      "image": "agent-compose-guest:latest",
      "driver": "docker",
      "scheduler_enabled": true
    }
  ],
  "schedulers": [
    { "agent_name": "reviewer", "enabled": true, "trigger_count": 1 }
  ]
}
```

### 5. Disable scheduler

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml down
Project: docker-scheduler-cron
ID: project-docker-scheduler-cron-034aaf526f91
Status: down
Failed session stops: 0

ACTION   TYPE               NAME      ID                                       MESSAGE
updated  project_scheduler  reviewer  scheduler-reviewer-default-ed0b5bed0daa  disabled by project down
updated  loader             reviewer  loader-reviewer-default-ed0b5bed0daa     disabled by project down
```

### 6. Current provider-path verification

On 2026-07-15 the E2E manually triggered the cron entry through a real daemon,
Docker guest, and guest Codex CLI. Its provider endpoint was a controlled local
fixture so the result remains deterministic:

```json
{
  "id": "b602c4c8479449081121ed0c9c8bfb9ded5c74d77027e6169098bc5f179e954c",
  "status": "succeeded",
  "sandbox_id": "2d60f25ed6752646d7468500d112dbf01c2b2f0f9c6c8aba8a965deab52c92e2",
  "duration_ms": 10822,
  "output": "cron scheduler ok",
  "driver": "docker",
  "image_ref": "ghcr.io/chaitin/agent-compose-guest:latest"
}
```

IDs and duration are generated values and will differ.
