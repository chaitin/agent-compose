# 第一性原理下的 LLM 模型调用链路设计

本文是 `llm_model_routing_redesign.md` 的替代版本（v2）。v1 保留了现状的
`<connection>/<model>` 语义，只做收敛；v2 按第一性原理重新划分职责，
目标是**没有歧义、没有 fallback、没有多套实现**。

---

## 0. 先回答你的 9 点

### 1. `anthropic_messages → chat_completions` 为什么没做？是做不到吗？

**不是做不到，是库作者的取舍。** 三个事实：

- 统一模型 `LLMRequest`/`LLMResponse` 覆盖文本、图片/文档、reasoning、refusal、
  tool call、tool result、JSON 格式、usage（`types.go:19-100`），信息量足以承载。
- OpenAI Chat adapter 本身支持 `reasoning_content` 与 `reasoning_effort`
  （`openai_chat.go:210/275/356`），所以 thinking 不是无法表达。
- `NewCrossFamilyBridge` 只注册了三向（`bridge_cross_family.go:29-40`），
  Anthropic 入站只映射到 **Responses**，README 也明说了这个选择。

原因是**保真度**而非可行性：Anthropic Messages 的 thinking block 带
`signature`/`redacted`，Responses 有原生 reasoning item + encrypted content 可以一一对应；
Chat 只有字符串 `reasoning_content`，签名无处安放。这也是反向桥要专门处理
`hasUnsignedOpenAIToolContinuation`、`isSignedOrRedactedReasoning` 的原因
（`bridge_openai_to_anthropic.go:99-144`）。

**实现之后 agent 能不能跑？能。** 对"只有 `/v1/chat/completions` 的网关"，
Claude Code 的文本、图片、tool use/tool result、SSE 流都能正常转换；损失的是
thinking 签名（网关不是 Anthropic，本来也不校验）、`cache_control`、以及网关自定义事件。
工作量约 150–250 行 + 测试（`bridge_anthropic_to_chat.go` + usage 转换），
属于 `github.com/chaitin/ai-api-protocol-bridge` 仓库，本仓库只能升版本。

**结论（已确认）**：补上这个桥，矩阵补全，从而删掉"codex 仅 openai /
claude 仅 anthropic"这类家族约束。桥在 `github.com/chaitin/ai-api-protocol-bridge`
仓库实现，本仓库升依赖版本。实现规格见 §5.0。

### 2. model name 是整体，不掺杂语义

采纳。**运行链路里不存在任何 model 字符串解析**：`agent.model`、请求体的 `model`、
guest 收到的 model，都是不透明字符串，含 `/` 也整体透传。
唯一允许出现 `<key>/<model>` 的地方是 **daemon 为 pi/opencode 合成 guest 显示串**的出口，
它是输出，不是输入。

### 3. default 是"配置的模型"，不是选路

采纳并再切一刀：

- `default` 只表示**模型选择**：`agent.model` 为空时用目录里的默认模型值；
- 它**不参与连接选择**。删掉 `defaultConfiguredConnection`、`reservedDefaultConnection`
  （`default_connection.go:38-101`）这类"保留默认连接 / 唯一连接 / 其它家族兜底"。
- 连接选择是确定性的：显式指定 → 模型绑定 → 唯一连接 → 报错。没有兜底链。

### 4. daemon 是弱调用方

采纳。daemon 在运行期只做：

```
route(入站协议) + token.connection + request.model(不透明)
   -> Connection.BaseURL/Key + 按上游协议转发
```

不再在运行期"解析模型 → 选 provider → 选 wire_api"。token 绑定的是**连接**，
不是某一次解析结果。

### 5. pi/opencode 的 `agent-compose/gpt-5.5` 会不会改动太多？

**不会，而且必须用 daemon 自己的 key，反而不能用 `openai`。** 理由：

