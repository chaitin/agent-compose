# Scheduler script runtime

Languages: English | [中文](README.zh-CN.md)

This inline QJS script combines a stable interval trigger, persisted state,
loader logs, and a Docker-backed shell command.

## Prerequisites and configuration

Docker and the daemon must be running. The scheduler uses `sandbox_policy: new`
and inline QJS. `scheduler.state` belongs to the loader and persists across
callbacks; `scheduler.shell` runs in a Docker sandbox. No provider call occurs.

## Run the tutorial

```bash
agent-compose up
agent-compose scheduler ls heartbeat
agent-compose scheduler inspect heartbeat warmup
agent-compose scheduler inspect heartbeat follow-up
# Wait until both timeout triggers report that they have fired.
agent-compose down
```

The two automatic timeout runs produce `heartbeat 1` and `heartbeat 2`,
demonstrating that loader state persists between runs. The interval remains as
the long-running schedule. No model provider is called.

## What to verify

`scheduler ls heartbeat` should list `warmup`, `follow-up`, and `heartbeat`.
Inspect the two timeout triggers until their events contain `heartbeat 1` and
`heartbeat 2`. That ordered output proves state persisted between distinct
loader callbacks. `down` disables the interval and cleans project sandboxes.
