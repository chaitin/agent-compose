# Scheduler 脚本 runtime

语言：[English](README.md) | 中文

该 inline QJS 脚本组合了稳定的 interval trigger、持久状态、Loader 日志和
Docker-backed shell command。

## 前置条件与配置

Docker 和 daemon 必须已启动。scheduler 使用 `sandbox_policy: new` 和 inline QJS。
`scheduler.state` 归 loader 所有并跨 callback 保留；`scheduler.shell` 在 Docker
sandbox 中执行，不调用模型 provider。

## 运行教程

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

## 验证要点

`scheduler ls heartbeat` 应列出 `warmup`、`follow-up` 和 `heartbeat`。检查两个
timeout trigger，直到 event 分别包含 `heartbeat 1` 和 `heartbeat 2`；有序输出证明
state 在不同 loader callback 间持久化。`down` 禁用 interval 并清理 sandbox。
