# agent-compose Docker cron scheduler 示例

语言：[English](README.md) | 中文

本示例展示一个使用 Docker runtime 的 agent-compose project，并为它配置
managed cron scheduler。

它验证 scheduler 控制面流程：

- 从 `agent-compose.yml` 解析 cron trigger
- 将 project 应用到 daemon
- 创建 managed project scheduler 和 loader
- 确认 scheduler 处于 enabled 状态
- 使用 `agent-compose down` 禁用 scheduler

本示例的 `config`、`up`、`ps` 和 `down` 不要求真实调用模型。真正的定时
运行仍然需要 guest runtime 可用，并且 provider 已完成认证。

## 前置条件

- Docker daemon 正在运行。
- `agent-compose` daemon 已经启动。
- Docker 能拉取 `ghcr.io/chaitin/agent-compose-guest:latest`，或本地已有该镜像。

如果还没有 guest image，可拉取本示例实际引用的镜像：

```bash
docker pull ghcr.io/chaitin/agent-compose-guest:latest
```

## Compose 文件

```yaml
name: docker-scheduler-cron

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
    driver:
      docker: {}
    scheduler:
      enabled: true
      triggers:
        - name: hourly-review
          cron: "0 * * * *"
          prompt: "Review the current project state and summarize any important changes."
```

trigger 使用标准 cron 语法。下面的表达式表示每小时整点运行：

```yaml
cron: "0 * * * *"
```

## 运行示例

在本目录执行：

```bash
agent-compose config
agent-compose up
agent-compose ps
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer hourly-review
agent-compose inspect project docker-scheduler-cron
agent-compose down
```

如果没有安装二进制，也可以在仓库根目录执行：

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml ps
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml scheduler ls reviewer
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml scheduler inspect reviewer hourly-review
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml inspect project docker-scheduler-cron
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml down
```

预期结果：

- `config` 显示 trigger 为 `kind: cron`。
- `up` 创建 `project_scheduler` 和 `loader` 资源。
- `ps` 显示 scheduler 为 `enabled`。
- `scheduler ls` 显示 `hourly-review` 且类型为 `cron`。
- `scheduler inspect` 显示配置的表达式和 prompt。
- `inspect project` 显示 `scheduler_count: 1` 和 `trigger_count: 1`。
- `down` 禁用 managed scheduler 和 loader。

## 更容易观察触发的方法

如果本地演示时希望 scheduler 很快触发，可以使用 interval trigger 替代 cron：

```yaml
scheduler:
  enabled: true
  triggers:
    - name: every-minute
      interval: 1m
      prompt: "Say hello from the interval trigger."
```

需要基于日历时间调度时使用 cron；需要本地快速反馈时使用 interval。

## 立即运行 agent 路径

测试 provider 路径不需要修改仓库中的 cron 表达式。手动 trigger 会使用相同的
托管 agent prompt 流程：

```bash
agent-compose scheduler trigger reviewer hourly-review \
  --prompt "Reply with exactly: cron scheduler ok"
```

该命令要求可用的 Codex/LLM provider。成功结果应包含 `status: succeeded`，输出中
包含 `cron scheduler ok`。

## 验证要点

真实 daemon E2E 验证控制面，provider E2E 则通过真实 Docker guest 和 Codex CLI
执行 trigger。应确认：

- `config` 将 trigger 归一化为 `kind: cron`。
- `up` 创建一个启用的 scheduler 和一个 trigger。
- `scheduler ls reviewer` 包含 `hourly-review` 和 `cron`。
- 配置 provider 后，手动 trigger 返回 run ID 和 `status: succeeded`。
- `down` 禁用托管 scheduler 和 loader。

project、scheduler、loader 和 run ID 均由环境动态生成。
