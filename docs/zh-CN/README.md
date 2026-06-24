# agent-compose 中文文档

agent-compose 是一个 daemon + CLI 形态的 agent/session 控制面。daemon 负责持久化状态、scheduler、runtime 生命周期、Connect API 和 Jupyter 代理；CLI 负责读取本地 `agent-compose.yml`，连接 daemon，并执行 `up`、`run`、`logs`、`ps`、`down`、`image` 等操作。

当前项目正在准备首次公开发布，建议按 preview/experimental 项目使用。API、运行时打包、部署默认值和生产环境建议后续仍可能调整。

英文首页见 [README.md](../../README.md)。

## 快速开始

构建：

```bash
task build
```

启动 daemon：

```bash
agent-compose daemon
```

daemon 默认使用本地 Unix socket。需要本地 TCP 访问时可以设置：

```bash
HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon
```

检查状态：

```bash
agent-compose status
agent-compose --host http://127.0.0.1:7410 status
```

准备 `agent-compose.yml`：

```yaml
name: demo
agents:
  reviewer:
    provider: codex
    model: gpt-test
    image: debian:bookworm-slim
    scheduler:
      triggers:
        - name: hourly
          cron: "0 * * * *"
          prompt: "Review the current workspace state."
```

应用并运行：

```bash
agent-compose up
agent-compose ps
agent-compose run reviewer --prompt "Review this change"
agent-compose logs --agent reviewer
agent-compose down
```

## 当前入口

- `agent-compose daemon`：启动长期运行的 HTTP/Connect daemon。
- `agent-compose up`：读取本地 `agent-compose.yml`，把 project 定义和 scheduler 应用到 daemon。
- `agent-compose run <agent>`：手动运行一次 project agent。
- `agent-compose logs`：查看 project run 日志。
- `agent-compose ps`：查看 project agent、latest run 和 running session 状态。
- `agent-compose down`：禁用 daemon 管理的 scheduler，并停止该 project 的 running sessions。
- `agent-compose images|pull|rmi|image inspect`：管理 daemon 侧 image store。

## Compose 配置

顶层字段：

- `name`：project 名称；未设置时使用 compose 文件所在目录名。
- `variables`：project 级变量，支持 `${ENV_NAME}` 环境变量插值。
- `workspace`：project 默认 workspace，当前支持 `local` 和 `git` provider。
- `agents`：agent 定义 map，key 是 agent 名称。
- `network.mode`：当前只支持 `default`。

agent 常用字段：

- `provider` / `model` / `system_prompt`：guest agent 配置（`provider` 选择 guest CLI runner；`model` 会传给支持显式模型选择的 provider runtime）。非空 `system_prompt` 在运行时生效，并作为 Agent Identity 层注入 provider system/developer 指令。目前支持 `codex`、`claude`、`gemini`、`opencode`。daemon 侧 LLM 调用（`LLMService`、`scheduler.llm`）使用 `LLM_MODEL`，不是 compose 里的 agent `model`。
- `image`：guest 镜像引用；为空时使用 driver 对应默认镜像。
- `driver`：每个 agent 可选择一个 runtime，支持 `boxlite`、`docker`、`microsandbox`。
- `env`：agent 级环境变量，支持 scalar 或 `{ value, secret }` 形状。
- `workspace`：覆盖 project 默认 workspace。
- `scheduler.enabled`：默认 `true`。
- `scheduler.triggers`：支持 `cron`、`interval`、`timeout`、`event` 四种 trigger。
- `scheduler.script`：内联 JavaScript scheduler 脚本。`scheduler.script` 和 `scheduler.triggers` 二选一。

## Runtime Driver

支持的 runtime driver：

- `docker`：默认 driver，使用 Docker daemon。
- `boxlite`：使用本仓库准备的 BoxLite runtime artifact 和 guest image。
- `microsandbox`：使用 Microsandbox runtime。

默认镜像为 `debian:bookworm-slim`，可通过 `DEFAULT_IMAGE`、`DOCKER_DEFAULT_IMAGE`、`MICROSANDBOX_DEFAULT_IMAGE` 覆盖。

