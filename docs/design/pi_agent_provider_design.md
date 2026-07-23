# Pi agent provider 技术方案

## 1. 背景与结论

本文给出在 agent-compose 中增加 `pi` agent provider 的实现方案。这里的 provider 指负责执行 coding agent 的 provider，而不是 `pkg/llms` 中负责模型路由的 LLM provider。

调研基线：

- agent-compose：`origin/main`，提交 `5ba97ec3`（2026-07-22 拉取）。
- Pi：[`earendil-works/pi`](https://github.com/earendil-works/pi)，提交 [`bc41f612`](https://github.com/earendil-works/pi/commit/bc41f612da8c15c4acc5f7ab7a7178a4fe17c942)，发布版 `v0.81.1`。
- Pi npm 包：`@earendil-works/pi-coding-agent@0.81.1`，要求 Node.js `>=22.19.0`；当前 guest image 使用 Node.js 22，满足要求。

建议将规范 provider 名称定为 `pi`，兼容输入别名 `pi-agent` 和 `pi_agent`。首版使用 Pi 的 JSON event stream 模式，每个 agent-compose prompt turn 启动一个 `pi --mode json` 子进程，并用 Pi session ID 延续上下文。暂不以 RPC 模式作为首版执行通道。

推荐分两个发布级别：

1. 基础可用：prompt、模型路由、system prompt、skills、session resume、流式日志、取消、镜像和文档全部可用。
2. 完整可用：补齐 agent-compose 声明式 MCP server 到 Pi tool 的适配后，才宣称与 Codex/Claude/OpenCode provider 功能对等。

Pi 官方刻意不内置 MCP。不能在配置了 `mcp_servers` 时静默忽略；基础版本如果尚未交付 MCP adapter，必须在执行前返回清晰的 `failed precondition`。

## 2. 当前 agent provider 执行链

当前实现实际支持四种 provider：`codex`、`claude`、`gemini`、`opencode`。一次 agent run 的主要链路如下：

```text
AgentDefinition / compose agent
  -> model.NormalizeAgentDefinition（provider 校验和规范化）
  -> adapters.AgentRunner.ExecuteAgentRun
     -> system prompt / skills / MCP materialization
     -> runtimefacade.EnsureSessionLLMFacadeConfig
     -> BuildAgentExecSpec
  -> guest: agent-compose-runtime prompt
     -> buildPromptRuntimeOptions
     -> provider-specific Runner.runPrompt
     -> AgentResult JSON
  -> execution.ParseAgentExecResult
```

主要扩展面如下。

| 责任 | 当前位置 | 增加 Pi 所需改动 |
| --- | --- | --- |
| provider 规范化和持久化校验 | `pkg/model/agent_model.go` | 接受 `pi` 及别名 |
| compose 文档和规范化 | `pkg/compose`、`docs/pages` | 文档列出 Pi；schema 本身仍为 string，无 proto schema 变更 |
| guest runtime provider 类型和分派 | `runtime/javascript/src/types.ts`、`provider.ts`、`prompt.ts` | 增加 `PiRunner` |
| agent 进程和事件翻译 | `runtime/javascript/src/runners/*` | 新增 `runners/pi.ts` |
| LLM facade | `pkg/llms/runtimefacade`、`pkg/llms` | 解析 Pi 模型选择并写 `models.json` |
| skills 投影 | `pkg/execution/agent_files.go`、runtime options | 复用 `~/.agents/skills`，通过显式 `--skill` 限定本次 agent skills |
| MCP | `AgentRunner.prepareAgentMCPConfig`、guest runtime | 增加 Pi MCP extension；未实现前显式拒绝 |
| guest image | `guest-images/Dockerfile.agent-compose-guest` 等 | 固定版本安装 npm CLI，增加镜像断言 |
| CLI prompt attach | `cmd/agent-compose/cli_resource_reference.go` | 将 `pi` 加入非交互/交互支持矩阵（按实际完成能力） |

Pi 不应被实现成新的 Go domain package。agent provider 的进程协议归 guest JavaScript runtime 所有；LLM 路由和配置生成仍归 `pkg/llms` 所有。

## 3. Pi 能力与现有契约的映射

### 3.1 执行模式选择

Pi 提供 interactive、print/JSON、RPC 和 SDK 四种模式。首版选择 `--mode json`：

- 与 `OpenCodeRunner`、`GeminiRunner` 的“一次 turn 一个进程”模型一致。
- stdout 是 LF 分隔 JSON，第一条是带 `id` 的 session header，后续是稳定的 agent/message/tool 生命周期事件。
- 支持 `--session-id <id>`：session 不存在时创建，存在时恢复，正好映射 `stateRoot` 下的 provider thread state。
- 进程退出天然定义本次 turn 完成；context cancellation 可通过终止子进程实现。
- 不需要在 Go/runtime 层新增长生命周期 stdin command multiplexer。

RPC 模式保留为后续交互能力升级选项。只有需要 run 中途 `steer`、`follow_up`、结构化 `abort`、切换模型或 extension UI 请求/响应时，RPC 的复杂度才有收益。若未来启用 RPC，应新增有明确 owner、cancel、wait 的会话进程组件，不能在 `PiRunner` 内遗留无主后台进程。

### 3.2 建议命令行

概念上的执行命令为：

```text
pi --mode json
   --session-dir <stateRoot>/pi/sessions
   --session-id <stored-or-generated-thread-id>
   --model agent-compose/<resolved-model>
   --append-system-prompt <temporary-system-context-file>
   --no-extensions --no-skills --no-prompt-templates --no-themes
   --no-context-files --no-approve --offline
   [--skill <absolute-SKILL.md-or-directory>]...
   <promptText>
```

具体实现注意事项：

- 不通过 shell 拼接 Pi 参数，使用 `spawn("pi", args, ...)`，避免 prompt、路径和 secret 的转义问题。
- `--append-system-prompt` 的 source 可以是文件。runtime 应在 provider 私有临时目录写入 system context，以避免大 prompt 触发 argv 长度限制；退出时清理。
- 默认保留 Pi 内置 coding-agent system prompt，再 append agent-compose system context。这样 Pi 的内置工具用法仍完整。如果产品希望 agent definition 完全替换默认 prompt，可后续增加显式配置，首版不改变现有 system prompt 的“附加上下文”语义。
- `--no-context-files` 避免 Pi 再次发现 AGENTS.md/CLAUDE.md，导致与 agent-compose 已注入上下文重复或出现未经编排的隐式输入。
- `--no-extensions`、`--no-skills` 等先关闭用户全局发现，再用显式参数加载 agent definition 声明的资源，保证可重复性和租户隔离。
- `--no-approve` 与显式资源组合使用，不信任 workspace 内未声明的 `.pi` / `.agents` 配置。
- `--offline`（同时建议设置 `PI_OFFLINE=1`、`PI_SKIP_VERSION_CHECK=1`、`PI_TELEMETRY=0`）只禁止 Pi 启动时的更新、catalog 和 telemetry 网络请求，不阻断实际 LLM 请求。
- 设置 `PI_CODING_AGENT_DIR=<home>/.pi/agent`，不要让 Pi 读取镜像构建用户或错误 HOME 下的配置。

### 3.3 事件映射

`PiRunner` 逐行解析 JSON，不把无法解析的 stdout 当成成功内容；protocol corruption 应记录有限上下文并返回错误。建议映射如下：

| Pi 事件 | agent-compose 行为 |
| --- | --- |
| `session` | 获取并保存 `id` 为 `AgentResult.threadId` |
| `message_update` + `assistantMessageEvent.type=text_delta` | 把 `delta` 追加到 transcript |
| `message_end`（assistant） | 从 message content 提取最终文本，更新 `finalText` |
| `tool_execution_start` | 输出标准化的 `\n[tool:<toolName>]` |
| `tool_execution_update` | 默认不输出，防止大段重复 partial result；需要时做有界摘要 |
| `tool_execution_end` | 输出文本结果或有界 JSON；`isError=true` 记入 transcript，但由最终 agent/进程状态决定 run 是否失败 |
| `agent_end` | 将 stop reason 设为 `completed`，从 messages 兜底提取最后 assistant text |
| `auto_retry_*`、`compaction_*` | 输出简短状态行，不能混入 `finalText` |
| 子进程非零退出、`error` message/异常终止 | 返回带 provider 和 exit code 上下文的 error |

必须同时监听 child process 的 `error` 与 `close`/`exit`，有界收集 stderr，避免错误文本无限占用内存。stderr 可写入 transcript/stream，但 `AgentResult.stderr` 只保存截断后的诊断。解析测试要覆盖 delta 重复问题：`message_update.message` 通常包含累计 message，正文只能取 `assistantMessageEvent.delta`，不能每次重新追加整个 message。

### 3.4 session 和续跑

复用 `runtime/javascript/src/session-state.ts`：

- provider key 使用 `pi`。
- 首次执行前生成 64 位小写 SHA-256 形式的随机 ID 作为 `--session-id`，与 agent-compose 新资源 ID 的表示形式一致。Pi 原生只要求 ID 使用安全字符，并不要求 UUID；Pi 的 session header 必须返回同一个 ID，否则以返回值为准并更新 store。
- session 文件定向到 `<stateRoot>/pi/sessions`，而不是默认 `~/.pi/agent/sessions`，使状态生命周期跟随 sandbox state root。
- 只有进程成功完成且拿到合法 session ID 后才原子写 stored thread；失败不能覆盖上一个可恢复 ID。
- Pi session 绑定 cwd。agent-compose 恢复 sandbox 时 workspace guest path 应保持稳定；若未来允许 guest workspace path 改变，应新增兼容性测试。

Pi 自带自动 compaction。首版保留 Pi 默认值，但在文档中说明 session 上下文由 Pi 管理，与 agent-compose transcript 是两个不同的数据面：前者用于模型续跑，后者用于 run 日志展示。

## 4. LLM facade 设计

### 4.1 为什么需要生成 Pi `models.json`

Pi 是多模型 harness，不等价于某个固定协议。agent-compose 又要求 sandbox 内的 agent 通过短期 token 访问 runtime LLM facade，不能把上游 provider secret 直接注入 guest。因此新增：

- `pkg/llms/pi_facade.go`：解析模型选择、选择 LLM target、签发 facade token。
- `pkg/llms/pi_runtime_config.go`：纯配置生成逻辑，写 host sandbox home 下的 `.pi/agent/models.json`。
- `runtimefacade.EnsureSessionAgentRuntimeConfig` 增加 `case "pi"`。

不要复用名为 OpenCode 的函数或配置类型；两者只是都支持多 provider 模型，配置格式和协议能力并不相同。

### 4.2 模型字符串契约

建议 Pi 与 OpenCode 统一采用：

```text
<llm-provider-id>/<model-name>
```

示例：`openai/gpt-5.4`、`anthropic/claude-sonnet-4-6`、`my-openai-compatible/qwen3-coder`。

理由：只给模型名无法在同时配置 OpenAI-family 与 Anthropic-family provider 时可靠确定 facade 下行协议。解析规则：

1. 必须能拆成非空 `providerID/modelName`，否则 startup/execute 阶段返回验证错误。
2. 根据配置存储中的 provider ID 解析 provider family。
3. OpenAI family 优先使用 runtime facade Responses API；确实只支持 chat completions 的 target 使用 chat completions。
4. Anthropic family使用 Messages API。
5. session env provider 继续复用现有 bootstrap 规则，但最终仍生成明确 provider family 的 Pi model entry。

为了减少与现有 OpenCode 分支的重复，实施时可抽取一个以“已解析 provider ID + model name + target family”为输入的内部 resolver；不要创建泛化但无行为的 `common` package。

### 4.3 生成配置

每次 run 根据 resolved target 覆盖 agent-compose 管理的 `.pi/agent/models.json`。配置只暴露一个确定模型，示意如下：

```json
{
  "providers": {
    "agent-compose": {
      "baseUrl": "http://runtime/api/runtime/sandboxes/<id>/llm/openai/v1",
      "apiKey": "$AGENT_COMPOSE_SANDBOX_TOKEN",
      "api": "openai-responses",
      "models": [
        {
          "id": "gpt-5.4",
          "name": "gpt-5.4",
          "reasoning": true,
          "contextWindow": 128000,
          "maxTokens": 16384
        }
      ]
    }
  }
}
```

Anthropic target 改用 `api: "anthropic-messages"` 和 `/llm/anthropic/v1` base URL；OpenAI chat target 使用 Pi 支持的 `openai-completions` API 类型。实际 `contextWindow`、`maxTokens` 和 reasoning 能力应来自 agent-compose model catalog；缺失时使用 Pi 的兼容默认，不在多处硬编码猜测值。

配置文件权限使用 `0600`，目录 `0700`。文件内只引用环境变量，不写 token 明文。runner 的 `--model` 参数固定为 `agent-compose/<resolved model name>`，不能把原始 provider ID 继续传给 Pi，否则会绕过 facade config。

guest runtime 在启动 Pi 子进程前还会用同一组 run-scoped facade 环境变量将该受管 catalog 收敛到当前 guest home。这是旧 Docker sandbox 的兼容路径：早于 Pi provider 创建的容器可能没有 `/root/.pi` bind mount，host 侧文件对它不可见。catalog 不包含 token，只保留 `$AGENT_COMPOSE_SANDBOX_TOKEN` 引用；新的 sandbox 仍通过持久化 mount 使用 daemon 写入的同一配置。

签发的 facade token沿用现有 run scoped token，run 完成后由 `AgentRunner` 删除。生成环境至少包含：

- `AGENT_COMPOSE_SANDBOX_TOKEN`
- `LLM_API_ENDPOINT`、`LLM_API_KEY`、`LLM_API_PROTOCOL`
- Pi 配置根需要的 `PI_CODING_AGENT_DIR`
- 为兼容 Pi provider resolver，可同时提供对应的 `OPENAI_API_KEY` 或 `ANTHROPIC_API_KEY`，值仍为短期 token

## 5. Skills、上下文与 MCP

### 5.1 Skills

Pi 原生实现 Agent Skills standard，并扫描 `~/.agents/skills`。现有 `execution.WriteAgentSkills` 已把声明式 skills 投影到该目录，内容格式兼容。

为保证一个 agent 只能看到自己声明的 skill，runner 应把 `RunnerOptions.skills` 中的名称解析为 `<home>/.agents/skills/<name>/SKILL.md`（或目录），验证 real path 未逃逸 skills root，然后逐个传 `--skill`。同时使用 `--no-skills` 禁止额外发现。这样无需新增 Pi 专用 skill copy/link。

不要再像 Codex/Gemini 那样手工 append skill catalog；Pi 会从显式 skill source 生成自己的 catalog，双重注入会浪费上下文并可能产生冲突。

### 5.2 Context files

agent definition 的 `system_prompt`、MPI context 与 agent-compose 管理的 runtime context合成后作为 append system prompt file 传入 Pi。`--no-context-files` 禁止 Pi自行扫描 workspace 中的 AGENTS.md/CLAUDE.md；否则同一输入会因 workspace 内容和 trust settings 发生不可见变化。

### 5.3 MCP adapter

Pi 官方 README 明确列出 “No MCP”，推荐用 CLI tools/skills 或 extension 扩展。因此不能仅写一个 `.pi` 配置文件期望 Pi 自动加载 MCP。

完整方案是在 agent-compose runtime 包中维护一个最小 Pi extension：

```text
runtime/javascript/src/pi-mcp/
  extension.ts          # 注册动态 tools，管理生命周期
  config.ts             # 读取 stateRoot 下现有规范化 MCP JSON
  transports.ts         # stdio、streamable HTTP/SSE client boundary
  result.ts             # MCP content -> Pi tool result
```

设计约束：

- 复用 `execution.WriteAgentMCPConfigFile` 生成的 provider-neutral MCP 配置，避免再维护第三种 host-side MCP 配置格式。
- extension 作为 runtime 包内受版本控制的资产，通过显式 `--extension <absolute path>` 加载；仍保留 `--no-extensions`，不加载任何用户 extension。
- MCP SDK依赖固定版本加入 `runtime/javascript/package.json`，不要运行时下载安装第三方 extension。
- local server 支持 command、args、env；remote server 支持 URL、transport、headers。secret 只在子进程环境/请求 header 中存在，日志必须脱敏。
- tools 应在 Pi 发起第一轮模型调用前完成发现和注册。server 不可用时返回带 server name 的启动错误，而非空 tool set。
- 每个 spawned MCP process 有明确 owner；Pi run 完成、取消或初始化失败时全部 close/kill 并 wait。
- tool name 冲突采用稳定前缀或明确拒绝，不能依赖 map 遍历顺序覆盖。
- 对 tool input schema 做 JSON Schema 兼容转换；不支持的 schema keyword 必须给出 server/tool 上下文。
- 对 text/image/resource content 明确定义转换；首版不支持的 audio/resource link 返回结构化错误。
- tool output 和 stderr 有大小上限，与 runtime command 的输出限制一致。

若 MCP adapter 不在首个 PR 范围，`prepareAgentMCPConfig` 仍写通用配置，但 `PiRunner` 发现 `mcpConfig` 非空时直接报错：`pi provider does not support configured MCP servers in this build`。文档和 release notes 标为限制，不能忽略。

## 6. 代码改动清单

建议按以下文件边界实施，避免把 provider-specific 逻辑继续堆入大文件。

### 6.1 Domain、compose 与 API

- `pkg/model/agent_model.go`
  - `NormalizeAgentKind` 增加 `pi-agent`、`pi_agent` -> `pi`。
  - provider allowlist 增加 `pi`。
  - 表驱动测试覆盖规范名、别名和未知 provider 拒绝。
- `pkg/execution/agent_config.go`
  - Pi 保留 definition `model`；不要复制 OpenCode 的 `OPENCODE_MODEL` 特例。
- compose schema 无字段变化，不改 proto 生成文件。
- 检查 API/CLI 中硬编码的 provider 列表和错误文本，加入 `pi`。

### 6.2 Runtime runner

- 新增 `runtime/javascript/src/runners/pi.ts`，只负责 Pi 进程协议、临时文件与事件到 `AgentResult` 的映射。
- `types.ts`、`provider.ts`、`prompt.ts` 增加 `pi` 类型、别名与分派。
- 若 MCP 同期交付，新增聚焦的 `pi-mcp` 文件组；不要放入 `pi.ts`。
- 抽取已有 runner 共用的 bounded stderr/child exit helper 只有在至少两个真实 consumer 使用时进行；不要为本次变化先造空泛 abstraction。

### 6.3 LLM facade

- 新增 `pkg/llms/pi_facade.go`：模型字符串解析、target resolution、token 与 env。
- 新增 `pkg/llms/pi_runtime_config.go`：纯 payload 构建和原子落盘。
- `pkg/llms/runtimefacade/config.go` 只增加薄分派。
- 配置写入采用 temp file + rename，防止并发读取半文件；若同一 sandbox 允许并发 Pi run，还需按 sandbox/config path 加实例级锁。不能用 package global mutex。

### 6.4 Guest image 与供应链

- `guest-images/Dockerfile.agent-compose-guest` 增加 `ARG PI_AGENT_VERSION=0.81.1`，安装 `@earendil-works/pi-coding-agent@${PI_AGENT_VERSION}`。
- `Dockerfile.devbox-archlinux` 同步安装，确保开发镜像语义一致。
- 为 `/usr/bin/pi` 建显式 symlink 或断言 npm bin 已在 PATH，并执行 `pi --version` 构建时 smoke。
- 更新镜像 CI contract，断言版本 pin、CLI 存在且不使用 floating `latest`。
- Pi MIT license；发布镜像的 third-party notices/SBOM 流程如有清单需同步。
- Pi 包自身依赖较多，会增加 guest image。实施 PR 中记录安装前后 image size；如增幅不可接受，再评估 Pi release standalone binary。首版优先 npm 包，因为当前镜像已有 Node 22 且 npm shrinkwrap 固定了 Pi transitive dependencies。

### 6.5 文档

- 同步修改 `docs/pages/agent-compose-yaml-manual.md` 与 `docs/pages/zh-CN/agent-compose-yaml-manual.md`。
- 更新 `docs/pages/guest-image-abi.md` 及中文版本的 built-in provider/CLI 要求。
- 提供 Pi 模型例子，并明确格式是 `<llm-provider-id>/<model-name>`。
- 如果 MCP 未同期交付，公开记录限制。
- 运行 `task docs:build`，不直接修改 `build/pages`。

## 7. 测试计划和验收标准

### 7.1 Runtime unit tests

新增 `runtime/javascript/src/runners/pi.test.ts`：

- 命令参数、env、cwd、offline/trust/discovery flags 正确。
- 新 session 与 stored session 的 `--session-id` 行为。
- session header、text delta、message end、tool start/end、retry、compaction 的 event mapping。
- 累计 message 不造成文本重复。
- malformed JSON、无 session header、Pi error、spawn error、非零退出和 stderr 截断。
- system context temp file 和 cleanup（成功、失败、取消三条路径）。
- structured output 明确拒绝，除非另行实现可靠 schema extension。
- MCP 配置存在时：adapter 路径成功；或未交付 adapter 时明确拒绝。

### 7.2 Go unit/integration tests

- `pkg/model`：provider/alias normalize 与 validation。
- `pkg/llms`：Pi model split、OpenAI Responses、OpenAI chat、Anthropic Messages 配置；secret 不落盘；文件权限；可选配置错误。
- `pkg/llms/runtimefacade`：target、token、env、config path 和 protocol。
- `pkg/agentcompose/adapters`：Pi 经过 facade 和 MCP preparation，run scoped token 被清理。
- `cmd/agent-compose`：provider 支持列表与 prompt attach 行为。
- compose normalize/output round trip 保持 `provider: pi`。

### 7.3 Image/E2E

增加 guest image smoke：

1. `pi --version` 输出 pin 的版本。
2. 注入本地 fake OpenAI Responses server/facade，运行最小 `pi --mode json`，不访问公共网络。
3. 连续执行两个 prompt，验证相同 session ID 和上下文续跑。
4. 取消长请求后 Pi、MCP child（如有）均退出，无残留进程。
5. 配置一个 fixture MCP server，验证 tool discovery/call/result；MCP 未交付版本则验证 fail-fast。

实现阶段至少执行：

```bash
cd runtime/javascript && npm test
go test ./pkg/model ./pkg/llms/... ./pkg/agentcompose/adapters ./pkg/compose
task lint
task build
task test
task docs:build
task image:agent-compose-guest
```

镜像 smoke 属 opt-in 时，应在 PR 中精确报告命令和环境；不能用纯 parser unit test 替代真实 Pi CLI compatibility test。

验收标准：

- `provider: pi` 能从 compose/API 持久化、创建 sandbox、运行、继续同一 session 并展示最终文本。
- 上游 LLM credential 不进入 guest；Pi 只拿到 run scoped facade token。
- system prompt 和声明的 skills 生效，未声明的 project/global Pi 资源不加载。
- stdout JSON 事件不会重复正文，tool 日志可读，错误可分类。
- cancel 后所有子进程退出。
- 有 MCP 配置时完整工作或明确失败，不静默降级。
- amd64/arm64 官方 guest image 都含版本固定的 Pi CLI。

## 8. 分阶段落地建议

### PR 1：provider 主链路

- provider normalize/validation、PiRunner、JSON event mapping、session、system context、skills。
- Pi facade config（OpenAI Responses/chat + Anthropic Messages）。
- guest image、文档、unit/integration/image smoke。
- MCP 非空 fail-fast。

### PR 2：声明式 MCP adapter

- runtime-owned Pi extension、stdio/remote transports、schema/content mapping、cleanup。
- MCP unit/integration/E2E，移除 fail-fast 限制。

### PR 3（按需求）：交互式 RPC

- 仅在产品需要 run 中途 steering/follow-up 或 extension UI 时引入。
- 建立 session-scoped process owner，支持 structured abort、backpressure、daemon/sandbox shutdown wait。
- 不改变普通 prompt run 的 JSON mode，除非数据证明维护双通道的成本高于迁移收益。

## 9. 风险与决策记录

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| Pi 上游迭代快、JSON schema 演进 | runner 解析失效 | pin npm 版本；fixture + real CLI image smoke；对未知事件前向兼容，对必需字段严格校验 |
| Pi 不原生支持 MCP | 功能不对等 | runtime-owned extension；交付前 fail-fast，不静默忽略 |
| Pi 同时支持多 LLM family | 模型名无法决定协议 | 强制 `provider-id/model`，由 config store 决定 family |
| 隐式加载 workspace/global 配置 | 不可重复、越权 | `--no-*`、`--no-context-files`、`--no-approve`，只显式加载编排资源 |
| 更新检查/telemetry | 启动慢、非预期外网 | `--offline` + Pi 环境开关；镜像 smoke 断言无公共网络依赖 |
| session 文件与 transcript 双份状态 | 排障混淆、磁盘增长 | 分离职责、stateRoot 定向、沿用 sandbox retention；文档说明 |
| 并发 run 改写 models.json | 配置竞争 | 原子写；确认同 sandbox run 串行保证，否则实例级 keyed lock |
| npm 依赖增加镜像体积 | 拉取和启动成本 | 记录 size budget；必要时再评估上游 standalone release |
| structured output | Pi CLI 无等价 schema flag | 首版像 OpenCode/Gemini 一样明确拒绝；后续用受控 extension 实现，不用 prompt 伪约束冒充 schema guarantee |

最终推荐：先落地 JSON mode + facade + session + skills 的完整主链路，并把 MCP 非空 fail-fast 作为安全边界；随后用仓库内固定版本的 Pi extension 补齐 MCP。这个路径对当前架构改动最小，同时保留将来用 RPC 支持更强交互的空间。