- pi 的 `models.json` 和 opencode 的 `opencode.json` **本身就是 daemon 写的**
  （`WritePiRuntimeConfig`、`WriteOpenCodeRuntimeConfig`）。既然 provider 条目由
  daemon 声明，用什么 key 对 pi/opencode 是零改动。
- 用 `openai` 会撞两件事：opencode 内置 provider 就叫 `openai`
  （`@ai-sdk/openai`），且字面 model id 本身可能是 `openai/gpt-4`，
  合成后变成 `openai/openai/gpt-4`，语义混乱。
- 现状已经不一致：配置 provider 路径用 `agent-compose`（`opencode_facade.go:189`），
  自定义 provider 路径却用上游 provider id（`opencode_facade.go:299`, `GuestModelReference(providerID, ...)`）。
  统一成 `agent-compose` 正好消除这个不一致。

### 6. `wire_api` = 上游协议，daemon 自动判断转换

采纳。语义收敛为**两个互不混用的名字**：

- `Connection.Protocol`：**上游**协议，唯一含义；
- `AgentDialect.Supported`：该 agent 客户端**能说的入站协议集合**。

转换规则一条：`入站 ∈ Supported` 就透传，否则选 canonical 入站再转换。
`LLM_API_PROTOCOL` 不再有"对 codex 是上游、对 opencode 是入站"的双重含义——
事实上 guest 根本不读它（只有 claude runner 读 `LLM_API_ENDPOINT` 兜底，
`claude.ts:43-48`），所以它可以退化为纯上游声明。

### 7. 四个来源不应该在解析期交织

采纳。env / models.json / RPC 是**同一份连接数据的三个来源**，
在配置装载期合并成一个 `Catalog`；解析期只读 `Catalog`，不再有
`EnvProviderLookup`、`hasCompleteDefault*`、`session_env` provider 行与重启恢复。

### 8. 职责明确：env-key 配置 = 走 env 链路，不走 daemon 链路

采纳为**两条互斥路径**（见 §3.4），并已确认为 **direct：agent 直连，daemon 完全不介入
请求路径**（真实 key 进 guest）。这条一落地，`session_env` provider、
`SandboxProviderEnvItems`、`HasOpenAIEnvProviderInput`/`genericLLMEnvProviderFamily`、
`SessionEnvProviderID`、opencode 的 `opencode` 原生 provider 特例
（`openCodeNativeProviderID`）、codex 的"保留自己的登录"兜底
（`OptionalFacadeConfigError`）全都可以删除。

### 9. 兼容层归一

采纳：**归一在 daemon**。daemon 负责把上游协议转成 agent 原生协议、
并合成 guest 需要的字符串；guest 只做"我是谁"这一件事——
用 `AGENT_COMPOSE_RESOLVED_MODEL` 直接喂自己的 CLI，做 agent 自身配置文件的
字段摆放（codex 写 config.toml、claude 读 ANTHROPIC_*），
删除 `runtime/javascript/src/runners/model-reference.ts` 的 `resolveFacadeModel`
和 daemon 侧 `RuntimeModelArgument` 的 dsh 前缀逻辑。

---

## 1. 第一性原理：系统只做两件事

```
        ┌──────────────────────────────────────────────┐
        │  1. 配置上游：Connection {URL, Key, Protocol} │
        │  2. 调用上游：转发 + 协议转换                  │
        └──────────────────────────────────────────────┘
```

agent runtime 只是 daemon 的一个**客户端**。它对模型侧的全部需求是：
**一个 baseURL、一个 credential、一个 model 字符串、一个它自己能说的协议**。
除此之外的一切（路由、默认值、协议适配）都是 daemon 的内部决定。

由此得到三条不可动摇的规则：

- **R-A** 模型是不透明字符串，永不被解析。
- **R-B** 连接选择是配置查找，不是推断，没有兜底。
- **R-C** 协议转换由 daemon 承担，guest 不参与。

---

## 2. 三个名字，各归其位