## 前端

前端源码在 `frontend/`：

```bash
npm ci
npm run build:ui
npm run dev:ui
```

前端可以由 daemon 静态资源能力提供，也可以作为独立静态服务部署，并反向代理到 daemon 的 API 和 Jupyter proxy 路由。

## 配置

本地实验可复制 `.env.example` 为 `.env`。

常用环境变量：

- `DATA_ROOT`：daemon 数据根目录；session 数据位于 `<DATA_ROOT>/sessions`。
- `HTTP_LISTEN`：可选 TCP 监听地址；本地无认证开发建议保持 loopback。
- `AGENT_COMPOSE_SOCKET`、`AGENT_COMPOSE_HOST`：daemon 连接设置。
- `AUTH_USERNAME`、`AUTH_PASSWORD`、`AUTH_SECRET`、`AUTH_SESSION_TTL`：密码登录设置。
- `OAUTH_*`：OAuth 登录设置。
- `HTTP_BASIC_AUTH`：额外 HTTP Basic 认证（base64 编码的 `username:password`）。
- `LLM_API_ENDPOINT`、`LLM_API_PROTOCOL`、`LLM_API_KEY`、`OPENAI_API_KEY`、`LLM_MODEL`、`LLM_TIMEOUT`：daemon 侧 OpenAI family LLM 配置，供 `LLMService`、`scheduler.llm` 和 runtime agent LLM facade bootstrap 使用。这些值不会作为 provider key 注入 guest agent runtime。对接 OpenAI 兼容 Chat Completions 后端时设置 `LLM_API_PROTOCOL=chat_completions`。
- `ANTHROPIC_BASE_URL`、`ANTHROPIC_API_ENDPOINT`、`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_MODEL`、`CLAUDE_MODEL`：daemon 侧 Anthropic family LLM facade bootstrap 配置。
- `AGENT_COMPOSE_RUNTIME_BASE_URL`：可选的 runtime 内可访问 daemon base URL，用于生成 Runtime LLM Facade 配置。Docker Compose 默认使用 `http://agent-compose:7410`；宿主机 Docker 场景应配置具体的宿主机 IP/名称和端口。
- `CAP_GRPC_LISTEN`、`CAP_GRPC_TARGET`：仅在 Agent 需要调用 OctoBus gRPC capability 时必须配置。`CAP_GRPC_LISTEN` 启动 agent-compose capability proxy；`CAP_GRPC_TARGET` 是注入新 session 的 guest 可达地址。修改后需要重启 daemon 并新建 session。
- `RUNTIME_DRIVER`：默认 runtime driver。
- `DEFAULT_IMAGE`、`DOCKER_DEFAULT_IMAGE`、`MICROSANDBOX_DEFAULT_IMAGE`：guest 镜像默认值。

### Agent Provider

