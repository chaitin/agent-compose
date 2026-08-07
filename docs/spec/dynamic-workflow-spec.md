# Dynamic Workflow Runtime 技术方案

## 背景与目标

agent-compose 当前已经具备 guest runtime、provider runner、runtime SDK、scheduler 和 LLM facade 等基础能力，但还没有 Claude Code 风格的 dynamic workflow 编排能力。现有脚本可以通过 `runtime.agent()` 主动调用单个 agent，也可以通过普通 Node.js 代码自行组合多个调用；这类组合缺少统一的 workflow 脚本形态、内置函数、运行时 phase、并发控制、失败语义、结构化结果、恢复缓存和进度协议。

本方案目标是在 guest runtime 内实现一套动态工作流能力：

- 用户或 workspace 脚本可以提供一段确定性 JavaScript workflow 脚本。
- workflow 脚本可以通过 `agent()`、`parallel()`、`pipeline()`、`phase()`、`log()`、`workflow()` 等内置函数编排多个子 agent。
- 子 agent 继续复用 agent-compose 已有 provider runners，包括 `codex`、`claude`、`gemini`、`opencode`、`pi`。
- workflow run 可以持久化运行状态，并在 `resumeRunId` 下复用已完成且输入 hash 匹配的子 agent 结果。
- workflow 可以通过 CLI 协议输出最终结果和流式进度事件，SDK 可以解析这些协议并暴露给调用方。
- `agent(..., { isolation: "worktree" })` 首版提供真实 git worktree 隔离，不做静默降级。

首版范围限定为 `runtime/javascript` 和 `runtime/agent-compose-runtime-sdk`。Go daemon、Connect proto、Web UI、QJS scheduler 不新增 workflow API。host 仍只看到外层 runtime 命令或 agent cell 的 stdout、stderr、output 和 artifacts。

## 现状和 harness 约束

### 项目 harness

`AGENTS.md` 定义 agent-compose 是 sandbox 控制面，主要入口包括：

- `cmd/agent-compose/main.go`：HTTP/Connect 服务启动、路由注册和 graceful shutdown。
- `pkg/agentcompose/app/`、`pkg/agentcompose/api/`、`pkg/agentcompose/adapters/`、`pkg/agentcompose/proxy/`：Go 控制面与代理层。
- `runtime/javascript`：guest-side runtime CLI。
- `runtime/agent-compose-runtime-sdk`：workspace Node.js 脚本使用的 SDK。

`AGENTS.md` 还明确 guest agent providers 仍是 guest 容器内的 CLI runners。dynamic workflow 首版必须遵守这个边界：workflow engine 调度 provider runner，但不改变 Go daemon 的服务图和 proto surface。

`TESTING.md` 要求项目按 unit、integration、E2E 三种 test shape 覆盖：

- unit tests 验证小模块、校验、序列化和错误路径。
- integration tests 验证组件协作、文件系统和本地依赖。
- E2E tests 验证完整用户可见 workflow。

`Taskfile.yml` 中与本方案直接相关的质量门禁包括：

```bash
cd runtime/javascript && TEST_SHAPE=unit npm run test:unit
cd runtime/javascript && TEST_SHAPE=integration npm run test:unit
cd runtime/javascript && TEST_SHAPE=e2e npm run test:unit
cd runtime/agent-compose-runtime-sdk && npm test
task test
```

`task test` 是项目总质量门禁，并由 `scripts/test-coverage.sh` 汇总覆盖率。

### 现有 runtime 能力

`runtime/javascript` 当前 CLI 暴露两个 host-dependent 子命令：

- `agent-compose-runtime prompt`
- `agent-compose-runtime exec`

`runtime/javascript/src/prompt.ts` 读取 message file、output schema file、state root、workspace、home、model，并分发到 provider runner。

现有 provider runner 状态：

- `CodexRunner` 使用 `@openai/codex-sdk`，支持 `outputSchema`、session resume、workspace、additional directories、danger-full-access sandbox。
- `ClaudeRunner` 使用 `@anthropic-ai/claude-agent-sdk`，支持 `outputFormat: { type: "json_schema" }`、session resume、system prompt、additional directories。
- `GeminiRunner` 支持普通 prompt，遇到 `outputSchema` 时返回 `structured JSON output is not supported by gemini runner`。
- `OpenCodeRunner` 支持普通 prompt 和 session resume，遇到 `outputSchema` 时返回 `structured JSON output is not supported by opencode runner`。

`runtime/agent-compose-runtime-sdk/src/agent.ts` 当前通过临时 message/schema 文件调用 `agent-compose-runtime prompt`，解析 stdout 中的 `__AGENT_RESULT__` 行，并在 SDK 侧解析 JSON。该 SDK API 不回调 Go host。

`docs/design/agent-compose-runtime_contract.md` 当前明确说明还没有：

- `workflow` 子命令。
- `__WORKFLOW_RESULT__` stdout 协议。
- scheduler 到 Node workflow 的专用 bridge token。
- 让 Node workflow 直接操作 scheduler state/event/artifact 的 context object。

本方案实现后需要同步更新该设计文档，避免文档继续描述“未实现”状态。

### 现有 scheduler 边界

`docs/design/agent-compose_design.md` 定义 scheduler runtime 使用 QJS，负责 trigger 注册、轻量 state、event publish，以及把需要 sandbox 能力的工作委托给 runtime sandbox。该层不是复杂 Node.js workflow、npm dependencies 或长耗时业务逻辑的承载点。

因此 dynamic workflow 首版不把 `scheduler` 扩展成完整 workflow engine。若 scheduler 要触发 workflow，应通过已有 `scheduler.exec` / `scheduler.shell` 运行 workspace Node.js 脚本，脚本再使用 `@chaitin-ai/agent-compose-runtime-sdk` 的 workflow API。

