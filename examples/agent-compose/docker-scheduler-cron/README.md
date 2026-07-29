# agent-compose Docker cron scheduler example

Languages: English | [中文](README.zh-CN.md)

This example defines a Docker-backed agent with one managed cron trigger. It
exercises the current project, agent, scheduler, and trigger control-plane
model without requiring a model call.

## Prerequisites

- The `agent-compose` daemon is running.
- Docker and `agent-compose-guest:latest` are needed only when the trigger
  actually starts an agent sandbox.
- A real scheduled model run requires provider authentication.

Build the guest image from the repository root if needed:

```bash
task image:agent-compose-guest
```

## Compose file

```yaml
name: docker-scheduler-cron

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    driver:
      docker: {}
    scheduler:
      enabled: true
      triggers:
        - name: hourly-review
          cron: "0 * * * *"
          prompt: "Review the current project state and summarize any important changes."
```

`0 * * * *` runs at the top of every hour. Because this trigger omits
`timezone`, it uses the daemon's local timezone (`TZ` first, otherwise
`/etc/localtime`). Add `timezone: UTC` or an IANA timezone such as
`Asia/Shanghai` when the schedule must be independent of daemon placement.

The scheduler defaults to `sandbox_policy: new` and
`concurrency_policy: skip`; both defaults are visible in normalized config.

## Validate and apply

From this directory:

```bash
agent-compose config
agent-compose up
agent-compose ls
agent-compose scheduler ls
agent-compose inspect project
agent-compose down
```

From the repository root without installing the binary:

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml ls
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml scheduler ls
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml inspect project
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml down
```

Expected result:

- `config` prints `kind: cron`, plus the scheduler's normalized policies.
- `up` creates the project, agent, and `hourly-review` trigger.
- `ls` shows the agent with `SCHEDULER` set to `true`.
- `scheduler ls` shows the registered cron trigger.
- `inspect project` reports `scheduler_count: 1` and `trigger_count: 1`.
- `down` removes the managed trigger, project, and agent from the daemon.

Representative normalized scheduler output:

```yaml
      scheduler:
        enabled: true
        sandbox_policy: new
        concurrency_policy: skip
        triggers:
            - name: hourly-review
              kind: cron
              cron: 0 * * * *
              prompt: Review the current project state and summarize any important changes.
```

Representative control-plane output (IDs depend on the local compose path):

```console
$ agent-compose up
ID            NAME                   TYPE     ACTION
<project-id>  docker-scheduler-cron  project  created
<agent-id>    reviewer               agent    created
<trigger-id>  hourly-review          trigger  created

$ agent-compose ls
AGENT     PROVIDER  MODEL  IMAGE                       DRIVER  SCHEDULER
reviewer  codex            agent-compose-guest:latest  docker  true

$ agent-compose down
ID            NAME                   TYPE     ACTION   MESSAGE
<trigger-id>  hourly-review          trigger  removed  disabled by project down
<project-id>  docker-scheduler-cron  project  removed  removed by project down
<agent-id>    reviewer               agent    removed
```

## Make the trigger easier to observe

For short local feedback, replace the cron trigger with an interval trigger:

```yaml
scheduler:
  enabled: true
  triggers:
    - name: every-minute
      interval: 1m
      prompt: "Say hello from the interval trigger."
```

Use cron for calendar-based schedules and interval for elapsed-time schedules.
