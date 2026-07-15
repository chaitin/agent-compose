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
- Docker can pull `ghcr.io/chaitin/agent-compose-guest:latest`, or it already exists locally.

Pull the exact image referenced by this example if needed:

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
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer hourly-review
agent-compose inspect project docker-scheduler-cron
agent-compose down
```

From the repository root without installing the binary:

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml ps
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml scheduler ls reviewer
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml scheduler inspect reviewer hourly-review
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml inspect project docker-scheduler-cron
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml down
```

Expected result:

- `config` prints the trigger as `kind: cron`.
- `up` creates `project_scheduler` and `loader` resources.
- `ps` shows the scheduler as `enabled`.
- `scheduler ls` reports `hourly-review` with kind `cron`.
- `scheduler inspect` reports the configured expression and prompt.
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

## Run the agent path immediately

You do not need to change the committed cron expression to test the provider
path. A manual trigger uses the same managed agent prompt flow:

```bash
agent-compose scheduler trigger reviewer hourly-review \
  --prompt "Reply with exactly: cron scheduler ok"
```

This requires a working Codex/LLM provider. A successful result has
`status: succeeded` and output containing `cron scheduler ok`.

## What to verify

The real-daemon E2E verifies the control plane, and the provider E2E executes
the trigger through a real Docker guest and Codex CLI. Check that:

- `config` normalizes the trigger to `kind: cron`.
- `up` creates one enabled scheduler with one trigger.
- `scheduler ls reviewer` contains `hourly-review` and `cron`.
- the manual trigger returns a run id and `status: succeeded` with a provider.
- `down` disables the managed scheduler and loader.

Generated project, scheduler, loader, and run IDs differ by environment.
