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
- Docker 能拉取 `ghcr.io/chaitin/agent-compose-guest:latest`，或本地已有该镜像。

如果还没有 guest image，可拉取本示例实际引用的镜像：

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
- `ps` 显示 `reviewer` agent 使用 Docker 和 `ghcr.io/chaitin/agent-compose-guest:latest`。

## 可选运行测试

启动一次 runtime session，并在运行结束后保留 session：

```bash
agent-compose run reviewer --keep-running --prompt "hello from docker minimal example"
```

真正执行 agent 需要 guest runtime 可用，并在 daemon 中配置 provider。长期凭据
保留在 daemon；guest Codex CLI 使用 agent-compose 注入的 sandbox 范围 LLM facade
变量。

如果 runtime session 仍在运行，可以在其中执行命令：

```bash
agent-compose exec <sandbox-id> -- pwd
agent-compose exec <sandbox-id> -- env
```

清理正在运行的 project sessions：

```bash
agent-compose down
```

将 `<sandbox-id>` 替换为 `run` 返回或 `agent-compose ps --all` 显示的 sandbox
ID。`down` 会停止项目 sandbox；如果还要删除它，请使用
`agent-compose rm <sandbox-id>`。

## 验证要点

真实 daemon Docker E2E 会运行本示例。手工检查时应确认：

- `config` 显示 `driver.name: docker` 和发布版 guest 镜像。
- `up` 显示一个已应用项目和一个 agent。
- `run reviewer --command "printf 'docker minimal ok\\n'"` 无需 provider 凭证即可成功。
- prompt run 只有在 daemon 配置了可用 LLM provider 时才会成功。
- 保留运行后，`ps --all` 显示非空 sandbox ID。
- `exec <sandbox-id> -- pwd` 返回 guest 工作目录。

project、revision、run 和 sandbox ID 均由环境动态生成，因此本文不写死这些值。
