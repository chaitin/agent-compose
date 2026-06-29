# agent-compose 与 agent-compose-runtime-js 调用规约

本文档描述 Go host 侧 `agent-compose` 与 sandbox 内 JavaScript runtime `agent-compose-runtime-js` 之间的调用边界。当前 runtime 主要用于 `AgentService`：host 在 sandbox 内执行统一入口命令，由 JS runtime 适配 Codex、Claude、Gemini，并把结构化结果回传给 host。

相关代码：

- host agent 调用：`pkg/agentcompose/service.go`
- host 执行与落库：`pkg/agentcompose/exec.go`
- runtime CLI 源码：`runtime/javascript/src/cli.ts`
- runtime provider 适配器：`runtime/javascript/src/runners/`
- guest SDK：`runtime/agent-compose-runtime-sdk/`
- guest 镜像安装：`guest-images/Dockerfile.agent-compose-guest`

## 1. 运行位置

`agent-compose` 是 host 侧 Go 服务，负责 session 生命周期、目录准备、runtime driver 调度、代理和持久化。

`agent-compose-runtime-js` 安装在 guest image 内。镜像构建时：

```text
COPY runtime/javascript /tmp/agent-compose-runtime
npm ci
npm install -g <packed runtime>
ln -sv ../lib/node_modules/agent-compose-runtime-js/dist/cli.js /usr/bin/agent-compose-runtime
```

host 侧实际调用的是 guest 内的：

```text
agent-compose-runtime
```

guest image 还会预置 `@chaitin-ai/agent-compose-runtime-sdk` tarball：

```text
/opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

## 2. 挂载与路径约定

session 创建后，host 生成 mount manifest，把 session 子目录逐项挂载到 guest 目标路径：

```text
host:  <SESSION_ROOT>/<session_id>/workspace
guest: /workspace
```

默认配置下：

```text
host:  ./data/agent-compose/sessions/<session_id>
```

因此以下路径一一对应：

| host 路径 | guest 路径 | 用途 |
|---|---|---|
| `<session>/workspace` | `/workspace` | 工作区，agent 的 cwd |
| `<session>/home/.codex` | `/root/.codex` | Codex 配置和状态 |
| `<session>/home/.claude` | `/root/.claude` | Claude 配置和状态 |
| `<session>/home/.claude.json` | `/root/.claude.json` | Claude root 配置 |
| `<session>/home/.gitconfig` | `/root/.gitconfig` | Git 配置 |
| `<session>/state` | `/data/state` | agent-compose 状态、cell artifact、agent prompt |
| `<session>/runtime` | `/data/runtime` | 运行期资源与扩展能力的预留目录 |
| `<session>/logs` | `/data/logs` | Jupyter 等日志 |

`boxlite`、`docker`、`microsandbox` 三个 driver 都消费 `<session>/vm/mount-manifest.json`，但 manifest 内容按 driver 生成。Docker 保留细粒度 home 子路径挂载，包括 `.claude.json` 和 `.gitconfig` file source；BoxLite 和 Microsandbox 只挂载目录 source，并通过 `<session> -> /data` 加 guest 侧 symlink 暴露 `/workspace` 和 `/root`，`/data/state`、`/data/runtime`、`/data/logs` 直接来自挂载目录。

## 3. Host 资源准备

### 3.1 session 目录

host 在 `Store.CreateSession` 阶段创建：

```text
<session>/
  context/
  home/
  runtime/
  workspace/
  state/
  logs/
  vm/
  proxy/
  metadata.json
  vm/runtime.json
  proxy/jupyter.json
  state/cells.json
  state/events.json