| 名字 | 所有者 | 含义 | 生命周期 |
| --- | --- | --- | --- |
| `Connection.Protocol` | 配置 | **上游**支持的协议 | 静态 |
| `AgentDialect.Supported` | agent 类型 | 该 CLI **能说**的入站协议集合 | 静态 |
| `GuestModel` | facade 出口 | 给 guest CLI 的显示串（仅 pi/opencode 带 `<key>/`） | 每 run 合成一次 |

`GuestModel` 只出现在"daemon → guest"方向。任何 `<a>/<b>` 都**不会**出现在
"配置 → daemon"方向（除了下节 `models.json.default` 这种 daemon 自有文档）。

---

## 3. 目标架构

### 3.1 Catalog：连接数据的唯一形态

```go
type Protocol string // "openai_responses" | "openai_chat" | "anthropic_messages"

type ModelSpec struct {          // 可选元数据，按字面 model id 索引
    ID              string
    Protocol        *Protocol
    BaseURL         *string
    Headers         map[string]string
    MaxOutputTokens *int
}

type Connection struct {         // 一个上游
    ID       string
    BaseURL  string
    APIKey   string
    Auth     Auth
    Protocol Protocol
    Headers  map[string]string
    Models   map[string]ModelSpec
}

type Catalog struct {
    Connections  map[string]Connection
    DefaultModel string
}
```

三个来源在**装载期**合并，之后只读：

| 来源 | 装载时机 | 产出 |
| --- | --- | --- |
| daemon env `LLM_API_*` | 启动 | 一个 Connection（id `env` 或显式 id）+ DefaultModel |
| `$DATA_ROOT/models.json` | 启动 | 多个 Connection + DefaultModel |
| RPC `LLMService` | 每次写操作后重建 | 多个 Connection |

冲突策略：显式 id 冲突在装载期报错，不静默覆盖。

### 3.2 模型选择：只有一处 default，没有 fallback

```go
func SelectModel(agentModel, catalogDefault string) (string, error) {
    if m := strings.TrimSpace(agentModel); m != "" { return m, nil } // 不透明
    if m := strings.TrimSpace(catalogDefault); m != "" { return m, nil }
    return "", ErrNoModel   // 明确报错，不猜
}
```

### 3.3 连接选择：确定性，无兜底

```go
func (c *Catalog) connectionFor(explicitID, model string) (Connection, error) {
    if explicitID != "" { return lookupOrError(explicitID) }
    if ids := c.boundConnections(model); len(ids) == 1 { return c.Connections[ids[0]], nil }
    if len(c.Connections) == 1 { return theOnly, nil }
    return Connection{}, ErrAmbiguous // 列出候选 + 提示显式声明
}
```

- 没有 `default`/`anthropic` 保留 id 的优先级；
- 没有"请求家族没有连接就借另一个家族"（`default_connection.go:46-49`）；
- 没有 family 参与解析。家族只在 §3.7 的转换矩阵里出现。

`models.json.default: "gateway/model"` 仍然是 `provider/model`——
它是 daemon 自有的、无歧义的文档格式，解析它不违反 R-A。

### 3.4 两条互斥路径：direct vs managed

判据是**该 agent 是否自带 LLM 连接配置**（`agents.*.env` 里出现任一：
`LLM_API_ENDPOINT`/`LLM_API_KEY`/`OPENAI_API_KEY`/`OPENAI_BASE_URL`/
`ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_BASE_URL`）。

```
direct  : agent 自带上游
          - 不 mint facade token，不代理，不转换
          - daemon 仍用同一个 Dialect writer 把 agent 指向 env 里的上游
          - model = agent.model 原样
          - 协议兼容由配置者负责（同 agent 原生协议）

managed : daemon 拥有上游
          - Catalog 解析 Connection + model
          - mint token(绑定 Connection)
          - daemon 指向 facade，按矩阵转换
          - model = GuestModel（仅 pi/opencode 合成 <key>/<model>）
```

