# agent-compose Docker 最小示例

语言：[English](README.md) | 中文

本示例展示一个使用 Docker runtime driver 的最小可用
`agent-compose.yml`。

它刻意保持最小化：

- 一个 project
- 一个 agent
- Docker runtime driver
- 显式指定 guest image
- 不启用 scheduler
- `config`、`up` 和 `ps` 不要求配置模型或 API key

## 前置条件

- Docker daemon 正在运行。
- `agent-compose` daemon 已经启动。
- Docker 能访问 `ghcr.io/chaitin/agent-compose-guest:latest`。

如有需要，拉取 compose 文件引用的镜像：

```bash
docker pull ghcr.io/chaitin/agent-compose-guest:latest
```

如果 `agent-compose` 二进制已经在 `PATH` 中，可以直接检查 daemon：

```bash
agent-compose status
```

如果是在源码仓库中调试，也可以直接运行 CLI：

```bash
go run ./cmd/agent-compose status
```

## Compose 文件

本目录包含一个最小 Docker project：

```yaml
name: docker-minimal

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
    driver:
      docker: {}
```

关键配置是：

```yaml
driver:
  docker: {}
```

如果 agent 省略 `driver`，compose normalizer 会默认使用 `docker`。
本示例显式设置 `docker: {}`，是为了明确说明预期的 runtime。

## 运行示例

在本目录执行：

```bash
agent-compose config
agent-compose up
agent-compose ps
```

如果没有安装二进制，也可以在仓库根目录执行：

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml ps
```

预期结果：

- `config` 输出标准化后的 project，并显示 `driver.name: docker`。
- `up` 创建或更新 project 和 managed agent definition。
- `ps` 显示 `reviewer` agent 使用 Docker 和发布版 guest 镜像。

## 可选运行测试

启动一次 runtime session，并在运行结束后保留 session：

```bash
agent-compose run reviewer --keep-running --prompt "hello from docker minimal example"
```

真正执行 agent 需要 guest runtime 可用，并在 daemon 中配置 provider。guest 使用
sandbox 范围的 LLM facade，不会获得长期 provider 凭据。

如果 runtime session 仍在运行，可以在其中执行命令：

```bash
agent-compose exec <sandbox-id> -- pwd
agent-compose exec <sandbox-id> -- env
```

清理正在运行的 project sessions：

```bash
agent-compose down
```

## 验证输出

以下为一次本地验证运行的输出。

下面记录的输出使用等价的本地构建镜像 `agent-compose-guest:latest`。当前 compose
文件使用发布版镜像；动态生成的 ID、hash 和时间戳也会不同。

### 1. 配置标准化

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml config
name: docker-minimal
agents:
    - name: reviewer
      provider: codex
      image: agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
network:
    mode: default
```

### 2. 应用 project

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml up
Project: docker-minimal
ID: project-docker-minimal-ad604c8bf8d3
Revision: 1
Spec: sha256:0e351a523ae793f780fc0933f3b88920501f20dfd4d855654fe711a8a3cb4edd
Status: applied
Agents: 1
Schedulers: 0

ACTION   TYPE              NAME                                                                     ID
created  project           docker-minimal                                                           project-docker-minimal-ad604c8bf8d3
created  project_revision  sha256:0e351a523ae793f780fc0933f3b88920501f20dfd4d855654fe711a8a3cb4edd  project-docker-minimal-ad604c8bf8d3/1
created  project_agent     reviewer                                                                 agent-reviewer-a9f84de36227
created  agent_definition  reviewer                                                                 agent-reviewer-a9f84de36227
```

### 3. Project 状态

```console
$ go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml ps
AGENT     SCHEDULER  LATEST RUN  RUN STATUS  SESSION  DRIVER  IMAGE
reviewer  disabled   -           -           -        docker  agent-compose-guest:latest
```

### 4. Docker runtime 容器

```console
$ docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
NAMES                                                IMAGE                        STATUS
agent-compose-8aa2625d-db67-4428-82ae-8bef1a137a2f   agent-compose-guest:latest   Up 14 seconds
```

### 5. Provider 成功输出

daemon 配置可用 provider 后，prompt run 成功输出如下：

```json
{
  "id": "8363e8c144f6ab0124054c11a6ff06e67f74fe561c2af46e7b06dd2ffb420027",
  "status": "succeeded",
  "sandbox_id": "9f060d2ea52b1a4bedc740715ac8f745274820df03f5b551e01841315b006fb7",
  "duration_ms": 15435,
  "output": "agent-compose live provider ok",
  "driver": "docker",
  "image_ref": "ghcr.io/chaitin/agent-compose-guest:latest"
}
```

ID 和耗时是动态值，本地运行会不同。
