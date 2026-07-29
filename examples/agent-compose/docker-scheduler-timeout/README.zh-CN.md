# agent-compose Docker timeout scheduler 示例

语言：[English](README.md) | 中文

本示例通过 Docker runtime 执行一次性 timeout trigger，并展示当前 run 模型的
两个层次：

- 外层 scheduler trigger run，使用 `scheduler runs`、`scheduler inspect` 和
  `scheduler logs` 查询；
- trigger 创建的内层 agent run，使用普通 `logs` 命令查询 transcript。

## 前置条件

- `agent-compose` daemon 和 Docker daemon 正在运行。
- 本地存在 `agent-compose-guest:latest`。
- guest 中已配置可用的 Codex 凭据或 API 访问能力。

如有需要，可在仓库根目录构建 guest image：

```bash
task image:agent-compose-guest
```

## Compose 文件

```yaml
name: docker-scheduler-timeout

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    driver:
      docker: {}
    scheduler:
      enabled: true
      triggers:
        - name: run-once-after-15-seconds
          timeout: 15s
          prompt: "Reply with exactly: timeout scheduler ok"
```

timeout 刻意设置得较短。scheduler 默认使用 `sandbox_policy: new` 和
`concurrency_policy: skip`。

## 运行示例

在本目录执行：

```bash
agent-compose config
agent-compose up
agent-compose ls
sleep 35
agent-compose scheduler runs reviewer --limit 1
agent-compose scheduler inspect <scheduler-run-id>
agent-compose scheduler logs <scheduler-run-id>
agent-compose logs reviewer
agent-compose ps --all
agent-compose down
```

如果没有安装二进制，可在仓库根目录为每条命令添加 compose 文件，例如：

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml scheduler runs reviewer --limit 1
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml scheduler inspect <scheduler-run-id>
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml logs reviewer
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml down
```

将 `<scheduler-run-id>` 替换为 `scheduler runs` 输出的外层 run ID。

预期结果：

- `config` 显示 `kind: timeout` 和标准化后的 scheduler 策略。
- `up` 创建 project、agent 和 timeout trigger。
- `ls` 显示该 agent 的 scheduler 为 `true`。
- timeout 触发后，`scheduler runs` 显示一个已结束的外层 run。
- `scheduler inspect` 显示其 trigger、状态、结果和 sandbox IDs。
- `scheduler logs` 显示外层 scheduler 结构化事件；它不会包含内层 agent
  transcript。
- provider 调用成功后，`logs reviewer` 输出内层 project-run transcript，其中
  包含 `timeout scheduler ok`。
- `ps --all` 列出 sandbox 生命周期状态，而不是 agent 列表。
- `down` 停止归属该 project 的 sandboxes，并移除 managed project resources。

标准化后的 scheduler 输出示例：

```yaml
      scheduler:
        enabled: true
        sandbox_policy: new
        concurrency_policy: skip
        triggers:
            - name: run-once-after-15-seconds
              kind: timeout
              timeout: 15s
              prompt: 'Reply with exactly: timeout scheduler ok'
```

timeout 触发前的控制面输出示例（ID 会随本地 compose 路径变化）：

```console
$ agent-compose up
ID            NAME                       TYPE     ACTION
<project-id>  docker-scheduler-timeout   project  created
<agent-id>    reviewer                   agent    created
<trigger-id>  run-once-after-15-seconds  trigger  created

$ agent-compose ls
AGENT     PROVIDER  MODEL  IMAGE                       DRIVER  SCHEDULER
reviewer  codex            agent-compose-guest:latest  docker  true
```

如果外层 scheduler run 成功但看不到预期 transcript，请通过 `logs reviewer`
检查内层 agent run；外层 scheduler logs 与内层 provider 输出本来就是两条独立
日志流。
