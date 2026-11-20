# 多智能体委派运行时技术规格

## 目标与非目标

本规格定义 agent-compose 的实验性多智能体委派能力。一个 agent 可以通过 YAML 的 `export` 将自身声明为可调用端点，并通过 `use` 获得调用同一项目 revision 内其他已导出 agent 的能力。委派入口同时支持自然语言驱动的 prompt 和确定性编排的 JavaScript，且统一具备 revision 固定、递归预算、取消、安全鉴权、持久化和可观测性。

首版目标如下：

- 在 compose schema 中为 agent 增加 `export` 与 `use`，并纳入严格校验、规范化、canonical JSON、spec hash 和项目 revision 快照。
- 以 MCP 工具作为 provider 侧统一调用面，使 Codex、Claude、Gemini、OpenCode 和 Pi 等现有 provider 不需要感知内部调度实现。
- 将 prompt 与 JavaScript 两种入口归一为同一种 `DelegationCall` 生命周期和错误语义。
- 支持有限递归，包括自引用和环；通过深度、整棵调用树总调用数、每个调用者的直接子调用并发数以及取消传播控制资源。
- 所有后代固定使用根执行开始时的项目 revision，不受运行期间再次 apply 的影响。
- 将 agent 间授权限制在具体 sandbox、项目、revision、根执行上下文和 allowlist 内，并使用可撤销的短期随机令牌。
- 复用现有 run 事件和 child project run 提供可观测性，同时持久化完整调用树以支持审计和故障诊断。

首版明确不包含：

- 跨项目、跨 revision 或远程地址的 agent 引用。
- 对外提供 A2A Server、A2A Client、Agent Card 发现或远程 A2A 联邦。
- 一个 agent 声明多个具名 export，或为 export/use 目标配置别名。
- 面向用户的 DelegationCall Get/List RPC；调用记录先作为内部领域数据存在。
- 基于调用记录的自动重放、跨 daemon 重启续跑、人工审批或长期任务订阅。
- 将该实验性语法视为稳定兼容承诺；进入稳定阶段前仍可根据真实使用反馈调整字段和协议细节。

## 现状与约束

- `pkg/compose/spec.go` 对 YAML key 进行严格白名单校验。新增字段必须同时进入解析、验证和规范化路径，不能通过松散 map 绕过未知字段检查。
- 项目 revision 是 compose 规范化结果的不可变 canonical JSON 快照；运行时不能回读 `CurrentRevision` 来解析后代，否则 apply 会造成同一调用树内配置漂移。
- 当前 `runs.Coordinator.BeginRun` 总是使用项目的当前 revision。委派 prompt 入口需要显式传入已固定 revision 的运行路径，并继续由 `pkg/runs` 负责 project run 生命周期。
- scheduler 的 QuickJS 已支持 `main(payload)`、异步返回、`scheduler.agent()`、LLM、exec、shell、state、事件、取消和超时。JavaScript export 应复用这些能力，而不是引入第二套 JS runtime。
- scheduler 校验目前仅用 warning 表示缺少 `main()`。被导出的 JavaScript 是可调用契约，必须取得结构化的 `HasMain` 校验结果，不能依赖 warning 文本匹配。
- runtime 已能向各 provider 注入 MCP server 配置；委派能力应沿用该扩展面，并避免为每个 provider 增加专用多智能体协议。
- runtime LLM facade 已证明 daemon 可以向 sandbox 提供 HTTP 端点、保存令牌哈希并在生命周期结束时撤销令牌。委派端点应复用相同的网络可达性和密钥处理原则，但使用独立令牌及权限域。
- protobuf 是公开项目投影的来源；如公开 YAML 投影包含新字段，必须修改 `proto/agentcompose/v2/agentcompose.proto` 并重新生成，不能手工编辑生成文件。
- 新行为横跨 compose、projects、runs、scheduler、runtime proxy 和 storage，但领域规则不能落在 CLI、transport handler 或通用 helper 中。

