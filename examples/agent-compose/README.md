# agent-compose examples

Languages: English | [中文](README.zh-CN.md)

Runnable examples for the `agent-compose` Docker runtime driver, ordered from
simplest to most complete.

| Example | What it shows | Needs provider auth |
| --- | --- | --- |
| [docker-minimal](docker-minimal/) | Smallest Docker-backed project: one agent, no scheduler. | No, for `config`/`up`/`ps` |
| [docker-scheduler-cron](docker-scheduler-cron/) | Managed cron scheduler control plane. | No, for `config`/`up`/`ps`/`down` |
| [docker-scheduler-script-url](docker-scheduler-script-url/) | A scheduler script loaded from a relative file URL source. | No, for `config`/`up`/`ps`/`down` |
| [docker-scheduler-timeout](docker-scheduler-timeout/) | End-to-end scheduled run that fires, executes the agent, and persists logs. | Yes, for the scheduled run |
| [docker-workspace-lifecycle](docker-workspace-lifecycle/) | Local workspace copy plus sandbox stop, resume, exec, and removal. | No |
| [docker-multi-agent](docker-multi-agent/) | Two independent agents using the same workspace source. | No for command runs; yes for prompts |
| [docker-env-secrets](docker-env-secrets/) | Dotenv, project/agent variables, and secret redaction. | No |
| [docker-volume-persistence](docker-volume-persistence/) | Managed volumes and read-only bind mounts. | No |
| [docker-build](docker-build/) | Build and run a guest-derived Docker image. | No |
| [docker-scheduler-script-runtime](docker-scheduler-script-runtime/) | Inline QJS, persisted scheduler state, and shell callbacks. | No |
| [boxlite-minimal](boxlite-minimal/) | Minimal BoxLite configuration template. | Only for prompt runs |
| [microsandbox-minimal](microsandbox-minimal/) | Minimal Microsandbox configuration template. | Only for prompt runs |

## Common prerequisites

- Docker daemon is running.
- The `agent-compose` daemon is already running.
- Docker can access `ghcr.io/chaitin/agent-compose-guest:latest`.

Pull the image used by the examples if needed:

```bash
docker pull ghcr.io/chaitin/agent-compose-guest:latest
```

Each example has its own `README.md` with the exact commands and expected
output.

BoxLite and Microsandbox additionally require Linux, KVM access, their runtime
artifacts, and a binary that includes the selected compiled driver. Their
examples are configuration templates unless run on a prepared host.