这条规则替代了现在散落的：session-env provider、`opencode` 原生 provider 特例、
codex/claude 的"保留自己登录"兜底。

> 已确认采用 direct 语义：环境里声明的真实 key 会进入 guest（现状
> `MergeManagedExecEnv` 只在 managed 分支剥掉 provider key）。这是"agent 自带凭据"
> 的明确定义：**要隐藏 key 或要协议转换，就不要在 agent env 里写 LLM 连接**，
> 改用 daemon 侧 Catalog（daemon env / models.json / RPC）。direct 路径下
> daemon 仍会用同一套 Dialect writer 把 guest CLI 指向声明的上游，否则 pi/opencode
> 没有 provider 条目无法启动；"不介入"指的是不代理、不转换、不签发 token。

### 3.5 Facade：dialect 表 + 一个 writer

```go
type Dialect struct {
    Kind          string
    Supported     []Protocol          // 能说的入站协议
    Canonical     Protocol            // 不支持上游协议时的入站
    GuestProvider string              // pi/opencode = "agent-compose"；其余 ""
    WriteConfig   func(host, baseURL, credential, model string) error
    GuestEnv      func(baseURL, credential string) map[string]string
}
```

| Kind | Supported | Canonical | GuestProvider |
| --- | --- | --- | --- |
| codex | responses | responses | — |
| claude | messages | messages | — |
| opencode | chat | chat | `agent-compose` |
| pi | chat, responses, messages | chat | `agent-compose` |
| dsh | chat, responses, messages | chat | — |

`PrepareAgentLLM` = 选模型 → 选连接 → 算入站协议 → 写配置 → mint token → 返回 env。
一次性运行、prompt attach、scheduler command 三个入口都调它。

### 3.6 代理：connection-bound token + 弱转发

token 只绑定 `{SandboxID, ConnectionID, InboundProtocol, Model?}`。
运行期：

```
route -> inbound
token.inbound == inbound          // 防串路由
conn = catalog[token.connectionID]
model = request.model             // 不透明，原样
target = conn + spec(model)       // 只取 per-model 覆盖，不做选择
convert(inbound, conn.Protocol)
```

没有 per-request resolution、没有 provider family 偏好、没有模型白名单。
`token.model` 仅作请求未带 model 时的默认值。

### 3.7 转换矩阵：一条规则

`inbound = (conn.Protocol ∈ dialect.Supported) ? conn.Protocol : dialect.Canonical`，
然后 `inbound == conn.Protocol` 透传，否则转换。

| agent | 上游 messages | 上游 responses | 上游 chat |
| --- | --- | --- | --- |
| codex | 转 responses→messages ✓ | 透传 | 转 responses→chat ✓ |
| claude | 透传 | 转 messages→responses ✓ | **转 messages→chat（缺口）** |
| opencode | 转 chat→messages ✓ | 转 chat→responses ✓ | 透传 |
| pi | 透传 | 透传 | 透传 |
| dsh | 透传 | 透传 | 透传 |

全表只有一格缺失，且**已确认补齐**（§5.0）。补桥后家族约束从代码里彻底消失：
`connectionFor` 不认识 family，`PrepareAgentLLM` 只做 `Supported` 集合判断，
`runtime_llm.go` 只做 `inbound==upstream ? 透传 : 转换`。

> pi/dsh 保留全协议透传是有实测理由的：某些网关会发自定义事件
> （`assets/.dsh/profiles/agent-compose/cordis.patch.yml:7-16` 记录的
> `codex.response.metadata`、`responsesapi.websocket_timing`），
> 转换会把它们渲染进正文。优先透传因此是正确策略，而不是优化。

---

## 4. 代码收敛

### 4.1 删除

- 模型引用解析：`SplitModelReference`、`ValidateFacadeModelReference`、
  `refineProviderAndModelFromReference`、`sessionHasEnvProvider`、
  `sessionEnvModelForDeclaration`、`SessionEnvModel`、`SessionAnthropicEnvModel`。
