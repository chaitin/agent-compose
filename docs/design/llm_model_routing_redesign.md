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
仓库实现，本仓库升依赖版本。实现规格见 §5 P0。

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

原结论是**两条互斥路径**（见 §3.4），并曾确认为 **direct：agent 直连，daemon 完全不介入
请求路径**（真实 key 进 guest）。**该结论已废弃**：识别的第一方声明现在由 daemon 吸收成
自己的连接并代理，真实 key 不进 guest；只有 daemon 识别不了的 `*_API_KEY` 才原样下发。
`session_env` provider、`SandboxProviderEnvItems`、
`HasOpenAIEnvProviderInput`/`genericLLMEnvProviderFamily`、`SessionEnvProviderID`、
opencode 的 `opencode` 原生 provider 特例（`openCodeNativeProviderID`）、codex 的
"保留自己的登录"兜底（`OptionalFacadeConfigError`）仍然删除——吸收走的是普通 Catalog
连接，不需要这些特例。

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
    Connections       map[string]Connection
    DefaultModel      string
    DefaultConnection string   // 该 default 的来源连接；无 default 时为空
}
```

`DefaultConnection` 不是第二个 default：它只是记录"这个 default 是从哪个连接来的"，
因此当被解析的 model 恰好是配置的 default 时，来源连接是确定的，不必去猜（§3.3）。

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
    if len(c.Connections) == 0 { return Connection{}, ErrNoConnection } // 无可托管的连接
    if model == c.DefaultModel && c.DefaultConnection != "" { return c.DefaultConnection, nil }
    if ids := c.boundConnections(model); len(ids) == 1 { return c.Connections[ids[0]], nil }
    if len(c.Connections) == 1 { return theOnly, nil }
    return Connection{}, ErrAmbiguous // 列出候选 + 提示显式声明
}
```

`ErrNoConnection` 与 `ErrAmbiguous` 分开，是因为"什么都没有"和"有好几个、你不说我
不知道选哪个"是两件事：前者是 daemon 不托管这个 agent，后者才是配置错误。两者都由
`llms.IsUnmanagedAgentLLMError` 收敛成调用点唯一的判定（`ErrNoModel` 同理）。

- 没有 `default`/`anthropic` 保留 id 的优先级；
- 没有"请求家族没有连接就借另一个家族"（`default_connection.go:46-49`）；
- 没有 family 参与解析。家族只在 §3.7 的转换矩阵里出现。

> **修订（后续实现）**：上面代码里的 `ErrAmbiguous` 已被删除。多个连接服务同一模型时
> 不再报错，而是按调用方的协议亲和性选路并优先透传；协议相同的候选随机选一个。
> 只有 `ErrNoConnection`（零连接）仍然是失败。详见 §8「未完成与偏差」。

`models.json.default: "gateway/model"` 仍然是 `provider/model`——
它是 daemon 自有的、无歧义的文档格式，解析它不违反 R-A。

### 3.4 单一路径：可识别的声明由 daemon 吸收，其余照原样下发

判据是**这个声明是否是 daemon 认识的第一方凭据**，而不是"agent 是否自带 LLM 连接配置"。

```
declared(可吸收) : daemon 认识的官方/知名 vendor 凭据
          - daemon 把它写成自己的连接（`session-env:<sandbox>:<family>`，scope=declared）
          - 用同一个 Catalog 解析这条连接，mint 绑定它的 facade token
          - guest 只拿到 facade URL + token，真实 key 不进 guest
          - 协议转换、模型选择与 daemon 托管路径完全一致

declared(不可吸收) : daemon 认识但代理不了（Azure / Google 专属协议），
                    或完全不认识的 `*_API_KEY`
          - 原样下发到 guest 环境
          - daemon 无法保护，只能在项目检查里告警
```

