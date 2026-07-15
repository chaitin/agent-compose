# 声明式 timeout scheduler

语言：[English](README.md) | 中文

该示例在 scheduler 应用 15 秒后触发一次 Codex prompt。

## 前置条件

需要 Docker、发布的 guest image，以及 daemon 侧可用的 Codex/OpenAI provider
配置。

```bash
agent-compose config
agent-compose up
agent-compose scheduler ls reviewer
agent-compose --json logs reviewer
agent-compose inspect run <run-id>
agent-compose logs --run <run-id>
agent-compose down
```

轮询 `logs reviewer` 直到 run 出现，不要依赖固定完成时间。JSON 日志响应中包含
run ID。成功的 detail 应显示 `source: scheduler`、`status: succeeded` 和 Docker
driver。

标准化 scheduler 包含 `sandbox_policy: new` 和 `kind: timeout`。
