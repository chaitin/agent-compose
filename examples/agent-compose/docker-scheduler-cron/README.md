# Declarative cron scheduler

Languages: English | [中文](README.zh-CN.md)

This project declares an hourly cron trigger. Scheduler state is inspected with
the scheduler commands; `ps` is reserved for sandboxes.

```bash
agent-compose config
agent-compose up
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer hourly-review
agent-compose inspect project docker-scheduler-cron
agent-compose down
```

The normalized scheduler includes `sandbox_policy: new` and the trigger includes
`kind: cron`. `up`, inspection, and `down` do not need provider credentials.
Waiting for or manually running the trigger does need a working Codex provider.

For quicker experiments, replace the cron field with `interval: 1m`.