## 关键设计

### 总体架构与职责

引入聚焦的 `pkg/delegations` 领域包。其一句话职责是：在固定项目 revision 和根执行上下文内，解析可委派端点并管理受策略约束的调用树。它拥有以下行为：

- 从规范化 revision 解析 export/use、目标 allowlist 和有效策略；
- 创建、验证、执行和终结 `DelegationCall`；
- 校验输入与输出 schema，维护树级调用预算和调用者级并发队列；
- 生成和验证 sandbox 委派凭证，绑定调用上下文并执行撤销；
- 传播取消、关联 project run、发布调用事件以及处理重启后的孤儿调用。

现有包继续保留各自职责：`pkg/runs` 创建和恢复 project run；scheduler 构建并执行 JavaScript 定义；runtime/provider adapter 生成 provider 配置；proxy/API 边界承载 Streamable HTTP MCP；storage 实现调用和令牌持久化；`pkg/agentcompose/app` 只协调这些组件的生命周期。`pkg/delegations` 不依赖 `pkg/agentcompose/*`、protobuf 或具体 HTTP 类型。

一次调用的主路径为：

```text
root project run / scheduler execution
  -> provider sandbox（自动注入 agent_compose_delegation MCP server）
    -> delegate.<target> MCP tool
      -> runtime proxy（Bearer delegation token）
        -> delegations domain（revision、allowlist、schema、预算、队列）
          -> prompt: pinned-revision project run
          -> js: pinned-revision scheduler main(payload)
        <- persisted result / structured error / call events
      <- MCP text or structuredContent
```

### 导出端点模型

每个 agent 最多有一个匿名 export。端点身份由 `(project_id, revision, agent_name)` 唯一确定，`type` 决定执行入口：

- `prompt`：把经过 schema 校验的 JSON 输入以规范化文本交给目标 agent 的 provider run。
- `js`：调用目标 agent 已有 `scheduler.script` 中的 `main(payload)`，`payload` 为校验后的 JSON object。

export 是 revision 内的能力声明，不是远程服务地址，也不单独拥有生命周期。只有同一 revision 中显式 `use` 该目标的 agent 才能获得工具。目标必须存在、启用且已 export；缺少目标、引用未导出或禁用 agent 均在 apply/规范化阶段失败，而不是延迟到 provider 调用时才发现。

自引用与环是合法图结构，例如 `planner -> reviewer -> planner`。配置校验不做 DAG 限制，安全终止依赖运行时递归预算，因此任何调用都不能绕开同一根上下文创建新的预算。

### 递归策略

根 agent 的执行深度为 `0`。从 provider 通过 MCP 创建一个目标调用时，目标深度为调用者深度加一。根执行的 `use` 策略确定整棵树的有效上限：

- `max_depth`：允许创建的最大目标深度，默认 `2`。
- `max_calls`：根上下文内累计获准创建的 DelegationCall 数，默认 `32`；失败、取消和被执行阶段拒绝的调用均占用名额，未通过鉴权而无法归属根上下文的 HTTP 请求不计入。
- `max_concurrency`：每个调用者同时处于 `running` 的直接子调用数，默认 `6`；不同调用者分别计数，等待项按服务端接受顺序 FIFO 排队。

daemon 另有不可由 YAML 提高的硬上限：`max_depth=8`、`max_calls=256`、`max_concurrency=32`。YAML 值必须为正整数且不得超过硬上限；默认值也由 delegations 领域在一个位置定义。

当一个带 `use` 的 agent 作为根启动时，使用它声明或默认的策略。当它作为后代运行时，继承根上下文的全部 policy；后代 YAML 中的 `max_depth`、`max_calls`、`max_concurrency` 不再求值，只有 `use.agents` 决定该后代可见的直接目标。因此同一 agent 独立作为根时使用自己的 policy，作为后代时不能重置、收紧或放宽根 policy。整棵树只有一个 root call counter 和取消域，根 `max_concurrency` 分别应用于树中每个 caller。

