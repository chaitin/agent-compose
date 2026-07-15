# Scheduler 脚本 runtime

语言：[English](README.md) | 中文

该 inline QJS 脚本组合了稳定的 interval trigger、持久状态、Loader 日志和
Docker-backed shell command。

```bash
agent-compose up
agent-compose scheduler ls heartbeat
agent-compose scheduler inspect heartbeat warmup
agent-compose scheduler inspect heartbeat follow-up
# 等待两个 timeout trigger 都显示已触发。
agent-compose down
```

两个自动 timeout run 分别产生 `heartbeat 1` 和 `heartbeat 2`，证明 Loader
state 会在 run 之间持久化；interval 保留为长期调度。该流程不会调用模型 provider。
