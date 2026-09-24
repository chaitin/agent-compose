# Daemon 多 Provider 模型目录设计

## 状态与范围

本文定义 daemon 全局 `models.json` 的产品行为：支持多个上游 Provider、literal 模型路由、Scheduler 集成，并兼容已有的 `LLM_*` 环境变量配置。

这里的 catalog 只负责 agent-compose 的 Provider 路由、模型选择和 daemon 侧请求行为，不替代 Codex、Claude Code、OpenCode 或 Pi 自身维护的模型能力目录。

本设计只包含 API Key 认证。账号登录、OAuth、订阅账号凭据、Token 刷新、登出和账号选择 API 不在本文范围内。

## 目标

- 一个 daemon 同时配置多个上游 LLM Provider。
- 使用 `provider/model` 选择 Provider 和上游 literal 模型名。
- 保持依赖 daemon 或项目 `LLM_*` 环境变量的旧项目继续工作。
- 允许模型条目定制行为，但不把模型目录变成白名单。
- 上游凭据只保留在 daemon，sandbox 只能拿到受限的 facade token。
- Coding Agent 与 `scheduler.llm` 使用一致的路由语义。

## 全局配置文件

daemon 在启动后台组件前加载：

```text
<DATA_ROOT>/models.json
```

文件不存在是合法状态，此时 catalog 为空。daemon 不安装 OpenAI、Anthropic 或其他 Provider 模板；已有的 `LLM_*` 环境变量兼容路径仍可独立工作。

以下情况会导致 daemon 启动失败，不允许部分应用配置：

- JSON 格式错误或存在未知字段；
- Provider ID 非法；
- Protocol 不受支持；
- 同一个 Provider 重复声明模型 ID；
- Provider ID 与已有非 catalog Provider 冲突；
- Token 上限非法；
- 环境变量引用无法解析。

示例：

```json
{
  "default": "baizhi/deepseek-v4-flash",
  "providers": {
    "baizhi": {
      "baseUrl": "https://gateway.example.com/api/openai",
      "protocol": "chat_completions",
      "apiKey": "${BAIZHI_API_KEY}",
      "models": [
        {
          "id": "deepseek-v4-flash",
          "maxOutputTokens": 99999
        }
      ]
    },
    "anthropic": {
      "baseUrl": "https://my-proxy.example.com/v1",
      "protocol": "anthropic_messages"
    },
    "openai": {
      "baseUrl": "https://api.openai.com/v1",
      "protocol": "responses",
      "apiKey": "$OPENAI_API_KEY"
    }
  }
}
```

## 配置结构

顶层对象包含：

- `default`：可选的 `provider/model` 引用。
- `providers`：以稳定 Provider ID 为 key 的 Provider 定义。

Provider 支持以下字段：

- `name`：可选显示名称。
- `baseUrl`：上游 API Base URL。
- `protocol`：`responses`、`chat_completions` 或 `anthropic_messages`。
- `apiKey`：明文 Secret，或完整的 `$ENV`／`${ENV}` 引用。
- `headers`：可选的 daemon 侧上游 Header；值同样支持完整环境变量引用。
- `models`：可选的模型行为定义。

每个 Provider 都必须显式提供非空的 `baseUrl` 和受支持的 `protocol`。`apiKey` 可以省略，使 Provider 保持已定义但不可用，后续请求不会自动从其他 Provider 或 daemon 环境借用凭据。

模型条目支持：

- `id`：上游 literal 模型 ID。
- `name`：可选显示名称。
- `baseUrl`：可选的模型级 endpoint 覆盖。
- `protocol`：可选的模型级 Protocol 覆盖。
- `headers`：可选的模型级上游 Header。
- `maxOutputTokens`：可选的正整数输出 Token 上限。

模型级 `protocol` 必须与 Provider 的协议族兼容：OpenAI Provider（`responses` 或 `chat_completions`）只允许模型覆盖为 `responses` 或 `chat_completions`，Anthropic Provider（`anthropic_messages`）只允许 `anthropic_messages`。

模型 `id` 是全局身份；`name`、模型级 Base URL、Protocol、Header 和输出限制属于具体的 `(provider, model)` 部署。两个 Provider 可以声明相同模型 ID，并分别保存这些部署属性，不能相互覆盖，也不能覆盖同名 system/env 模型的全局默认状态。

`maxOutputTokens` 是唯一支持的配置字段。上游协议使用的
`max_output_tokens`、`max_completion_tokens` 或 `max_tokens` 由 daemon 在转发时转换，不属于 `models.json` schema。