- 连接兜底：`defaultConfiguredConnection`、`reservedDefaultConnection`、
  `ambiguousDefaultConnectionError`、`resolveLiteralModelTarget`、
  `ProviderFamilyIsRequired`。
- env provider 交织：`provider_bootstrap.go`、`provider_environment.go`、
  `env_provider.go` 的探测函数、`session_env` provider、`bootstrapDefaultLLMConfig`。
- facade 重复：`codex_facade.go` / `pi_facade.go` / `opencode_facade.go` /
  `dsh_facade.go` 的 6 个函数与 4 个 opencode 变体、`resolvePiFacadeTarget` /
  `resolveDshFacadeTarget` / `resolveCustomOpenAIFacadeTarget`、
  `runtimefacade/config.go` 内联 claude、`runs/prompt_attach_facade.go` 的 claude 副本。
- 第二套预览解析：`agent_model_resolution.go`（预览改为复用 `PrepareAgentLLM` 的纯函数部分）。
- guest 兼容：`RuntimeModelArgument`（dsh 前缀）、`model-reference.ts` 的 `resolveFacadeModel`。

### 4.2 新增

- `pkg/llms/catalog.go`：Connection/ModelSpec/Protocol + 三源合并。
- `pkg/llms/resolve.go`：`SelectModel` + `connectionFor` 纯函数。
- `pkg/llms/dialect.go`：dialect 表 + `PrepareAgentLLM`。
- `pkg/llms/guestconfig.go`：三个 writer（现有实现迁移过来，去掉 prefix 逻辑）。
- 运行期 `runtime_llm.go`：按 §3.6 简化。

预计 `pkg/llms` 生产代码从 ~40 个文件降到 ~10 个。

---

## 5. 分阶段落地

**P0 — 补桥，补全矩阵**（`ai-api-protocol-bridge` 仓库）。

*API 变更*：现有 `NewCrossFamilyBridge(inbound, upstreamFamily)` 无法区分
"Anthropic 入站去 OpenAI Responses" 与 "去 Chat"（`bridge_cross_family.go:29-40`）。
新增协议精确版本，旧函数保留以兼容：

```go
func NewCrossFamilyBridgeForProtocol(inbound, upstream Protocol) (CrossFamilyBridge, bool)
```

*新增 `bridge_anthropic_to_chat.go`*，与 `bridge_anthropic_to_responses.go` 同构：

- `InboundProtocol() = ProtocolAnthropicMessages`，`UpstreamProtocol() = ProtocolOpenAIChat`；
- 请求编码：`system`/`developer` → `role=system`；`tool_use` → `assistant.tool_calls`
  （复用 `encodeOpenAIToolInput`）；`tool_result` → `role=tool` + `tool_call_id`；
  文本 → `content`；图片 → `image_url`；`tools`/`tool_choice`/`max_tokens`/
  `temperature`/`top_p`/`stop`/`response_format` 直映射（`openAIChatRequest` 字段齐全）；
- reasoning：`Reasoning`/`ReasoningBudgetTokens` → `reasoning_effort`；
  **签名/redacted 无法表达，丢弃**（chat 只有 `reasoning_content` 字符串通道）；
- 流：`NewStreamDecoder` 用 `OpenAIChatAdapter`，`NewStreamEncoder` 用
  `anthropicStreamEncoder`，start/finish 走 OpenAI→Anthropic 的 usage 转换；
- 注册进 `NewCrossFamilyBridge` 的兼容分支与新的按协议构造函数。

*本仓库*：`EncodeRuntimeUpstreamRequest`（`pkg/llms/facade_bridge.go:162`）改用
按协议构造函数，删除 `bridge.UpstreamProtocol() != upstreamProtocol` 的兜底判断。

**P1 — Catalog 与纯函数解析**：建立 `Catalog`，把 env/models.json/RPC 合并到装载期；
`SelectModel` + `connectionFor` 落地；旧解析路径保留薄适配层以便增量切换。

