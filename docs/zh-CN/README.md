# agent-compose 中文文档

**agent-compose 是一个 daemon + CLI 形态的控制面，用于在隔离 sandbox 中运行 AI coding agent。** 你在 `agent-compose.yml` 里声明 agent，一个常驻 daemon 负责为每个 agent 构建、运行、调度并代理一个隔离的 runtime。

> 公开预览阶段。API、运行时打包和部署默认值仍可能调整。适合实验、本地开发和预览部署，尚未达到稳定生产平台。

英文首页见 [../../README.md](../../README.md)。

## agent-compose 是什么？

如果你了解 Docker Compose，这里的心智模型很类似：你声明的不是容器，而是 **agent**。每个 agent 选择一个 provider CLI —— `codex`、`claude`（Claude Code）、`gemini` 或 `opencode` —— daemon 给它一个带 workspace 的隔离 sandbox，然后按 prompt、shell 命令、定时或事件来运行它。真实的 provider API key 留在 daemon 上，不会进入 guest。

你用 Compose 风格的 CLI（`up`、`run`、`ps`、`logs`、`down`）管理整个生命周期，一切由一个声明式文件驱动。

具体能力：

- **声明式 compose 模型**（`agent-compose.yml`），支持 `${ENV}` 插值。
- **多 provider guest agent**：Codex、Claude Code、Gemini、OpenCode CLI。
- **三种 runtime driver**：`docker`（默认）、`boxlite`（microVM）、`microsandbox`。
- **scheduler**：`cron`、`interval`、`timeout`、`event` 四种 trigger，或内联 JavaScript scheduler 脚本。
- **事件触发与 webhook**，支持事件驱动的 agent run。
- **workspace** 从本地目录或 Git 仓库拉取。
- **Runtime LLM Facade**，托管 LLM 凭据，使 provider key 不进入 guest 容器。
- 每个 agent 可配 **MCP server、可复用 skill、具名 volume**。
- **Jupyter 代理**，支持 notebook 风格的 guest runtime。
- **v1/v2 Connect API** 与生成的 TypeScript client。

## 工作原理

