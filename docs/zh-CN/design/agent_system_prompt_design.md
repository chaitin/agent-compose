# Agent System Prompt — Phase 1 设计

**状态：** Phase 1 已在当前代码库中**实现**。截至 [验收标准（Phase 1，已验证）](#验收标准phase-1已验证) 的章节描述本次已落地的改动。[下一步计划](#下一步计划) 列出 Phase 1 **未包含**、计划后续推进的工作。

英文版本：[../../design/agent_system_prompt_design.md](../../design/agent_system_prompt_design.md)

相关文档：

- Runtime 调用契约：[agent-compose-runtime-js_contract.md](agent-compose-runtime-js_contract.md)

Phase 1 之前，`AgentDefinition.system_prompt` 已持久化、通过 API/Proto 暴露、并可在 Agents UI 中编辑，但执行路径从未读取它。只有 MPI（Model Program Interface）能力目录会进入 provider 的 system/developer instruction 通道。

Phase 1 通过分层 prompt 模型接入了 Agent Identity，未引入完整的平台级 runtime brief。

## 背景

### 改动前已有能力

| 层级 | 存储 / 来源 | 运行时行为（Phase 1 之前） |
| --- | --- | --- |
| Agent identity | `config_store` 中的 `AgentDefinition.system_prompt` | **未注入** |
| MPI catalog | Host 从 OctoBus capset 写入 `runtime/mpi/catalog.md` | 仅注入 Codex / Claude |
| Per-turn task | Host 写入 `state/agents/prompts/<provider>-<nano>.txt` | 通过 `--message-file` 传递 |

Phase 1 之前各 Provider 的注入方式：

| Provider | 机制 |
| --- | --- |
| Codex | `config.developer_instructions = mpiContext` |
| Claude | `systemPrompt: { preset: "claude_code", append: mpiContext }` |
| Gemini | 无 system context（MPI 被忽略） |

### Prompt 模型

agent-compose 将运行时指令分为三个可分离的层级：

1. **Platform context** — MPI 能力目录（已有）
2. **Agent identity** — 每个 agent 的 `system_prompt`
3. **Per-turn task** — 当前轮次的用户消息

Phase 1 将 **Agent Identity** 接入了已有的 MPI 平台层，未增加部署级 runtime brief、基于文件的 workspace 注入或 skills 发现。

## 目标与非目标

### Phase 1 范围（已交付）

- 使已配置的 `system_prompt` 对 Codex、Claude、Gemini 运行生效
- 保持每轮消息隔离（`--message-file` 仅承载任务文本）
- 当两层均存在时，Agent Identity 排在 MPI catalog **之前**
- 当 `system_prompt` 为空或未绑定 agent 时保持向后兼容
- Codex resume 时传递最新的组合 context（通过 constructor 级 config）
- 覆盖绑定 agent definition 的 loader 运行和 managed project 运行

### 延后项（见 [下一步计划](#下一步计划)）

- 将 `system_prompt` 重命名为 `instructions`
- Workspace 级全局 context 字段
- AGENTS.md / CLAUDE.md 标记块文件注入与清理
- Skills 列表或 skill 绑定的 prompt 段落
- 平台级 issue 工作流 brief（mentions、元数据语义、评论格式等）
- 前端改动（UI 已支持编辑 `system_prompt`）
- Proto 或 DB schema 变更（`system_prompt` 列已存在）
- 将 `description` 注入运行时指令

## Prompt 分层

Provider system / developer instructions 的组合结构（Phase 1 已实现）：

```text
┌──────────────────────────────────────────────────────────────┐
│ Provider system / developer instructions                     │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ ## Agent Identity                                      │  │
│  │ AgentDefinition.system_prompt (DB, per agent)          │  │
│  ├────────────────────────────────────────────────────────┤  │
│  │ ## MPI Catalog                                         │  │
│  │ OctoBus capset guides → runtime/mpi/catalog.md         │  │
│  └────────────────────────────────────────────────────────┘  │
├──────────────────────────────────────────────────────────────┤
│ Per-turn user message (--message-file)                       │
│ Chat text, loader script prompt, structured task input       │
└──────────────────────────────────────────────────────────────┘
```

规则：

- `system_prompt` trim 后为空时，省略 Agent Identity。
- 无可用 MPI catalog 时，省略 MPI。
- 存在 MPI 时，runtime 保留 `formatMpiContext` 生成的 MPI 字符串，包括既有 `## MPI Catalog` 标题。
- `description` 仅为目录元数据，**不得**出现在运行时指令中。

## 端到端数据流

```text
┌─────────────┐     claim / chat      ┌──────────────────┐
│ ConfigStore │◄──GetAgentDefinition──│ Service/Executor │
│ system_prompt│                      │                  │
└─────────────┘                       │ writeAgent       │
                                      │ SystemPromptFile │
                                      │ writeAgent       │
                                      │ PromptFile       │
                                      └────────┬─────────┘
                                               │ ExecStream
                                               ▼
                              ┌────────────────────────────────┐
                              │ guest: agent-compose-runtime   │
                              │   prompt                       │
                              │   --provider codex|claude|gemini│
                              │   --message-file …/prompts/…   │
                              │   --state-root /data/state     │
                              └────────┬───────────────────────┘
                                       │
                    agentSystemPromptPath(stateRoot) + readMpiContext
                                       │
                              buildSystemContext()
                                       │
              ┌────────────────────────┼────────────────────────┐
              ▼                        ▼                        ▼
         CodexRunner              ClaudeRunner            GeminiRunner
    developer_instructions    systemPrompt.append      prepend to -p
```

Guest 命令形态：

```sh
agent-compose-runtime prompt \
  --provider <provider> \
  --message-file /data/state/agents/prompts/<provider>-<unix_nano>.txt \
  --state-root /data/state \
  --workspace /workspace \
  --home /root
```

Guest runtime 从 `--state-root` 下的**固定约定路径**发现 agent identity（与 MPI 从
`runtime/mpi/catalog.md` 发现的方式一致）：

```text
host:  <session>/state/agents/system-prompts/system-prompt.txt
guest: /data/state/agents/system-prompts/system-prompt.txt
```

当 `system_prompt` 为空时，host **删除** `system-prompt.txt`，避免同 session 后续 run 读到过期的 identity 文本。

## Host（Go）实现

主要文件：`pkg/agentcompose/service.go`、`pkg/agentcompose/exec.go`、
`pkg/agentcompose/loader_manager.go`、`pkg/agentcompose/run_service.go`。

### 解析 agent system prompt

**函数：** `Executor.resolveAgentSystemPrompt(ctx, session, agentDefinitionID string) (string, error)`

解析顺序：

1. 若 `agentDefinitionID`（即 `ExecuteAgentRequest.AgentDefinitionID`）非空，直接加载该 agent definition。
2. 否则，若 session 带有 `source=agent` 和 `agent_id=<uuid>` 标签，按标签中的 agent id 加载。
3. 否则返回 `""`（非错误）。

DB 查找失败时，host 记录 warning 并在无 agent identity 的情况下继续运行（MPI-only）。不会因 definition 行缺失而失败整个 run。

### 写入 system prompt 文件

**函数：** `writeAgentSystemPromptFile(session, systemPrompt string) error`

| 属性 | 值 |
| --- | --- |
| Host 路径 | `{hostSessionDir}/state/agents/system-prompts/system-prompt.txt` |
| Guest 路径 | `{GuestStateRoot}/agents/system-prompts/system-prompt.txt`（`--state-root` 下的约定路径） |
| 内容 | UTF-8 原始 `systemPrompt` 字节（section 标题由 guest runtime 添加） |
| 非空 prompt | `MkdirAll` + 写入固定文件名 |
| 空 prompt | `os.Remove` 固定文件；忽略 `ENOENT` |

固定文件名与 provider 无关，避免 Go `normalizeAgentKind` 与 runtime provider 归一化耦合。发现方式与 MPI 一致，均通过 `--state-root` 下的约定路径。

### 扩展执行请求

`ExecuteAgentRequest` 新增 `AgentDefinitionID string`：

- **Loader 运行**（`loader_manager.go`）：来自解析后的 agent definition id，或 `loader.Summary.AgentID` 回退。
- **Managed project 运行**（`run_service.go`）：来自 `run.ManagedAgentID`。
- **Session chat 运行**：未显式传入 id 时，依赖 session 标签。

`buildAgentExecSpec` 仅传入 `--state-root`；guest 从该根路径下的约定路径发现 agent identity。

### 并发假设

固定文件名假设同 session 内 agent run 不会并发写入不同的 agent identity。当前 UI/API 路径按 cell 串行执行 chat。若未来需要并发不同 identity，再评估 per-run 文件名或 session 级锁。

### 错误处理摘要

| 条件 | 行为 |
| --- | --- |
| 存在 agent id 但 DB 行缺失 | Warning；无 agent identity 继续运行 |
| 写入/删除 system prompt 文件失败 | 失败（与 prompt 文件写入失败相同） |
| 空 system prompt | 删除 `system-prompt.txt` |
| Guest 上约定文件缺失 | `readSystemPromptFile` 返回 `""`；MPI-only |

## Guest Runtime（TypeScript）实现

主要文件：`runtime/javascript/src/system-context.ts`、`prompt.ts`、`cli.ts`、
`types.ts`，以及 `runners/` 下的 provider runner。

### 新模块：`system-context.ts`

```typescript
buildSystemContext(agentPrompt: string, mpiContext: string): string
readSystemPromptFile(path?: string): Promise<string>
```

组合逻辑：

- agent prompt 非空：输出 `## Agent Identity`、空行、trim 后的 agent 文本。
- agent prompt 与 MPI 均非空：追加 trim 后的 MPI context，并保留其 `## MPI Catalog` 标题。
- agent prompt 为空但 MPI 存在：原样返回 MPI，保持向后兼容。
- 两者均为空：返回 `""`。

`readSystemPromptFile` 在路径缺失、`ENOENT` 或 trim 后内容为空时返回 `""`。

### 约定路径与 prompt 命令

- `system-context.ts`：导出 `agentSystemPromptPath(stateRoot)` →
  `{stateRoot}/agents/system-prompts/system-prompt.txt`。
- `prompt.ts`：通过 `readSystemPromptFile` 读取约定路径、读取 MPI catalog、
  调用 `buildSystemContext`，将结果作为 `systemContext` 传给 runner。
- 通过 `--state-root` 发现（与 MPI catalog 约定路径一致）。

### RunnerOptions 变更

`RunnerOptions.mpiContext` 替换为 `systemContext`（组合后的字符串）。MPI 仍在 `prompt.ts` 内部读取用于组合；runner 不再单独接收原始 MPI。

## 各 Provider 注入方式

### Codex

组合 context 通过 Codex constructor 传递：

```typescript
new Codex({
  config: { developer_instructions: systemContext },
})
```

`@openai/codex-sdk` 在 constructor 作用域读取 `config`，而非 `ThreadOptions`。constructor `config` 在每次运行都会传入，但上游缺陷 [codex#19045](https://github.com/openai/codex/issues/19045) 意味着 **resume 时无法保证模型在首轮看到更新后的 `developer_instructions`**。

**Phase 2（已实现）：** guest runtime 对完整 `systemContext` 计算 SHA-256，并以 `systemContextHash` 存入 `codex.json`。仅当 `sessionId` 存在**且**哈希匹配时才 resume，否则调用 `startThread`。因 hash 不匹配而强制新建 thread 时会向 stderr 写入一条 warning。无 `systemContextHash` 的旧状态会先 resume 一次，随后补写 hash（lazy migration）。

### Claude

组合 context 追加到 Claude Code preset：

```typescript
systemPrompt: {
  type: "preset",
  preset: "claude_code",
  append: systemContext,
}
```

### Gemini

当前 runner 无原生 system-instruction 通道。Phase 1 落地了临时回退方案：

```typescript
const userPrompt = systemContext
  ? `${systemContext}\n\n${promptText}`
  : promptText;
```

子进程通过 `-p userPrompt` 调用。在出现原生 system 通道之前，identity 与 task 有意合并为单一 CLI 参数。

Phase 1 不改变 Gemini trust 或 permission 参数；这些内容不属于 system prompt 接线范围。

## 绑定场景

| 运行类型 | Agent identity 解析方式 |
| --- | --- |
| Agent session chat | Session 标签 `source=agent` + `agent_id` |
| Loader 脚本 `agent()` 调用 | Loader 绑定 agent 的 `AgentDefinitionID` |
| Managed project run (v2) | `run.ManagedAgentID` |
| 裸 provider 字符串，无 agent | 无 agent identity；有 catalog 时仅 MPI |

## 测试

### Go（`pkg/agentcompose/agent_system_prompt_test.go`）

- 空 `system_prompt` 解析为 `""`
- 带 session 标签的 agent 解析出 trim 后的 prompt 文本
- `writeAgentSystemPromptFile` 写入/删除固定 `system-prompt.txt`
- 空 prompt 删除文件（避免 stale identity）
- `buildAgentExecSpec` 传入 `--state-root` 供约定路径发现

### Runtime JS（`runtime/javascript/test/system-context.test.ts`）

- Section 顺序（Agent Identity 在 Capabilities 之前）
- 仅 agent、仅 MPI、两者皆有、两者皆无
- 仅 MPI 路径与改动前注入一致（无 `## Agent Identity`）
- `readSystemPromptFile` 的 trim 与缺失文件行为

Runner 测试（`runners.test.ts`、`runner-execution.test.ts`）已更新为使用 `systemContext` 而非 `mpiContext`。

## 安全与运维

- `system_prompt` 由 workspace 所有者控制（与现有 agent 管理 API 相同的信任边界）。不超出当前 agent 配置引入新的注入面。
- System prompt 文件与 per-turn prompt 文件同处 session state 树，受相同 session 生命周期与清理策略约束。
- 路径通过 exec spec 中的 `shellQuote` 传递；prompt 内容不参与 shell 插值。
- Phase 1 不引入硬性大小限制。遵循现有 prompt 文件的实际限制。

## 发布（Phase 1）

| 领域 | 变更 |
| --- | --- |
| 数据库 | 无 |
| API / Proto | 无 |
| Guest 镜像 | Runtime JS 改动已合并；生产 guest 镜像重建走常规发布流程。开发环境 mount 可能无需重建。 |
| 行为 | 非空 `system_prompt` 在部署后生效；为空时保持改动前 MPI-only 行为 |

## 文件变更映射（Phase 1）

| 文件 | 变更 |
| --- | --- |
| `pkg/agentcompose/service.go` | `resolveAgentSystemPrompt`、`writeAgentSystemPromptFile`、`executeAgentRun` |
| `pkg/agentcompose/exec.go` | `ExecuteAgentRequest` 新增 `AgentDefinitionID`；`Executor` 注入 `configDB` |
| `pkg/agentcompose/loader_manager.go` | 向 agent 执行传递 agent definition id |
| `pkg/agentcompose/run_service.go` | 向 agent 执行传递 `ManagedAgentID` |
| `pkg/agentcompose/agent_system_prompt_test.go` | **新增** — host 解析、固定路径写入/删除测试 |
| `runtime/javascript/src/system-context.ts` | **新增** — `agentSystemPromptPath`、组合与文件读取 |
| `runtime/javascript/src/prompt.ts` | 约定路径读取；runner 分发前组合 `systemContext` |
| `runtime/javascript/src/types.ts` | `RunnerOptions` 使用 `systemContext` |
| `runtime/javascript/src/runners/codex.ts` | `developer_instructions` 来自 `systemContext` |
| `runtime/javascript/src/runners/claude.ts` | `systemPrompt.append` 来自 `systemContext` |
| `runtime/javascript/src/runners/gemini.ts` | 将 `systemContext` prepend 到 `-p` |
| `runtime/javascript/test/system-context.test.ts` | **新增** — 组合单元测试 |
| `runtime/javascript/test/runners.test.ts` | 更新为 `systemContext` |
| `runtime/javascript/test/runner-execution.test.ts` | 更新为 `systemContext` |
| `docs/design/agent-compose-runtime-js_contract.md` | 记录约定路径与分层 |

## 验收标准（Phase 1，已验证）

1. `system_prompt: "Reply only in Chinese"` 的 agent 在 Codex/Claude chat 运行后应遵守该指令。
2. 空 `system_prompt` → 与改动前 MPI-only 行为一致。
3. `systemContext` 未变时 Codex session resume 保留连续性；变更时新建 thread（Phase 2）。
4. 绑定 agent definition 的 loader 继承 `system_prompt`。
5. `task test`、runtime JS 测试套件、以及 touched packages 的 `task lint` 均通过。

## 下一步计划

以下条目 **未在 Phase 1 中实现**，作为后续计划记录。

### 平台 runtime brief

在 Agent Identity 与 MPI 之上增加 workspace 或 deployment 级 brief 层，覆盖平台 guardrails、issue 工作流语义、以及与单个 agent definition 无关的评论格式规则。

### Workspace 全局 context

引入与 per-agent `system_prompt` 分离的 workspace 级 context 字段。

### 基于文件的 workspace 注入

向 `AGENTS.md`、`CLAUDE.md` 或类似 discovery 文件注入标记块，并在运行完成后安全清理。与 Codex/Claude 在 `local_directory` workspace 上的原生 discovery 对齐。

### System context 中的 Skills

发现 skill 摘要或按需加载 `SKILL.md` 段落，注入组合 brief（类似 Cursor Agent Skills）。

### Gemini 原生 system 通道

当 Gemini CLI 或 SDK 提供专用 system-instruction 参数时，替换 `-p` prepend 回退方案。在此之前，仅 Gemini 继续放宽 per-turn 消息隔离。

### 字段重命名：`system_prompt` → `instructions`

为 API 与 UI 语义更清晰而重命名。需要 Proto、DB 迁移、UI 与客户端更新。

### Prompt 变更时强制新建 Codex thread —— **已实现（Phase 2）**

当组合 `systemContext` 的 SHA-256 哈希与 `codex.json.systemContextHash` 不一致时，`CodexRunner` 改为调用 `startThread` 而非 `resumeThread`，以规避 [codex#19045](https://github.com/openai/codex/issues/19045)。详见 [issues/codex-start-thread-on-system-prompt-change.md](issues/codex-start-thread-on-system-prompt-change.md)。

### Prompt 文件软大小限制

文档化 `system_prompt` 与组合 system context 的软建议上限（如 < 8 KiB）。

### 前端呈现

Agents UI 已支持编辑 `system_prompt`。`docs/README.md` 已说明该字段运行时生效。可选后续：应用内提示或更完整的用户文档。