**P2 — Dialect 收敛 facade**：六个 facade + 三个入口合成 `PrepareAgentLLM`；
guest `resolveFacadeModel` / `RuntimeModelArgument` 删除，guest 只读
`AGENT_COMPOSE_RESOLVED_MODEL`。

**P3 — direct/managed 分叉**：agent env 命中 LLM key → direct；
删除 session-env provider 与所有 env 探测函数。

**P4 — 配置面显式化**：`agents.*.llm_connection` + `model` 字面化；
兼容期允许 `model` 前缀命中唯一连接时按旧语义解释并告警，之后移除。

**P5 — 删除 fallback 与 family 约束**：删 §4.1 剩余项；
codex/claude 不再限制上游家族（由矩阵决定）。

---

## 6. 迁移

- 配置：`model: gateway/model` → `llm_connection: gateway` + `model: model`；
  提供 `agent-compose` 校验/迁移提示。
- 部署：daemon env / models.json / RPC 继续作为连接来源；
  **agent 级 `LLM_API_*` 语义由"daemon 代理的环境上游"改为"agent 直连"**，
  真实 key 进入 guest。这是唯一需要显式通告的行为变更，需要在 release note
  与管理手册中标注；依赖 daemon 隐藏 key 的部署应迁移到 models.json/RPC。
- 协议：补齐 `anthropic_messages→chat_completions` 前，claude + chat-only 上游
  在运行期报 `unsupported llm protocol bridge`；补齐后该格变为可用。
- 文档：`docs/pages/agent-compose-yaml-manual.md`（中英）第 506/535-546/603-619 行、
  `docs/design/llm-provider-rpc.md` 是契约来源，P4 同步更新并跑 `task docs:build`。

---

## 7. 测试

- `SelectModel` / `connectionFor` 纯函数表驱动：显式/绑定/唯一/歧义/无模型。
- `GuestModel` 合成：含 `/` 的字面 model（`baizhi/gpt-5.5`、`meta-llama/Llama-3.1-8B`）
  必须以 `agent-compose/baizhi/gpt-5.5` 形式到达 pi/opencode，且整体不被切分。
- 转换矩阵表驱动：15 个 `(dialect, upstream)` 组合的成功/失败与所选入站协议；
  claude × chat 必须走通（依赖 P0 的桥）。
- direct/managed 分叉：命中 LLM env 的 agent 不产生 token、不写 facade 配置、
  真实 key 出现在 guest 环境；未命中的 agent 一定产生 token 且不泄漏上游 key。
- 回归：删除 `ValidateFacadeModelReference` 后，原"unknown prefix"用例转为
  "字面 id 透传"用例。

---

## 8. 实现进度（分支 `feat/llm-call-chain`）

基线 `origin/main` `5a21a5c1`。每个里程碑各自提交，提交时
`gofmt -l` / `go build ./...` / `go vet ./pkg/...` / `go test ./pkg/...` 全绿
（`test/e2e` 在本 worktree 内因 unix socket 路径超过 107 字节而失败，与改动无关）。

### 已完成

**M1 纯函数内核**（`a9bd09cf`）

- `pkg/llms/protocol.go`：`Protocol` 类型与三个常量，`NormalizeProtocol` /
  `Valid` / `Family` / `ProtocolForFamily`。
- `pkg/llms/connection_catalog.go`：`Catalog` 快照 + `LoadCatalog`（一次查询装完
  连接与模型绑定）+ `SelectModel` + `Resolve` + 四个哨兵错误。连接选择是固定
  优先级的**查找**，无兜底。
- `pkg/llms/agent_dialect.go`：`DialectFor` / `Supports` / `InboundProtocol` /
  `NeedsConversion` / `GuestModel`。`GuestModel` 是全链路唯一合成
  `<provider>/<model>` 的地方。
- `pkg/llms/target.go`：新增纯函数 `NewResolvedTarget`。
- `configstore.ListLLMProviderModelConfigs`：一次列出全部 model binding。