## 核心概念或领域模型

### Workflow Script

Workflow Script 是一段 JavaScript 源码。第一条语句必须是：

```js
export const meta = {
  name: "inspect_project",
  description: "Inspect a repository and summarize modules"
}
```

`meta` 是 workflow 的静态元数据，必须可在执行前通过 AST 字面量求值解析。支持字段：

```ts
type WorkflowMeta = {
  name: string
  description: string
  whenToUse?: string
  phases?: Array<{
    title: string
    detail?: string
    model?: string
  }>
}
```

`meta.phases` 是文档性 outline，不驱动实际进度。实际进度由运行时 `phase(title)` 调用产生。

### Workflow Run

Workflow Run 是一次 workflow 执行实例。它拥有：

- `runId`：当前这次物理执行的唯一 ID，显式传入或由 runtime 生成。
- `resumedFrom`：可选，只读的历史 run ID；当前 run 可以从中复用已完成的 agent 结果。
- `meta`：脚本元数据。
- `args`：调用方传入的 JSON 值。
- `status`：`running`、`completed`、`failed`、`aborted`；持久化内部还允许用 `interrupted` 标识进程异常退出后遗留的非终态 run。
- `phases`：运行中出现过的 phase 名称。
- `logs`：workflow 级 log 文本。
- `agents`：子 agent 调用记录。
- `result`：workflow 返回值。
- `startedAt`、`completedAt`、`durationMs`。

首版 Workflow Run 的所有权在 guest runtime 的 `stateRoot` 下，不写入 Go daemon 数据库。

### Agent Invocation

Agent Invocation 是 workflow 内一次 `agent(prompt, opts)` 调用。它拥有：

- `agentId`：当前 run 内递增的展示和事件 ID，不参与恢复缓存寻址。
- `invocationKey`：由 nested workflow、parallel branch、pipeline item/stage 和局部 agent key 组成的稳定逻辑路径。
- `label`：进度显示名称。
- `phase`：归属 phase。
- `prompt`：传给 provider runner 的 prompt。
- `options`：归一化后的 agent options。
- `inputHash`：由完整 prompt、归一化 options、schema、script hash 和 `invocationKey` 规范序列化后计算，用于 resume 缓存。
- `status`：`queued`、`running`、`done`、`error`、`cached`、`skipped`、`interrupted`。
- `result`：文本或结构化 JSON。
- `error`：失败信息。
- `providerSessionId`：provider 原生 session/thread ID。
- `worktreePath`：启用 worktree 隔离时的路径。
- `gitStatus`：隔离 worktree 完成后的 `git status --short` 摘要。

每个 Agent Invocation 使用独立 provider session root，避免并发子 agent 共享同一个 provider resume state 文件；system prompt、MPI、MCP 和 skills 等 runtime 配置仍从父 workflow 的 `stateRoot` 读取。两者的兼容规则见“Provider runner 复用”。

### Workflow Event

Workflow Event 是 runtime CLI 写到 stderr 的结构化进度事件，供 SDK `onUpdate` 消费。事件类型包括：

- `workflow_start`
- `phase`
- `log`
- `agent_start`
- `agent_end`
- `agent_cached`
- `workflow_complete`
- `workflow_error`

CLI stderr 可以同时包含 provider transcript 输出和 workflow event。SDK 只解析带 `__WORKFLOW_EVENT__` 前缀的行，其他 stderr 保留为 raw stderr。

### Isolation Worktree

Isolation Worktree 是子 agent 使用 `opts.isolation === "worktree"` 时创建的独立 git worktree。它用于并发修改型任务，避免多个子 agent 在同一 workspace 上互相覆盖。

首版 worktree 语义：

- 只支持 git workspace。
- 创建 detached worktree。
- 不自动 merge。
- 不自动删除有变更的 worktree。
- workflow result 中记录路径和状态，交给调用方后续处理。

### Nested Workflow

Nested Workflow 是 workflow 内调用另一个 workflow。首版只允许一层嵌套，用于复用较小的 workflow 模块。嵌套 workflow 共享父 workflow 的 concurrency limiter、budget 和 abort signal。

## 架构和组件边界

### runtime/javascript

`runtime/javascript` 新增 workflow engine 和 CLI command。建议新增模块边界：

- `src/workflow/parser.ts`：AST 解析、metadata 字面量求值、确定性校验。
- `src/workflow/runtime.ts`：VM 上下文、内置函数、并发控制、预算、abort 和最终结果。
- `src/workflow/state.ts`：run root、agent record、resume 缓存、snapshot 文件读写。
- `src/workflow/events.ts`：workflow event 类型和 stderr emitter。
- `src/workflow/worktree.ts`：git worktree 创建、状态读取、错误处理。
- `src/workflow/command.ts`：`runWorkflowCommand()`，连接 CLI 参数、文件读取、engine 执行和协议输出。

这些模块只属于 guest runtime，不依赖 Go 代码，也不依赖 scheduler QJS。

`src/cli.ts` 新增 `workflow` 子命令。现有 `prompt` 和 `exec` 命令保持兼容。

`src/constants.ts` 新增：

```ts
export const WORKFLOW_RESULT_PREFIX = "__WORKFLOW_RESULT__";
export const WORKFLOW_EVENT_PREFIX = "__WORKFLOW_EVENT__";
```

### Provider runner 复用

workflow engine 的 `agent()` 不直接调用 Codex/Claude SDK，而是复用现有 `runPromptCommand()` 或抽出可复用 runner factory。这样可以继续继承：

- MPI context 注入。
- system prompt convention。
- provider session persistence。
- provider transcript writer。
- output schema 处理。

为支持 workflow，需要扩展 `PromptCommandOptions` / `RunnerOptions`：