超出深度或总调用数的请求创建状态为 `rejected` 的调用记录并立即返回稳定策略错误。超过直接子调用并发数的请求进入 `pending` 队列，不占运行槽；它在取得槽位前仍受根取消和调用方断开影响。槽位只由 `running` 调用占用，并在任何终态释放。

JavaScript export 本身位于当前 DelegationCall 的目标深度。其内部 `scheduler.agent()` 运行 export 所属 agent 时保持当前委派深度，不额外消耗一次 DelegationCall；该 provider 随后发出的 MCP 委派才增加深度并占用 root call budget。所有 scheduler 分支必须显式携带同一个 delegation context，禁止通过构造新 scheduler 执行来重置预算。

### Revision 一致性

根 provider 准备阶段固定 `project_id` 和 `revision`，并把二者写入根执行上下文和凭证绑定。所有目标解析、export/use 读取、scheduler script 构建以及 prompt project run 创建均只读取这份不可变 revision。

运行期间 apply 新配置只改变未来根执行使用的 revision，不改变既有 MCP `tools/list`、目标行为或后代创建。即使 agent 名称在新 revision 中被删除或重新导出，既有树仍按旧 revision 执行。所需 revision 若因数据损坏不可读取，调用应以稳定的 revision 错误失败，不能退回当前 revision。

等价的 YAML 简写和完整写法必须规范化为相同结构并产生相同 spec hash。未使用 `export`/`use` 的历史项目不应因新增零值字段改变 canonical JSON 或 spec hash。

### 安全模型

provider 启动前为每个具备 `use` 的 sandbox 生成独立的高熵随机 delegation token。明文只进入该 sandbox 的 MCP server header 配置；daemon 仅持久化密码学哈希和非敏感 fingerprint。令牌至少绑定：

- sandbox ID；
- project ID 和固定 revision；
- caller agent、caller/root execution kind 与 ID；
- 当前 delegation context；
- 该 caller 在 revision 中声明的目标 allowlist；
- 创建、失效和撤销时间。

MCP 请求必须同时满足路径 sandbox ID、Bearer token、活跃 sandbox、根上下文和 allowlist。令牌不能用于 runtime LLM facade，也不能跨 sandbox、revision 或根执行复用。根 run 完成、取消，或 sandbox 停止时立即撤销；撤销和过期校验必须位于每次调用的鉴权路径，而非只在连接初始化时执行。

日志、事件、错误和持久化记录不得包含 token、Authorization header 或 provider 配置中的秘密。鉴权失败在 HTTP/MCP 边界返回通用错误并写安全审计日志；在无法可靠确认根身份时不创建伪造的 DelegationCall。

当 agent 有 `use` 时，provider 启动前必须得到 sandbox 可访问的 `AGENT_COMPOSE_RUNTIME_BASE_URL`。缺失或不可构造该地址属于准备失败，provider 不应在缺少委派工具的降级状态下启动。用户定义的 MCP server 不得占用保留名称 `agent_compose_delegation`；冲突应在配置合并时显式失败。

输入和输出只按 JSON 数据处理，JSON Schema 远程引用被禁止；JavaScript 沿用现有 sandbox/runtime 权限，不因 export 获得额外主机权限。持久化的输入输出遵循现有敏感数据和容量治理，首版不把任意调用内容写入日志。

### A2A 决策

首版不直接使用 A2A 作为内部实现基础。A2A v1.0 的 Agent Card、Task、Message、Artifact、stream/subscribe/cancel 很适合跨系统 agent 互操作，但没有定义本项目需要的 YAML 依赖图、固定 revision、整树递归预算、sandbox 凭证绑定或现有 JavaScript scheduler 语义。当前所有 provider 的共同工具扩展面是 MCP；把内部领域对象绑定到 A2A transport 会增加适配层和协议生命周期耦合，却不能替代核心领域规则。

delegations 领域接口和持久化模型保持协议中立，未来可在独立 adapter 中进行如下映射：