**M1 修正**（`4d0ef569`）

- opencode 的 `Supported` 是 `{chat, messages}` 而非 `{chat}`：它经 AI SDK 的
  Anthropic provider 原生说 messages，因此 `messages` 上游对它是**透传**。
- `CanConvert` 不复制 bridge 注册表，而是直接询问
  `protocolbridge.NewCrossFamilyBridge`。于是全矩阵只剩一格缺口
  （`messages → chat`），且补桥后本仓库无需改动。

**M2 facade 收敛**（`80946581`）

- `PrepareAgentLLM` 成为唯一入口，一次完成选模型 → 选连接 → 定入站协议 →
  校验可转换 → 签发 token → 写 guest 配置 → 返回 env。
- `pkg/llms/dialect_writers.go`：五个 agent 各自写 env/文件，全部只消费已解析
  结果，不做任何解析。
- opencode 的 writer 合并为一个，provider key 恒为 `agent-compose`：删除了
  `anthropic` / 上游 provider id / `openCodeNativeProviderID` 三种 guest key
  并存的不一致。
- `runtimefacade.EnsureSessionAgentRuntimeConfig` 与
  `runs.ensurePromptAttachLLMFacadeEnv` 改为调用 `PrepareAgentLLM`；
  `RuntimeModelArgument`（dsh 兼容前缀）与 `GuestModelReference` 删除。
- 删除 `codex_facade.go` / `pi_facade.go` / `dsh_facade.go` / `opencode_facade.go` /
  `custom_openai_facade_target.go` / `facade_config_error.go`。

**M2 期间发现并修掉的三个真实回归**

1. **daemon env 不再被投影**。旧实现把 `LLM_API_*` / `ANTHROPIC_*` 的物化放在
   解析路径里懒执行；`PrepareAgentLLM` 只读 catalog，于是只用环境变量配置的
   daemon 会得到空 catalog，agent 静默退化为"自带凭据"。
   修复：`llms.ProjectDaemonLLMConfig` 由 `app.loadLLMConfig` 在启动时与
   models.json 一起投影一次；请求路径不再写配置。无 key 的环境不注册连接。
2. **零连接被报成"歧义"**。`connectionFor` 在 `len(providers)==0` 时走进歧义
   分支，产生 `model "x" matches ; declare llm_connection` 这种无候选的错误。
   修复：新增 `ErrNoConnection`。三个哨兵统一由
   `llms.IsUnmanagedAgentLLMError` 判定为"daemon 不管这个 agent"。
3. **检查顺序**。未托管的 agent 在 daemon 没有可达 URL 时应当是 `ErrNoModel`
   而不是 failed-precondition，因此改为"先选模型、后校验可达 URL"。

**M3 代理：connection-bound token + 弱转发**（`9510a3b3`）

- `FacadeToken` 增加 `GuestModel`（migration 16）。token 从此记录三件事：
  连接 id、上游字面 model、guest 侧拼写。旧 token `GuestModel` 为空，保持
  旧的"单模型锁定"行为。
- `FacadeToken.ResolveUpstreamModel(requested)`：请求命中 `GuestModel` → 精确
  替换为字面 `Model`（纯字符串相等，不切分，含 `/` 的字面 model 因此完好）；
  其他 model → **原样转发**；无连接的旧 token → 继续锁定单一 model。
- `RuntimeLLMTargetResolver`（参数含 sandbox / providerFamily）换成
  `RuntimeLLMConnectionResolver(ctx, connectionID, model)`。代理不再选择连接，
  原来的 `token.ProviderID != target.Provider.ID` 校验随之消失——连接来自
  token，不可能不一致；取而代之的是 `token.ProviderID == ""` → 403。

**M3 预览路径**（`6b4e6f18`）