可吸收集合：`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`OPENAI_API_KEY`、
`CODEX_API_KEY`、`DEEPSEEK_API_KEY`、`OPENROUTER_API_KEY`，以及通用的
`LLM_API_KEY`（配合 `LLM_API_PROTOCOL` / `LLM_API_ENDPOINT`）。识别但不吸收：
`AZURE_OPENAI_API_KEY`、`GOOGLE_API_KEY`、`GEMINI_API_KEY`。

声明的连接只按显式 ID 寻址，不进入 `Catalog.serving`、唯一连接兜底和
`Connections()`，所以一个 agent 的凭据永远不会服务另一个 agent。同一个 run 的
base 环境在应用 managed 环境之前会剥掉这些 provider 变量名，因此 facade token
写在 vendor 变量名下也能存活。剥离范围是**声明本身**而不只是 key：端点与协议
变量（`LLM_API_ENDPOINT`、`LLM_API_PROTOCOL`、`ANTHROPIC_BASE_URL`、
`ANTHROPIC_API_ENDPOINT`、`OPENAI_BASE_URL`、`DEEPSEEK_BASE_URL`、
`OPENROUTER_BASE_URL`）同样被移除，否则 guest 会拿到一个它已无法用 facade token
认证的上游地址——那是更容易误判的失败，而不是一项能力。工程与 Agent 的显示视图
读的是声明（project spec），而显示层对**被吸收的凭据**一律脱敏（变量名保留、值显示
`********`，与该变量是否写 `secret: true` 无关）：吸收后的值只属于 daemon，若视图仍回显，
就等于经由一个其它响应都很克制的 API 把 daemon 持有的凭据发出去。识别但不吸收的
`*_API_KEY` 刻意不脱敏——它们会进入 sandbox，项目检查的告警才是运维的信号，在视图里
遮住值既不会改变暴露，又会把未受保护的值说成受保护。某次 run 真正使用的 facade 地址
与 token 只存在于该 run 的 `RuntimeEnvItems`，从不落盘。

> 这条替代了 PR #715 的 direct 语义。当时的结论是"agent 自带凭据就让真实 key 进
> guest"，但那会让 operator 写在 project/agent 环境里的官方 key 出现在 agent
> runtime 中，与"上游凭据只保留在 daemon"的既有边界冲突。现在一律保护：可识别的
> 声明被吸收并代理，daemon 不做流量劫持；不可识别的声明明确不受保护，并在项目
> 检查时告警。
>
> writer 生成的是"配置文件引用哪个环境变量"，现在只有一种模式：永远指向
> `AGENT_COMPOSE_SANDBOX_TOKEN`（facade token）。codex 的 `env_key`、pi 的
> `apiKey`、opencode 的 `{env:...}` 都由同一个 `guestCredentialEnvName` 决定。

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
配置写在 token 落库之前是有意的：writer 可能失败，若 token 已落库，这次失败就会留下
一条没有任何运行会使用的凭据；反过来失败最多留下一份会被下次运行覆盖的旧配置文件。

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
| claude | 透传 | 转 messages→responses ✓ | 转 messages→chat ✓ |
| opencode | 转 chat→messages ✓ | 转 chat→responses ✓ | 透传 |
| pi | 透传 | 透传 | 透传 |
| dsh | 透传 | 透传 | 透传 |

全表 15 格全部可服务。补齐 messages→chat 后家族约束从代码里彻底消失：
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
- 第二套预览解析：`agent_model_resolution.go` 已改为按 `PrepareAgentLLM` 的同一套
  优先级取模型，并且只在 direct 模式真正生效时才读 agent 环境里的 model（见 §8）。
- guest 兼容：daemon 侧 `RuntimeModelArgument`（dsh 前缀）已删除；guest 侧
  `model-reference.ts` 的 `resolveFacadeModel` **保留**，它是 `guest-image-abi.md`
  明文承诺的滚动升级兼容面（新 guest + 旧 daemon），不是改造残留。删除它必须与
  该文档承诺一同变更，见 §8「未完成项」。

### 4.2 新增

实际落点与本节最初的命名不同，职责一一对应：

| 本节原名 | 实际文件 | 职责 |
| --- | --- | --- |
| `catalog.go` | `connection_catalog.go` | Connection/ModelSpec/Protocol + 三源合并 |
| `resolve.go` | `connection_catalog.go` 内的 `SelectModel` / `connectionFor` | 选择是 Catalog 的不变量，不另立文件 |
| `dialect.go` | `agent_dialect.go` + `agent_llm.go` | dialect 表与 `PrepareAgentLLM` 入口 |
| `guestconfig.go` | `dialect_writers.go` | 各 agent 的 guest 配置 writer |

`pkg/llms` 生产代码从 39 个文件降到 26 个。原始估计的 ~10 个偏乐观：其中约 8 个
是改造范围之外且各有一项独立职责（HTTP facade、MCP 配置、pi runtime 配置、provider
CRUD、token、env headers 等），路由核心本身已是 8 个文件。

---

## 5. 分阶段落地

**P0 — 补桥，补全矩阵**（改 `ai-api-protocol-bridge` 仓库）。

*状态：已闭环。* 上游 PR `chaitin/ai-api-protocol-bridge#1` 已合入该库 `main`，
本仓库把依赖从 `v1.0.0` 升级到包含该桥的提交。该提交尚无 tag，因此 go.mod 记录
伪版本 `v1.1.6-0.20260922130207-cfe67158b4c7`；该库发版后再换成正式 tag 即可。
`PrepareAgentLLM` 不再拒绝 claude × chat，这一格由库按协议精确选出的桥服务。

