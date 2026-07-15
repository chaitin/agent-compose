# agent-compose Runtime LLM Facade 设计

本文档描述 agent-compose 的 Runtime LLM Facade 设计：daemon 统一管理 LLM provider、provider key、sandbox-scoped 调用 token、协议转换和 runtime env 注入，runtime 内的 agent 不再接触真实 provider key。

Runtime LLM Facade 是 agent-compose 进程内能力，不是独立外部服务，也不要求依赖 OctoBus。它是一个受控 LLM 调用面：所有成功请求都会进入协议层 decode/encode 管线，协议兼容边界由 agent-compose 和 `github.com/chaitin/ai-api-protocol-bridge` 共同承担。

## 相关代码

- 配置加载：`pkg/config/config.go`
- 全局 env 存储：`pkg/storage/configstore/llm_config.go`
- sandbox 创建和 env 合并：`pkg/agentcompose/adapters/session_rpc_bridge.go`、`pkg/agentcompose/adapters/loader_session_runner.go`、`pkg/runs/preparation.go`
- runtime env 注入：`pkg/driver/docker_runtime.go`、`pkg/driver/microsandbox_runtime.go`、`pkg/driver/boxlite_cgo.go`
- 默认 guest home assets：`assets/`、`pkg/driver/runtime_mount_manifest.go`
- LLM service：`pkg/agentcompose/api/llm.go`、`pkg/agentcompose/adapters/llm_client.go`
- Runtime LLM Facade：`pkg/agentcompose/proxy/runtime_llm.go`、`pkg/llms/runtimefacade/config.go`、`pkg/agentcompose/app/llm_facade.go`
- 协议转换：`github.com/chaitin/ai-api-protocol-bridge`

## 架构目标

```text
Codex / Claude / runtime SDK / OpenAI or Anthropic compatible client
  |
  | sandbox token + facade URL
  v
agent-compose daemon
  |
  | auth, provider resolve, protocol decode/encode, key injection
  v
configured upstream LLM provider
```

核心边界：

- daemon 是 LLM provider、model、provider key 和 provider headers 的状态权威。
- runtime 只拿 sandbox-scoped facade token，不拿真实 `LLM_API_KEY`、`OPENAI_API_KEY`、`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`。
- Runtime LLM Facade 对外提供 OpenAI Responses、OpenAI Chat Completions 和 Anthropic Messages 三类入口。
- 所有成功请求都通过 `ai-api-protocol-bridge` 的 adapter 或 cross-family bridge 做协议 decode/encode。
- agent-compose 负责 HTTP handler、sandbox token 鉴权、provider 选择、真实 key 注入、header 过滤、SSE parser、SSE flush 和错误分类。
- `.codex/config.toml` 不从静态 assets 写死 provider、model 和 upstream base URL，而是按 sandbox 动态生成。

## Provider And Model

当前实现使用 daemon-side provider/model 配置，`system` scope 作为默认 scope。数据库字段 `provider_type` 保留不改名，语义上表示 API family。

建议表结构：

```sql
llm_provider(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  provider_type TEXT NOT NULL DEFAULT 'openai_compatible',
  default_wire_api TEXT NOT NULL DEFAULT 'responses',
  base_url TEXT NOT NULL,
  api_key TEXT NOT NULL DEFAULT '',
  auth_header TEXT NOT NULL DEFAULT 'Authorization',
  auth_scheme TEXT NOT NULL DEFAULT 'Bearer',
  headers_json TEXT NOT NULL DEFAULT '{}',
  weight INTEGER NOT NULL DEFAULT 10,
  enabled INTEGER NOT NULL DEFAULT 1,
  scope TEXT NOT NULL DEFAULT 'system',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

llm_model(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  default_model INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  scope TEXT NOT NULL DEFAULT 'system',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

llm_provider_model(
  provider_id TEXT NOT NULL,
  model_id TEXT NOT NULL,
  wire_api TEXT NOT NULL DEFAULT '',
  weight INTEGER NOT NULL DEFAULT 10,
  PRIMARY KEY(provider_id, model_id)
);
```

`provider_type` 归一化规则：

- `openai_compatible`、`openai-compatible`、`openai` -> `openai`
- `anthropic`、`claude`、`anthropic_messages` -> `anthropic`

不使用 `anthropic_compatible` 这类非官方兼容性命名。已有数据库字段名保持不变，避免破坏现有 SQLite 配置。

`default_wire_api` 表示 provider 的默认上游 API 形态。OpenAI family 支持：