| 内部概念 | A2A 对应概念 |
| --- | --- |
| exported agent endpoint | Agent Card / AgentSkill |
| DelegationCall ID | Task ID |
| root execution ID | context ID |
| JSON 调用输入 | Message `Part.data` |
| 结构化结果 | Artifact `Part.data` |
| 调用事件与取消 | streaming / subscription / cancel |
| input/output schema | 可选 A2A extension |

未来的外部 A2A 身份认证必须与 sandbox delegation token 分离；A2A adapter 只能调用协议中立领域接口，不能让 Agent Card 或 Task 成为内部事实来源。

## 接口与数据变化

### YAML 语法与规范化

推荐完整示例：

```yaml
agents:
  reviewer:
    description: Review code changes
    export: prompt

  workflow:
    description: Run deterministic review workflow
    scheduler:
      script: |
        function main(payload) {
          return scheduler.agent(
            `Review this input:\n${JSON.stringify(payload)}`
          );
        }
    export:
      type: js
      input_schema:
        type: object
        properties:
          files:
            type: array
            items: { type: string }
        required: [files]
        additionalProperties: false
      output_schema:
        type: object
        properties:
          summary: { type: string }
        required: [summary]
        additionalProperties: false

  coordinator:
    use:
      agents: [reviewer, workflow]
      max_depth: 2
      max_calls: 32
      max_concurrency: 6
```

`export` 支持标量简写和 mapping：

```yaml
export: prompt

# 等价规范化结果
export:
  type: prompt
```

字段语义：

- `type` 必填，枚举为 `prompt`、`js`。
- `input_schema` 可选；省略时使用 `type: object`、必填 string 字段 `prompt`、`additionalProperties: false` 的默认契约。
- `output_schema` 可选；省略表示输出为 text。
- 自定义 input schema 的根必须为 object；output schema 存在时根也必须为 object。
- schema 使用 JSON Schema Draft 2020-12。只允许文档内 `$defs` 和本地 fragment `$ref`；任何带 scheme、host、绝对路径或外部文档目标的 `$ref` 均在 apply 时拒绝。
- schema 自身必须合法；不支持的关键字不能被静默忽略。首版实现若只支持 Draft 2020-12 的受控子集，必须在 apply 时明确拒绝其余关键字，并在 YAML 文档中列出该子集。
- `type: js` 必须同时存在非空 `scheduler.script`，且结构化编译/解析结果确认全局可调用 `main`。`scheduler.enabled` 只控制自动 scheduler 触发，不阻止显式 export 调用。
- `type: prompt` 可以与 scheduler 配置共存，但委派时只运行目标 agent provider，不隐式触发其 scheduler。

`use` 同样支持简写和完整 mapping：

```yaml
use: [reviewer, workflow]

# 等价于使用默认策略
use:
  agents: [reviewer, workflow]
```

`agents` 的规范化结果是非空且唯一的 agent name 列表；重复项、未知 key、空名称、目标不存在、目标禁用或目标未 export 都是 apply 错误。目标顺序不影响语义或 spec hash，规范化时按稳定规则排序。自身名称可以出现，多个 agent 之间可以形成环。

`use` 缺失表示不注入委派 MCP server。存在 `use` 但 `agents` 为空没有有效意义，应拒绝，而不是启动一个空 server。所有 policy 值必须满足 YAML 上限和 daemon 硬上限。

### Prompt 与 JavaScript 输出契约

prompt export 在输入验证成功后，使用稳定 JSON canonicalization 生成一次 user message：

```text
Delegated input JSON:
<canonical-json>
```

目标 agent 的既有 description、prompt/provider 配置和 revision 内容照常生效。未声明 `output_schema` 时，目标 run 的最终文本作为 MCP text result。声明后，目标最终响应必须是一个完整 JSON object；runtime 对其解析并按 schema 校验，代码围栏、前后解释文本或部分 JSON 均不接受。成功时 canonical JSON 同时作为 text result 和 `structuredContent` 返回。

