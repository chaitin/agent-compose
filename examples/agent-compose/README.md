# agent-compose examples

Languages: English | [中文](README.zh-CN.md)

Runnable examples for the `agent-compose` Docker and Kubernetes (`k8s`)
runtime drivers, ordered from simplest to most complete.

| Example | What it shows | Needs provider auth |
| --- | --- | --- |
| [docker-minimal](docker-minimal/) | Smallest Docker-backed project: one agent, no scheduler. | No, for `config`/`up`/`ls` |
| [docker-scheduler-cron](docker-scheduler-cron/) | Managed cron scheduler control plane. | No, for `config`/`up`/`ls`/`down` |
| [docker-scheduler-script-url](docker-scheduler-script-url/) | A scheduler script loaded from a relative `file` source, with an HTTP source alternative. | No, for `config`/`up`/`ls`/`down` |
| [docker-scheduler-timeout](docker-scheduler-timeout/) | End-to-end scheduled run that fires, executes the agent, and persists logs. | Yes, for the scheduled run |
| [k8s-scheduler-skills-mcp](k8s-scheduler-skills-mcp/) | Scheduler, Git-sourced skills, MCP, and a PVC-backed volume on the `k8s` driver. | Yes, for the scheduled run |

## Prerequisites

For the Docker examples:

- Docker daemon is running.
- The `agent-compose` daemon is already running.
- The `agent-compose-guest:latest` image exists locally.

The `k8s-scheduler-skills-mcp` example instead needs a daemon running on the
`k8s` driver (`runtime.driver: k8s` in `charts/agent-compose`, or
`RUNTIME_DRIVER=k8s` for a native daemon), a guest image the cluster nodes can
pull, and a StorageClass for its `cache` volume.

From the repository root, build the guest image if needed:

```bash
task image:agent-compose-guest
```

Each example has its own `README.md` with the exact commands and expected
output.