- `responses` -> `<base_url>/responses`
- `chat_completions` -> `<base_url>/chat/completions`

Anthropic family 使用 Messages API：

- `messages` -> `<base_url>/messages`

`base_url` 存 API root，不存单个 operation endpoint。bootstrap 旧配置时，如果 `LLM_API_ENDPOINT` 已经带 `/v1/responses`、`/v1/chat/completions` 或 `/v1/messages`，保存 provider 时剥离 operation suffix。

`api_key` 和 `headers_json` 是 server-side sensitive config。它们可以来自 daemon 环境变量或 `global_env` bootstrap，但不能写入 sandbox metadata、runtime env、guest home config 或 driver runtime 配置。HTTP API 返回 provider 时必须脱敏 key 和敏感 header。

provider key 注入规则：

- facade 先设置协议必需 header。
- `headers_json` 只能补充 provider 自定义 header。
- `headers_json` 不能覆盖 `auth_header`，也不能设置 `Authorization`、`Proxy-Authorization`、`Host`、`Content-Length`、`Cookie`、`Set-Cookie`。
- facade 最后根据 `auth_header`、`auth_scheme`、`api_key` 注入真实 provider auth。

bootstrap 输入：

```text
OpenAI family:
LLM_API_ENDPOINT, LLM_API_KEY, OPENAI_API_KEY, LLM_MODEL, LLM_API_PROTOCOL

Anthropic family:
ANTHROPIC_BASE_URL, ANTHROPIC_API_ENDPOINT, ANTHROPIC_API_KEY,
ANTHROPIC_AUTH_TOKEN, ANTHROPIC_MODEL, CLAUDE_MODEL, LLM_API_ENDPOINT, LLM_API_KEY
```

当数据库已有对应 family 的 enabled provider 时，不再用 bootstrap 覆盖数据库 provider。provider/model 表是默认模型选择、严格 LLM 调用和已登记模型 wire API 覆盖的运行时权威，但不作为 provider-bound Runtime LLM Facade 的请求模型白名单。

## HTTP Facade

daemon 暴露 sandbox-scoped LLM facade：

```text
POST /api/runtime/sandboxes/:sandbox_id/llm/openai/v1/responses
POST /api/runtime/sandboxes/:sandbox_id/llm/openai/v1/chat/completions
POST /api/runtime/sandboxes/:sandbox_id/llm/anthropic/v1/messages
```

facade path 决定入口协议：

- `/llm/openai/v1/responses` -> `ProtocolOpenAIResponses`
- `/llm/openai/v1/chat/completions` -> `ProtocolOpenAIChat`
- `/llm/anthropic/v1/messages` -> `ProtocolAnthropicMessages`

provider `provider_type` 和 `wire_api` 决定上游协议：

- `openai` + `responses` -> `ProtocolOpenAIResponses`
- `openai` + `chat_completions` -> `ProtocolOpenAIChat`
- `anthropic` -> `ProtocolAnthropicMessages`

处理流程：

```text
runtime request
  -> validate sandbox-scoped facade token
  -> validate sandbox is available
  -> decode inbound request with inbound adapter
  -> pin provider from token scope and resolve wire API for request model
  -> encode upstream request with adapter or cross-family bridge
  -> inject real provider auth and provider headers
  -> call upstream
  -> decode upstream response
  -> encode response back to inbound protocol
  -> return to runtime
```

SSE 流式处理：

```text
upstream HTTP body
  -> agent-compose SSE parser
  -> RawStreamEvent
  -> bridge StreamDecoder
  -> StreamPart
  -> bridge StreamEncoder
  -> RawStreamEvent
  -> agent-compose SSE writer + Flush
```

`ai-api-protocol-bridge` 不提供 HTTP handler、auth、provider routing、SSE parser、network I/O、flush 或日志能力；这些由 agent-compose 负责。

上游非 2xx 响应按上游状态和 body 返回，不按成功协议转换，避免把 provider 错误误编码成成功响应。

## Token Scope

LLM facade token 独立于 Jupyter `ProxyState.Token`，使用单独的 `LLMFacadeToken` 记录。原始 token 只进入 runtime env，不持久化明文；数据库只保存 hash 和 fingerprint。

token scope：

```text
sandbox_id
model
provider_id
wire_api
source
run_id
issued_at
expires_at
revoked_at
```

当 token 包含 `provider_id` 时，`model` 记录签发时选择的默认模型，供 runtime 初始配置和审计使用，不限制请求模型。既有 Claude/generic provider 兼容路径可以签发空 `provider_id` token；这类 token 保持原有的严格 model/provider 解析和 model scope 行为。

