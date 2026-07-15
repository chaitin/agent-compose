# Scheduler script runtime

Languages: English | [中文](README.zh-CN.md)

This inline QJS script combines a stable interval trigger, persisted state,
loader logs, and a Docker-backed shell command.

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
