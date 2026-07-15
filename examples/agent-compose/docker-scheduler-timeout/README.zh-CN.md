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
- Docker 能访问 `ghcr.io/chaitin/agent-compose-guest:latest`。
- daemon 已配置可用的 Codex/LLM provider。guest 使用 sandbox 范围的 LLM facade，
  不会获得长期 provider key。

如有需要，拉取 compose 文件引用的镜像：

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

## 输出示例

scheduled run 成功后的输出示例如下。动态生成的 ID、hash、时间戳和耗时会不同。

### 1. 配置标准化

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml config
name: docker-scheduler-timeout
agents:
    - name: reviewer
      provider: codex
      image: ghcr.io/chaitin/agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
      scheduler:
        enabled: true
        triggers:
            - name: run-once-after-15-seconds
              kind: timeout
              timeout: 15s
              prompt: 'Reply with exactly: timeout scheduler ok'
network:
    mode: default
```

### 2. 应用 project

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml up
Project: docker-scheduler-timeout
ID: project-docker-scheduler-timeout-3a00cafbae27
Revision: 1
Spec: sha256:283623fe82f0f04270f27a0ec9da4809fc45b4a45c3f15df3f688aba074990b2
Status: applied
Agents: 1
Schedulers: 1

ACTION   TYPE               NAME                                                                     ID
created  project            docker-scheduler-timeout                                                 project-docker-scheduler-timeout-3a00cafbae27
created  project_revision   sha256:283623fe82f0f04270f27a0ec9da4809fc45b4a45c3f15df3f688aba074990b2  project-docker-scheduler-timeout-3a00cafbae27/1
created  project_agent      reviewer                                                                 agent-reviewer-a0befcb745b8
created  agent_definition   reviewer                                                                 agent-reviewer-a0befcb745b8
created  project_scheduler  reviewer                                                                 scheduler-reviewer-default-181247660dc1
created  loader             docker-scheduler-timeout/reviewer scheduler                              loader-reviewer-default-181247660dc1
```

### 3. 成功的 scheduled run

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml ps
AGENT     SCHEDULER  LATEST RUN                 RUN STATUS  SESSION  DRIVER  IMAGE
reviewer  enabled    run-reviewer-28c0ef985c8d  succeeded   -        docker  ghcr.io/chaitin/agent-compose-guest:latest
```

### 4. 查看成功 run

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml inspect run run-reviewer-28c0ef985c8d
{
  "run_id": "run-reviewer-28c0ef985c8d",
  "project_name": "docker-scheduler-timeout",
  "agent_name": "reviewer",
  "source": "scheduler",
  "status": "succeeded",
  "session_id": "23a1ede4-3325-470d-99db-377e3296e7a2",
  "exit_code": 0,
  "duration_ms": 10917,
  "prompt": "Reply with exactly: timeout scheduler ok",
  "output": "timeout scheduler ok",
  "result_json": "{\"agent\":\"codex\",\"exitCode\":0,\"stopReason\":\"completed\",\"success\":true}",
  "driver": "docker",
  "image_ref": "ghcr.io/chaitin/agent-compose-guest:latest"
}
```

### 5. Run 日志

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml logs --run run-reviewer-28c0ef985c8d
timeout scheduler ok
```

### 6. 禁用 scheduler

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-timeout/agent-compose.yml down
Project: docker-scheduler-timeout
ID: project-docker-scheduler-timeout-3a00cafbae27
Status: down
Failed session stops: 0

ACTION   TYPE               NAME      ID                                       MESSAGE
updated  project_scheduler  reviewer  scheduler-reviewer-default-181247660dc1  disabled by project down
updated  loader             reviewer  loader-reviewer-default-181247660dc1     disabled by project down
```

### 7. 自动 timeout 成功输出

自动 timeout trigger 成功后，run 输出如下：

```console
status=RUN_STATUS_SUCCEEDED
run=236f8ab07988370527571cd40eaf38e9ced83afb22651e2c8b0f4b6e15d4b960
sandbox=825642e2863644eac04f63e4bec17af025379ce8a050561abe58a178a3443c0e
```

run 和 sandbox ID 每次动态生成，本地结果会不同。
