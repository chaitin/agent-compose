<p align="center">
  <img src="images/agent-compose-logo.png" alt="Agent-compose" width="384">
</p>

# agent-compose 中文文档

**agent-compose 是一个 daemon + CLI 形态的控制面，用于在隔离 sandbox 中运行 AI coding agent。** 你在 `agent-compose.yml` 里声明 agent，一个常驻 daemon 负责为每个 agent 构建、运行、调度并代理一个隔离的 runtime。

> 公开预览阶段。API、运行时打包和部署默认值仍可能调整。适合实验、本地开发和预览部署，尚未达到稳定生产平台。

英文首页见 [README.md](README.md)。

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

完整架构见 [docs/design/agent-compose_design.md](docs/design/agent-compose_design.md)。

## 快速开始

### 方式 A —— 部署服务器（推荐）

一行安装脚本会用 Docker Compose 部署并启动 agent-compose daemon，支持 Linux amd64/arm64。Web UI 位于可选的 `with-ui` profile，不会由安装脚本的普通 `docker compose up -d` 自动启动：

```bash
curl -fsSL https://github.com/chaitin/agent-compose/releases/latest/download/install.sh | bash
```

首次运行会生成 `admin` 密码并打印一次，同时打印安装目录和浏览器 URL。使用浏览器前，先在该安装目录显式启动 Web UI：

```bash
cd <安装脚本打印的目录>
docker compose --profile with-ui up -d
```

然后访问安装脚本打印的 URL。基础 `docker-compose.yml` 不启用 `privileged`，也不映射 `/dev/kvm`；installer 在新安装时检测 KVM，并在可用时把 `COMPOSE_FILE=docker-compose.yml:docker-compose.kvm.yml` 持久化到安装目录的 `.env`。没有 KVM 时仍可使用默认 Docker driver。安装选项（`--dir`、`--port`、`--upgrade`、使用镜像/私有 registry 等）见 [deploy/README.md](deploy/README.md)。

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

更多可运行示例（cron、timeout、scheduler 脚本）见 [examples/agent-compose/](examples/agent-compose/)。

## Compose 配置

**顶层字段：** `name`、`env_file`、`variables`、`workspaces`、`agents`、`mcp_servers`、`volumes`、`network`。

**agent 常用字段：** `provider`、`model`、`system_prompt`、`image`、`driver`、
`env`（scalar 或 `{ value, secret }`）、`workspace`、`scheduler`、`mcp_servers`、`skills`、`volumes`。

为 agent 从本地路径（`provider: file`）或 Git 仓库（`provider: git`）配置 workspace：

```yaml
agents:
  reviewer:
    workspace:
      provider: git
      url: https://github.com/example/repo.git
      ref: main
      target: .
```

Scheduler 脚本可以是内联 JavaScript，也可以通过 `provider: file`、
`provider: http` 或 `provider: git` 配置外部来源。`config` 和 `up` 会在本地
读取外部脚本，并把内联内容快照发送给 daemon。例如，通过 HTTP 加载脚本：

```yaml
agents:
  reviewer:
    scheduler:
      enabled: true
      script:
        provider: http
        url: https://example.com/scheduler.js
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

完整字段说明见[命令行使用手册](docs/pages/zh-CN/command-line-manual.md)。

## CLI 概览

| 命令 | 用途 |
| --- | --- |
| `agent-compose daemon` | 启动 HTTP/Connect daemon。 |
| `agent-compose up` | 读取 `agent-compose.yml` 并应用 project。 |
| `agent-compose run <agent> --prompt/--command` | 以 agent 身份执行 prompt 或 shell 命令。 |
| `agent-compose exec <sandbox>` | 在运行中的 sandbox 内执行命令或 prompt。 |
| `agent-compose ps` / `stats` | 列出 project sandbox / 查看 sandbox 资源统计。 |
| `agent-compose logs` | 查看 project run 日志；可直接传入 project、agent、run 或 sandbox ID，无需指定资源类型。 |
| `agent-compose scheduler ls\|runs\|logs\|trigger\|inspect` | 查看 trigger 和 run、读取 scheduler 日志、手动执行 trigger 或检查 scheduler 资源。 |
| `agent-compose sandbox ls\|stop\|resume\|rm\|prune` | 管理 project sandbox。 |
| `agent-compose images\|pull\|build\|rmi\|inspect` | 管理 daemon 镜像并构建 agent 镜像。 |
| `agent-compose volume ls\|create\|inspect\|rm\|prune` | 管理 daemon volume。 |
| `agent-compose cache ls\|inspect\|prune\|rm` | 查看并清理 daemon runtime cache。 |
| `agent-compose auth login\|logout\|ls` | 验证、删除或列出已保存的 daemon Bearer Token。 |
| `agent-compose down` | 禁用受管 scheduler 并停止 sandbox。 |
| `agent-compose status` | 查看 daemon 状态。 |

常用全局参数：`--file, -f`（指定 compose 文件）、`--project-name`、`--json`
（脚本用的稳定 JSON 输出）、`--host` / `AGENT_COMPOSE_HOST`（连接 TCP daemon）、
`AGENT_COMPOSE_SOCKET`（Unix socket 路径）。完整参考见[命令行使用手册](docs/pages/zh-CN/command-line-manual.md)。

`scheduler.script` 支持内联 JavaScript，或使用显式的 `{ url: ... }` 来源
（本地路径、`file://`、`http://`、`https://`）。`config` 和 `up` 在 CLI 本机
获取来源并向 daemon 发送内联快照；同一 scheduler 中 `scheduler.script` 和
`scheduler.triggers` 二选一。

