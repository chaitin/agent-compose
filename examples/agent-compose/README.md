# agent-compose examples

Languages: English | [中文](README.zh-CN.md)

Runnable examples for the `agent-compose` Docker runtime driver, ordered from
simplest to most complete.

| Example | What it shows | Needs provider auth |
| --- | --- | --- |
| [docker-minimal](docker-minimal/) | Smallest Docker-backed project: one agent, no scheduler. | No, for `config`/`up`/`ps` |
| [docker-scheduler-cron](docker-scheduler-cron/) | Managed cron scheduler control plane. | No, for `config`/`up`/`ps`/`down` |
| [docker-scheduler-script-url](docker-scheduler-script-url/) | A scheduler script loaded from a relative `file` source. | No, for `config`/`up`/`ps`/`down` |
| [docker-scheduler-timeout](docker-scheduler-timeout/) | End-to-end scheduled run that fires, executes the agent, and persists logs. | Yes, for the scheduled run |

## Common prerequisites

- Docker daemon is running.
- The `agent-compose` daemon is already running.
- The `agent-compose-guest:latest` image exists locally.

From the repository root, build the guest image if needed:

```bash
task image:agent-compose-guest
```

Each example has its own `README.md` with the exact commands and expected
output.