**daemon** 是唯一的状态权威：负责持久化、scheduler 执行、runtime 生命周期、Connect/HTTP API 和 Jupyter 代理。**CLI** 是一个轻客户端 —— 读取本地 `agent-compose.yml`，做本地校验，再调用 daemon。compose 文件描述的是 *project 和 agent*，不是已经在跑的 sandbox。**Web UI** 是独立服务（[agent-compose-ui](https://github.com/chaitin/agent-compose-ui)），不由 daemon 托管。

完整架构见 [design/agent-compose_design.md](design/agent-compose_design.md)。

## 快速开始

### 方式 A —— 部署服务器（推荐）

一行安装脚本会用 Docker Compose 部署 agent-compose（含 Web UI），支持 Linux amd64/arm64：

```bash
curl -fsSL https://github.com/chaitin/agent-compose/releases/latest/download/install.sh | bash
```

首次运行会生成 `admin` 密码并打印一次，之后通过打印出的 URL 使用 Web UI。安装选项（`--dir`、`--port`、`--upgrade`、使用镜像/私有 registry 等）见 [../../deploy/README.md](../../deploy/README.md)。

### 方式 B —— 从源码构建（用于 CLI 工作流）

```bash
task build                       # 产物在 ./build/agent-compose
export PATH="$PWD/build:$PATH"   # 让 `agent-compose` 进入 PATH
agent-compose daemon
```

daemon 默认监听本地 Unix socket。需要本地 HTTP endpoint 时：

```bash
HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon
agent-compose --host http://127.0.0.1:7410 status
```

### 运行第一个 agent

在本地 daemon 运行的前提下（方式 B），创建 `agent-compose.yml`：

```yaml
name: demo

agents:
  reviewer:
    provider: codex
    image: ghcr.io/chaitin/agent-compose-guest:latest
    driver:
      docker: {}
```

然后驱动生命周期：

```bash
agent-compose up                                  # 把 project 应用到 daemon
agent-compose ps                                  # 列出 project sandbox
agent-compose run reviewer --prompt "Review this change"
agent-compose logs --agent reviewer
agent-compose down                                # 停止 sandbox、禁用 scheduler
```

更多可运行示例（cron、timeout、scheduler 脚本）见 [../../examples/agent-compose/](../../examples/agent-compose/)。

## Compose 配置

**顶层字段：** `name`、`variables`、`agents`、`mcps`、`volumes`、`network`。

**agent 常用字段：** `provider`、`model`、`system_prompt`、`image`、`driver`、
`env`（scalar 或 `{ value, secret }`）、`workspace`、`scheduler`、`mcps`、`skills`、`volumes`。

为 agent 从本地路径（`provider: local`）或 Git 仓库（`provider: git`）配置 workspace：

```yaml
agents:
  reviewer:
    workspace:
      provider: git
      url: https://github.com/example/repo.git
      branch: main
```

添加定时或事件驱动的 run。`scheduler.triggers` 与内联 `scheduler.script` 在同一 scheduler 中二选一：

```yaml
agents:
  reviewer:
    scheduler:
      enabled: true
      triggers:
        - name: hourly-review
          cron: "0 * * * *"
          prompt: "Review the current project state and summarize changes."
```

完整字段说明见[命令行使用手册](command-line-manual.md)。

## CLI 概览

| 命令 | 用途 |
| --- | --- |
| `agent-compose daemon` | 启动 HTTP/Connect daemon。 |
| `agent-compose up` | 读取 `agent-compose.yml` 并应用 project。 |
| `agent-compose run <agent> --prompt/--command` | 以 agent 身份执行 prompt 或 shell 命令。 |
| `agent-compose exec <sandbox>` | 在运行中的 sandbox 内执行命令或 prompt。 |
| `agent-compose ps` / `stats` | 列出 project sandbox / 查看 sandbox 资源统计。 |
| `agent-compose logs [ref]` | 查看 project、agent、run 或 sandbox 日志。 |
| `agent-compose scheduler ls\|trigger\|inspect` | 查看、执行或检查 scheduler trigger。 |
| `agent-compose sandbox ls\|stop\|resume\|rm\|prune` | 管理 project sandbox。 |
| `agent-compose images\|pull\|build\|rmi\|inspect` | 管理 daemon 镜像并构建 agent 镜像。 |
| `agent-compose volume ls\|create\|inspect\|rm\|prune` | 管理 daemon volume。 |
| `agent-compose cache ls\|inspect\|prune\|rm` | 查看并清理 daemon runtime cache。 |
| `agent-compose down` | 禁用受管 scheduler 并停止 sandbox。 |
| `agent-compose status` | 查看 daemon 状态。 |

常用全局参数：`--file, -f`（指定 compose 文件）、`--project-name`、`--json`
（脚本用的稳定 JSON 输出）、`--host` / `AGENT_COMPOSE_HOST`（连接 TCP daemon）、
`AGENT_COMPOSE_SOCKET`（Unix socket 路径）。完整参考见[命令行使用手册](command-line-manual.md)。

`scheduler.script` 支持内联 JavaScript，或使用显式的 `{ url: ... }` 来源
（本地路径、`file://`、`http://`、`https://`）。`config` 和 `up` 在 CLI 本机
获取来源并向 daemon 发送内联快照；同一 scheduler 中 `scheduler.script` 和
`scheduler.triggers` 二选一。

## Runtime Driver

- **`docker`**（默认）：使用 Docker 容器运行 guest，需要可用的 Docker daemon。
- **`boxlite`**：使用 BoxLite runtime artifact 以 microVM 运行 guest。
- **`microsandbox`**：使用 Microsandbox VM runtime 运行 guest。

镜像处理由 `IMAGE_STORE_MODE` 选择（`auto` / `docker` / `oci`，其中 `oci` 使用无 daemon 的镜像缓存）。新 sandbox 使用 `DEFAULT_IMAGE` 指定的镜像；自带的 `.env.example` 和安装脚本将其设为 `ghcr.io/chaitin/agent-compose-guest:latest`，该镜像内置 agent runtime 和各 provider CLI。

## Agent Provider

每个 agent 设置一个 `provider`，决定 sandbox 内运行的 CLI：

| Provider | 运行 |
| --- | --- |
| `codex` | Codex CLI |
| `claude` | Claude Code CLI |
| `gemini` | Gemini CLI |
| `opencode` | OpenCode CLI |

LLM 凭据只在 daemon（`.env`）配置一次，而不是每个 guest 各配。对 Codex、Claude 和 OpenCode，daemon 的 **Runtime LLM Facade** 给每个 sandbox 一个受限的 scoped token，而不是你的真实 API key，因此 provider key 不会进入 guest。

按你的 agent 使用的后端家族设置变量。**OpenAI 家族**（Codex，以及 daemon 自身的 `LLMService` 和 scheduler LLM 调用）：

```env
LLM_API_ENDPOINT=https://api.openai.com
LLM_API_PROTOCOL=responses    # DeepSeek / vLLM / Ollama 用 chat_completions
LLM_API_KEY=sk-...
LLM_MODEL=gpt-...
```

**Anthropic 家族**（Claude）：

```env
ANTHROPIC_BASE_URL=https://api.anthropic.com
ANTHROPIC_API_KEY=sk-ant-...
ANTHROPIC_MODEL=claude-...
```

设置 `LLM_API_PROTOCOL=chat_completions` 可对接任意 OpenAI 兼容 endpoint（DeepSeek、vLLM、Ollama）。

**各 provider 说明。** OpenCode 从 agent 的 `model`（`provider/model`，如 `anthropic/…` 或 `openai/…`）选择上游家族并获得对应的 facade token；只有 OpenCode 自带的原生 provider 才走 OpenCode 自身登录。**Gemini 是例外** —— 它不会拿到任何 LLM key（`GEMINI_API_KEY` / `GOOGLE_API_KEY` 会从 guest 中过滤），而是通过 Gemini CLI 自身登录，凭据持久化在 sandbox home（`~/.gemini`）。

完整变量（超时、endpoint 别名、`OPENAI_API_KEY` / `ANTHROPIC_AUTH_TOKEN` 等）见 [../../.env.example](../../.env.example)；facade 的托管机制见 [design/agent-compose-runtime-llm-facade.md](design/agent-compose-runtime-llm-facade.md)。

## 部署与配置

使用已发布镜像部署到服务器：

```bash
cp .env.example .env
openssl rand -base64 24   # 用于 AUTH_PASSWORD
openssl rand -hex 32      # 用于 AUTH_SECRET
docker compose pull && docker compose up -d
docker compose --profile with-ui up -d   # 同时启动 Web UI
```

**[../../.env.example](../../.env.example) 是权威的、带完整注释的配置参考。** 对外部署前至少检查这些：

- `AUTH_PASSWORD`、`AUTH_SECRET` —— UI server 登录 secret（务必替换示例值）。
- `AGENT_COMPOSE_HTTP_PORT` —— 启用 `with-ui` 时 Web UI / 反向代理的宿主机端口。
- `AGENT_COMPOSE_RUNTIME_BASE_URL` —— guest 可达的 daemon URL，用于 LLM facade。
- `RUNTIME_DRIVER` —— 默认 runtime driver。

## Web UI

Web UI 在独立仓库 [agent-compose-ui](https://github.com/chaitin/agent-compose-ui)。它消费已发布的 API client [`@chaitin-ai/agent-compose-client`](https://www.npmjs.com/package/@chaitin-ai/agent-compose-client)（由本仓库 `proto/` 生成）。daemon 不托管 UI 或浏览器登录流程；UI 镜像用 nginx 前置一个 Go UI server，由后者处理 auth/OAuth 并把 API、Jupyter 路由代理到 daemon。

## 安全提醒

默认配置面向本地开发。对外部署前请加固：

- 浏览器入口通过 agent-compose-ui server 暴露，不要直连 daemon。
- 设置稳定、高熵的 `AUTH_SECRET`；生产环境使用 HTTPS 终止。
- daemon TCP API（`HTTP_LISTEN`）应置于容器网络、反向代理或 VPN 之后。
- 不要直接暴露 guest Jupyter 端口 —— 通过 agent-compose proxy 访问。
- 把 Git 凭据、上传的 workspace、环境变量和 LLM API key 都当作 secret。

更多说明见 [../../SECURITY.md](../../SECURITY.md)。

## 构建与测试

```bash
task lint
task build
task test          # 或：task test:unit / task test:integration / task test:e2e
```

用 `task image:agent-compose-guest` 和 `task image:agent-compose` 构建 guest 和 daemon 镜像。启用 BoxLite 的二进制（`task build:agent-compose:boxlite`）为可选，需要 BoxLite runtime artifact。JavaScript runtime 组件在 `runtime/` 下。

## 文档

- [英文文档索引](../README.md)
- [命令行使用手册](command-line-manual.md)
- [架构说明](design/agent-compose_design.md)

## 贡献

欢迎贡献 —— 见 [../../CONTRIBUTING.md](../../CONTRIBUTING.md)。

## 许可

agent-compose 使用 [GNU Affero General Public License v3.0](../../LICENSE.txt) 授权。

## 社区与支持

欢迎加入技术社区，与更多开发者交流 agent-compose 的使用、部署和开发经验。

<table>
  <tr>
    <td align="center"><img src="https://github.com/user-attachments/assets/fcdbb42b-2e06-409e-b116-60544461fbc1" width="160" /><br/>微信交流群</td>
  </tr>
</table>