*实现方式*：`NewCrossFamilyBridgeForProtocol(inbound, upstream Protocol)` 按协议精确
选桥，旧的按家族版本行为不变；`anthropicToOpenAIChatBridge` 把请求编码、响应解码、
流解码全部委托给 `OpenAIChatAdapter`。原计划列了一张逐字段映射表，但那张表就是
`OpenAIChatAdapter.EncodeRequest` 本身——入站请求早已被 dialect 解码成中立
`LLMRequest`，再手写一遍等于复制该库的 chat 编解码器。唯一需要自带的是 Anthropic 的
usage 口径，直接复用该库的 `responsesUsageToAnthropicUsage`。

本仓库因此不需要新增桥文件：`crossFamilyBridge` 改调该构造函数，并按协议成对传参，
家族参数从该 helper 及其调用链上移除。`CanConvert` 早就改为询问注册表而不是复制表，
所以这一格自动变为可服务。

*上游顺带修掉的两个真实缺陷*（都是回放 agent-compose 实际流量才暴露的，且 `v1.0.0` 同样存在）：

1. **工具结果被丢弃**。Anthropic 把 tool result 放在**user** 消息里的 `tool_result`
   块中，而 `encodeOpenAIChatMessages` 只看 text/refusal/tool-call，于是编码成
   `{"role":"user","content":""}`——工具输出根本没到模型。已改为先产出 tool 消息
   （chat 协议要求它紧跟发起调用的 assistant 轮），再编码该消息其余部分。
2. **流式 usage 丢失**。OpenAI 上游在 `finish_reason` 之后**单独一 chunk** 报 usage，
   chat 解码器把它变成 `StreamResponseMetadata`，而 Anthropic 编码器对该 part 直接
   返回 nil。结果：12000 prompt（11000 命中缓存）的一轮报成 `input_tokens=0`。
   已改为把没有 usage 的 finish 暂存，等 usage 到达再发；`Close` 兜底补发。

本仓库的测试从两侧钉住这一格：`TestCrossFamilyBridgeServesMessagesToChat` 断言
按协议选桥返回 chat，`TestCrossFamilyBridgeKeepsToolResultsForChatUpstream` 断言
工具结果出现在 chat 报文里，`TestPrepareAgentLLMClaudeConvertsChatUpstream` 断言
运行准备成功且 token 仍锁 messages。

*`UpstreamProtocol()` 校验保留*：按协议选桥是库的行为，不是本仓库能假设的不变量；
保留该校验后，将来库若返回另一协议，本地会明确报错而不是静默把请求发错上游。

**P1 — Catalog 与纯函数解析**：建立 `Catalog`，把 env/models.json/RPC 合并到装载期；
`SelectModel` + `connectionFor` 落地；旧解析路径保留薄适配层以便增量切换。

**P2 — Dialect 收敛 facade**：六个 facade + 三个入口合成 `PrepareAgentLLM`；
guest `resolveFacadeModel` / `RuntimeModelArgument` 删除，guest 只读
`AGENT_COMPOSE_RESOLVED_MODEL`。

**P3 — 声明凭据的判定**：agent/project env 命中可识别的第一方 key →
吸收成 daemon 侧声明连接并代理；不可识别的 `*_API_KEY` 原样下发。
删除 session-env provider 与所有 env 探测函数。

**P4 — model 字面化与诊断**：`model` 保持不透明，不再按 `/` 解释。
按旧语义解释 `model` 前缀的方案已否决：那正是本设计要消灭的第二套解释，
且 model 是整体、可以合法含 `/`。改为**只诊断、不解释**（见 §6）。
agent 侧的 connection 选择配置（一度以 `agents.*.llm_connection` 落地）已移除：
connection 选择完全属于 daemon 的 LLM 配置，agent 只声明 `model`。

