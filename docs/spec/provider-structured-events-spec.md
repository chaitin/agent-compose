# Provider 结构化执行事件规格

## 目标与非目标

agent-compose 应把 Codex、Claude Code、Gemini CLI、OpenCode 和 Pi 的机器可读执行流归一为稳定的 `AgentExecutionEvent v1`，贯通 guest runtime、Go host、流式 API、Run 事件投影和持久化。调用方可按统一语义观察消息、工具、文件修改、用量和终态，同时仍能使用 provider 独有能力。

首版必须兼容两类不可结构化输出：承载 `runtime.agent()` 的外层 JavaScript/shell 脚本 stdout/stderr，以及 agent 内部 shell/tool 产生的原始输出。现有 CLI 展示、transcript 和旧 guest image 不因升级失效。

首版不把“最终答案的 JSON Schema”混同于执行事件，不保证任意第三方脚本可产生结构化事件，不保存完整 provider 原生事件，也不公开隐藏思维链。

## 现状与约束

- 五个 provider 都有机器可读模式，但事件名称、生命周期、增量粒度和独有能力不同；当前 runner 已解析其中一部分，主要仍渲染为 stderr 文本（见 [`runtime/javascript/src/runners`](../../runtime/javascript/src/runners)）。
- one-shot runtime 通过 stdout 中的 `__AGENT_RESULT__` 返回结果，Go 侧据此解析（见 [`pkg/execution/parse.go`](../../pkg/execution/parse.go)）；脚本自身输出可能与控制帧混杂。
- interactive runtime 已有 `agent_event` 和 `agent_turn_completed` 帧，但不同 provider 的投影不一致，Run projector 还依赖 Codex 原生字段。
- 公共 API 已有 output、transcript 和通用 Run 事件，但没有稳定的 provider-neutral agent 事件。protobuf 只能修改源文件并重新生成，既有字段号与客户端行为必须兼容。
- 全量高频 delta 不适合写入 SQLite；Run 聚合、事件快照和 artifact 的数据所有权归 `pkg/runs`，provider 解码留在 runtime adapter 边界。

## 关键设计

### 规范事件

`AgentExecutionEvent v1` 包含：`event_id`、单 invocation 单调递增的 `event_seq`、`type`、`provider`、`provider_session_id`、`invocation_id`、`turn_id`、`item_id`、`parent_item_id`、`text`、`data_json`、`extensions_json`、`occurred_at`。ID 缺失时由 runtime 生成；消费者以 `(invocation_id, event_seq)` 排序和去重，不依赖跨 invocation 全局顺序。

公共类型为：

- `session.started`；同时声明本次实际可用的 capabilities，而不是静态 provider 猜测。
- `turn.started|completed|failed`、`message.started|delta|completed`、`reasoning.started|delta|completed`。
- `tool.started|updated|completed|failed`、`file_change.completed`、`plan.updated`、`usage.updated`。
- `retry.started|completed|failed`、`context_compaction.started|completed`、`warning`、`error`。

`data_json` 只承载各公共类型定义的 allowlist 字段。provider 独有事件使用 `provider.<provider>.<name>`，独有属性放入同名 allowlist 下的 `extensions_json`；未知字段不得透传。不得包含环境变量、凭证、请求头、未过滤路径内容或完整原生事件；reasoning 只转发 provider 明确公开的摘要。

### Provider adapter 与能力降级

每个 runner 将原生流转换为规范事件：Codex SDK `runStreamed()`、Claude SDK `SDKMessage`、Gemini `stream-json`、OpenCode JSON 和 Pi JSON 模式分别在所属 adapter 内解码。无法一一映射的能力进入命名空间事件；provider 不提供某生命周期时可省略该事件，不合成误导性细节。

单个未知原生事件被忽略并产生限流后的 `warning`；已知事件字段非法时产生 `error` 或 `warning`，不得泄漏原始 payload。provider 进程退出、取消和超时仍由既有执行终态决定，规范事件不能把失败伪装成成功。

### 原始输出兼容

- 外层脚本 stdout/stderr 继续使用 raw `output` frame，并增加 `stream`、`origin`、`encoding`、`truncated`；`origin=agent_script` 表示外层脚本，默认 UTF-8，非法字节可标记编码后安全传输。
- agent 内部 shell/tool 输出在 provider 可识别时进入 `tool.*` 的规范字段；超长或二进制内容按统一上限截断，完整原始内容仍进入既有 stdout/stderr 日志或独立 artifact，并由事件引用其路径/摘要。
- 官方 Runtime SDK 为嵌套 `runtime.agent()` 继承专用 event FD，以 NDJSON 传递控制帧；任意脚本 stdout 无法破坏 framing。绕过 SDK 的脚本保持 raw-only。
- `scheduler.agent()` 只在父 Run 中记录 child ProjectRun 的关联标识和生命周期，不复制子 Run 的完整事件流。

### 传输与兼容