## Daemon 认证

在 daemon 环境中设置 `AGENT_COMPOSE_AUTH_TOKEN` 后，HTTP(S) 控制面请求必须携带共享 Bearer Token；配置为空或未配置时，认证保持关闭。受信任的本地 Unix socket 连接不需要此 Token。

Health RPC、Runtime LLM Facade、Jupyter proxy 和 webhook ingestion 继续使用各自已有的认证或信任边界，不使用 daemon Token。

为一个 daemon 站点验证并保存 Token：

```bash
export AGENT_COMPOSE_AUTH_TOKEN='your-token'
export HTTP_LISTEN='127.0.0.1:7410'
agent-compose daemon

agent-compose --host http://127.0.0.1:7410 auth login --token 'your-token'
agent-compose --host http://127.0.0.1:7410 status
```

登录命令会先向 daemon 验证 Token，成功后将凭据保存到当前平台的用户配置目录；标准 Linux 路径为 `~/.config/agent-compose/config.yml`，且仅当前用户可读写。后续通过相同的 `--host` 或 `AGENT_COMPOSE_HOST` 连接时，CLI 会自动携带对应 Token。使用 `agent-compose auth ls` 查看已保存站点，使用 `agent-compose --host <site> auth logout` 删除站点凭据。

Bearer Token 不会加密网络流量。跨机器连接时，请使用 HTTPS、SSH 隧道、VPN 或其他受保护网络；明文 HTTP 中的 Token 可能被监听并重放。UI server 或反向代理若调用受保护的 daemon 控制面 API，也必须注入相同的 `Authorization: Bearer <token>` 请求头。

## Runtime Driver

- **`docker`**（默认）：使用 Docker 容器运行 guest，需要可用的 Docker daemon。
- **`boxlite`**：使用 BoxLite runtime artifact 以 microVM 运行 guest。
- **`microsandbox`**：使用 Microsandbox VM runtime 运行 guest。

产物的平台能力并不相同：macOS 原生二进制只编译 `docker`；Linux 原生二进制和发布的 Linux daemon 镜像编译 `docker`、`boxlite`、`microsandbox`。`agent-compose --json version` 和 `/api/version` 中的 `compiled_drivers` 只表示真实 driver 实现已编入当前产物，不代表 Docker daemon、KVM、native artifact 或 runtime 本身当前可用或健康。BoxLite 和 Microsandbox 的真实运行仍要求 Linux/KVM 及对应 runtime artifact；完整 Linux 镜像可以在 macOS Docker Desktop 中以 Docker driver 运行，但不承诺在该环境运行两种 KVM driver。

镜像处理由 `IMAGE_STORE_MODE` 选择（`auto` / `docker` / `oci`，其中 `oci` 使用无 daemon 的镜像缓存）。新 sandbox 使用 `DEFAULT_IMAGE` 指定的镜像；自带的 `.env.example` 和安装脚本将其设为 `ghcr.io/chaitin/agent-compose-guest:latest`，该镜像内置 agent runtime 和各 provider CLI。

## Agent Provider

每个 agent 设置一个 `provider`，决定 sandbox 内运行的 CLI：

| Provider | 运行 |
| --- | --- |
| `codex` | Codex CLI |
| `claude` | Claude Code CLI |
| `gemini` | Gemini CLI |
| `opencode` | OpenCode CLI |

LLM 凭据只在 daemon（`.env`）配置一次，而不是每个 guest 各配。对 Codex、Claude 和 OpenCode，daemon 的 **Runtime LLM Facade** 给每个 sandbox 一个受限的 scoped token，而不是你的真实 API key，因此 provider key 不会进入 guest。当 token 已固定上游 provider 时，runtime 每次请求中的模型会透传给该 provider，无需预先登记到 agent-compose；provider 不支持模型时返回其上游错误。未绑定 provider 的兼容 token 保持既有的已配置 model/provider 解析行为。

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