```ts
interface PromptCommandOptions {
  provider?: string
  messageFile?: string
  stateRoot?: string
  sessionRoot?: string
  workspace?: string
  home?: string
  model?: string
  effort?: string
  outputSchemaFile?: string
  systemContextPrefix?: string
}
```

`stateRoot` 保持现有含义，是 runtime 配置和默认 provider session 状态的根目录。`sessionRoot` 是新增的可选覆盖项，只改变 provider thread/session 的读写位置：

- 未传 `sessionRoot` 时，默认使用 `stateRoot`，现有 `prompt` CLI、SDK `agent()`、已部署 agent 和磁盘布局完全不变，不需要迁移。
- workflow 调用子 agent 时，`stateRoot` 仍指向父 runtime 的原目录，`sessionRoot` 显式指向 `<runRoot>/agents/<agentId>/state/`。
- system prompt、MPI context、MCP config 和 skills 从 `stateRoot` 读取；provider 原生 session/thread 从 `sessionRoot` 读取。
- 不复制、不移动、不链接已有 `stateRoot` 内容，也不为非 workflow 调用创建新的目录层级。

内部 runner options 应使用不同字段表达这两个所有权边界，避免 workflow 实现先用 `sessionRoot` 覆盖 `stateRoot` 后再调用现有配置加载逻辑。

`systemContextPrefix` 或等价机制用于注入 workflow phase、agentType、label、isolation 等子 agent 指令，不破坏已有 agent system prompt 和 MPI context 组合规则。

### runtime SDK

`runtime/agent-compose-runtime-sdk` 新增 `workflow.ts` 并导出：

- `workflow(script, options?)`
- `workflowFile(scriptPath, options?)`
- `RuntimeWorkflowOptions`
- `RuntimeWorkflowResult`
- `RuntimeWorkflowEvent`

`src/index.ts` 将新函数加入 named exports 和 `runtime` default object。现有 `exec`、`shell`、`agent`、`llm`、`report`、`ssh` API 不变。

### Go daemon 和 scheduler

Go daemon 不新增 API，不修改 proto。scheduler 首版不新增 `scheduler.workflow`。如果项目需要 scheduler 触发 workflow，推荐路径是：

```js
function main() {
  return scheduler.shell("node scripts/run-workflow.mjs", {
    sessionPolicy: "new"
  })
}
```

其中 `scripts/run-workflow.mjs` 使用 runtime SDK 调用 `runtime.workflowFile()`。

## API、CLI、配置、数据模型或协议变化

### CLI: agent-compose-runtime workflow

新增命令：

```bash
agent-compose-runtime workflow \
  --script-file <path> \
  [--args-file <path>] \
  [--state-root <path>] \
  [--workspace <path>] \
  [--home <path>] \
  [--provider <provider>] \
  [--model <model>] \
  [--concurrency <n>] \
  [--token-budget <n>] \
  [--run-id <id>] \
  [--resume-run-id <id>]
```

参数语义：

| 参数 | 语义 |
| --- | --- |
| `--script-file` | 必填，workflow JavaScript 文件路径。 |
| `--args-file` | 可选，包含 JSON 值，暴露为 workflow 全局 `args`。 |
| `--state-root` | 可选，默认沿用 runtime prompt 的 state root 默认值。 |
| `--workspace` | 可选，默认 `WORKSPACE` / `AGENT_COMPOSE_WORKSPACE` / session workspace。 |
| `--home` | 可选，默认 `HOME` / session home。 |
| `--provider` | 默认子 agent provider，默认 `codex`。 |
| `--model` | 默认子 agent model。 |
| `--concurrency` | 子 agent 并发上限，归一化到 `[1, 16]`。 |
| `--token-budget` | workflow budget total，未设置时为 `null`。 |
| `--run-id` | 当前新 run 的显式 ID；未设置时生成。已存在的 ID 拒绝执行。 |
| `--resume-run-id` | 只读的历史 run ID；当前新 run 从头 replay 脚本并复用匹配的 agent 结果。 |

`--run-id` 与 `--resume-run-id` 同时存在时，`resumeRunId` 表示读取哪个历史 run，`runId` 表示新 run 写入位置。若只传 `--resume-run-id`，runtime 仍生成新的 `runId`；历史 run 永远只读且不被原地恢复或覆盖。`runId === resumeRunId` 时拒绝执行。

这里的 resume 是“从头 replay workflow 脚本，并复用历史 run 中匹配的已完成 agent 结果”，不是恢复 JavaScript continuation 或调用栈。脚本的本地计算、`phase()` 和 `log()` 会重新执行。

### CLI stdout result protocol

workflow 成功或失败都应尽量输出可解析 payload。最终 stdout 行：

```text
__WORKFLOW_RESULT__{"runId":"run_...","status":"completed",...}
```

成功 payload：

```ts
type WorkflowResultPayload = {
  runId: string
  status: "completed"
  meta: WorkflowMeta
  result: unknown
  phases: string[]
  logs: string[]
  agents: WorkflowAgentRecord[]
  agentCount: number
  durationMs: number
}
```

失败 payload：

```ts
type WorkflowErrorPayload = {
  runId: string
  status: "failed" | "aborted"
  meta?: WorkflowMeta
  error: {
    message: string
    name?: string
    stack?: string
  }
  phases: string[]
  logs: string[]
  agents: WorkflowAgentRecord[]
  durationMs: number
}
```

如果在读取 script 或解析 meta 前失败，payload 可以没有 `meta`。如果进程级异常导致无法输出 result payload，SDK 返回“未发现 workflow result payload”的错误，保持与现有 `runtime.agent()` 对缺失 `__AGENT_RESULT__` 的处理一致。

### CLI stderr event protocol

每个 workflow event 写一行：

```text
__WORKFLOW_EVENT__{"type":"agent_start","runId":"run_...","agentId":"a1",...}
```

事件结构：