Provider ID 不触发 catalog 模板继承。即使 ID 是 `openai` 或 `anthropic`，也必须在文件中完整声明 Base URL 和 Protocol。订阅账号 Provider 不在本设计范围内。

## `models` 是元数据，不是白名单

`models` 数组可以省略。它只用于给已知模型 ID 附加行为。

假设 `baizhi` 已配置且可用，以下两个选择都合法：

```text
baizhi/deepseek-v4-flash
baizhi/a-new-model-not-listed-in-models-json
```

第一个模型会应用匹配的模型级覆盖；第二个模型使用 Provider 默认配置，并把 `a-new-model-not-listed-in-models-json` 原样发送给上游。模型是否合法由上游 Provider 判断。

显式指定未知或不可用 Provider 是另一种情况：daemon 必须在本地失败，不能把请求悄悄转发给默认 Provider。

## Provider 可用性

catalog Provider 只有在最终定义包含非空 API Key 时才可用。

没有 API Key 的 Provider 仍是“已定义”状态，因此可以校验其 default 和模型元数据，但不能接收请求。

环境变量引用只在 daemon 启动时解析一次。解析后的 Secret 投影到 daemon 配置数据库中，不会出现在公开模型元数据中，也不会复制进 sandbox 环境。

## 选择优先级与兼容规则

连接 Provider 的优先级为：

```text
完整的 run/session LLM 环境 Provider
> 显式的非 legacy provider/model
> 完整的 daemon LLM 环境 Provider
> models.json default
```

具体规则：

1. run/session 环境里声明的凭据，如果 daemon 能识别，会被吸收成一条 scope 为 `declared` 的连接（ID 为 `session-env:<sandbox-id>:<family>`），再用 Catalog 正常解析它的 endpoint、protocol、key 和选定模型；缺失值不能从 catalog 或 daemon 环境借用。识别不了的 `*_API_KEY` 不参与连接解析，原样下发到 sandbox 环境。
2. `baizhi/model` 这样的显式自定义引用固定选择 `baizhi`，即使 daemon 已存在完整的默认 `.env` Provider。
3. Legacy 引用 `openai/model` 和 `anthropic/model` 可以继续使用兼容且完整的 run/session 或 daemon 环境 Provider，但发给上游的模型名只取右侧的 `model`。
4. 没有显式模型时，完整的 daemon 环境 Provider 仍是全局默认值。
5. daemon 环境不完整时，使用 `models.json.default`。
6. 所有来源都无法得到可用 Provider 和模型时，以配置前置条件错误失败。

继续兼容的环境变量包括：

```text
LLM_API_ENDPOINT
LLM_API_PROTOCOL
LLM_API_KEY
LLM_MODEL
OPENAI_API_KEY
ANTHROPIC_BASE_URL
ANTHROPIC_API_KEY
ANTHROPIC_AUTH_TOKEN
ANTHROPIC_MODEL
CLAUDE_MODEL
```

项目的 `env_file` 用于插值项目 YAML，不会替代 daemon 全局环境来源。Agent 的 `env` 作为 run/session 兼容层参与解析。

### 声明的第一方凭据

project 的 `variables` 和 agent 的 `env` 里如果出现 daemon 认识的第一方 LLM key，
daemon 会把它写进自己的连接配置，而不是把它交给 agent runtime：

```text
可吸收（写进连接并代理）: LLM_API_KEY（配 LLM_API_PROTOCOL / LLM_API_ENDPOINT）
  ANTHROPIC_API_KEY  ANTHROPIC_AUTH_TOKEN  OPENAI_API_KEY
  CODEX_API_KEY  DEEPSEEK_API_KEY  OPENROUTER_API_KEY
识别但不吸收（原样下发）: AZURE_OPENAI_API_KEY  GOOGLE_API_KEY  GEMINI_API_KEY
不识别（原样下发）: 其它 *_API_KEY / *_AUTH_TOKEN
```

吸收后的连接只按显式 ID 寻址，不进入 `Catalog.serving`、唯一连接兜底与
`Connections()`，因此一个 sandbox 的声明凭据不会服务另一个 sandbox。sandbox 停止或
移除时，`RevokeLLMFacadeTokensForSandbox` 会同时删除该 sandbox 的声明连接。

项目检查（`ValidateProject` / `ApplyProject`）对以上三类分别给出 warning：可吸收的
说明 key 会留在 daemon 并且不会进入 sandbox；识别但不吸收与不识别的说明值会进入
agent runtime，需要改用 daemon 侧连接才能保护。

daemon 不做流量劫持，因此无法识别或无法代理的凭据只能告警，不能被保护。长期凭据
仍然建议配置在 daemon 侧（daemon env / models.json / RPC），那是它可以被显式管理、
轮换和共享的地方；project 里写下的官方 key 只是被识别后自动保护。

