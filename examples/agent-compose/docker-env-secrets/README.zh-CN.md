# Project 环境变量和 secret

语言：[English](README.md) | 中文

该示例使用显式 dotenv 文件、project variables、agent 专属环境变量和 secret
元数据。仓库中提交的值是刻意设置的假值。

```bash
agent-compose config
agent-compose up
agent-compose run inspector --command 'test "$PROJECT_VALUE" = project-level && test "$AGENT_VALUE" = agent-level && test "$PROJECT_SECRET" = safe-example-secret && test "$AGENT_SECRET" = safe-example-secret && echo "environment ok"'
agent-compose down
```

`config` 会隐藏标记为 `secret: true` 的值。Project variables 会传给 run，agent
env 只属于该 agent。启动 CLI 时的进程环境变量优先于 `example.env`。
