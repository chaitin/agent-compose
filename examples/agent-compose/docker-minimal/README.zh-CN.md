# agent-compose Docker 最小示例

语言：[English](README.md) | 中文

本示例给出使用 Docker runtime driver 的最小可用 `agent-compose.yml`：一个
enabled agent、一个显式 guest image，并且不配置 scheduler。

`config`、`up` 和 `ls` 只验证控制面，不要求配置模型或 API key。

## 前置条件

- `agent-compose` daemon 正在运行。
- 只有真正启动 agent sandbox 时才需要 Docker。
- 真正运行前，本地需要存在 `agent-compose-guest:latest`。

如有需要，可在仓库根目录构建 guest image：

```bash
task image:agent-compose-guest
```

继续之前先检查 daemon：

```bash
agent-compose status
```

在源码仓库中工作时，可将 `agent-compose` 替换成
`go run ./cmd/agent-compose`。

## Compose 文件

```yaml
name: docker-minimal

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    driver:
      docker: {}
```

agent 默认使用 `enabled: true`。默认 driver 也是 Docker；这里仍显式写出
`docker: {}`，以便清楚表达 runtime 要求。

## 校验并应用

在本目录执行：

```bash
agent-compose config
agent-compose up
agent-compose ls
```

如果没有安装二进制，也可以在仓库根目录执行：

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml up
go run ./cmd/agent-compose --file examples/agent-compose/docker-minimal/agent-compose.yml ls
```

预期结果：

- `config` 标准化后输出 `enabled: true` 和 `driver.name: docker`。
- `up` 创建或更新 project 及其 `reviewer` agent。
- `ls` 显示该 agent 使用 Docker 和 `agent-compose-guest:latest`，scheduler 为
  `false`。

标准化输出示例：

```yaml
name: docker-minimal
agents:
    - name: reviewer
      enabled: true
      provider: codex
      image: agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
```

控制面输出示例（ID 会随本地 compose 路径变化）：

```console
$ agent-compose up
ID            NAME            TYPE     ACTION
<project-id>  docker-minimal  project  created
<agent-id>    reviewer        agent    created

$ agent-compose ls
AGENT     PROVIDER  MODEL  IMAGE                       DRIVER  SCHEDULER
reviewer  codex            agent-compose-guest:latest  docker  false
```

## 可选的真实运行

真正执行 agent 需要 Docker、兼容的 guest image，以及 guest 环境中可用的
Codex 凭据或 API 访问能力：

```bash
agent-compose run reviewer --keep-running --prompt "hello from docker minimal example"
```

先列出运行中的 sandbox，再把它的 ID 作为 `exec` 的位置参数：

```bash
agent-compose ps
agent-compose exec <sandbox-id> -- pwd
agent-compose exec <sandbox-id> -- env
```

`exec` 已不再支持旧的 agent-name 目标参数。

清理 project 和仍在运行的 project sandboxes：

```bash
agent-compose down
```