`expires_at` 为 0 表示 token 随 sandbox 生命周期有效，不使用固定 24h 过期时间；sandbox 停止或需要失效时通过 `revoked_at` 撤销。

校验规则：

- token 必须属于 path 中的 `sandbox_id`。
- token 未撤销且未过期。
- sandbox 存在且未停止。
- 请求 body 必须包含非空 `model`。
- token scope 包含 `provider_id` 时，模型名不需要与 token 默认模型一致，请求模型始终发送给该 provider，不能根据模型切换 provider。
- token scope 不包含 `provider_id` 时，保持既有 model scope 与已配置 model/provider 解析规则。
- token scope 中有 `wire_api` 时，请求路径必须与入口 wire API 一致。

对于包含 `provider_id` 的 token，请求模型命中该 provider 的显式 provider/model 绑定时，使用绑定的上游 `wire_api`；模型未登记、未绑定当前 provider 或只绑定其他 provider 时，使用 token provider 的默认 `wire_api`。上游是否支持该模型及其权限、配额由 provider 决定，非 2xx 状态和错误 body 按现有规则透传。

## Runtime Env

使用 facade 后，runtime 侧环境变量收敛为 sandbox token 和 facade URL。长期 provider key 只属于 daemon。

通用 runtime SDK 需要：

```bash
AGENT_COMPOSE_SANDBOX_TOKEN=<llm_facade_token>
LLM_API_KEY=<llm_facade_token>
LLM_API_ENDPOINT=<guest-reachable-facade-family-base-url>
LLM_API_PROTOCOL=<responses|chat_completions|messages>
```

Codex 兼容映射：

```bash
AGENT_COMPOSE_SANDBOX_TOKEN=<llm_facade_token>
LLM_API_KEY=<llm_facade_token>
LLM_API_ENDPOINT=<guest-reachable-agent-compose-url>/api/runtime/sandboxes/<sandbox_id>/llm/openai/v1
LLM_API_PROTOCOL=<responses|chat_completions>
OPENAI_API_KEY=<llm_facade_token>
OPENAI_BASE_URL=<guest-reachable-agent-compose-url>/api/runtime/sandboxes/<sandbox_id>/llm/openai/v1
```

Claude 兼容映射：

```bash
AGENT_COMPOSE_SANDBOX_TOKEN=<llm_facade_token>
LLM_API_KEY=<llm_facade_token>
LLM_API_ENDPOINT=<guest-reachable-agent-compose-url>/api/runtime/sandboxes/<sandbox_id>/llm/anthropic
LLM_API_PROTOCOL=messages
ANTHROPIC_API_KEY=<llm_facade_token>
ANTHROPIC_BASE_URL=<guest-reachable-agent-compose-url>/api/runtime/sandboxes/<sandbox_id>/llm/anthropic
```

`LLM_API_KEY`、`OPENAI_API_KEY` 和 `ANTHROPIC_API_KEY` 在 runtime 中是 daemon 写入的 facade token，不是 provider key。`LLM_API_ENDPOINT` 在 runtime 中也由 daemon 覆盖为 facade endpoint，避免旧代码把 facade token 发往真实 provider endpoint。daemon 在过滤用户 env 后写入这些受管变量，防止用户配置的长期 key 穿透到 runtime。

禁止进入 runtime 的长期 key：

```text
LLM_API_KEY
OPENAI_API_KEY
ANTHROPIC_API_KEY
ANTHROPIC_AUTH_TOKEN
OPENROUTER_API_KEY
AZURE_OPENAI_API_KEY
GOOGLE_API_KEY
GEMINI_API_KEY
```

过滤发生在两层：

- sandbox 准备层：合并 global/project/agent/request env 后，从 `Sandbox.EnvItems` 移除 LLM provider key。
- runtime env 组装层：Docker、Microsandbox、BoxLite 启动 env 和 `ExecSpec.Env` 再次过滤用户 env 中的 LLM key denylist，然后写入 daemon 生成的 facade token 和 base URL。非 LLM 的 `secret=true` env 保持原有行为，仍可进入 runtime。

## Sandbox Home Config

`assets/.codex/config.toml` 不写死 upstream provider。daemon 按 sandbox effective model 和 guest-reachable facade URL 动态生成：

```toml
model_provider = "agent_compose"
model = "<selected-model>"

[model_providers.agent_compose]
name = "agent-compose"
base_url = "<guest-reachable-agent-compose-url>/api/runtime/sandboxes/<sandbox_id>/llm/openai/v1"
env_key = "AGENT_COMPOSE_SANDBOX_TOKEN"
wire_api = "responses"
request_max_retries = 30
stream_max_retries = 50
stream_idle_timeout_ms = 120000
```