Guest agent session 在 guest 容器内运行 provider CLI（`agent-compose-runtime-js`）。Codex 和 Claude 通过 Runtime LLM Facade 调用：真实 provider key 保存在 daemon 侧 LLM provider 配置中，runtime 只拿 session-scoped facade token 和 facade base URL。`LLM_API_KEY`、`OPENAI_API_KEY`、`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`GOOGLE_API_KEY`、`GEMINI_API_KEY` 等 provider key 名称会从用户提供的 runtime env 中过滤。兼容别名 `LLM_API_KEY`、`LLM_API_ENDPOINT` 仍可能出现在 runtime 中，但它们是 daemon 写入的 facade 值，不是上游 provider 凭据。Gemini 和 OpenCode 仍直接使用各自 provider CLI；OpenCode 凭据取决于所选 OpenCode model provider。

| Provider | 典型环境变量 | 说明 |
| --- | --- | --- |
| `codex` | daemon LLM provider 配置；runtime 获取 `AGENT_COMPOSE_SESSION_TOKEN`、`LLM_API_KEY`、`LLM_API_ENDPOINT`、`OPENAI_BASE_URL` 和 facade-token API key aliases | 使用 guest 镜像中的 Codex CLI/SDK |
| `claude` | daemon Anthropic family provider 配置；runtime 获取 `AGENT_COMPOSE_SESSION_TOKEN`、`LLM_API_KEY`、`LLM_API_ENDPOINT`、`ANTHROPIC_BASE_URL` 和 facade-token API key aliases | 使用 guest 镜像中的 Claude Code CLI |
| `gemini` | 暂未接入 LLM facade | 使用 guest 镜像中的 Gemini CLI |
| `opencode` | 取决于所选 OpenCode model provider，例如 `ANTHROPIC_API_KEY` 或 `OPENAI_API_KEY` | 使用 guest 镜像中的 OpenCode CLI |

修改 guest runtime 代码或 provider 支持后，需重建 guest 镜像：

```bash
task image:agent-compose-guest
```

创建新 session（或 resume 已有 session）以加载更新后的镜像和环境变量。

> **升级注意（部分 Docker 部署存在破坏性变更）：** provider key 不再透传进 guest runtime，Codex/Claude 改为通过 daemon facade 访问上游 LLM，需要一个 runtime 内可达的 daemon URL。自带的 `docker-compose.yml` / `docker-compose.deploy.yml` 已默认设置 `AGENT_COMPOSE_RUNTIME_BASE_URL=http://agent-compose:7410`。如果你在宿主机上直接运行 daemon（Docker driver）且 `HTTP_LISTEN=127.0.0.1:...`，容器无法访问该 loopback 地址，facade 配置会被跳过，agent run 将没有可用的 LLM 凭据。此时需要把 `AGENT_COMPOSE_RUNTIME_BASE_URL` 设为宿主机可达的具体 IP/名称和端口（例如 `http://host.docker.internal:7410`）。

Runtime LLM Facade 设计见 [design/agent-compose-runtime-llm-facade.md](design/agent-compose-runtime-llm-facade.md)。

### Chat Completions LLM 协议

设置 `LLM_API_PROTOCOL=chat_completions`（别名 `chat`、`chat_completion`）后，daemon 侧单次文本生成（`LLMService.Generate`、`scheduler.llm`）可走 OpenAI 兼容 Chat Completions 后端：

```env
LLM_API_PROTOCOL=chat_completions
LLM_API_ENDPOINT=https://api.example.com
LLM_API_KEY=...
LLM_MODEL=your-model
```

兼容后端包括 DeepSeek、本地 OpenAI 兼容代理（vLLM/Ollama）等 Chat Completions endpoint。

该路径不会创建具备 workspace 能力的 agent session，也不提供文件、命令或 MCP 工具访问。

使用 `outputSchema` 时，`chat_completions` 通过 prompt 引导并设置 `response_format: json_object`，不等价于 Responses API 的 strict JSON Schema。

## 安全提醒

默认配置面向本地开发。公开部署前需要审查并加固：

- 未启用认证时，不要把 daemon 暴露到非 loopback 地址。
- 启用认证时设置稳定、高熵的 `AUTH_SECRET`。
- 生产环境建议使用 HTTPS 终止。
- `HTTP_LISTEN=0.0.0.0:7410` 只应在有认证和网络控制的环境中使用。
- Jupyter 访问应通过 agent-compose proxy，不应直接暴露 guest Jupyter 端口。
- 对不可信 workload，需要额外审查 runtime driver 的隔离和网络访问行为。

更多说明见 [SECURITY.md](../../SECURITY.md)。

## 构建和测试

```bash
task lint
task build
task test
```

相关文档：

- [英文文档索引](../README.md)
- [架构说明](design/agent-compose_design.md)
- [Agent system prompt（Phase 1）](design/agent_system_prompt_design.md)
- [Runtime JS contract](design/agent-compose-runtime-js_contract.md)
- [OpenCode CLI Provider 支持](design/opencode_cli_support.md)
- [Webhook design](design/webhook_design.md)
- [Webhook queue design](design/webhook_queue_design.md)
- [Loader script API](../../loader-script/README.md)