```ts
type RuntimeWorkflowEvent =
  | { type: "workflow_start"; runId: string; meta: WorkflowMeta }
  | { type: "phase"; runId: string; title: string }
  | { type: "log"; runId: string; message: string }
  | { type: "agent_start"; runId: string; agent: WorkflowAgentSummary }
  | { type: "agent_cached"; runId: string; agent: WorkflowAgentSummary }
  | { type: "agent_end"; runId: string; agent: WorkflowAgentSummary }
  | { type: "workflow_complete"; runId: string; status: "completed"; durationMs: number }
  | { type: "workflow_error"; runId: string; status: "failed" | "aborted"; message: string }
```

provider transcript 和普通 stderr 不加该前缀，SDK 保留到 `stderr` 字段。

### Workflow script globals

VM context 暴露：

```ts
declare function agent<T = string>(prompt: string, options?: WorkflowAgentOptions): Promise<T>
declare function parallel<T>(thunks: Array<() => Promise<T>>): Promise<Array<T | null>>
declare function pipeline<TItem, TResult>(
  items: TItem[],
  ...stages: Array<(previous: unknown, original: TItem, index: number) => TResult | Promise<TResult>>
): Promise<Array<TResult | null>>
declare function phase(title: string): void
declare function phase<T>(title: string, body: () => T | Promise<T>): Promise<T>
declare function log(message: unknown): void
declare function workflow<T = unknown>(nameOrRef: string | WorkflowRef, args?: unknown): Promise<T>
declare const args: unknown
declare const cwd: string
declare const process: { cwd(): string }
declare const budget: {
  total: number | null
  spent(): number
  remaining(): number
}
```

`WorkflowAgentOptions`：

```ts
type WorkflowAgentOptions = {
  key?: string
  label?: string
  phase?: string
  schema?: Record<string, unknown>
  provider?: "codex" | "claude" | "gemini" | "opencode" | "pi"
  model?: string
  effort?: "low" | "medium" | "high" | "xhigh" | "max"
  isolation?: "worktree"
  agentType?: string
  timeoutMs?: number
}
```

`WorkflowRef`：

```ts
type WorkflowRef = {
  script?: string
  scriptPath?: string
}
```

### Agent option 语义

`label` 用于进度、日志和 record。未设置时根据稳定的调用身份生成：显式 `key` 使用
`${phase} agent ${key}` 或 `agent ${key}`，否则使用上下文内的 agent 序号。并发分支的到达顺序不得改变默认
label，避免相同 `invocationKey` 在 replay 时产生不同的 system context 和 `inputHash`。

`key` 是当前结构上下文内可选的稳定逻辑名称，用于派生 `invocationKey`。它必须匹配 `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`，同一上下文内重复时报错。未设置时使用当前 parallel/pipeline/nested workflow 结构路径加局部递增序号；该序号在每个异步上下文内独立分配，不依赖并发任务的实际启动顺序。

`phase` 覆盖当前异步上下文的 phase。若未设置，agent 归属当前 scoped phase 或最近一次顶层 `phase(title)`。`phase(title, body)` 为 `body` 创建异步安全的局部 phase，并在结束后恢复父 phase；并发 thunk 内必须使用 scoped phase 或显式 `agent({ phase })`，裸 `phase(title)` 只用于 workflow 顶层顺序流程。

`schema` 是 plain JSON Schema object。传入后 agent 返回解析后的 JSON 值；未传时返回 final text string。

`provider` 覆盖 CLI 默认 provider。未设置时使用 workflow command 的 `--provider`，再默认 `codex`。

`model` 覆盖 CLI 默认 model。所有 provider runner 都应透传 model：

- Codex：`ThreadOptions.model`。
- Claude：query options `model`。
- Gemini：CLI 参数中加入 provider 支持的 model 参数；如当前 Gemini runner 未实现 model 参数，本方案要求补齐。
- OpenCode：已支持 `--model`。

`effort`：

- Codex：映射到 `ThreadOptions.modelReasoningEffort`。`max` 不在 Codex SDK 当前类型内，收到 `max` 时返回 unsupported error，避免静默降级。
- Claude：映射到 query options `effort`。
- Gemini/OpenCode：首版返回 unsupported error。

`agentType` 首版注入 system context，格式为：

```text
Workflow agent type: <agentType>
```

它不是 Claude native subagent registry 或 agent-compose agent definition registry 的稳定绑定。

`timeoutMs` 传给 prompt command 执行层。若底层 provider runner 没有原生 timeout，workflow engine 用 AbortController 或 child process timeout 包裹。

### SDK API

新增 named exports：

```ts
export { workflow, workflowFile } from "./workflow.js"
export type {
  RuntimeWorkflowOptions,
  RuntimeWorkflowResult,
  RuntimeWorkflowEvent,
  RuntimeWorkflowAgent,
  RuntimeWorkflowMeta
} from "./workflow.js"
```

`runtime` default object 新增：

```ts
runtime.workflow
runtime.workflowFile
```

SDK 类型：

```ts
type RuntimeWorkflowOptions = {
  args?: unknown
  provider?: "codex" | "claude" | "gemini" | "opencode" | "pi"
  model?: string
  concurrency?: number
  tokenBudget?: number
  runId?: string
  resumeRunId?: string
  stateRoot?: string
  workspace?: string
  home?: string
  timeoutMs?: number
  onUpdate?: (event: RuntimeWorkflowEvent) => void
}

type RuntimeWorkflowResult<T = unknown> = {
  runId: string
  status: "completed"
  meta: RuntimeWorkflowMeta
  result: T
  phases: string[]
  logs: string[]
  agents: RuntimeWorkflowAgent[]
  agentCount: number
  durationMs: number
  events: RuntimeWorkflowEvent[]
  stderr: string
}

type RuntimeWorkflowOutcome<T = unknown> =
  | RuntimeWorkflowResult<T>
  | RuntimeWorkflowFailed
  | RuntimeWorkflowAborted

type RuntimeWorkflowFailed = WorkflowErrorPayload & {
  status: "failed"
  events: RuntimeWorkflowEvent[]
  stderr: string
}

type RuntimeWorkflowAborted = WorkflowErrorPayload & {
  status: "aborted"
  events: RuntimeWorkflowEvent[]
  stderr: string
}
```