```

如果 session 绑定了 git workspace，host 会在启动 runtime 前把仓库 clone 到 `<session>/workspace`。

### 3.2 agent prompt 文件

发送 agent message 时，host 不通过 stdin 传 prompt，而是先写 prompt 文件：

```text
host:  <session>/state/agents/prompts/<provider>-<unix_nano>.txt
guest: /data/state/agents/prompts/<provider>-<unix_nano>.txt
```

guest 路径随后通过 `--message-file` 传给 JS runtime。

当 run 绑定到非空 `system_prompt` 的 agent definition 时，host 将 trim 后的文本写入固定约定路径：

```text
host:  <session>/state/agents/system-prompts/system-prompt.txt
guest: /data/state/agents/system-prompts/system-prompt.txt
```

Guest runtime 在 `prompt.ts` 中通过 `agentSystemPromptPath(stateRoot)` 读取该路径。文件缺失或为空时，`readSystemPromptFile` 返回 `""`，run 组合为 MPI-only context。当 `system_prompt` 变为空时，host 删除 `system-prompt.txt`，避免同 session 后续 run 读到过期 identity。

### 3.3 agent HOME 与初始配置

host 为 agent 执行设置：

```text
Cwd=/workspace
WORKSPACE=/workspace
STATE_ROOT=/data/state
RUNTIME_ROOT=/data/runtime
```

agent-compose 不再覆盖 `HOME`，guest 工具使用镜像默认 `HOME=/root`。默认 Codex/Claude/Git 配置由 host 在 session home 中初始化，并通过 mount manifest 挂到 `/root` 下对应路径。

## 4. 入口命令

host 通过 runtime driver 的 `ExecStream` 在 sandbox 内执行：

```sh
sh -lc 'set -e && cd /workspace && agent-compose-runtime prompt \
  --provider <provider> \
  --message-file /data/state/agents/prompts/<provider>-<unix_nano>.txt \
  --state-root /data/state \
  --workspace /workspace \
  --home /root'
```

JS runtime 支持两个子命令：

```text
prompt
exec
```

CLI 使用 `commander` 解析命令和参数。npm 包的 `bin` 入口为 `agent-compose-runtime-js`，guest image 额外创建 `agent-compose-runtime` 软链接指向编译后的 `dist/cli.js`。

命令参数：

| 参数 | 必填 | 说明 |
|---|---:|---|
| `--provider` | 是 | `codex`、`claude`、`gemini`、`opencode`，支持少量别名 |
| `--message-file` | 是 | prompt 文件路径 |
| `--state-root` | 否 | agent-compose runtime 状态根目录，默认 `/srv/agent-compose/session/state`。Guest 从此根路径按约定发现 agent identity（`agents/system-prompts/system-prompt.txt`）与 MPI catalog |
| `--workspace` | 否 | agent 工作目录，默认 `WORKSPACE` 或 `/workspace` |
| `--home` | 否 | agent HOME，默认 `HOME` 或 `/root` |
| `--model` | 否 | agent model；由支持显式模型选择的 provider 使用 |
| `--system-prompt-file` | 否 | system prompt 文件路径；当前由需要 prompt 级 system instructions 的 provider 使用 |

Agent identity 使用 §3.2 文档中的固定约定路径。

在 agent-compose session 中，host 总是显式传入 `--state-root`、`--workspace`、`--home`。

### 4.1 `exec` 子命令

loader script 通过 `scheduler.exec` / `scheduler.shell` 执行 runtime command 时，host 通过 runtime driver 的 `ExecStream` 在 sandbox 内执行：

```sh
sh -lc 'set -e && agent-compose-runtime exec \
  --request-file /data/state/cells/<cell_id>/command-request.json \
  --state-root /data/state \
  --workspace /workspace \
  --home /root'