## Coding Agent 写法

Agent 继续使用已有的 `model` 字段：

```yaml
agents:
  baizhi-coder:
    provider: opencode
    model: baizhi/deepseek-v4-flash
    image: chaitin/agent-compose-guest:latest

  review:
    provider: codex
    model: openai/gpt-5.6-sol
    image: chaitin/agent-compose-guest:latest
```

Pi 的旧配置继续有效：

```yaml
agents:
  reviewer:
    provider: pi
    model: openai/gpt-5.4
    env:
      LLM_API_ENDPOINT: ${PI_API_ENDPOINT}
      LLM_API_PROTOCOL: responses
      LLM_API_KEY:
        value: ${PI_API_KEY}
        secret: true
```

如果接口只支持 Chat Completions，则设置：

```yaml
LLM_API_PROTOCOL: chat_completions
```

## Scheduler 写法与优先级

Scheduler 可以定义自己的默认模型：

```yaml
agents:
  reviewer:
    provider: pi
    model: openai/gpt-5.4
    scheduler:
      model: baizhi/deepseek-v4-flash
      script: |
        // scheduler.llm(...) 可以在单次调用中覆盖该模型。
```

Scheduler 模型优先级为：

```text
scheduler.llm options.model
> scheduler/agent LLM_MODEL
> scheduler.model
> agent.model
> daemon LLM_MODEL
> models.json default
```

Scheduler 会把环境变量条目和稳定的 Scheduler scope 传给与 runtime facade 相同的 daemon resolver。`scheduler.llm` 支持 Responses、Chat Completions 和 Anthropic Messages。

## Runtime 与安全边界

上游 API Key 始终留在 daemon：

```text
models.json / daemon environment
        -> daemon provider store
        -> runtime LLM facade
        -> upstream provider
```

上游凭据始终只保留在 daemon：无论是 models.json 里的 `apiKey`、daemon 环境的 Secret，还是 project/agent 环境声明并被吸收成 `declared` 连接的第一方 key，sandbox 都只能收到 facade URL 和受限 facade token，不会收到上游 `apiKey` 或 Secret Header。

facade token 与 sandbox 和 Provider 绑定。daemon 校验 token 后，才在向上游发起请求时重新构造认证 Header。

模型级输出 Token 上限在 daemon 边界应用：

- Responses：`max_output_tokens`
- Chat Completions：`max_completion_tokens`
- Anthropic Messages：`max_tokens`

模型级限制优先于 daemon 全局的 `LLM_MAX_OUTPUT_TOKENS` fallback。

## 持久化与迁移

SQLite migration v13 为 Provider/Model 绑定增加：

```text
llm_provider_model.base_url
llm_provider_model.headers_json
llm_provider_model.max_output_tokens
llm_provider_model.display_name
```

同时增加单行表 `llm_catalog_default`，保存精确的默认 Provider 和模型。Catalog default 不写入或清除全局 `llm_model.default_model`；该字段继续归已有 system/env 配置所有。

每次 daemon 启动时，文件中的有效 catalog 会被事务化投影：

1. 禁用已失效的 catalog-owned Provider 和模型；
2. 替换当前 Provider 定义与模型绑定；
3. 原子更新 catalog default；
4. 完整的环境 Provider 在 resolver 使用时按兼容规则物化。

所有更新只允许修改 catalog-owned 行。与非 catalog Provider 同 ID 时整个事务失败并回滚。文件不存在或显式为空都会清除 catalog-owned 的有效状态，但不能修改 system/env Provider、模型或默认选择。

## 失败行为

- `models.json` 不存在：继续启动，catalog-owned 状态为空，system/env 配置和默认模型保持不变。
- 文件非法或 Secret 环境变量无法解析：启动失败。
- Provider ID 与已有非 catalog Provider 冲突：启动失败，原配置保持不变。
- 显式 Provider 未知：失败，不使用默认 Provider。
- 显式 Provider 没有 API Key：以不可用失败。
- 可用的显式 Provider 下模型未知：原样转发 literal 模型 ID。
- run/session 环境 Provider 不完整：不能与低优先级来源拼接。
- 没有可用默认值：以配置前置条件错误失败。

## 非目标与后续扩展

后续可以增加账号登录、OAuth Connector、订阅账号凭据、Token 刷新、登出、账号选择和 Provider 状态 API。

这些能力应当把凭据绑定到既有逻辑 Provider 上，同时保持以下契约不变：

- `provider/model` 选择语义；
- 模型元数据不是白名单；
- 配置优先级；
- sandbox 不接触上游 Secret 的安全边界。