SDK behavior：

- `workflow(script, options)` 写临时 script file。
- 若 `options.args !== undefined`，写临时 args JSON file。
- 调用 `agent-compose-runtime workflow`。
- 使用增量 line decoder 从 stdout 解析唯一的 `__WORKFLOW_RESULT__`，不能依赖进程退出后的有界整段字符串捕获。
- 使用增量 line decoder 从 stderr 实时解析 `__WORKFLOW_EVENT__`，每收到一个完整事件就调用 `onUpdate`；普通 stderr 单独保留。
- 成功 payload 正常返回；合法的 `failed` 或 `aborted` payload 抛出 `RuntimeWorkflowError`，并在 `error.outcome` 中保留 run、agents、events 和 stderr。
- 缺失、重复或 JSON 无效的 result payload 抛出 `RuntimeWorkflowProtocolError`。
- 清理临时 script/args 文件。
- `timeoutMs` 到期后先发送 `SIGTERM`，给 CLI 短暂机会持久化并输出 aborted payload，再以 `SIGKILL` 兜底；没有合法 payload 时抛出 `RuntimeWorkflowTimeoutError`，并保留已收集的 events 和 stderr。

### 持久化数据模型

路径：

```text
<stateRoot>/workflows/runs/<runId>/run.json
<stateRoot>/workflows/runs/<runId>/events.jsonl
<stateRoot>/workflows/runs/<runId>/agents/<agentId>/record.json
<stateRoot>/workflows/runs/<runId>/agents/<agentId>/state/
<stateRoot>/workflows/runs/<runId>/nested/<nestedId>.json
<stateRoot>/workflows/worktrees/<runId>/<agentId>/
```

`run.json`：

```json
{
  "schemaVersion": 1,
  "runId": "run_...",
  "resumedFrom": "run_previous",
  "status": "completed",
  "meta": { "name": "inspect_project", "description": "..." },
  "argsHash": "sha256:...",
  "scriptHash": "sha256:...",
  "phases": ["Scan", "Analyze"],
  "logs": ["..."],
  "result": {},
  "agentCount": 3,
  "startedAt": "2026-07-10T00:00:00.000Z",
  "completedAt": "2026-07-10T00:00:10.000Z",
  "durationMs": 10000
}
```

`agents/<agentId>/record.json`：

```json
{
  "schemaVersion": 1,
  "agentId": "a1",
  "invocationKey": "root/parallel:0/key:repo-scan",
  "index": 1,
  "inputHash": "sha256:...",
  "label": "repo scan",
  "phase": "Scan",
  "provider": "codex",
  "model": "",
  "effort": "",
  "agentType": "",
  "isolation": "",
  "status": "done",
  "promptPreview": "Inspect the repository...",
  "result": "final text",
  "providerSessionId": "thread_...",
  "worktreePath": "",
  "gitStatus": "",
  "startedAt": "2026-07-10T00:00:00.000Z",
  "completedAt": "2026-07-10T00:00:05.000Z",
  "durationMs": 5000
}
```

持久化规则：

- `runId` 必须匹配 `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`，禁止路径分隔符、绝对路径和 `..` 路径段。
- run 目录以 exclusive create 声明所有权；目标 `runId` 已存在时拒绝启动，避免两个进程同时写同一个 run。
- `run.json` 和 agent record 使用同目录临时文件完整写入后再 rename；state 持久化失败是 workflow fatal error。
- `events.jsonl` 由单一 event writer 串行 append，并发 agent 不直接写文件，保证事件行不会交错。
- 读取历史 run 时，将残留的 `running` run/agent 解释为 `interrupted`；`interrupted` 记录不能作为缓存命中。
- `schemaVersion` 不受支持、重复 `invocationKey`、`done` 记录缺少可反序列化 result 等情况按历史状态损坏处理，不静默忽略。

完整 prompt 可以不写入 `agents/<agentId>/record.json`，避免泄露大段敏感上下文；`inputHash` 必须包含完整 prompt。若为了调试需要完整 prompt，应写到同目录 `prompt.txt`，并在文档中标注它属于 guest runtime state。

### 包版本和依赖

`runtime/javascript/package.json` 新增依赖：

```json
{
  "dependencies": {
    "acorn": "^8.x"
  }
}
```

`runtime/javascript/package-lock.json` 更新。

`runtime/javascript` 和 `runtime/agent-compose-runtime-sdk` package version 从当前 `0.8.0` 升到 `0.9.0`。`dist/` 不在 Git 跟踪文件内，不手工提交。

## 工作流和失败语义

### 脚本解析和确定性

workflow engine 使用 `acorn` 解析脚本。规则：

- 第一条语句必须是 `export const meta = ...`。
- `meta.name` 和 `meta.description` 是必填非空字符串。
- `meta` 只能包含 plain object、array、string/number/boolean/null literal、无插值 template literal、负数字面量。
- 禁止 object spread、array spread、sparse array、computed key、method/accessor。
- 禁止 `__proto__`、`constructor`、`prototype` key。
- 禁止脚本中出现可静态识别的 `Date.now()`、`Math.random()`、`new Date()`，包括 `Date["now"]()`、`Math["ran" + "dom"]()` 等静态字符串属性形式。
- 禁止 static 或 dynamic `import`、`require()`。

解析后，engine 删除 meta export，把剩余脚本包裹为 async function body 执行。

