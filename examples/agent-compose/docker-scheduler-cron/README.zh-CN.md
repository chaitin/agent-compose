# 声明式 cron scheduler

语言：[English](README.md) | 中文

该 project 声明一个每小时触发的 cron。scheduler 状态应通过 scheduler 命令
查看；`ps` 只用于列出 sandbox。

```bash
agent-compose config
agent-compose up
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer hourly-review
agent-compose inspect project docker-scheduler-cron
agent-compose down
```

标准化配置包含 `sandbox_policy: new`，trigger 包含 `kind: cron`。`up`、查询和
`down` 不需要 provider 凭证；等待或手动运行 trigger 需要可用的 Codex provider。

本地快速观察时，可以把 cron 字段替换为 `interval: 1m`。
