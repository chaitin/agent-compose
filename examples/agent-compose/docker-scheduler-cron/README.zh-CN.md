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
- Docker 能访问 `ghcr.io/chaitin/agent-compose-guest:latest`。

如有需要，拉取 compose 文件引用的镜像：

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
agent-compose inspect project docker-scheduler-cron
agent-compose down
```

如果没有安装二进制，也可以在仓库根目录执行：

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml ps
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml inspect project docker-scheduler-cron
go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml down
```

预期结果：

- `config` 显示 trigger 为 `kind: cron`。
- `up` 创建 `project_scheduler` 和 `loader` 资源。
- `ps` 显示 scheduler 为 `enabled`。
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

## 验证输出

以下为一次本地验证运行的输出。

下面记录的输出使用等价的本地构建镜像 `agent-compose-guest:latest`。当前 compose
文件使用发布版镜像；动态生成的 ID、hash 和时间戳也会不同。

### 1. 配置标准化

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml config
name: docker-scheduler-cron
agents:
    - name: reviewer
      provider: codex
      image: agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
      scheduler:
        enabled: true
        triggers:
            - name: hourly-review
              kind: cron
              cron: 0 * * * *
              prompt: Review the current project state and summarize any important changes.
network:
    mode: default
```

### 2. 应用 project

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml up
Project: docker-scheduler-cron
ID: project-docker-scheduler-cron-034aaf526f91
Revision: 1
Spec: sha256:93950d90a6dbd56a141cbd0b059c06eb37b4db6bb27860b24cb78bea781536d5
Status: applied
Agents: 1
Schedulers: 1

ACTION   TYPE               NAME                                                                     ID
created  project            docker-scheduler-cron                                                    project-docker-scheduler-cron-034aaf526f91
created  project_revision   sha256:93950d90a6dbd56a141cbd0b059c06eb37b4db6bb27860b24cb78bea781536d5  project-docker-scheduler-cron-034aaf526f91/1
created  project_agent      reviewer                                                                 agent-reviewer-4bff2fb6372a
created  agent_definition   reviewer                                                                 agent-reviewer-4bff2fb6372a
created  project_scheduler  reviewer                                                                 scheduler-reviewer-default-ed0b5bed0daa
created  loader             docker-scheduler-cron/reviewer scheduler                                 loader-reviewer-default-ed0b5bed0daa
```

### 3. Scheduler 状态

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml ps
AGENT     SCHEDULER  LATEST RUN  RUN STATUS  SESSION  DRIVER  IMAGE
reviewer  enabled    -           -           -        docker  agent-compose-guest:latest
```

### 4. 查看 project

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml inspect project docker-scheduler-cron
{
  "project": {
    "id": "project-docker-scheduler-cron-034aaf526f91",
    "name": "docker-scheduler-cron",
    "current_revision": 1,
    "agent_count": 1,
    "scheduler_count": 1
  },
  "agents": [
    {
      "agent_name": "reviewer",
      "provider": "codex",
      "image": "agent-compose-guest:latest",
      "driver": "docker",
      "scheduler_enabled": true
    }
  ],
  "schedulers": [
    { "agent_name": "reviewer", "enabled": true, "trigger_count": 1 }
  ]
}
```

### 5. 禁用 scheduler

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-scheduler-cron/agent-compose.yml down
Project: docker-scheduler-cron
ID: project-docker-scheduler-cron-034aaf526f91
Status: down
Failed session stops: 0

ACTION   TYPE               NAME      ID                                       MESSAGE
updated  project_scheduler  reviewer  scheduler-reviewer-default-ed0b5bed0daa  disabled by project down
updated  loader             reviewer  loader-reviewer-default-ed0b5bed0daa     disabled by project down
```

### 6. 手动 trigger 成功输出

配置可用 provider 后，手动触发 cron 条目的输出如下：

```json
{
  "id": "b602c4c8479449081121ed0c9c8bfb9ded5c74d77027e6169098bc5f179e954c",
  "status": "succeeded",
  "sandbox_id": "2d60f25ed6752646d7468500d112dbf01c2b2f0f9c6c8aba8a965deab52c92e2",
  "duration_ms": 10822,
  "output": "cron scheduler ok",
  "driver": "docker",
  "image_ref": "ghcr.io/chaitin/agent-compose-guest:latest"
}
```

ID 和耗时是动态值，本地运行会不同。
