# agent-compose Docker timeout scheduler example

Languages: English | [中文](README.zh-CN.md)

This example runs an end-to-end scheduled agent flow with the Docker runtime.

It verifies that agent-compose can:

- parse a timeout trigger from `agent-compose.yml`
- apply the project to the daemon
- create a managed scheduler and loader
- let the scheduler fire automatically
- start a Docker-backed agent runtime session
- run the configured agent prompt
- persist the successful project run and logs
- disable the scheduler with `agent-compose down`

## Prerequisites

- Docker daemon is running.
- The `agent-compose` daemon is already running.
- Docker can pull `ghcr.io/chaitin/agent-compose-guest:latest`, or it already exists locally.
- The daemon has a working Codex/LLM provider. Long-lived provider credentials
  remain in the daemon; the guest calls its sandbox-scoped LLM facade.

Pull the exact image referenced by this example if needed:

```bash
docker pull ghcr.io/chaitin/agent-compose-guest:latest
```

## Compose file

```yaml
name: docker-scheduler-timeout

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
    driver:
      docker: {}
    scheduler:
      enabled: true
      sandbox_policy: new
      triggers:
        - name: run-once-after-15-seconds
          timeout: 15s
          prompt: "Reply with exactly: timeout scheduler ok"
```

The `timeout: 15s` trigger is intentionally short so the full flow can be
tested quickly.

## Run the example

From this directory:

```bash
agent-compose config
agent-compose up
sleep 35
agent-compose ps
agent-compose inspect run <run-id>
agent-compose logs --run <run-id>
agent-compose down
```

From the repository root without installing the binary:

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml up
sleep 35
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml ps
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml inspect run <run-id>
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml logs --run <run-id>
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml down
```

Replace `<run-id>` with the run id shown in the `ps` output.

Expected result:

- `config` prints the trigger as `kind: timeout`.
- `up` creates or updates the managed scheduler and loader.
- After the timeout fires once, `ps` shows a scheduler-created run.
- `inspect run <run-id>` shows `source: scheduler`, `status: succeeded`, `driver: docker`, and output from the agent.
- `logs --run <run-id>` prints the agent output.
- `down` disables the managed scheduler and loader.

`sandbox_policy: new` creates a fresh sandbox for this scheduled run. The guest
receives a facade token and guest-reachable daemon URL, not the provider key.

## What to verify

The provider E2E runs this flow through a real daemon, Docker guest, and Codex
CLI. In a manual run, verify:

- `config` reports `kind: timeout`, `timeout: 15s`, and `sandbox_policy: new`.
- `scheduler inspect reviewer run-once-after-15-seconds` eventually reports the
  trigger has fired.
- `ps` shows a scheduler-sourced run with `status: succeeded`.
- `inspect run <run-id>` reports `source: scheduler`, `driver: docker`, a
  non-empty sandbox id, and output containing `timeout scheduler ok`.
- `logs --run <run-id>` prints the same model output.
- `down` disables the scheduler and cleans up project sandboxes.

Generated IDs and exact durations vary and are intentionally not hard-coded.