**P5 — 删除 fallback 与 family 约束**：删 §4.1 剩余项；
codex/claude 不再限制上游家族（由矩阵决定）。

---

## 6. 迁移

- 配置：`model: gateway/model` 需改写为字面 `model: model`，并让 gateway 成为该模型
  的唯一提供者（或默认模型的所有者）。迁移提示已落地为
  `Catalog.legacyQualifiedModelError`：当一个 model
  **不被任何连接提供**、而它的第一个 `/` 前缀**是某个连接**且该连接**确实提供
  剩余部分**时，返回 `ErrLegacyQualifiedModel`，错误信息给出应写下的 `model`
  与应配置的连接。

  这不是兜底：行为完全不改，值也不会被重新解释。三个条件同时成立才触发，
  所以合法含 `/` 的 model（如 `meta-llama/Llama-3.1-8B`）不受影响；它只是把
  本来会出现的上游 "unknown model" 或令人困惑的歧义错误，换成本地可执行的提示。
- 部署：daemon env / models.json / RPC 继续作为连接来源；
  **agent 级 `LLM_API_*`（以及认识的 vendor key）现在由 daemon 吸收成声明连接并代理**，
  真实 key 不进 guest；只有 daemon 识别不了的 `*_API_KEY` 才原样下发。项目检查会对两种
  情况分别告警。仍然建议把长期凭据配置在 daemon 侧（daemon env / models.json / RPC），
  因为那才是能被显式管理、轮换和共享的地方；project 里的官方 key 只是被识别后自动保护。
- 协议：`anthropic_messages→chat_completions` 已随依赖升级补齐（见 P0），
  claude + chat-only 上游可直接服务；此前该格在运行期报
  `unsupported llm protocol bridge`。
- 文档：`docs/pages/agent-compose-yaml-manual.md`（中英）第 506/535-546/603-619 行、
  `docs/design/llm-provider-rpc.md` 是契约来源，P4 同步更新并跑 `task docs:build`。

---

## 7. 测试

- `SelectModel` / `connectionFor` 纯函数表驱动：显式/绑定/唯一/歧义/无模型。
- `GuestModel` 合成：含 `/` 的字面 model（`baizhi/gpt-5.5`、`meta-llama/Llama-3.1-8B`）
  必须以 `agent-compose/baizhi/gpt-5.5` 形式到达 pi/opencode，且整体不被切分。
- 转换矩阵表驱动：15 个 `(dialect, upstream)` 组合的成功/失败与所选入站协议；
  claude × chat 必须走通（P0 的桥已随依赖升级到位）。
- 声明凭据的吸收：命中可识别第一方 key 的 agent 产生 token、把真实 key 写进
  daemon 侧的 `declared` 连接，且 guest 环境只有 facade token；不可识别的
  `*_API_KEY` 原样出现在 guest 环境；daemon 托管的 agent 一定产生 token 且不泄漏上游 key。
- 隔离：`declared` 连接不出现在 `Catalog.serving` / 唯一连接兜底 / `Connections()` 中，
  一个 sandbox 的声明凭据不会服务另一个 sandbox。
- 回归：删除 `ValidateFacadeModelReference` 后，原"unknown prefix"用例转为
  "字面 id 透传"用例。

---

## 8. 实现进度（分支 `feat/llm-call-chain`）

> 下面的里程碑记录是当时的实现过程，其中的 **direct 模式已被 §3.4 的单一路径取代**：
> 可识别的第一方声明现在由 daemon 吸收并代理，真实 key 不进 guest。M5/M6 中所有
> "direct 让真实 key 进 guest"的描述只作为历史阅读。

基线 `origin/main` `5a21a5c1`。每个里程碑各自提交，提交时
`gofmt -l` / `go build ./...` / `go vet ./pkg/...` / `go test ./pkg/...` 全绿
（`test/e2e` 在本 worktree 内因 unix socket 路径超过 107 字节而失败，与改动无关）。

### 已完成

**M1 纯函数内核**（`a9bd09cf`）

- `pkg/llms/protocol.go`：`Protocol` 类型与三个常量，`NormalizeProtocol` /
  `Valid` / `Family`。（`ProtocolForFamily` 一度存在，后因无生产调用方而删除；
  按家族取协议这件事由 dialect 表的 `Canonical` 承担。）
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
  `protocolbridge.NewCrossFamilyBridge`。于是当时全矩阵只剩一格缺口
  （`messages → chat`），且补桥后本仓库无需改动——该缺口已在 M6 随依赖升级补齐。