### VM 沙箱

VM context 暴露必要内置对象：

- `agent`、`parallel`、`pipeline`、`phase`、`log`、`workflow`。
- `args`、`cwd`、`process.cwd()`、`budget`。
- `JSON`、`Math`、`Array`、`Object`、`String`、`Number`、`Boolean`、`Set`、`Map`、`Promise`。

不暴露：

- Node `fs`、`child_process`、`net`、`http`、`fetch`。
- `require`、`import`。
- writable `process`。

该 VM 沙箱是确定性和 API 收口机制，不声明为强安全边界。workflow 脚本来源仍应是受信任用户或受信任 workspace。运行时移除 `Date` 和 `Math.random`，避免通过别名或间接调用绕过 AST 确定性检查。

### 并发控制

默认 concurrency：

```ts
Math.max(1, Math.min(options.concurrency ?? Math.max(1, cpuCount - 2), 16))
```

当 Node 无法可靠获取 CPU 数时，默认按 8 核处理，即默认 6，最大 16。

所有 `agent()` 和 nested workflow agent 共享同一个 limiter。`parallel()` 不绕过 limiter，只负责启动多个 thunk 并按输入顺序收集结果。

workflow runtime 使用 Node.js `AsyncLocalStorage` 传播当前 phase、nested workflow path、parallel branch、pipeline item/stage 和局部 agent 序号。`parallel()` 在启动 thunk 前按输入下标预分配 `parallel:<index>` 上下文，`pipeline()` 按 item/stage 预分配上下文，因此 `invocationKey` 不依赖 Promise 的实际调度和完成顺序。

单次 workflow run 最多允许 1000 次 agent invocation。超过上限报错：

```text
workflow agent limit exceeded: 1000
```

### parallel 和 pipeline

`parallel(thunks)`：

- 参数必须是 array。
- 每个元素必须是 function。
- 如果传入 promises，抛错提示使用 `() => agent(...)`。
- `agent()` 自身失败时 reject；`parallel()` 默认捕获非 abort 分支错误、记录 log，并在对应位置返回 `null`。
- abort 永远立即向上传播，不转换为 `null`。
- 结果数组顺序与输入 thunk 顺序一致。

`pipeline(items, ...stages)`：

- 第一个参数必须是 array。
- stage 必须是 function。
- 每个 item 内按 stage 顺序串行执行。
- 不同 item 之间并发执行，共享 limiter。
- 单个 item 任一 stage 发生非 abort 错误时，该 item 返回 `null`，后续 stage 不再执行；abort 立即向上传播。

首版组合器固定使用上述 settle 语义。需要关键步骤失败即终止 workflow 时，脚本应直接 `await agent()`，或在组合器返回后检查 `null` 并主动抛错；未来如增加 fail-fast 模式，应通过显式 option 引入，不能改变首版默认行为。

### budget

`budget.total` 来自 CLI `--token-budget` 或 SDK `tokenBudget`，未设置时为 `null`。

`budget.spent()` 首版使用估算：

```ts
Math.ceil(JSON.stringify(agentResult ?? "").length / 4)
```

估算值仅用于 workflow 内自我控制，不作为 billing 或精确 token 统计。若 `remaining() <= 0`，新的 `agent()` 调用报错：

```text
workflow token budget exhausted
```

### agent 执行

`agent()` 执行流程：

1. 校验 prompt 是非空 string。
2. 归一化 options。
3. 从当前异步结构上下文派生 `invocationKey`，再分配当前 run 的 `agentId`、label、phase。
4. 对完整 prompt、script hash、`invocationKey` 和最终归一化 options 使用 canonical JSON 序列化并计算 `inputHash`；`agentId` 不参与 hash。
5. 如果 resume cache 命中，返回 cached result 并发 `agent_cached` event。
6. 如果 `isolation: "worktree"`，创建 worktree 并切换 workspace。
7. 写 agent record 为 `running`。
8. 调用 provider runner。
9. 成功时写 result、provider session id、duration、git status，状态为 `done`。
10. 失败时写 error，状态为 `error` 并 reject；是否将非 abort 错误转换为 `null` 由 `parallel()` / `pipeline()` 决定。

非结构化 agent 返回 `finalText` string。结构化 agent 返回 JSON object。结构化 JSON 解析失败、provider 不支持 schema 或 option 不受支持均作为明确错误 reject，不在 `agent()` 内静默转换为 `null`。

### resume 缓存

resume 使用 `invocationKey` 定位历史逻辑调用，再以 `inputHash` 判断是否复用。`inputHash` 输入包括：

- workflow script hash。
- `invocationKey`，其中包含 nested workflow 和 parallel/pipeline 结构路径。
- agent prompt。
- normalized options。
- 最终 label 和 phase；两者会进入子 agent system context。
- schema JSON。
- default provider/model/effort。
- isolation mode。

对象使用 canonical JSON 序列化，避免对象 key 插入顺序造成无意义的 hash 差异。默认 provider/model/effort 必须先解析成 invocation 的最终值，再参与 hash。

命中条件：

- 历史记录 `invocationKey` 完全一致。
- 历史 agent record status 是 `done` 或 `cached`。
- `inputHash` 完全一致。
- result 字段存在且可 JSON 序列化。

`inputHash` 不匹配时是正常 cache miss，重新执行 agent，不视为 resume 错误。历史 run 中出现重复 `invocationKey`、无法解析的 record，或 `done` 记录缺少有效 result 时才报告缓存状态损坏。由于首版将完整 workflow script hash 纳入每次 invocation 的 `inputHash`，脚本发生任何变化都会使该脚本内的历史 agent 缓存 miss；仅 args 变化且没有影响最终 prompt/options 的调用仍可复用。该保守策略避免脚本语义变化后错误复用结果，同时避免并发启动顺序影响恢复结果。未来若需要局部脚本变更复用，应显式引入可审计的调用片段 hash，不能移除脚本身份校验后静默放宽。