host 设置 `AGENT_COMPOSE_EVENT_PROTOCOL=1` 协商新协议；新 runtime 在专用 FD 输出规范 NDJSON，旧 runtime 忽略该变量并继续输出 `__AGENT_RESULT__`。host 自动识别新帧或 legacy marker，因此 daemon 可运行旧 guest image；协议帧永不从普通 stdout 猜测。

CLI 和 transcript 默认继续渲染当前可读文本。新事件是附加能力，消费者未读取时不改变结果和终态；慢客户端不得阻塞 provider 消费或 artifact 写入，允许在实时投影层合并高频 delta，但持久化流不合并。

## 接口与数据变化

- 在 `proto/agentcompose/v2/agentcompose.proto` 新增 `AgentExecutionEvent`，并向 `StreamAgentRunResponse`、`StreamExecResponse`、`AttachAgentRunResponse`、`AttachExecResponse` 和 `RunEvent` 添加 typed event 字段；保留现有 output、generic agent event、turn completed 和 transcript 字段。
- output 增加 `origin` 及上述原始输出元数据；未知枚举按兼容默认值处理，不能静默解释成另一来源。
- Run detail 增加 `agent_events_path`；Runtime SDK 导出同一事件类型，并为 `agent()` 增加可选 `onEvent` 回调。回调异常只终止调用方自己的消费逻辑，其语义必须被 SDK 明确约定。
- 全量规范流按接收顺序追加到 `<run-artifacts>/agent-events.jsonl`；stdout/stderr 继续写既有日志。文件只包含 canonical allowlist 数据，不包含完整 provider payload。
- SQLite 复用现有 `project_run_event` 列，无 schema migration；仅保存 lifecycle、terminal 和可查询 snapshot，跳过 message/reasoning delta、tool progress 与 raw output。复用 `agent_activity`、`agent_message`，最终 assistant message 按稳定 item/event ID 去重。

## 核心流程与失败语义

1. runner 消费 provider 原生流，adapter 校验、脱敏并生成有序规范事件；runtime 同时保持最终结果与 raw output 通道。
2. SDK 将嵌套 agent 事件写入 event FD；Go host 解帧后先追加 canonical artifact，再投影到实时 API、Run 快照和默认文本展示。
3. terminal 事件关闭事件流，legacy 或新协议的最终结果完成既有 Run 状态转换；缺失 terminal 事件时以进程结果补充 `turn.failed`/`error`，但不改写 provider 已报告的事实。
4. artifact 创建失败时在启动 agent 前返回基础设施错误；执行中追加失败时标记结构化流不完整、发出持久化错误并按既有 Run 失败路径取消执行，避免把不完整 artifact 宣称为全量记录。
5. event FD 出现坏帧或序号回退时拒绝该帧并终止本次结构化协议；raw stdout/stderr 保留到日志，Run 以协议错误失败。客户端重连通过已持久化 Run 事件和 artifact 恢复，不要求内存队列重放全部 delta。

## 验收

### 核心行为

- 五个 provider 的代表性 fixture 均投影为同一公共生命周期，并分别覆盖至少一个独有事件/扩展、未知事件、非法字段和敏感字段过滤。
- 外层脚本并发写 stdout/stderr 时，结构化帧仍完整有序；旧 marker runtime、raw-only 脚本和现有 CLI/transcript 输出保持兼容。
- tool 文本、二进制、超长输出的 origin、encoding、截断和 artifact 引用可观察且一致；provider 原始 payload 与隐藏 reasoning 不出现在 API、SQLite 或 canonical artifact。
- artifact 包含全部已接收 canonical 事件；SQLite 只含快照类事件，assistant 最终消息不重复。写盘失败、坏帧、取消、超时和 provider 非零退出均有确定终态。
- protobuf 兼容性、attach/stream 投影、慢消费者隔离、事件排序去重和旧 guest image 协商均有自动化覆盖。

### 阶段门禁

实现完成后运行 `task lint`、`task build`、`task test`；protobuf 变更运行 `task generate:proto`，公共文档同步英文与 `zh-CN` 后运行 `task docs:build`。涉及并发队列或 goroutine 生命周期的包额外运行相关 `go test -race`。

### 发布验证

用固定版本的五个真实 provider 各执行一次消息、shell/tool、文件修改和失败场景，确认声明 capabilities 与实际事件一致；再以当前 daemon 搭配一个旧 guest image 验证 legacy fallback。

## 假设与延后项

- 首版面向内部 Beta；事件 schema 以版本字段演进，新增公共类型/字段必须向后兼容，删除或改义需要新版本。
- 默认不提供 raw provider-event passthrough；需要诊断时也只能增加受控、脱敏且显式启用的 artifact，另行设计保留期和权限。
- 跨 Run 的统一检索、事件 artifact 索引、完整 delta 的 API 重放、任意脚本结构化协议和所有 provider 的最终 JSON Schema 支持延后。