**M2 facade 收敛**（`80946581`）

- `PrepareAgentLLM` 成为唯一入口，一次完成选模型 → 选连接 → 定入站协议 →
  校验可转换 → 写 guest 配置 → 签发 token → 返回 env。写配置在前、落库在后，
  配置写入失败就不会留下无人引用的 token（见 §3.5）。
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
   分支，产生 `model "x" matches `（候选为空）这种无候选的错误。
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
  catalog，按"agent 声明的 model → catalog 默认 model → 无"取值。输出契约
  （`AgentModelSource` 与 proto 枚举）不变。
  （"agent env 里的 model"那一级在 M4 被改成只在 direct 模式生效时才读，见下。）

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

**M4 显式连接（已回退）+ 预览对齐**

- `Catalog.Resolve` 的**最高优先级**（显式连接）在生产中曾经不可达，而
  `ambiguousConnectionError` 却要求运维 "declare llm_connection"——即错误信息让人去
  写一个不存在的字段。
- 曾以 `agents.<name>.llm_connection`（compose schema / `NormalizedAgentSpec` /
  canonical JSON / spec hash / mig 17 / `project_agent.llm_connection` /
  `ProjectAgent.llm_connection = 18` / API 映射）补齐到
  `AgentLLMRequest.ConnectionID`。该字段已**整体移除**：agent 侧只声明 `model`
  （或省略，由 daemon 用默认模型），connection 选择属于 daemon 的 LLM 配置
  （models.json / daemon env / RPC）。`Resolve` 的显式连接参数只保留给 facade
  token 的再查询，不再存在任何 agent 配置入口；歧义错误改为提示"把模型绑定到唯一
  连接或设置默认模型"。migration 17 一并删除（PR 未合并，无历史数据）。
- 同时删除的还有"命名连接 vs agent 自带上游 env"的矛盾检查：agent 自带上游 env 走
  direct 模式，daemon 托管走 catalog，两者不再可能同时由 agent 声明。
- 预览改为与 `PrepareAgentLLM` 同一套优先级，且**只在 direct 模式生效时**才读
  agent 环境里的 model。此前"agent env 里有 model"会直接作为预览结果，而 managed
  运行根本不读该环境，UI 会承诺一个不会发生的模型。
- 该修正顺带消灭了第二份 model key 列表：direct 路径原先不认 `CODEX_MODEL` /
  `OPENCODE_MODEL`，与预览的 key 列表已经漂移；现在由 `directModelFromEnv`
  单点承担，两条路径共用。

**M6 P0 补桥：messages→chat 到位**（依赖升级）

- 上游 `chaitin/ai-api-protocol-bridge#1` 合入库 `main` 后，go.mod 从 `v1.0.0`
  升级到包含该桥的提交（`v1.1.6-0.20260922130207-cfe67158b4c7`，尚无 tag）。
- `crossFamilyBridge` 改用 `NewCrossFamilyBridgeForProtocol(inbound, upstream)`，
  家族参数随之从 helper、`EncodeRuntimeUpstreamRequest` /
  `EncodeRuntimeClientResponse` / `RuntimeStreamBridge` 及其调用方移除：
  `UpstreamFamily` 这个转发的中间字段不再存在。
- 矩阵第 15 格从"配置期明确拒绝"变回可服务：`CanConvert(messages, chat)` 为真，
  `TestDialectConversionMatrix` 的 `unservable` 声明表清空，
  `TestCanConvertPinsBridgeCoverage` 断言 messages→chat 为真。
- 直接回归覆盖两张真实的失败面：`TestCrossFamilyBridgeServesMessagesToChat`、
  `TestCrossFamilyBridgeKeepsToolResultsForChatUpstream`、
  `TestPrepareAgentLLMClaudeConvertsChatUpstream`。
- 流式路径同样被钉住：proxy 的 messages→chat 用例回放「usage 分片在
  `finish_reason` 之后才到」的 chat SSE，断言转出的 Anthropic `message_delta`
  报 `input_tokens=1000`（12000 减去 11000 命中缓存）与
  `cache_read_input_tokens=11000`，而不是 0 或缺失。

### 未完成与偏差

此节区分三类：已经完成但落点与原计划不同的、刻意保留的、以及真正还没做的。