### worktree 隔离

`isolation: "worktree"` 流程：

1. 在 workspace 内执行 `git rev-parse --show-toplevel`。
2. 如果失败，agent 返回错误：`workflow worktree isolation requires a git workspace`。
3. 创建目录 `<stateRoot>/workflows/worktrees/<runId>/`。
4. 执行 `git worktree add --detach <target> HEAD`。
5. 子 agent workspace 指向 `<target>`。
6. 完成后执行 `git -C <target> status --short`，写入 `gitStatus`。

worktree 清理策略：

- worktree 基于当前 `HEAD` 创建，不包含主 workspace 的未提交修改；该限制必须在 CLI/SDK 文档中明确。
- 没有变更且 agent 成功时，可以通过 `git worktree remove <target>` 删除 worktree，不能直接删除目录。
- 有变更或 agent 失败时必须保留 worktree，并在 result 中记录路径。
- 删除失败不使 workflow 失败，只写 log。
- cached record 引用 worktree 时，命中前必须验证路径位于受管 worktree root、仍是有效 linked worktree、HEAD 和 `gitStatus` 与记录一致；验证失败按 cache miss 处理。

首版不做自动 merge、patch 生成或 conflict 处理。

### nested workflow

`workflow(nameOrRef, args?)` 支持：

```js
await workflow({ script: "export const meta = ...\n..." }, { area: "api" })
await workflow({ scriptPath: "workflows/audit.js" }, { area: "api" })
await workflow("audit", { area: "api" })
await workflow("workflows/audit.js", { area: "api" })
```

字符串解析：

