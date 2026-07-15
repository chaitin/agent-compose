# Minimal Docker project

Languages: English | [中文](README.zh-CN.md)

This is the smallest Docker-backed project: one agent, an explicit guest image,
and no workspace or scheduler.

## Prerequisites

- The agent-compose daemon and Docker daemon are running.
- `ghcr.io/chaitin/agent-compose-guest:latest` is available locally.

## Run

```bash
agent-compose config
agent-compose up
agent-compose inspect agent reviewer
agent-compose run reviewer --command "printf 'docker minimal ok\\n'" --keep-running
agent-compose ps
```

`ps` lists sandboxes, not project agents. Copy the sandbox ID from `run` or
`ps` when executing another command:

```bash
agent-compose exec <sandbox-id> -- pwd
agent-compose exec <sandbox-id> -- env
agent-compose logs reviewer
agent-compose down
```

Expected behavior: `config` reports `driver.name: docker`, `up` applies one
agent, the command prints `docker minimal ok`, and `down` stops its sandbox and
removes the applied project.