- **P0 桥：已闭环，落点在上游而非本仓库**。缺口曾属实：该库 `v1.0.0` 只有按**家族**
  选桥的 `NewCrossFamilyBridge(inbound, upstreamFamily)`，而 Anthropic 入站去 OpenAI
  有 responses / chat 两条，家族无法区分，所以它只返回 messages→responses。

  该库对本环境只读（`git push` 403），改动走 fork 提在
  `chaitin/ai-api-protocol-bridge#1`。提 PR 时用真实流量回放发现两个缺陷，且它们同时
  存在于 `v1.0.0`——本仓库当时锁的正是它：

  1. **tool result 被静默丢弃**。Anthropic 把它放在 user 消息的 `tool_result` 块里，
     而 chat 编码器只看 text/refusal/tool-call，于是编码成
     `{"role":"user","content":""}`。任何 claude × chat 会话从第二轮起，模型收到的
     就是空输入。
  2. **流式 usage 全丢**。上游在 `finish_reason` 之后单独一 chunk 报 usage，
     chat 解码器把它变成 `StreamResponseMetadata`，而 Anthropic 编码器对该 part
     返回 nil。12000 prompt（11000 命中缓存）的一轮报成 `input_tokens=0`。

  两者都已在该 PR 内修掉，并配有"去掉修复即失败"的回归测试；测试本身用的就是
  agent-compose 实际收发的报文形状（去掉域名与凭据）。该 PR 现已合并，本仓库升级
  依赖后这一格直接转为可服务（见 M6），本仓库没有保留任何桥实现。

  `UpstreamProtocol()` 校验保留：它是防"静默走错协议"的，不是重复表达请求。

- **guest 侧 `resolveFacadeModel` 保留是刻意的**。`docs/pages/guest-image-abi.md`
  明文承诺滚动升级期间新 runtime 仍接受旧 daemon 传的 legacy 参数，并声明该兼容
  已废弃、待那些版本不再支持时移除。它只在 `AGENT_COMPOSE_RESOLVED_MODEL` 缺席
  （即新 guest + 旧 daemon）时生效，当前 daemon 恒设该变量。删它必须连同那段 ABI
  承诺一起改，属于发布策略决定，不是代码清理。

- **dsh legacy 参数前缀：已决定放弃该方向**。同一段 ABI 文档原本还承诺"daemon 也为
  旧 guest 保留 DSH 的 legacy 参数前缀"，实现它的 `RuntimeModelArgument` 已随 M2
  删除，而新 daemon 传给 dsh 的 CLI 参数是字面 model。于是"新 daemon + 旧 dsh guest"
  这条方向失去保护：旧 guest 会剥掉字面 model 的第一个 `/` 分量。

  决定：**不恢复**。兼容只保留一个方向（新 runtime 容忍旧 daemon），
  `docs/pages/guest-image-abi.md` 的中英两版已改写为明确的升级顺序约束——
  必须先更新 guest 镜像，或与 daemon 同时更新；`agent_dialect.go` 的 dsh 分支
  也加了注释说明这个"空 `GuestProvider`"是刻意的，避免后人误当遗漏而恢复前缀。

- **§7 的 15 格转换矩阵表驱动测试：已补齐**。`TestDialectConversionMatrix` 单表覆盖
  全部 15 格，并断言"需要转换的格只有在 `CanConvert` 也同意时才可服务"——此前它只断言
  入站协议决定，于是 claude×chat 看起来和普通转换格一样，缺口因此长期不可见。
  它同时保留 `unservable` 声明表，当前为空（M6 补上最后一格后）；将来若再有缺口，
  必须在表里写明理由，否则测试失败。

- **文档：已同步**。`docs/pages` 的 YAML 手册 en / zh-CN 随 M4 更新并通过
  `task docs:build`；`guest-image-abi.md` 中英两版按 dsh 的决策改写；
  `docs/design/llm-provider-rpc.md` 的「Runtime behavior」「Validation」两节按新的
  解析规则重写——它此前仍在描述被本次改造删掉的保留连接优先级、`<connection>/<model>`
  前缀、按家族兜底，并且断言 claude 不能用 chat-only 连接、codex 不能用 anthropic
  连接（这两条现在都与事实相反）。

  设计稿提到的 release note 在仓库内没有落点：本仓库无 CHANGELOG，
  `.github/pull_request_template.md` 也不要求，发布说明是在打 tag 时写进 GitHub
  release body 的（`.github/workflows/notify-dingtalk-release.yml` 消费它）。
  因此这不是一个待补的文件，而是发布时的动作；相关素材在各 commit message 里。