JavaScript export 直接以输入 object 调用 `main(payload)`。没有 output schema 时，string 返回值原样作为 text；其他可 JSON 序列化的返回值以 canonical JSON text 表示。存在 output schema 时，返回值必须是 object 并通过 schema 校验，然后以 canonical text 和 `structuredContent` 返回。`undefined`、函数、循环对象或其他不可序列化结果属于输出错误。

### MCP 端点与工具契约

每个 sandbox 的 provider 配置中自动注入一个 remote Streamable HTTP MCP server：

- server name：`agent_compose_delegation`；
- URL：`${AGENT_COMPOSE_RUNTIME_BASE_URL}/api/runtime/sandboxes/:sandbox_id/delegations/mcp`；
- header：`Authorization: Bearer <delegation-token>`；
- 工具名：每个 allowlisted target 对应 `delegate.<agent-name>`。

端点至少实现 MCP initialize、`tools/list` 和 `tools/call` 所需的 Streamable HTTP 行为，并尊重 request context 取消。`tools/list` 的 description 来自目标 agent description，`inputSchema` 为目标 export 的规范化 input schema。工具 arguments 必须是 JSON object，服务端不会信任 provider 侧 schema 校验。

成功结果包含：

- 无 output schema：一个 text content；
- 有 output schema：canonical JSON text content 和相同对象的 `structuredContent`；
- `_meta.agentCompose.delegationCallId`；
- 有关联 project run 时的 `_meta.agentCompose.projectRunId`。

执行层错误使用 MCP tool result 的 `isError: true`，并在 text content 中返回可机器解析的稳定对象；HTTP 鉴权、MCP framing 或 JSON-RPC 级错误仍使用对应协议错误层。稳定错误对象至少包含 `code`、安全的 `message`、`delegationCallId`（若已创建）以及可选的非敏感 `details`。首版错误 code 至少区分：

- `DELEGATION_INPUT_INVALID`
- `DELEGATION_TARGET_UNAVAILABLE`
- `DELEGATION_DEPTH_EXCEEDED`
- `DELEGATION_CALL_LIMIT_EXCEEDED`
- `DELEGATION_REVISION_UNAVAILABLE`
- `DELEGATION_EXECUTION_FAILED`
- `DELEGATION_OUTPUT_INVALID`
- `DELEGATION_CANCELED`
- `DELEGATION_INTERRUPTED`
- `DELEGATION_INTERNAL`

输入/schema、预算、目标、执行、输出和取消错误只要已通过身份认证并能归属根上下文，都必须对应一条持久化调用记录。内部错误对 provider 返回稳定概括，详细 cause 只进入受控诊断信息，不暴露存储、宿主路径或秘密。

### 持久化调用与令牌模型

所有 prompt 和 JS 调用统一持久化为 `DelegationCall`，最小字段如下：

| 字段 | 语义 |
| --- | --- |
| `id` | 全局唯一、不可猜测的调用 ID |
| `project_id`, `revision` | 固定项目及不可变 revision |
| `root_execution_kind`, `root_execution_id` | 根 project run 或 scheduler execution 身份 |
| `parent_call_id` | 直接父调用；根 agent 发起的首层调用为空 |
| `caller_run_id`, `caller_agent` | 发起工具调用的 run 与 agent |
| `target_agent`, `entry_type` | revision 内目标及 `prompt`/`js` |
| `depth` | 目标调用在根树中的绝对深度 |
| `status` | 调用生命周期状态 |
| `input`, `output` | 规范化 JSON 输入及成功输出；text 输出按明确类型存储 |
| `error` | 稳定 code、公开 message 和受控诊断字段 |
| `project_run_id` | prompt 入口或可关联执行产生的 project run，可空 |
| timestamps | created、started、finished、updated 时间 |

状态集合为 `pending`、`running`、`succeeded`、`failed`、`canceled`、`rejected`，允许的主转换为：

