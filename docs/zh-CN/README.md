# agent-compose 中文文档

agent-compose 是一个 daemon + CLI 形态的 agent/session 控制面。daemon 负责持久化状态、scheduler、runtime 生命周期、Connect API 和 Jupyter 代理；CLI 负责读取本地 `agent-compose.yml`，连接 daemon，并执行 `up`、`run`、`logs`、`ps`、`down`、`image` 等操作。

当前项目正在准备首次公开发布，建议按 preview/experimental 项目使用。API、运行时打包、部署默认值和生产环境建议后续仍可能调整。

英文首页见 [README.md](../../README.md)。

## 快速开始

完整 CLI 说明见 [agent-compose 命令行使用手册](command-line-manual.md)。

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

Web UI 在独立仓库 [agent-compose-ui](https://github.com/chaitin/agent-compose-ui)。它通过已发布的 [`@chaitin-ai/agent-compose-client`](https://www.npmjs.com/package/@chaitin-ai/agent-compose-client) 包消费 API 客户端——该包由本仓库的 `proto/` 经 `proto-client/` 生成发布。

daemon 不托管 Web UI。前端仓库构建一个 nginx 镜像（`ghcr.io/chaitin/agent-compose-ui`），负责托管构建后的前端并把 API、Jupyter proxy 路由反向代理到 daemon。仓库根目录的 `docker-compose.yml` 直接引用该已发布镜像，并作为默认部署入口。

使用已发布容器镜像部署到服务器：

```bash
cp .env.example .env
openssl rand -base64 24 # 将输出写入 AUTH_PASSWORD
openssl rand -hex 32    # 将输出写入 AUTH_SECRET
docker compose pull
docker compose up -d
# 如需同时拉取并启动 Web UI：
docker compose --profile with-ui pull
docker compose --profile with-ui up -d
```

首次启动前编辑 `.env`。至少替换 `AUTH_PASSWORD` 和 `AUTH_SECRET`；需要 Web UI 时启用 `with-ui` profile，如果不能使用宿主机 `80` 端口，修改 `AGENT_COMPOSE_HTTP_PORT`。本地开发时，Docker Compose 会自动加载 `docker-compose.override.yml`，使用本地 Dockerfile 构建后端镜像；需要重建时执行 `docker compose up -d --build`，需要同时启动 Web UI 时执行 `docker compose --profile with-ui up -d --build`。

## 配置

复制 `.env.example` 为 `.env`，按部署环境修改后执行 `docker compose up -d`。如需同时启动 Web UI，添加 `--profile with-ui`。

常用环境变量：

- `AUTH_USERNAME`、`AUTH_PASSWORD`、`AUTH_SECRET`、`AUTH_SESSION_TTL`：密码登录设置。对外部署前应替换示例密码和 secret。
- `AGENT_COMPOSE_HTTP_PORT`：启用 `with-ui` profile 时，Web UI 和反向代理发布到宿主机的端口。
- `AGENT_COMPOSE_IMAGE`、`AGENT_COMPOSE_FRONTEND_IMAGE`：Docker Compose 服务镜像；前端镜像仅在启用 `with-ui` profile 时使用。
- `DEFAULT_IMAGE`、`DOCKER_DEFAULT_IMAGE`、`MICROSANDBOX_DEFAULT_IMAGE`：guest 镜像默认值。
- `RUNTIME_DRIVER`：默认 runtime driver。
- `OAUTH_*`：OAuth 登录设置。
- `LLM_API_ENDPOINT`、`LLM_API_PROTOCOL`、`LLM_API_KEY`、`OPENAI_API_KEY`、`LLM_MODEL`、`LLM_TIMEOUT`：daemon 侧 OpenAI family LLM 配置，供 `LLMService`、`scheduler.llm` 和 runtime agent LLM facade bootstrap 使用。这些值不会作为 provider key 注入 guest agent runtime。对接 OpenAI 兼容 Chat Completions 后端时设置 `LLM_API_PROTOCOL=chat_completions`。
- `ANTHROPIC_BASE_URL`、`ANTHROPIC_API_ENDPOINT`、`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_MODEL`、`CLAUDE_MODEL`：daemon 侧 Anthropic family LLM facade bootstrap 配置。
- `AGENT_COMPOSE_RUNTIME_BASE_URL`：可选的 runtime 内可访问 daemon base URL，用于生成 Runtime LLM Facade 配置。Docker Compose 默认使用 `http://agent-compose:7410`；宿主机 Docker 场景应配置具体的宿主机 IP/名称和端口。
- `DOCKER_HOST_SESSION_ROOT`：guest 容器 bind mount 使用的宿主机 session 数据路径。Docker Compose 默认使用 `./data/agent-compose/sessions`。
- `CAP_GRPC_LISTEN`、`CAP_GRPC_TARGET`：仅在 Agent 需要调用 OctoBus gRPC capability 时必须配置。`CAP_GRPC_LISTEN` 启动 agent-compose capability proxy；`CAP_GRPC_TARGET` 是注入新 session 的 guest 可达地址。修改后需要重启 daemon 并新建 session。

### Agent Provider

Guest agent session 在 guest 容器内通过 `agent-compose-runtime` CLI 运行 provider CLI；该 CLI 由 `@chaitin-ai/agent-compose-runtime` npm 包提供。Codex 和 Claude 通过 Runtime LLM Facade 调用：真实 provider key 保存在 daemon 侧 LLM provider 配置中，runtime 只拿 session-scoped facade token 和 facade base URL。`LLM_API_KEY`、`OPENAI_API_KEY`、`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`GOOGLE_API_KEY`、`GEMINI_API_KEY` 等 provider key 名称会从用户提供的 runtime env 中过滤。兼容别名 `LLM_API_KEY`、`LLM_API_ENDPOINT` 仍可能出现在 runtime 中，但它们是 daemon 写入的 facade 值，不是上游 provider 凭据。Gemini 和 OpenCode 仍直接使用各自 provider CLI；OpenCode 凭据取决于所选 OpenCode model provider。

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

> **升级注意（部分 Docker 部署存在破坏性变更）：** provider key 不再透传进 guest runtime，Codex/Claude 改为通过 daemon facade 访问上游 LLM，需要一个 runtime 内可达的 daemon URL。自带的 `docker-compose.yml` 已默认设置 `AGENT_COMPOSE_RUNTIME_BASE_URL=http://agent-compose:7410`。如果你在宿主机上直接运行 daemon（Docker driver）且 `HTTP_LISTEN=127.0.0.1:...`，容器无法访问该 loopback 地址，facade 配置会被跳过，agent run 将没有可用的 LLM 凭据。此时需要把 `AGENT_COMPOSE_RUNTIME_BASE_URL` 设为宿主机可达的具体 IP/名称和端口（例如 `http://host.docker.internal:7410`）。

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
- [命令行使用手册](command-line-manual.md)
- [架构说明](design/agent-compose_design.md)
- [CLI 改进计划](design/agent-compose-cli-improvement-plan.md)
- [Agent system prompt（Phase 1）](design/agent_system_prompt_design.md)
- [Runtime contract](design/agent-compose-runtime_contract.md)
- [OpenCode CLI Provider 支持](design/opencode_cli_support.md)
- [Webhook design](design/webhook_design.md)
- [Webhook queue design](design/webhook_queue_design.md)
- [Loader script API](../../loader-script/README.md)