- **连接选择：歧义报错改为按协议亲和性选路**（§3.3 的 `ErrAmbiguous` 已删除，
  `docs/design/llm-provider-rpc.md` 与 YAML 手册 en / zh-CN 同步改写）。同一模型由多个
  连接提供时不再让运行失败：按调用方（agent dialect）的协议亲和性排序候选，优先可透传
  的连接，避免把本可透传的调用降级为转换；协议相同的候选彼此等价，用 `math/rand/v2`
  随机选一个（`Catalog.chooser` 可注入，测试用 `WithConnectionChooser` 固定结果）。
  没有任何连接声明该模型时，所有已配置连接都进入候选——模型名是不透明的，未声明不等于
  不支持——这与"唯一连接服务任意模型"的既有行为一致；只有 `ErrNoConnection`（零连接）
  仍然是失败。显式连接 id 的路径不变，它仍服务于 facade token 的再查询。

  排序入口是 `ProtocolPreference` 与 `Dialect.PreferredProtocols()`；`pi` / `dsh` 的
  `Supported` 随之从"枚举"改为亲和性顺序（`responses` → `chat_completions` →
  `messages`）。若没有任何连接能透传，则按 `DefaultProtocolPreference()` 的顺序退化到
  转换——转换优于拒绝运行——并且按 per-model binding 覆盖后的**实际**上游协议排序，
  而不是连接自身的 `defaultWireAPI`。

- **direct/managed 的判定来源统一**（此前各入口不一致）。direct 判定只看传入
  `PrepareAgentLLM` 的 `AgentEnv`，而各调用点的来源曾经不同：sandbox 启动传
  `session.ProviderEnvItems`（project `variables` + agent `env` + 创建请求 env），
  run 传运行时重新解析的 `agentDef.EnvItems`，prompt attach 传 `agent.EnvItems`，
  scheduler command 根本没有这个入参。于是同一个 sandbox 在启动阶段与之后的 run/命令
  阶段可能被判成不同模式；更糟的是 agent 定义解析失败时 `agentDef` 为 nil，run 会静默
  退化成 managed，为一个自带上游的 agent 签令牌并把调用改道到 catalog。

  现在五个入口统一经 `(*domain.Sandbox).DeclaredProviderEnv(agentEnv)`：把沙箱**已准备**
  的 provider env 与 agent 定义自身的 env 合并，判定规则因此是"任何一处声明了凭据，就由
  agent 拥有上游"，也让 scheduler command 的 direct 变为可达。

  为什么是合并，而不是让 run 也直接读 `session.ProviderEnvItems`：provider env 的值是
  **故意不持久化**的（可能含密钥，落库的只有 `ProviderEnvOverrideNames`），从存储加载
  回来的 sandbox 上该字段必为空；单独使用它会把 RPC 驱动的 run 从 direct 静默变成
  managed，恰好是这条规则要消除的漂移的反方向。合并保留了每条路径能看到的全部声明：
  进程内路径（新沙箱的启动与运行、scheduler 命令）用快照，存储回读路径（RPC run、
  prompt attach、release resume）至少还有定义里的那一份。

  回落规则统一在 `DeclaredProviderEnv` 内部，不再由各调用点各写一份：已准备的
  `ProviderEnvItems` 优先；它为空且 provenance 名缺失（在 provenance 拆分之前创建的旧
  sandbox）时回落到持久化的 `EnvItems`——对那批元数据来说它就是当时的声明，
  `execution.ApplyAgentProviderEnv` 与 scheduler command 一直用这个回落。传入的定义 /
  请求 / 命令 env 覆盖回落值。于是同一个 sandbox 在五个入口上要么都 direct、要么都
  managed，不会再写出互相覆盖的 guest 配置。

  已知边界：provenance 拆分之后创建的 sandbox，凭据值已被过滤出 `EnvItems`（落库的只有
  名字），因此当运行来自存储回读（RPC run、prompt attach、release resume），且凭据只写在
  project `variables` 或创建请求 env 里时无法复原，这类会话按 managed 运行；要在 RPC
  驱动的 sandbox 上稳定使用 direct，应把凭据写在 agent 自己的 `env` 里。