```text
pending -> running -> succeeded | failed | canceled
pending -> canceled | rejected
running -> failed | canceled
```

终态不可回退。所有状态变化、预算占用和并发槽释放必须在可恢复的一致性边界内完成，重复取消或重复完成应幂等，竞争只允许一个终态获胜。`rejected` 用于已经归属调用树但未获执行许可的输入、目标或预算问题；执行启动后的 schema/运行错误为 `failed`。

令牌记录与调用记录分开，至少保存 token hash、fingerprint、绑定上下文、allowlist、状态和生命周期时间。明文 token 不落库。一个 token 的撤销不删除历史调用；历史调用也不能用于重新生成 token。

首版不增加 public Get/List RPC。可观测性通过现有 parent run 事件、被创建的 child project run，以及 MCP result 中的 call/run ID 暴露。父事件至少表达 queued、started 和 terminal 状态，并携带 call ID、parent call ID、目标、深度、状态及安全错误 code，以便现有事件流重建树的关键阶段。

### Proto、canonical 与兼容性

公开项目投影需要表达规范化后的 export/use 和 policy；字段从 `.proto` 源定义并生成 Go/Connect 代码。未知 enum 必须在 transport mapper 中显式拒绝或按兼容规则处理，不能落为有效零值。内部 JSON Schema 可使用确定性的 JSON 表示，但 transport 类型不能泄漏进 compose/delegations 领域。

canonical JSON 必须包含显式配置及解析后的稳定默认值，使标量/完整形式等价、map key 与无语义顺序稳定。项目 revision 一旦创建不可就地重写。实验期若 schema 结构发生不兼容变化，应通过规范版本或清晰的 apply 错误处理旧格式，而不是静默改变已保存 revision 的含义。

公开 YAML 手册的英文和 `zh-CN` 版本必须同步说明语法、默认值、硬上限、递归、错误与安全边界；compose schema 变化后文档渲染必须继续通过 schema coverage 校验。

## 核心流程与失败语义

### 根 provider 准备与工具注入

1. 根执行固定 project revision，并从该 revision 读取 caller agent 的 `use`。
2. delegations 解析目标 export，计算根策略，创建 root delegation context；任何静态配置不一致均在 sandbox/provider 启动前失败。
3. 系统生成绑定 sandbox 与 root context 的 token，只保存哈希，并组装保留 MCP server 配置。
4. provider adapter 将该 server 与用户 MCP 配置合并；名称冲突、runtime base URL 缺失或 provider 不支持所需 remote MCP 配置时，准备失败且不启动一个能力残缺的 provider。
5. sandbox 结束或根执行进入终态时撤销 token，并取消仍在 pending/running 的后代。

根准备失败属于根 run/provider 错误，不创建虚假的子 DelegationCall。已生成但尚未交付的 token 也必须在失败清理路径撤销。

当 prompt child run 或 JS 内部的 `scheduler.agent()` 启动后代 provider 时，重复同一准备过程，但使用继承的 root context、当前深度和当前 DelegationCall 作为 parent；目标工具来自该后代 agent 自己的 `use.agents`。任何层级都不能从 daemon 当前项目配置重新派生 revision 或 policy。

### Prompt 调用

1. MCP 边界验证 token 和 sandbox 绑定，将协议请求映射为领域命令。
2. 领域在固定 revision 中重新确认 caller allowlist 和 target export，创建调用记录，并校验输入 schema、深度及整树调用数。
3. 调用等待 caller 的直接子并发槽；取消可在排队阶段终止它。
4. 获得槽位后转为 `running`，以显式 revision 创建目标 agent 的 project run，将 canonical input user message 作为本次委派输入，并保存关联 run ID。
5. run 成功后提取最终输出；有 output schema 时解析、校验并 canonicalize。事务性写入输出与 `succeeded` 后返回 MCP result。
6. run 失败、输出不合法或 context 取消时，分别落为 `failed`/`canceled`，保存稳定错误并返回 `isError`。

