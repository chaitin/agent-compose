# OpenCode CLI Provider 支持

英文版见：[../../design/opencode_cli_support.md](../../design/opencode_cli_support.md)

本文档记录把 `opencode` 接入为 guest agent provider 的实现方案。它遵循当前
[agent-compose-runtime_contract.md](agent-compose-runtime_contract.md)：
Go 控制面负责创建 sandbox 并在 guest 内执行统一 runtime 命令，
`runtime/javascript` 负责适配各 provider CLI。

## 当前代码结构

Provider 相关逻辑分在四层：

- Go 控制面：`pkg/model/agent_model.go` 的 `NormalizeAgentKind`、
  `NormalizeAgentDefinition`、
  loader 默认 agent 校验，以及 run/sandbox 编排当前会把 provider 字符串传给
  guest runtime。
- JavaScript runtime：`runtime/javascript/src/provider.ts` 归一化 provider
  alias，`runtime/javascript/src/prompt.ts` 选择 runner，
  `runtime/javascript/src/runners/` 存放 provider adapter。
- Guest 镜像：`guest-images/Dockerfile.agent-compose-guest` 安装 provider
  CLI，并创建 `/usr/bin/codex` 这类稳定可执行路径。
- UI 和文档：`frontend/src/pages/AgentsPage.svelte`、`README.md`、设计文档
  显示支持的 provider 列表。

`pkg/compose` 当前不限制 provider 名称；真正拒绝未知 provider 的位置在 daemon
侧 agent definition 校验。Proto 字段是 string，因此本变更不需要改 protobuf。

`model` 和 `system_prompt` 已经存在于 compose、v1、v2 和 store model 中。
OpenCode 集成会把它们透传到 `ExecuteAgentRequest` 和
`agent-compose-runtime prompt`；现有 Codex、Claude、Gemini runner 仍保持原行为，
除非后续显式消费这些字段。

## OpenCode CLI 事实

根据当前 OpenCode CLI 文档 <https://opencode.ai/docs/cli/>，和 agent-compose
相关的非交互命令是：

```sh
opencode run [message..]
```

需要使用或关注的 flags：

- `--format json`：输出原始 JSON events。
- `--session <id>` 和 `--continue`：续接 OpenCode 原生 session。
- `--model <provider/model>`：选择模型。
- `--agent <agent>`：选择 OpenCode agent profile。
- `--dir <path>`：指定工作目录。
- `--dangerously-skip-permissions`：自动批准未显式拒绝的权限。
- `--attach <url>`：连接已运行的 OpenCode server。首版不接入该模式，因为
  agent-compose 已经负责 sandbox 生命周期，不会为每个 sandbox 额外维护一个
  OpenCode server。

OpenCode 文档中的 npm 安装命令是 `npm install -g opencode-ai`。

## 目标行为

用户可以在 compose 中配置：

```yaml
agents:
  reviewer:
    provider: opencode
    model: anthropic/claude-sonnet-4-5
```

也可以在 UI/API 中创建 provider 为 `opencode` 的 agent definition。

执行 agent 时应当：

- 调用 `agent-compose-runtime prompt --provider opencode ...`；
- 在 `/workspace` 中运行 `opencode run`；
- 将 OpenCode provider thread id 保存到
  `/data/state/agents/providers/opencode.json` 并在后续运行中复用；
- 通过 stderr 持续输出可读 transcript，和现有 runners 保持一致；
- 通过 stdout 的 `__AGENT_RESULT__` 协议返回最终 `AgentResult` JSON；
- 当 `opencode` 非零退出或输出错误事件时，给出清晰失败信息。

## 实现概要

1. 增加 provider 归一化。

   两侧 provider normalizer 都支持 OpenCode：

   - `runtime/javascript/src/types.ts`：`Provider` 增加 `"opencode"`。
   - `runtime/javascript/src/provider.ts`：把 `opencode`、`open-code`、
     `open_code` 归一化为 `opencode`。
   - `pkg/model/agent_model.go`：更新 `NormalizeAgentKind`。
   - `pkg/model/agent_model.go`：provider 白名单允许
     `opencode`。