- `pkg/llms/agent_model_resolution.go` 从 336 行降到 77 行：项目 UI 预览不再
  自己重算 provider family / session env / 全局环境 / 存储默认值，而是加载一次
  catalog，按"agent 声明的 model → agent env 里的 model → catalog 默认 model →
  无"取值。输出契约（`AgentModelSource` 与 proto 枚举）不变。

**M5 direct / managed 二分**（`c608d64b`）

`PrepareAgentLLM` 先做一次判定，再决定走哪条路：

- **direct**：agent 的 `env` 里声明了自己的上游 → 用**同一个** Dialect writer 把
  guest 指向那个上游，凭据就是 agent 自己的 key，**不查 catalog、不签 token、
  不代理、不转换**，model 原样。
- **managed**：agent 没声明 → catalog 拥有上游，一切照旧。

两条路互斥，判据是**声明**而不是**兜底**：

- 只声明 key（无 endpoint）算完整声明——vendor 的公开 endpoint 是 vendor 的
  属性，不是 daemon 的路由选择；只声明 endpoint 不算，daemon 没有凭据可用，
  也不能编一个。
- `LLM_API_PROTOCOL` 未声明时取该 agent 的 `Canonical`：这条路上没有转换，
  这正是它自己的 CLI 对该 endpoint 会选的协议。
- 声明的协议 agent 说不出来 → **报错**，不静默改写：在 agent env 里写连接，
  就是要求"key 进 guest、daemon 别管"；要 daemon 代管就写 catalog connection。

`AgentLLM` 因此显式携带 `Endpoint`/`Credential`（guest 视角的最终值），而不是一个
还需要每个 writer 自己去拼 facade 路由的 daemon base URL——writer 从此只是格式化
一个已经定好的决定。

**M3b/M5 删除旧层**（`c26e4905`，净 -3192 行）

- 删除 `resolver.go`、`default_connection.go`、`selection.go`、
  `provider_bootstrap.go`、`provider_environment.go`、`env_provider.go`、
  `runtime_facade_provider.go`、`runtime_target_sandbox.go`、
  `runtime_facade_target.go`、`model_reference.go`（含 `SplitModelReference`）、
  `runtimefacade/startup_config.go` 及其专属测试。
- scheduler 自己的 LLM client 不再读 per-scope env，改为
  `LoadCatalog` → `SelectModel` → `Resolve`：daemon 自己发起的调用不是 agent
  运行，没有 sandbox。
- sandbox 准备阶段原本在 agent kind 未知时先给**两个 family** 各签一个 facade
  token，再用 agent 的覆盖掉。该调用点其实已知 agent kind，两步因此删除；
  session 自己声明的 provider env 作为 `AgentEnv` 传入——这正是 direct 路径
  生效的入口。
- scheduler command facade 原本合并 startup token 与 selected token（一次命令
  3 个 token），现在只签 1 个。
- `SetSandboxProviderEnvItems` 移到数据的所有者：`(*domain.Sandbox).SetProviderEnvItems`
  （实际有 3 个调用点，不是 1 个）。
- `MergeManagedExecEnv` 不再抹掉 base env 里的 provider key。那层剥离是为了
  保护 daemon 托管 facade 不被 sandbox 自己的 provider env 覆盖；direct 模式下
  要保住的恰是 agent 自己的声明，managed 值仍按 key 覆盖。

### 未完成

- **M4** `llm_connection` 配置面（compose schema、proto、API、configstore）：
  目前 catalog 的三个来源是 daemon env（启动投影）、models.json（启动投影）、
  RPC（写入 store），声明式 compose 字段尚未提供。
- **M6** 补 `anthropic_messages → chat_completions` 桥。这一格位于外部仓库
  `github.com/chaitin/ai-api-protocol-bridge`（本机无源码 checkout，需联网），
  补桥后升级依赖即可，本仓库已通过 `CanConvert` 询问注册表而无需改动。
- **文档**：`docs/pages` 的 en / zh-CN 两版仍需按新语义更新，并跑
  `task docs:build`。