如果 parent MCP request 断开，其 request context 会触发该调用取消；取消继续传给 child project run。底层 run 的状态转换和资源清理由 runs 负责，delegations 只负责请求取消并使调用记录最终与可观测结果一致。

### JavaScript 调用

1. 鉴权、记录创建、schema、预算和排队步骤与 prompt 相同。
2. 从固定 revision 取得目标 scheduler 定义，按已有 QuickJS 构建路径确认 `main` 并以 JSON object 调用 `main(payload)`；DelegationCall ID 作为 correlation ID 注入 scheduler 事件/执行上下文。
3. scheduler timeout、取消、异步 rejection 和 runtime exception 映射为稳定执行错误；原始 exception 只保留经脱敏的诊断摘要。
4. `scheduler.agent()` 创建的 provider run 继承当前 delegation context、revision 和深度；它获得目标 agent 自身 `use.agents` 对应的工具，但不能创建新的根预算。
5. `main` 返回值按输出契约序列化和校验，随后终结统一调用记录。

`scheduler.enabled: false` 不影响上述显式路径。缺少 `main` 本应在 apply 阶段阻止该 revision；若持久化数据损坏导致运行期仍发生缺失，则调用以 `DELEGATION_EXECUTION_FAILED` 失败，不能回退到自动 scheduler 或 prompt 入口。

### 嵌套、取消与竞争

每个后代命令携带 root execution、parent call、caller run、固定 revision、绝对深度和共享预算身份。服务端只信任 token/持久化上下文推导出的值，不接受 MCP arguments 覆盖这些字段。

根取消会原子地关闭 root cancellation domain：停止接受新调用，取消所有 pending 调用，向 running 的 scheduler/project run 传播 context cancellation，并最终撤销全部相关 token。中间父调用取消时，同样递归取消其后代，但不影响树中不属于该父节点的兄弟分支。父调用只有在自身执行和所拥有的后代达到可终结状态后才发布最终事件，避免终态事件之后继续出现新子调用。

深度、总调用数和槽位必须支持并发原子决策。两个并发请求争用最后一个 call budget 时只能有一个获准；取消和完成争用时只能写入一个终态；FIFO 队列中的取消项不能阻塞后续项。实现不能在持有领域/存储锁时执行 provider、磁盘外 I/O 或 scheduler 等无界工作。

### 重启恢复

首版不自动重放 DelegationCall。daemon 启动时扫描没有活跃 owner 的非终态调用：孤儿 `running` 调用终结为 `failed`，错误为 `DELEGATION_INTERRUPTED`；尚未开始且无法恢复根上下文的 `pending` 调用终结为 `canceled`。相关 token 全部撤销。

底层 project run 的重启恢复仍由 `pkg/runs` 独立负责。delegations 可以依据关联 run 的最终事实补全诊断，但不能接管或重复创建 project run；一个已被标记 interrupted 的调用也不会因底层 run 后来恢复成功而重新变为 succeeded。此取舍保证终态单调，跨重启续接留待后续协议设计。

## 验收

### 聚焦自动化行为

- compose 测试证明 `export`/`use` 的简写与完整形式规范化等价，合法 schema 被保留，未知字段、远程 `$ref`、非 object 根、无 `main` 的 JS、空 use、无效目标和超硬上限策略被拒绝。
- canonical/revision 测试证明新字段参与 spec hash；等价 YAML 产生相同 hash；未使用新字段的历史 fixture 保持兼容；运行期间 apply 不改变已固定树的目标和脚本。
- delegations 领域测试覆盖 prompt/js 成功、输入与输出 schema 失败、目标失效、深度和整树 call budget、每 caller FIFO 并发、self/cycle、终态幂等、取消竞争和重启 orphan terminalization。
- 并发与取消测试使用可控同步点，不依赖脆弱 sleep；共享 budget、队列、token/revocation 和状态机相关测试执行 `go test -race`。
- token 测试证明只存哈希，sandbox/revision/root/allowlist 任一不匹配都拒绝，请求和诊断不泄漏 token，run/sandbox 终结后旧 token 无法调用。
- scheduler 测试证明导出 JS 要求结构化 `HasMain`，`scheduler.enabled: false` 仍可显式调用，async/timeout/cancel 正确映射，内部 `scheduler.agent()` 继承而不重置 delegation context。
- provider 配置测试覆盖所有受支持 provider 的远程 MCP 注入、保留名称冲突、缺少 runtime base URL，以及不带 `use` 时配置完全不变。