2. 增加 `OpenCodeRunner`。

   `runtime/javascript/src/runners/opencode.ts` 使用
   `spawn("opencode", args, ...)`，因为当前明确文档化的接入面是 CLI。

   首版命令形态：

   ```text
   opencode run <prompt> --format json --dir <workspace>
     --dangerously-skip-permissions
     [--model <model>]
     [--session <stored-thread-id>]
   ```

   实现要点：

   - 使用 `readStoredThread(stateRoot, "opencode")` 读取已有 provider thread；
   - 子进程环境里默认设置 `OPENCODE_DISABLE_AUTOUPDATE=true`，除非用户已显式
     设置；
   - 仅当 `RunnerOptions.model` 存在时传 `--model`；
   - 尽量按行解析 JSON events；非 JSON 行不丢弃，写入 transcript；
   - 从 `sessionID`、`sessionId`、`session_id` 等常见字段提取 provider 原生 session id；
   - 用 `extractText` 从 message/result 字段提取最终文本；
   - 只有拿到非空 provider 原生 session id 后才写 provider thread state。

3. 接入 runner 选择。

   - 在 `runtime/javascript/src/prompt.ts` 和 `runtime/javascript/src/index.ts`
     import/export `OpenCodeRunner`。
   - 更新 `runtime/javascript/src/cli.ts` 的 provider 帮助文本。

4. Agent model 和 system prompt 透传。

   OpenCode 需要 `model` 来选择目标 provider/model。host 显式透传这些字段，而不是
   依赖已保存的 metadata：

   - 执行配置 helper 会在 sandbox 有 agent tags 时，从 agent definition
     解析 provider、model、system prompt；
   - `ExecuteAgentRequest` 包含 `Model` 和 `SystemPrompt`；
   - `runProjectAgent` 会从 managed project agent definition 设置这些字段；
   - 通过 `SendAgentMessage` / `SendAgentMessageStream` 执行 v1 agent definition
     sandbox 兼容路径时，也要设置这些字段；
   - loader 绑定 agent definition 并调用 `scheduler.agent` 时，也会设置这些字段；
   - runtime CLI 支持 `--model`、`--system-prompt-file`；
   - `PromptCommandOptions` 和 `RunnerOptions` 包含 `model?: string` 和
     `systemPrompt?: string`。

5. 在 guest 镜像中安装 CLI。

   `guest-images/Dockerfile.agent-compose-guest`：

   - 全局 npm install 增加 `opencode-ai`；
   - 链接 executable 到 `/usr/bin/opencode`；
   - 创建 `/root/.opencode`。

6. 更新 UI 和文档。

   - `frontend/src/pages/AgentsPage.svelte` 和 `frontend/src/api/agents.ts`
     接受 `opencode`。
   - `README.md`、SDK 文档和 runtime contract 中的支持列表增加 OpenCode。
   - OpenCode 环境变量说明会指出：OpenCode 使用哪个 provider key 取决于其
     `model` 的 provider，因此文档应写常见情况，不写成单一固定 key。

7. 测试。

   Runtime JS 测试：

   - provider normalization 接受 `opencode`、`open-code`、`open_code`；
   - `OpenCodeRunner` 在新 provider thread 和续接 provider thread 下构造正确 args；
   - event 解析覆盖 transcript、final text、stderr、退出失败和 provider 原生 session id。

   Go 测试：

   - `normalizeAgentKind` 映射 alias；
   - agent definition 校验接受 `opencode`；
   - loader/default-agent 路径接受归一化后的 `opencode`；
   - service API 测试覆盖 `open-code` alias 归一化为 `opencode`。

   镜像验证应在具备镜像构建条件时覆盖 `task image:agent-compose-guest` 和容器内
   `opencode --help` smoke check。

## Structured Output

当前 runtime 对 Codex 和 Claude 支持 structured JSON output，但 Gemini 会拒绝
`outputSchema`。OpenCode CLI 文档中的 `--format json` 是原始事件格式，不是 strict
schema enforcement。因此首版应在 `opencode` runner 中遇到 `outputSchema` 时返回清晰
错误，除非实现时确认 OpenCode 已有文档化的 structured-output 合约。

如果后续需要 structured output，应作为独立变更加入 provider 专属合约和测试。

## 兼容性和迁移

不需要数据迁移。Provider thread state 按 provider 名称拆分，OpenCode 会使用新文件：

```text
/data/state/agents/providers/opencode.json
```

已有 `codex`、`claude`、`gemini` provider thread state 继续使用当前 state 文件和命令路径。

## 后续待确认

- 用真实模型后端确认 guest 镜像中实际安装的 `opencode-ai` 版本输出的 JSON event 形状。
- 确认 OpenCode 是否直接读取常见 provider API key 环境变量，还是需要在
  `/root/.opencode` 下准备默认配置文件。
- 另行决定现有 Codex/Claude/Gemini runners 是否消费透传后的
  `model` / `system_prompt`，还是仅作为 OpenCode runtime 选项使用。