model 解析优先级：

```text
explicit request model
> project run request model
> compose agent model
> agent definition model
> loader / scheduler default model
> llm_model default
> LLM_MODEL env
```

该优先级用于 token 签发、默认 runtime config、scheduler、`LLMService` 和未绑定 provider 的兼容 token 等严格解析路径。Runtime LLM Facade 收到已绑定 provider 的 token 后直接使用请求 body 中的非空 model；token 中记录的默认 model 不覆盖请求值。

provider 解析优先级：

```text
token scope provider_id
> explicit request provider_id
> model 绑定的 enabled provider 按 weight 选择
> 默认 enabled provider 按 weight 选择
```

Runtime LLM Facade 收到已绑定 provider 的 token 后只能使用 token scope provider，不按请求模型重新选择。未绑定 provider 的兼容 token 保持既有 provider 解析优先级。

入口 wire API 和上游 wire API 分开处理：

- token `wire_api` 约束 runtime 请求路径，也就是入口 facade wire API。
- token provider 对请求模型存在显式 provider/model 绑定时，该绑定的 `wire_api` 决定上游 OpenAI family operation；否则使用 provider 默认 `wire_api`。
- 当 Codex 入口使用 OpenAI facade 且上游 provider 是 Anthropic family 时，token `wire_api` 仍是 `responses`，上游协议由 provider family 解析为 Anthropic Messages。

## Header And Log Rules

runtime 请求中的敏感 header 一律丢弃：

```text
Authorization
Proxy-Authorization
Cookie
Set-Cookie
Host
Content-Length
任何包含 token、secret、api-key、apikey、auth 的 header
```

日志允许记录：

```text
sandbox_id, agent_id, loader_id, run_id, request_id,
model, provider_id, provider_scope, stream,
upstream_status, upstream_request_id, token_fingerprint, failure_kind
```

日志不得记录：

```text
provider api key
Authorization
request body
response body
prompt 正文
完整敏感错误正文
```

## Compatibility

现有 daemon 部署变量继续支持：

```bash
LLM_API_ENDPOINT
LLM_API_KEY
OPENAI_API_KEY
LLM_API_PROTOCOL
LLM_MODEL
ANTHROPIC_BASE_URL
ANTHROPIC_API_ENDPOINT
ANTHROPIC_API_KEY
ANTHROPIC_AUTH_TOKEN
ANTHROPIC_MODEL
CLAUDE_MODEL
```

这些变量只作为 daemon bootstrap 或 server-side provider key 来源，不作为 runtime env 注入依据。

`runtime.llm()` 保持现有 SDK API。它调用 Connect `LLMService/Generate` 时也复用 daemon-side provider/model 解析和 server-side key 注入逻辑，不能从 runtime env 读取 provider key。

Gemini CLI 当前 runner 是直接 spawn `gemini`，本仓库代码里没有可验证的 base URL 注入点。当前实现先保证 `GOOGLE_API_KEY` / `GEMINI_API_KEY` 等长期 key 不进入 sandbox metadata 或 runtime env；Gemini facade 入口需要在确认 CLI/SDK endpoint 机制后接入。

## Implementation Scope

- system scope provider/model 表。
- 从 daemon env 和 `global_env` bootstrap OpenAI / Anthropic provider。
- OpenAI Responses、OpenAI Chat Completions、Anthropic Messages facade route。
- sandbox-scoped LLM facade token，包含 daemon BasicAuth skipper 和 fail-closed token auth。
- 统一 SDK 协议管线：请求 decode/encode、响应 decode/encode、SSE event decode/encode。
- OpenAI / Anthropic cross-family conversion。
- guest-reachable agent-compose URL 的 driver 注入。
- 动态生成 `.codex/config.toml`。
- sandbox 准备层过滤 LLM provider key，避免真实 key 进入新的 `Sandbox.EnvItems`。
- Docker、Microsandbox、BoxLite sandbox 启动 env 和 `ExecSpec.Env` 过滤用户 env 中的 LLM key denylist。
- agent/loader run 通过 per-run `ExecSpec.Env` 注入 facade token 和 facade URL。
- 测试覆盖 key 不进入 runtime、config 不写死 upstream、OpenAI/Anthropic facade 鉴权、跨协议非流式转换、SSE 转换与 flush、401/403 分类和 key 脱敏返回。