```

命令参数：

| 参数 | 必填 | 说明 |
|---|---:|---|
| `--request-file` | 是 | runtime command request JSON 文件 |
| `--state-root` | 否 | agent-compose runtime 状态根目录 |
| `--workspace` | 否 | 默认工作目录 |
| `--home` | 否 | command HOME |

request JSON 示例：

```json
{
  "mode": "exec",
  "command": "python3",
  "args": ["-V"],
  "cwd": "/workspace",
  "env": {
    "FOO": "bar"
  },
  "timeoutMs": 30000,
  "maxOutputBytes": 1048576,
  "artifactDir": "/data/state/cells/<cell_id>"
}
```

shell request 示例：

```json
{
  "mode": "shell",
  "script": "set -e\necho hello\n",
  "cwd": "/workspace",
  "maxOutputBytes": 1048576,
  "artifactDir": "/data/state/cells/<cell_id>"
}
```

runtime 行为：

- `mode=exec` 使用 `spawn(command, args, { shell: false })`。
- `mode=shell` 使用 `spawn("bash", ["-lc", script])`。
- stdout/stderr 分流采集，并合并到 output。
- 用户命令 stdout/stderr 会实时镜像到 `agent-compose-runtime exec` 的 stderr，供 host 流式展示；`agent-compose-runtime exec` 的 stdout 只用于最终 command result 协议 payload。
- 默认每个 stream 返回最多 `1 MiB`；完整 stdout/stderr/output 写入 artifact。

## 5. 环境变量约定

host 调用 JS runtime 时会从 session env 合并出环境变量，并覆盖/补充：

```text
GOPATH=/usr/local/go
PATH=/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
SESSION_ID=<session_id>
WORKSPACE=/workspace
STATE_ROOT=/data/state
RUNTIME_ROOT=/data/runtime
VERSION=<version>
```

session 创建时运行 Jupyter 的环境还会包含：

```text
JUPYTER_TOKEN=<token>
```

JS runtime 对 Codex 额外支持：

```text
CODEX_BIN=<custom codex executable>
```

如果未设置，会依次查找 `/usr/bin/codex`、`/usr/local/bin/codex` 和 `PATH` 中的 `codex`。

`agent-compose-runtime exec` 启动用户 command 时还会注入：

```text
WORKSPACE=/workspace
STATE_ROOT=/data/state
RUNTIME_ROOT=/data/runtime
```

artifact dir 只来自 command request 或 CLI 参数，不再作为全局环境变量注入。子进程继承 runtime 进程自身的原生 `HOME`。

## 6. 标准输入输出协议

### 6.1 stdin

当前不使用 stdin。prompt 必须通过 `--message-file` 指定。

### 6.2 stderr：人类可读 transcript

JS runtime 将 agent 运行过程中的人类可读输出写到 stderr。host 的 `ExecStream` 会把 stderr chunk 作为流式输出传给 `SendAgentMessageStream`，并最终落到 cell 的 `stderr` / `output`。

### 6.3 stdout：结构化结果

`prompt` 子命令成功完成后，在 stdout 输出一行结构化结果：

```text
__AGENT_RESULT__{"provider":"codex","sessionId":"...","stopReason":"completed","finalText":"...","transcript":"...","stderr":""}
```

固定前缀：

```text
__AGENT_RESULT__
```

JSON 字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `provider` | string | 归一化后的 provider |
| `sessionId` | string | provider 自己的续接 ID |
| `stopReason` | string | 停止原因，通常是 `completed` |
| `finalText` | string | 最终回复文本 |
| `transcript` | string | 聚合的人类可读 transcript |
| `stderr` | string | 预留字段，当前多数 provider 为空 |

host 解析时会从 stdout 最后一行往前找 payload；如果 stdout 没找到，会再从合并 output 中查找。解析器兼容两种格式：

```text
__AGENT_RESULT__{...}
{...}
```

但 runtime 应始终输出带前缀的格式，以避免和普通 stdout 混淆。

`exec` 子命令完成后，在 stdout 输出一行 command result：

```text
__COMMAND_RESULT__{"stdout":"...","stderr":"...","output":"...","exitCode":0,"success":true,"stdoutTruncated":false,"stderrTruncated":false,"outputTruncated":false,"artifacts":{"stdout":"/data/state/cells/<cell_id>/stdout.txt","stderr":"/data/state/cells/<cell_id>/stderr.txt","output":"/data/state/cells/<cell_id>/output.txt","request":"/data/state/cells/<cell_id>/command-request.json","result":"/data/state/cells/<cell_id>/command-result.json"}}
```

固定前缀：

```text
__COMMAND_RESULT__
```

command result JSON 字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `stdout` | string | 截断后的 stdout |
| `stderr` | string | 截断后的 stderr |
| `output` | string | 截断后的 stdout/stderr 合并输出 |
| `exitCode` | number | 子进程 exit code |
| `success` | boolean | `exitCode == 0` |
| `stdoutTruncated` | boolean | stdout 返回值是否截断 |
| `stderrTruncated` | boolean | stderr 返回值是否截断 |
| `outputTruncated` | boolean | output 返回值是否截断 |
| `artifacts` | object | guest 侧 artifact 路径 |

`exec` 子命令即使 command exit code 非 0，也应输出 command result payload。只有 request 无效、spawn/timeout/artifact 等基础设施错误才由 runtime 顶层错误处理返回非 0 且不保证有 payload。

## 7. Host 解析与落库

host 的解析流程：

```text
runtime.ExecStream
  -> ExecResult{Stdout, Stderr, Output, ExitCode, Success}
  -> parseAgentExecResult
  -> AgentRunResult
  -> sanitizeAgentExecResult
  -> writeCellArtifacts
  -> Store.AddCell
  -> Store.AddEvent
