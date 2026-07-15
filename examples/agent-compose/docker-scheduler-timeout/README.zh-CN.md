# agent-compose Docker timeout scheduler 示例

语言：[English](README.md) | 中文

本示例使用 Docker runtime 跑通一个端到端的 scheduled agent 流程。

它验证 agent-compose 可以完成：

- 从 `agent-compose.yml` 解析 timeout trigger
- 将 project 应用到 daemon
- 创建 managed scheduler 和 loader
- 由 scheduler 自动触发运行
- 启动 Docker-backed agent runtime session
- 执行配置的 agent prompt
- 持久化成功的 project run 和日志
- 使用 `agent-compose down` 禁用 scheduler

## 前置条件

- Docker daemon 正在运行。
- `agent-compose` daemon 已经启动。
- Docker 能拉取 `ghcr.io/chaitin/agent-compose-guest:latest`，或本地已有该镜像。
- daemon 配置了可用的 Codex/LLM provider。长期 provider 凭证保留在 daemon 中；
  guest 通过 sandbox 范围的 LLM facade 调用模型。

如果还没有 guest image，可拉取本示例实际引用的镜像：

```bash
docker pull ghcr.io/chaitin/agent-compose-guest:latest
```

## Compose 文件

```yaml
name: docker-scheduler-timeout

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
    driver:
      docker: {}
    scheduler:
      enabled: true
      sandbox_policy: new
      triggers:
        - name: run-once-after-15-seconds
          timeout: 15s
          prompt: "Reply with exactly: timeout scheduler ok"
```

`timeout: 15s` 刻意设置得较短，方便快速验证完整流程。

## 运行示例

在本目录执行：

```bash
agent-compose config
agent-compose up
sleep 35
agent-compose ps
agent-compose inspect run <run-id>
agent-compose logs --run <run-id>
agent-compose down
```

如果没有安装二进制，也可以在仓库根目录执行：

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml up
sleep 35
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml ps
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml inspect run <run-id>
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml logs --run <run-id>
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml down
```

将 `<run-id>` 替换为上一步 `ps` 输出中显示的 run id。

预期结果：

- `config` 显示 trigger 为 `kind: timeout`。
- `up` 创建或更新 managed scheduler 和 loader。
- 等待 timeout 触发一次后，`ps` 显示 scheduler 创建的 run。
- `inspect run <run-id>` 显示 `source: scheduler`、`status: succeeded`、`driver: docker`，并包含 agent 输出。
- `logs --run <run-id>` 输出 agent 日志。
- `down` 禁用 managed scheduler 和 loader。

`sandbox_policy: new` 会为 scheduled run 创建全新 sandbox。guest 获得 facade token
和可访问的 daemon URL，而不是长期 provider key。

## 验证要点

provider E2E 会通过真实 daemon、Docker guest 和 Codex CLI 运行该流程。手工运行时
应确认：

- `config` 显示 `kind: timeout`、`timeout: 15s` 和 `sandbox_policy: new`。
- `scheduler inspect reviewer run-once-after-15-seconds` 最终显示 trigger 已触发。
- `ps` 显示一个来自 scheduler 且 `status: succeeded` 的 run。
- `inspect run <run-id>` 显示 `source: scheduler`、`driver: docker`、非空 sandbox
  ID，且输出包含 `timeout scheduler ok`。
- `logs --run <run-id>` 输出相同的模型结果。
- `down` 禁用 scheduler 并清理项目 sandbox。

动态 ID 和实际耗时因环境而异，因此本文不写死这些值。
