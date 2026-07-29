# agent-compose Docker cron scheduler 示例

语言：[English](README.md) | 中文

本示例为一个 Docker-backed agent 定义 managed cron trigger。它验证当前的
project、agent、scheduler 和 trigger 控制面模型，不要求真实调用模型。

## 前置条件

- `agent-compose` daemon 正在运行。
- 只有 trigger 真正启动 agent sandbox 时才需要 Docker 和
  `agent-compose-guest:latest`。
- 真正执行 scheduled model run 需要 provider 认证。

如有需要，可在仓库根目录构建 guest image：

```bash
task image:agent-compose-guest
```

## Compose 文件

```yaml
name: docker-scheduler-cron

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    driver:
      docker: {}
    scheduler:
      enabled: true
      triggers:
        - name: hourly-review
          cron: "0 * * * *"
          prompt: "Review the current project state and summarize any important changes."
```

`0 * * * *` 表示每小时整点运行。该 trigger 没有设置 `timezone`，因此使用
daemon 本地时区（优先读取 `TZ`，否则使用 `/etc/localtime`）。如果调度时间不应
随 daemon 部署位置变化，请添加 `timezone: UTC` 或 `Asia/Shanghai` 等 IANA
时区。

scheduler 默认使用 `sandbox_policy: new` 和 `concurrency_policy: skip`；
标准化后的 config 会显示这两个默认值。

## 校验并应用

在本目录执行：

```bash
agent-compose config
agent-compose up
agent-compose ls
agent-compose scheduler ls
agent-compose inspect project
agent-compose down
```

如果没有安装二进制，也可以在仓库根目录执行：

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml ls
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml scheduler ls
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml inspect project
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml down
```

预期结果：

- `config` 显示 `kind: cron` 和 scheduler 标准化后的策略。
- `up` 创建 project、agent 和 `hourly-review` trigger。
- `ls` 显示该 agent 的 `SCHEDULER` 为 `true`。
- `scheduler ls` 显示注册的 cron trigger。
- `inspect project` 显示 `scheduler_count: 1` 和 `trigger_count: 1`。
- `down` 从 daemon 移除 managed trigger、project 和 agent。

标准化后的 scheduler 输出示例：

```yaml
      scheduler:
        enabled: true
        sandbox_policy: new
        concurrency_policy: skip
        triggers:
            - name: hourly-review
              kind: cron
              cron: 0 * * * *
              prompt: Review the current project state and summarize any important changes.
```

控制面输出示例（ID 会随本地 compose 路径变化）：

```console
$ agent-compose up
ID            NAME                   TYPE     ACTION
<project-id>  docker-scheduler-cron  project  created
<agent-id>    reviewer               agent    created
<trigger-id>  hourly-review          trigger  created

$ agent-compose ls
AGENT     PROVIDER  MODEL  IMAGE                       DRIVER  SCHEDULER
reviewer  codex            agent-compose-guest:latest  docker  true

$ agent-compose down
ID            NAME                   TYPE     ACTION   MESSAGE
<trigger-id>  hourly-review          trigger  removed  disabled by project down
<project-id>  docker-scheduler-cron  project  removed  removed by project down
<agent-id>    reviewer               agent    removed
```

## 更容易观察 trigger

如需在本地快速观察，可将 cron trigger 替换为 interval trigger：

```yaml
scheduler:
  enabled: true
  triggers:
    - name: every-minute
      interval: 1m
      prompt: "Say hello from the interval trigger."
```

需要基于日历时间调度时使用 cron；需要基于经过时长调度时使用 interval。