```

解析成功后，host 会把 `__AGENT_RESULT__...` 从 `Stdout` 和 `Output` 中剥离，避免协议 payload 出现在最终 cell artifact 中。

需要注意：流式输出阶段没有专门过滤协议 payload；如果 runtime 在 stdout 中发送最终 payload，流式客户端可能短暂收到这行协议文本。最终持久化结果会被 sanitize。

loader command 的 host 解析流程：

```text
LoaderHost.Command
  -> ensureLoaderSession
  -> Executor.ExecuteLoaderCommand
  -> Store.AddCell(running SHELL)
  -> write command-request.json
  -> runtime.ExecStream(agent-compose-runtime exec)
  -> parseCommandExecResult
  -> preserve guest command-result.json; mirror stdout/stderr/output artifacts
  -> Store.AddCell(completed SHELL)
  -> loader.command.completed / loader.command.failed
```

解析成功后，guest runtime 已经在共享 cell 目录写入 `command-result.json`，host 不再重写该文件；host 只在缺失时补写 `stdout.txt`、`stderr.txt`、`output.txt`。host 使用 command result payload 中的 stdout/stderr/output 更新 cell，而不是把协议 payload 保存为 cell 输出。返回给 loader script 的 artifact 路径是 host 侧路径。

同一个 loader run 内多次 command/shell 调用复用该 run 的 loader session；run 结束后 host 统一停止本 run 使用过的 command session，并记录 `loader.session.stopped`。`scheduler.agent` 的 session stop 行为仍按 agent 路径处理。

## 8. 续接状态约定

JS runtime 负责保存 provider 级续接索引：

```text
/data/state/agents/providers/<provider>.json
```

内容：

```json
{
  "provider": "codex",
  "sessionId": "<provider-session-id>",
  "updatedAt": "2026-01-01T00:00:00.000Z",
  "systemContextHash": "<sha256-hex-of-systemContext>",
  "systemContextHashVersion": 1
}
```

`systemContextHash` 与 `systemContextHashVersion` 仅由 Codex runner 写入，Claude 及其他 provider 不写这两个字段。

Codex 和 Claude 会在下一次调用时读取该文件并 resume：

- Codex：当 `sessionId` 存在**且** `systemContextHash` 与当前组合 system context 匹配（或属于无 hash 的旧状态——见下文 lazy migration）时执行 `codex.resumeThread(sessionId, ...)`；否则执行 `codex.startThread(...)`。当因 hash 不匹配而强制新建 thread 时，runtime 会向 stderr 写入 `[agent-compose-runtime] warning: system context changed; started new Codex thread`。
- Claude：`resume: sessionId`

Gemini 当前不写该 provider state。

host 侧在 agent 执行完成后还会生成 cell 级 manifest：

```text
/data/state/cells/<cell_id>/agent-session.json
```

该文件由 host 写入，用于记录：

- provider
- provider state 文件路径
- provider session id
- provider 原生日志路径，例如 Codex 的 `/data/home/.codex/sessions/.../*.jsonl`

### 8.1 失败和取消时的续接限制

`/data/state/agents/providers/<provider>.json` 当前只在 JS runtime 正常跑到结尾后写入。

如果 host context 取消、agent 超时、sandbox 被停止，或者 provider runner 抛错，可能出现：

- provider 原生日志已经写入，例如 `/data/home/.codex/sessions/.../*.jsonl`
- 但 `/data/state/agents/providers/codex.json` 尚未生成
- host 只能在 `agent-session.json` 中记录已发现的原生日志路径，无法得到明确的 `sessionId`

这意味着取消/失败后的自动续接能力取决于 provider state 是否已经成功写入。

## 9. 运行期资源目录

`/data/runtime` 当前作为运行期资源与扩展能力的预留目录。它由 mount manifest 映射到 host session runtime 目录，因此 host 和 guest 都可读写：

```text
host:  <session>/runtime
guest: /data/runtime
```

### 9.1 MPI 资源目录

`/data/runtime/mpi/` 用于传递 MPI 资源文件。这里的 MPI 指 Model Program Interface，用于把运行期可访问的模型资源暴露给 agent。

JS runtime 在每次启动 Codex 或 Claude 前会尝试读取：

```text
/data/runtime/mpi/
  catalog.md
  resources/
    <resource-name>.md
```

行为约定：

- 只自动读取并注入 `/data/runtime/mpi/catalog.md`。
- `catalog.md` 不存在时静默跳过。
- `catalog.md` 存在但不可读或不是普通文件时，JS runtime 向 stderr 写 warning，但不中断 agent。
- 注入上下文会包含 catalog 内容，并提示详细资源文件位于 `/data/runtime/mpi/resources/`。
- `resources/` 是平坦目录，不预加载；agent 仅在 catalog 引用具体资源时按需读取。
- Codex 和 Claude 的 `additionalDirectories` 会包含 `/data/runtime`，以允许 agent 读取 `resources/` 下的详细文档。

当前边界：

- JS runtime 只读取并注入已存在的 `/data/runtime/mpi/catalog.md`。
- 资源文件的生成、同步、版本、权限、刷新和失效策略不在 runtime 内实现。
- MPI Markdown 资源条目与后端接口之间没有额外的强制映射层。

## 10. Provider 适配行为

### 10.1 Codex

JS runtime 使用 `@openai/codex-sdk`。

线程选项：

```text
workingDirectory=/workspace
additionalDirectories=[/data/state, /root, /data/runtime]
skipGitRepoCheck=true
sandboxMode=danger-full-access
approvalPolicy=never
networkAccessEnabled=true
```

如果 `/data/runtime/mpi/catalog.md` 存在且可读，JS runtime 会通过 Codex `config.developer_instructions` 注入 MPI catalog 上下文。

线程选择会将 `systemContext` 的 SHA-256 哈希与 `codex.json.systemContextHash` 比较：哈希匹配（或旧状态无 hash）时 resume，否则新建 thread。详见 §8。

Codex 事件会被转换成人类可读 transcript，包括 agent message、reasoning、command execution、file change、MCP call、web search、todo list 等。

### 10.2 Claude

JS runtime 使用 `@anthropic-ai/claude-agent-sdk`。

关键选项：

```text
cwd=/workspace
additionalDirectories=[/data/state, /root, /data/runtime]
includePartialMessages=true
forwardSubagentText=true
permissionMode=bypassPermissions
allowDangerouslySkipPermissions=true
resume=<stored session id>
```

如果 `/data/runtime/mpi/catalog.md` 存在且可读，JS runtime 会通过 `systemPrompt: { type: "preset", preset: "claude_code", append: <mpi-context> }` 注入 MPI catalog 上下文。

### 10.3 Gemini

JS runtime 通过子进程调用：

```sh
gemini -p <prompt> --output-format stream-json --approval-mode yolo
```

当前 Gemini runner 读取 stream-json 并生成 transcript，但不写 `/data/state/agents/providers/gemini.json`。

### 10.4 OpenCode

JS runtime 通过子进程调用 OpenCode：

```sh
opencode run <prompt> --format json --dir /workspace --dangerously-skip-permissions
```

当 host 提供 model 时，runner 会追加 `--model <model>`。当已有 provider session
state 时，runner 会追加 `--session <stored session id>`。runner 会默认设置
`OPENCODE_DISABLE_AUTOUPDATE=true`，除非环境变量中已经显式设置。

OpenCode 原始 JSON events 会被转换成人类可读 transcript。成功运行且拿到非空
provider session id 后，runner 会写入
`/data/state/agents/providers/opencode.json`。

## 11. 错误语义

JS runtime 顶层错误处理：

```text
stderr: error stack/message
exit:   1
```

host 侧行为：

- `ExecStream` 返回错误：host 保存失败 cell，`Success=false`
- exit code 非 0：host 仍尝试解析协议 payload；没有 payload 时按失败处理
- 找不到结构化 payload：报 `decode agent result ... no result payload found`
- stdout 为空：报 `agent <provider> returned empty stdout`

失败 cell 会写入：

```text
/data/state/cells/<cell_id>/source.txt
/data/state/cells/<cell_id>/stdout.txt
/data/state/cells/<cell_id>/stderr.txt
/data/state/cells/<cell_id>/output.txt
/data/state/cells/<cell_id>/exitcode.txt
/data/state/cells/<cell_id>/agent-session.json
```

并写入 `agent.assistant.failed` event。

loader command 错误语义：

- command/shell exit code 非 0：`scheduler.exec` / `scheduler.shell` 不抛错，返回 `success=false`，并记录 error level 的 `loader.command.completed`。
- runtime driver exec 失败、`agent-compose-runtime exec` 没有输出可解析 command payload、timeout/context cancel、artifact 写入失败：`scheduler.exec` / `scheduler.shell` 抛错，并记录 `loader.command.failed`。
- command cell 使用 `SHELL` 类型，不新增 proto cell enum。

## 12. Guest Runtime SDK

`@chaitin-ai/agent-compose-runtime-sdk` 是给 guest 内普通 Node.js 脚本使用的 SDK。它位于 `runtime/agent-compose-runtime-sdk`，guest image 构建时打成 tarball 并放到：

```text
/opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

workspace 脚本可以离线安装：

```bash
npm install --offline /opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

SDK 是普通 npm 依赖，runtime runner 不会隐式安装依赖，也不会修改 workspace 的 dependency tree。需要在 workspace 脚本中使用 SDK 时，应由 workspace 自己通过 npm registry、`.npmrc` 或 guest image 内的 offline tarball 安装。

支持 CommonJS 和 ESM：

```js
const { runtime } = require("@chaitin-ai/agent-compose-runtime-sdk");
```

```js
import { runtime } from "@chaitin-ai/agent-compose-runtime-sdk";
```

`runtime` 是 Node.js 脚本侧的主对象，SDK default export 与命名导出的 `runtime` 指向同一对象。也可以按需导入 `exec`、`shell`、`agent`、`llm` 等函数，但面向产品文档和示例时推荐使用 `runtime.*`。

SDK 当前只使用 Node 标准库、环境变量、文件系统、子进程、内置 `fetch` 和声明的 npm 依赖。`runtime.exec`、`runtime.shell`、`runtime.agent` 不直接回调 Go host；host 仍只看到最外层 command cell 的 stdout/stderr/output 和 artifact。`runtime.llm` 会调用 agent-compose 的 `LLMService.Generate` Connect JSON endpoint。

当前 runtime CLI 只有 `prompt` 和 `exec` 两个 host 依赖子命令。还没有 `workflow` 子命令、`__WORKFLOW_RESULT__` stdout 协议、scheduler 到 Node workflow 的专用 bridge token，或能让 Node workflow 直接操作 loader state/event/artifact 的上下文对象。需要复杂 Node.js 逻辑时，调用方应通过 `agent-compose-runtime exec`、`scheduler.exec` / `scheduler.shell` 或普通 workspace 脚本运行，并用已实现的 SDK API 组合能力。

### 12.1 SDK API

`runtime.exec(command, args?, options?)` 使用 `child_process.spawn(command, args, { shell: false })` 执行命令。

`runtime.shell(script, options?)` 使用 `bash -lc <script>` 执行 shell。

共同选项：

| 字段 | 说明 |
|---|---|
| `cwd` | 默认 `runtime.paths.workspace` |
| `env` | 覆盖本次子进程环境 |
| `timeoutMs` | 超时后终止子进程 |
| `maxOutputBytes` | 每个 stream 返回上限，默认 `1 MiB` |
| `rejectOnFailure` | 非零 exit code 时抛 `CommandError` |
| `streamOutput` | 是否把子进程 stdout/stderr 转发到当前进程，默认 true |

返回：

```ts
type RuntimeCommandResult = {
  stdout: string;
  stderr: string;
  output: string;
  exitCode: number;
  success: boolean;
  stdoutTruncated: boolean;
  stderrTruncated: boolean;
  outputTruncated: boolean;
};
```

`runtime.agent(prompt, options?)` 写临时 message file，并在 guest 内调用既有 `agent-compose-runtime prompt`。它复用 Codex、Claude、Gemini provider adapter、MPI 注入和 provider state，但不会回调 host 创建独立 agent cell。

`runtime.agent` 支持 `outputSchema`。它接受 Zod schema 或 plain JSON Schema object；Zod schema 会转换成 JSON Schema 写入 `--output-schema-file`，并在返回后用同一个 Zod schema 校验 `result.json`。设置 `outputSchema` 时，`finalText` 必须是 JSON 字符串，SDK 会解析到 `result.json`；未设置时 `result.json` 为 `null`。

`runtime.llm(prompt, options?)` 调用 `LLMService.Generate`。daemon 通过
`LLM_API_PROTOCOL` 选择 HTTP 协议（默认 `responses`，或
`chat_completions` 对接 OpenAI 兼容 Chat Completions 后端）：

| 字段 | 说明 |
|---|---|
| `model` | 可选模型名，不传时使用服务端配置 |
| `baseUrl` | agent-compose 服务地址；默认依次读取 `BASE_URL`、`HTTP_URL`，最后使用 `http://127.0.0.1:7410` |
| `timeoutMs` | 请求超时毫秒数 |
| `outputSchema` | Zod schema 或 plain JSON Schema object |

返回：

```ts
type RuntimeLLMResult<T = unknown> = {
  text: string;
  model: string;
  responseId: string;
  finishReason: string;
  json: T | null;
};
```

设置 `outputSchema` 时，SDK 会将 JSON Schema 作为 `output_schema` 发给
`LLMService.Generate`；`text` 必须是 JSON 字符串，SDK 解析到 `json` 并对 Zod
schema 二次校验。`LLM_API_PROTOCOL=responses` 时，daemon 通过 Responses API
做 strict JSON Schema 约束；`chat_completions` 则改为 prompt 引导并设置
`json_object`。

`runtime.env` 提供：

```ts
runtime.env.get(name)
runtime.env.require(name)
runtime.env.all()
```

`runtime.paths` 从环境变量推导当前 guest 路径：

| 字段 | 环境变量 | 默认值 |
|---|---|---|
| `workspace` | `WORKSPACE` | `/workspace` |
| `stateRoot` | `STATE_ROOT` | `/data/state` |
| `runtimeRoot` | `RUNTIME_ROOT` | `/data/runtime` |
| `home` | `HOME` | `/root` |

`runtime.log(message, payload?)` 向 stdout 写一行 JSON：

```json
{"type":"agent-compose.runtime.log","message":"...","payload":{},"createdAt":"..."}
```

`runtime.report.writeMarkdown(name, content, options?)` 把 markdown 写到指定目录、artifact 目录或 workspace，返回写入路径。

## 13. 兼容性要求

修改 JS runtime 或 host 调用时应保持以下兼容性：

- `agent-compose-runtime prompt` 子命令继续可用。
- `agent-compose-runtime exec` 子命令继续输出带 `__COMMAND_RESULT__` 前缀的 command result JSON。
- `--provider`、`--message-file`、`--state-root`、`--workspace`、`--home` 参数语义不变。
- Agent identity 通过 `<state-root>/agents/system-prompts/system-prompt.txt` 约定路径发现。
- 成功时 stdout 必须输出可解析的 agent result JSON，推荐使用 `__AGENT_RESULT__` 前缀。
- 人类可读过程输出应继续走 stderr，避免污染 stdout 协议通道。
- provider state 文件路径保持 `/data/state/agents/providers/<provider>.json`。
- host 可继续通过 `/data/home` 收集 provider 原生会话记录。
