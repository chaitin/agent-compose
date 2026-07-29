# agent-compose Docker timeout scheduler example

Languages: English | [中文](README.zh-CN.md)

This example runs a one-shot timeout trigger through the Docker runtime. It
shows both layers of the current run model:

- the outer scheduler trigger run, queried with `scheduler runs`, `scheduler
  inspect`, and `scheduler logs`;
- the inner agent run created by the trigger, whose transcript is queried with
  the ordinary `logs` command.

## Prerequisites

- The `agent-compose` daemon and Docker daemon are running.
- `agent-compose-guest:latest` exists locally.
- The guest has working Codex credentials or API access.

Build the guest image from the repository root if needed:

```bash
task image:agent-compose-guest
```

## Compose file

```yaml
name: docker-scheduler-timeout

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    driver:
      docker: {}
    scheduler:
      enabled: true
      triggers:
        - name: run-once-after-15-seconds
          timeout: 15s
          prompt: "Reply with exactly: timeout scheduler ok"
```

The timeout is intentionally short. The scheduler defaults to
`sandbox_policy: new` and `concurrency_policy: skip`.

## Run the example

From this directory:

```bash
agent-compose config
agent-compose up
agent-compose ls
sleep 35
agent-compose scheduler runs reviewer --limit 1
agent-compose scheduler inspect <scheduler-run-id>
agent-compose scheduler logs <scheduler-run-id>
agent-compose logs reviewer
agent-compose ps --all
agent-compose down
```

From the repository root without installing the binary, add the compose file to
each command, for example:

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml scheduler runs reviewer --limit 1
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml scheduler inspect <scheduler-run-id>
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml logs reviewer
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml down
```

Replace `<scheduler-run-id>` with the outer run ID printed by `scheduler runs`.

Expected result:

- `config` prints `kind: timeout` and the normalized scheduler policies.
- `up` creates the project, agent, and timeout trigger.
- `ls` shows the agent's scheduler as `true`.
- `scheduler runs` reports one terminal outer run after the timeout fires.
- `scheduler inspect` shows its trigger, status, result, and sandbox IDs.
- `scheduler logs` shows outer structured scheduler events; it intentionally
  does not contain the inner agent transcript.
- `logs reviewer` prints the inner project-run transcript, including
  `timeout scheduler ok` after a successful provider call.
- `ps --all` lists the sandbox lifecycle state rather than the agent list.
- `down` stops owned sandboxes and removes the managed project resources.

Representative normalized scheduler output:

```yaml
      scheduler:
        enabled: true
        sandbox_policy: new
        concurrency_policy: skip
        triggers:
            - name: run-once-after-15-seconds
              kind: timeout
              timeout: 15s
              prompt: 'Reply with exactly: timeout scheduler ok'
```

Representative control-plane output before the timeout fires (IDs depend on
the local compose path):

```console
$ agent-compose up
ID            NAME                       TYPE     ACTION
<project-id>  docker-scheduler-timeout   project  created
<agent-id>    reviewer                   agent    created
<trigger-id>  run-once-after-15-seconds  trigger  created

$ agent-compose ls
AGENT     PROVIDER  MODEL  IMAGE                       DRIVER  SCHEDULER
reviewer  codex            agent-compose-guest:latest  docker  true
```

If the outer scheduler run succeeds but the expected transcript is missing,
inspect the inner agent run through `logs reviewer`; outer scheduler logs and
inner provider output are intentionally separate streams.