- 包含 `/`、`\` 或以 `.js` 结尾时，按路径解析。
- 相对路径以当前 workflow script 所在目录为第一优先级，再以 workspace 为 fallback。
- 普通名称先查 `${workspace}/.agent-compose/workflows/<name>.js`，再查 `${stateRoot}/workflows/library/<name>.js`。

首版最大嵌套深度为 1。超过时报错：

```text
nested workflow depth exceeded
```

子 workflow 共享父 workflow 的 limiter、budget、abort signal。每次 `workflow()` 调用在父异步结构上下文中预分配稳定 ordinal，路径形如 `workflow:<ordinal>:<meta.name>`；同名 workflow 的重复或并发调用因此具有不同且可重放的 `invocationKey`。子 workflow 的状态写入父 run root 的 `nested/<nestedId>.json`，记录 invocationKey、meta、args/script hash、状态、结果或错误以及持续时间。

### abort 和 timeout

CLI 进程收到 SIGINT/SIGTERM 时：

- workflow engine 标记 abort。
- 不再启动新 agent。
- 已在运行的 provider runner 尽力终止。
- running agent 标为 `skipped` 或 `error`，error message 为 aborted。
- 输出 `status: "aborted"` 的 `__WORKFLOW_RESULT__` payload。

SDK `timeoutMs` 到期：

- 先发送 `SIGTERM`，给 CLI 一个有界 grace period 持久化状态并输出 aborted payload，再以 `SIGKILL` 兜底。
- 如果收到合法 aborted payload，抛出带完整 `outcome` 的 `RuntimeWorkflowError`。
- 如果没有收到合法 payload，抛出 `RuntimeWorkflowTimeoutError`，错误中保留 collected events 和 raw stderr。

### 最终结果

workflow body 返回值必须 JSON 可序列化。若返回 Promise、function、symbol、包含循环引用等不可序列化值，报错：

```text
workflow result must be JSON-serializable; did you forget to await agent(), parallel(), or pipeline()?
```

## 测试、质量门禁和验收标准

### runtime/javascript unit tests

新增或扩展 unit tests，覆盖：

- parser 接受合法 static metadata。
- parser 拒绝非首条 meta、非 const meta、缺 name/description。
- parser 拒绝 spread、computed key、reserved key、getter/setter、sparse array。
- parser 拒绝 `Date.now()`、`Math.random()`、`new Date()` 及静态字符串属性变体。
- parser 允许 prompt 文本中出现 `Date.now()` 等字符串。
- VM context 暴露预期 globals，不暴露 `require`、`import`、`fs`。
- `phase()` 记录 runtime-created phases，不预渲染 meta phases；scoped phase 在并发分支间不串扰并在结束后恢复父 phase。
- `parallel()` 要求 thunk array，并保持结果顺序。
- `pipeline()` 对每个 item 串行执行 stages。
- `agent()` 失败 reject；`parallel()` / `pipeline()` 捕获非 abort 分支错误并在相应位置返回 `null`。
- parallel/pipeline 预分配的结构上下文在不同调度顺序下产生相同 `invocationKey`。
- workflow result 不可序列化时报错。
- budget spent/remaining 行为。
- agent limit 1000 行为。
- nested workflow depth limit。

### runtime/javascript integration tests

使用临时目录、fake provider runner 或 mock `runPromptCommand`，覆盖：

- `agent-compose-runtime workflow` 读取 `--script-file` 和 `--args-file`。
- stdout 输出单行 `__WORKFLOW_RESULT__`。
- stderr 输出 `__WORKFLOW_EVENT__` 并保留普通 transcript。
- resume run 命中 cached agent。
- resume `inputHash` 不匹配时重新执行，不误报缓存损坏。
- 历史 run 只读，只有 `resumeRunId` 时仍创建新的 `runId`。
- 每个 agent 使用独立 `sessionRoot`，同时从原 `stateRoot` 读取 system prompt、MPI、MCP 和 skills。
- 未传 `sessionRoot` 的现有 prompt/SDK 调用继续使用原 `stateRoot` 布局，不创建或复制新目录。
- `provider`、`model`、`schema` 传入 prompt command。
- Codex runner 透传 `modelReasoningEffort`。
- Claude runner 透传 `effort`。
- Gemini/OpenCode 收到 `effort` 或 `schema` 时保持明确 unsupported error。
- git worktree isolation 在临时 git repo 中创建真实 worktree，并记录 `gitStatus`。

### runtime/javascript E2E tests

在现有 mock SDK/CLI 风格下覆盖 production-like CLI：

- 一个 workflow 通过 `parallel()` 启动多个 fake agent，并由 synthesis agent 汇总。
- 一个 workflow 使用 `pipeline()` 对多个 item 逐阶段处理。
- 一个 workflow 使用 `workflow("name")` 调用本地 workflow library。
- 一个 workflow 启用 `isolation: "worktree"` 并产生文件修改，最终 result 包含 worktree path。

这些测试不依赖真实外部 LLM，不要求 Docker。

### runtime SDK tests

`runtime/agent-compose-runtime-sdk` 新增 tests，覆盖：

- `runtime.workflow(script, options)` 写临时 script/args 文件，调用 `agent-compose-runtime workflow`。
- CLI 参数包括 provider、model、concurrency、tokenBudget、runId、resumeRunId、stateRoot、workspace、home。
- `runtime.workflowFile(path, options)` 直接传 script path，不复制用户文件。
- 解析 `__WORKFLOW_RESULT__` 为 `RuntimeWorkflowResult`。
- 增量解析跨 chunk 的 `__WORKFLOW_EVENT__`，实时触发 `onUpdate`，并写入 `events`。
- 普通 stderr 保留到 `stderr`。
- 缺失、重复或无效 result payload 抛 `RuntimeWorkflowProtocolError`。
- failed/aborted payload 抛 `RuntimeWorkflowError` 并保留完整 outcome。
- timeout 先 SIGTERM 后 SIGKILL，抛出的错误保留 partial events 和 stderr。
- temp files 在成功和失败后都清理。
- named exports 和 default `runtime` object 都包含 workflow APIs。

### 文档验收

需要更新：

- `runtime/javascript/README.md`：新增 `workflow` CLI 使用和协议说明。
- `runtime/agent-compose-runtime-sdk/README.md`：新增 `runtime.workflow()`、`runtime.workflowFile()` 示例和类型说明。
- `docs/design/agent-compose-runtime_contract.md`：把“没有 workflow 子命令/协议”更新为新契约。
- `docs/design/agent-compose_design.md`：保留 scheduler QJS 不承载复杂 workflow 的边界，并说明推荐通过 runtime SDK 运行。

### 质量门禁

最小验收命令：

```bash
cd runtime/javascript && npm run typecheck
cd runtime/javascript && TEST_SHAPE=unit npm run test:unit
cd runtime/javascript && TEST_SHAPE=integration npm run test:unit
cd runtime/agent-compose-runtime-sdk && npm test
```

完整验收：

```bash
task test
```

涉及 guest image 发布时，还需验证：

```bash
task image:agent-compose-guest
```

## 首版不做事项

首版不做：

- Go `WorkflowService`。
- Connect proto 或 proto-client 变更。
- Web UI `/workflows` 管理器。
- scheduler `scheduler.workflow`。
- workflow phase/agent progress 写入 scheduler event 记录。
- host-side workflow run 查询、停止、恢复 API。
- worktree 自动 merge、自动 PR、自动 conflict resolution。
- 分布式 workflow 队列。
- 多层 nested workflow。
- 强安全沙箱声明。
- Gemini/OpenCode 结构化输出补齐。
- Claude native subagent registry / agent-compose agent definition registry 与 `agentType` 的稳定绑定。

## 关键假设和已确认决策

已确认决策：

- 交付范围是 Guest runtime + SDK 优先。
- 内置函数按公开核心能力加合理补齐：`agent`、`parallel`、`pipeline`、`phase`、`log`、`args`、`budget`、`workflow`。
- `agent(..., { isolation: "worktree" })` 必须创建真实 git worktree，不允许静默复用 workspace。
- 首版不改 Go proto/UI，不新增 host workflow manager。

关键假设：

- workflow 脚本由受信任用户或受信任 workspace 提供。
- VM 沙箱用于确定性和 API 收口，不作为强隔离安全边界。
- workflow 进度首版主要服务 SDK 调用方和 CLI 用户，host 只保留外层 stdout/stderr/output 可观测性。
- `agentType` 首版是 role hint，不是稳定 agent registry selector。
- `budget.spent()` 首版是估算，不代表真实 token billing。

## 关键设计依据

- Node.js `AsyncLocalStorage` 用于在异步调用链中传播 scoped phase 和 invocation path：<https://nodejs.org/api/async_context.html#class-asynclocalstorage>。
- Node.js VM 的同步 timeout 不能替代 workflow、provider 和 SDK 子进程的 abort/timeout 生命周期：<https://nodejs.org/api/vm.html#timeout-interactions-with-asynchronous-tasks-and-promises>。
- Temporal 区分应用层 workflow identity 与每次物理 run identity，本方案据此保持历史 run 只读并为 resume 创建新 run：<https://docs.temporal.io/workflow-execution/workflowid-runid>。
- Azure Durable Functions 将恢复描述为确定性 orchestrator replay；首版没有持久化 JavaScript continuation，因此明确限定为 replay 加 agent result memoization：<https://learn.microsoft.com/en-us/azure/azure-functions/durable/durable-functions-code-constraints>。
- Canonical JSON hash 采用 RFC 8785 的确定性对象属性排序原则：<https://www.rfc-editor.org/rfc/rfc8785>。
- worktree 的创建、删除和残留管理使用 Git 官方 `worktree add/remove/prune` 生命周期，不直接删除 linked worktree 目录：<https://git-scm.com/docs/git-worktree>。