### 边界集成

- MCP 集成测试经真实 Streamable HTTP initialize、tools/list、tools/call 验证工具名、description、inputSchema、text/structuredContent、metadata 和稳定错误对象。
- runs/scheduler/storage 集成测试验证 prompt 显式 revision run、JS 固定 revision 构建、call 与 child run 关联、事件顺序、调用树 parent ID、取消传播和 daemon 重启恢复边界。
- transport/proto 测试验证公开项目投影的缺失值、enum、schema JSON 和兼容默认，不让 protobuf 类型进入领域层。
- runtime proxy 测试验证 sandbox 路由与鉴权先于领域调用，断连传播 context cancellation，HTTP/MCP 错误层次不会把执行错误误报为协议故障。

### 阶段质量门

实现完成前执行仓库现有质量门，不另造覆盖率标准：

```bash
task lint
task build
task test
task docs:build
```

`task test` 继续满足 `TESTING.md` 定义的 unit、integration、E2E shape 各 60% 及 combined 70% 门槛。compose schema 和公开文档变化必须通过 `task docs:build`。若变更跨越 deployment/runtime HTTP 拓扑，还应执行 `task test:deploy`；任何因环境缺失未运行的门禁必须明确记录命令与原因。

### 发布前真实链路验证

在 opt-in 环境使用一个真实 guest image 和至少一个实际 provider，验证 coordinator 通过自动 MCP 工具先后调用 prompt reviewer 与 JS workflow，并让其中一个目标再次递归调用子 agent。验证输出、call ID、child project run、事件树、apply 后 revision 固定、并发排队、根取消和 token 撤销均可观察。该验证不得依赖公网不稳定服务作为普通单元/集成测试前提，并按现有 E2E 分类隔离凭证与宿主能力。

首版只有在上述行为、边界和安全验收均成立时才可标记 experimental 可用；只完成 YAML 解析或单 provider happy path 不构成该特性完成。

## 假设与延后项

- 假设 agent 名称已满足可安全映射为 `delegate.<agent-name>` 的现有命名约束；若现有规则允许 MCP tool name 不支持的字符，应在 compose 规范化时增加可逆编码，并把映射规则作为协议的一部分，不能静默替换造成冲突。
- 假设现有 provider 均可配置带 Authorization header 的 Streamable HTTP MCP；若个别 provider 缺少该能力，应在该 provider 的准备阶段明确报 unsupported，不允许把 token 放进 URL。
- 默认 policy 与 daemon hard limit 是产品初始值；未来可增加 daemon 配置，但必须保持 hard limit 是部署方上限、YAML 是项目方请求值的关系。
- 调用输入输出的精确字节上限、保留期、加密和内容级脱敏沿用或扩展现有数据治理，待形成统一运行数据策略后单独规格化；这不改变首版“日志不记录正文/秘密、存储有界”的要求。
- 远程 A2A、跨项目引用、Agent Card 发现、长任务订阅、artifact 文件传递、公开 DelegationCall API、人工审批、重试/幂等键、断点续跑、调用级计费和动态 agent marketplace 均延后。
- 多 named exports 和 use alias 延后；首版以 agent 为能力单元，避免在 schema、工具命名、授权和未来 A2A AgentSkill 映射尚未稳定时扩大标识空间。
- experimental 期间必须为持久化 revision 保持可解释性；任何格式迁移都需要显式版本和 migration 设计，不能依赖重新 apply 来改写历史执行事实。