完整变量（超时、endpoint 别名、`OPENAI_API_KEY` / `ANTHROPIC_AUTH_TOKEN` 等）见 [`.env.example`](.env.example)；facade 的托管机制见 [docs/design/agent-compose_design.md#daemon-llm-client](docs/design/agent-compose_design.md#daemon-llm-client)。

## 部署与配置

使用已发布镜像部署到服务器：

```bash
cp .env.example .env
openssl rand -base64 24   # 用于 AUTH_PASSWORD
openssl rand -hex 32      # 用于 AUTH_SECRET
docker compose pull && docker compose up -d
docker compose --profile with-ui up -d   # 同时启动 Web UI
```

以上命令默认使用不含 KVM 权限的基础 Compose。需要在 Linux KVM 主机显式启用 BoxLite/Microsandbox 时，把 `COMPOSE_FILE=docker-compose.yml:docker-compose.kvm.yml` 写入部署目录的 `.env`；`docker-compose.kvm.yml` 只增加 `privileged` 和 `/dev/kvm` 能力，不是本地 build override。

**[`.env.example`](.env.example) 是权威的、带完整注释的配置参考。** 对外部署前至少检查这些：

- `AUTH_PASSWORD`、`AUTH_SECRET` —— UI server 登录 secret（务必替换示例值）。
- `AGENT_COMPOSE_AUTH_TOKEN` —— daemon HTTP(S) 控制面可选的共享 Bearer Token。
- `AGENT_COMPOSE_HTTP_PORT` —— 启用 `with-ui` 时 Web UI / 反向代理的宿主机端口。
- `AGENT_COMPOSE_RUNTIME_BASE_URL` —— guest 可达的 daemon URL，用于 LLM facade。
- `RUNTIME_DRIVER` —— 默认 runtime driver。

## Web UI

Web UI 在独立仓库 [agent-compose-ui](https://github.com/chaitin/agent-compose-ui)。它直接跟踪由本仓库 `proto/` 生成的 `agentcompose/v2` 和 `health/v1` TypeScript 客户端，协议与生成文件同步评审。daemon 不托管 UI 或浏览器登录流程；UI 镜像用 nginx 前置一个 Go UI server，由后者处理 auth/OAuth 并把 API、Jupyter 路由代理到 daemon。

## 安全提醒

默认配置面向本地开发。对外部署前请加固：

- 浏览器入口通过 agent-compose-ui server 暴露，不要直连 daemon。
- 设置稳定、高熵的 `AUTH_SECRET`；生产环境使用 HTTPS 终止。
- daemon TCP API（`HTTP_LISTEN`）应置于容器网络、反向代理或 VPN 之后。
- 启用 daemon Token 认证时，跨机器连接使用 HTTPS 或其他受保护隧道；明文 HTTP 无法防止 Token 被截获和重放。
- 不要直接暴露 guest Jupyter 端口 —— 通过 agent-compose proxy 访问。
- 把 Git 凭据、上传的 workspace、环境变量和 LLM API key 都当作 secret。

更多说明见 [SECURITY.md](SECURITY.md)。

## 构建与测试

```bash
task lint
task build
task test          # 或：task test:unit / task test:integration / task test:e2e
```

用 `task image:agent-compose-guest` 和 `task image:agent-compose` 构建 guest 和 daemon 镜像。`task build:agent-compose` 按当前宿主选择原生 profile：Darwin 构建仅支持 Docker 的二进制，Linux 构建同时支持 Docker、BoxLite 和 Microsandbox；Linux full 构建会通过 Docker 准备两种 native runtime artifact。也可通过 `task build:agent-compose:darwin` 或 `task build:agent-compose:linux` 显式选择。旧任务 `build:agent-compose:boxlite` 已废弃，仅作为 Linux full profile 的兼容 alias。JavaScript runtime 组件在 `runtime/` 下。

macOS/Linux 原生二进制只用于本地开发和 CI 验证，不作为 GitHub Release 资产。正式发布载体仍是 GHCR 中 `linux/amd64`、`linux/arm64` 的 multi-arch daemon/guest 镜像；GitHub Release 只包含 standalone installer、installer bundle 和校验和，不包含平台二进制。

## 文档

- [英文文档索引](README.md)
- [命令行使用手册](docs/pages/zh-CN/command-line-manual.md)
- [架构说明](docs/design/agent-compose_design.md)

## 贡献

欢迎贡献 —— 见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可

agent-compose 使用 [GNU Affero General Public License v3.0](LICENSE.txt) 授权。

## 社区与支持

欢迎加入技术社区，与更多开发者交流 agent-compose 的使用、部署和开发经验。

<table>
  <tr>
    <td align="center"><img src="https://github.com/user-attachments/assets/fcdbb42b-2e06-409e-b116-60544461fbc1" width="160" /><br/>微信交流群</td>
  </tr>
</table>
