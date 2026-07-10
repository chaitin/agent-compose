# agent-compose And agent-compose-runtime Call Contract

Chinese version: [../zh-CN/design/agent-compose-runtime_contract.md](../zh-CN/design/agent-compose-runtime_contract.md)

This document describes the call boundary between the Go host side
`agent-compose` process and the JavaScript runtime
`agent-compose-runtime` inside the sandbox. The current runtime is primarily
used by `AgentService`: the host executes a unified entry command inside the
sandbox, the JavaScript runtime adapts Codex, Claude, and Gemini, and structured
results are returned to the host.

Related code:

- Stream model: `pkg/model/model.go`, `pkg/driver/types.go`
- Marker parsing and stream filtering: `pkg/execution/parse.go`
- Host agent calls: `pkg/agentcompose/adapters/agent_runner.go`
- Host execution and persistence: `pkg/agentcompose/adapters/cell_executor.go`, `pkg/agentcompose/adapters/agent_executor.go`, and `pkg/storage/sessionstore`
- v2 stream API: `proto/agentcompose/v2/agentcompose.proto`
- CLI stream writer: `cmd/agent-compose/main.go`
- Runtime CLI source: `runtime/javascript/src/cli.ts`
- Runtime provider adapters: `runtime/javascript/src/runners/`
- Guest SDK: `runtime/agent-compose-runtime-sdk/`
- Guest image installation: `guest-images/Dockerfile.agent-compose-guest`

## 1. Runtime Location

`agent-compose` is the host-side Go service. It owns sandbox lifecycle,
directory preparation, runtime driver scheduling, proxying, and persistence.

`agent-compose-runtime` is installed inside the guest image. During image
build:

```text
COPY runtime/javascript /tmp/agent-compose-runtime
npm ci
npm install -g <packed runtime>
ln -sv ../lib/node_modules/@chaitin-ai/agent-compose-runtime/dist/cli.js /usr/bin/agent-compose-runtime
```

The host actually invokes this command inside the guest:

```text
agent-compose-runtime
```

The guest image also includes the `@chaitin-ai/agent-compose-runtime-sdk`
tarball:

```text
/opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

## 2. Mount And Path Conventions

After sandbox creation, the host generates a mount manifest and mounts sandbox
subdirectories to guest target paths one by one:

```text
host:  <SANDBOX_ROOT>/<sandbox_id>/workspace
guest: /workspace
```

With default configuration:

```text
host:  <DATA_ROOT>/sandboxes/<sandbox_id>
```

Therefore these paths correspond:

| Host path | Guest path | Purpose |
| --- | --- | --- |
| `<sandbox>/workspace` | `/workspace` | Workspace and agent cwd |
| `<sandbox>/home/.codex` | `/root/.codex` | Codex config and state |
| `<sandbox>/home/.claude` | `/root/.claude` | Claude config and state |
| `<sandbox>/home/.claude.json` | `/root/.claude.json` | Claude root config |
| `<sandbox>/home/.gitconfig` | `/root/.gitconfig` | Git config |
| `<sandbox>/state` | `/data/state` | agent-compose state, cell artifacts, agent prompts |
| `<sandbox>/runtime` | `/data/runtime` | Reserved runtime resource and extension directory |
| `<sandbox>/logs` | `/data/logs` | Jupyter and related logs |

The `boxlite`, `docker`, and `microsandbox` drivers all consume
`<sandbox>/vm/mount-manifest.json`, but manifest content is generated per
driver from the same logical runtime mount list. Docker keeps fine-grained home
subpath mounts, including file sources such as `.claude.json` and `.gitconfig`.
BoxLite and Microsandbox use directory sources only. They expose
`/workspace -> /data/workspace` through guest-side symlink and keep `/root` as a
real image directory, while declared home entries such as `/root/.codex` and
`/root/.gitconfig` are symlinked to `/data/home/...`. `/data/state`,
`/data/runtime`, and `/data/logs` come directly from mounted directories.

## 3. Host Resource Preparation

### 3.1 Sandbox Directory

During `Store.CreateSandbox`, the host creates:

```text
<sandbox>/
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
  state/events.jsonl
```

If the sandbox is bound to a Git workspace, the host clones the repository into
`<sandbox>/workspace` before starting runtime.

### 3.2 Agent Prompt File

When sending an agent message, the host does not pass the prompt through stdin.
It first writes a prompt file:

```text
host:  <sandbox>/state/agents/prompts/<provider>-<unix_nano>.txt
guest: /data/state/agents/prompts/<provider>-<unix_nano>.txt
```

The guest path is then passed to the JavaScript runtime through `--message-file`.

When a run is bound to an agent definition with non-empty `system_prompt`, the
host writes the trimmed text to a fixed convention path:

```text
host:  <sandbox>/state/agents/system-prompts/system-prompt.txt
guest: /data/state/agents/system-prompts/system-prompt.txt
```

The guest runtime reads this path via `agentSystemPromptPath(stateRoot)` in
`prompt.ts`. If the file is missing or empty, `readSystemPromptFile` returns
`""` and the run composes MPI-only context. When `system_prompt` becomes empty,
the host removes `system-prompt.txt` to avoid stale identity on later runs in
the same sandbox.

### 3.3 Agent HOME And Initial Config

The host sets these values for agent execution:

```text
Cwd=/workspace
WORKSPACE=/workspace
STATE_ROOT=/data/state
RUNTIME_ROOT=/data/runtime
```

agent-compose no longer overrides `HOME`; guest tools use the image default
`HOME=/root`. Default Codex, Claude, and Git config is initialized by the host in
sandbox home and exposed to the corresponding paths under `/root` through the
mount manifest or directory-only bootstrap.

## 4. Entry Command

The host executes this command inside the sandbox through runtime driver
`ExecStream`:

```sh
sh -lc 'set -e && cd /workspace && agent-compose-runtime prompt \
  --provider <provider> \
  --message-file /data/state/agents/prompts/<provider>-<unix_nano>.txt \
  --state-root /data/state \
  --workspace /workspace \
  --home /root'
```

The JavaScript runtime supports two subcommands:

```text
prompt
exec
```

The CLI uses `commander` to parse commands and arguments. The
`@chaitin-ai/agent-compose-runtime` package exposes the `agent-compose-runtime`
bin entry; the guest image also creates an `agent-compose-runtime` symlink
pointing to the compiled `dist/cli.js`.

Command arguments:

| Argument | Required | Description |
| --- | ---: | --- |
| `--provider` | yes | `codex`, `claude`, `gemini`, `opencode`, with a small set of aliases |
| `--message-file` | yes | Prompt file path |
| `--state-root` | no | agent-compose runtime state root; default `/srv/agent-compose/sandbox/state`. Guest discovers agent identity at `agents/system-prompts/system-prompt.txt` and MPI catalog from this root |
| `--workspace` | no | Agent working directory; default `WORKSPACE` or `/workspace` |
| `--home` | no | Agent HOME; default `HOME` or `/root` |
| `--model` | no | Agent model; consumed by providers that support explicit model selection |
| `--system-prompt-file` | no | System prompt file path; currently consumed by providers that need prompt-level system instructions |
| `--skill <name>` | no | Repeatable enabled skill name. Host projects the active set into `/root/.agents/skills` before invoking runtime |

Agent identity uses the fixed convention path documented in §3.2.

Inside an agent-compose sandbox, the host always passes `--state-root`,
`--workspace`, and `--home` explicitly.

### 4.1 `exec` Subcommand

When a loader script runs a runtime command through `scheduler.exec` /
`scheduler.shell`, the host executes this command inside the sandbox through
runtime driver `ExecStream`:

```sh
sh -lc 'set -e && agent-compose-runtime exec \
  --request-file /data/state/cells/<cell_id>/command-request.json \
  --state-root /data/state \
  --workspace /workspace \
  --home /root'
```

Command arguments:

| Argument | Required | Description |
| --- | ---: | --- |
| `--request-file` | yes | Runtime command request JSON file |
| `--state-root` | no | agent-compose runtime state root |
| `--workspace` | no | Default working directory |
| `--home` | no | Command HOME |

Example exec request JSON:

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

Example shell request:

```json
{
  "mode": "shell",
  "script": "set -e\necho hello\n",
  "cwd": "/workspace",
  "maxOutputBytes": 1048576,
  "artifactDir": "/data/state/cells/<cell_id>"
}
```

Runtime behavior:

- `mode=exec` uses `spawn(command, args, { shell: false })`.
- `mode=shell` uses `spawn("bash", ["-lc", script])`.
- stdout/stderr are captured separately and merged into output.
- User command stdout is mirrored in real time to `agent-compose-runtime exec`
  stdout; user command stderr is mirrored in real time to
  `agent-compose-runtime exec` stderr. The host preserves these stdio streams
  when forwarding command transcript chunks.
- After the child process exits, `agent-compose-runtime exec` writes one final
  `__COMMAND_RESULT__...` protocol payload line to stdout.
- By default, each returned stream is capped at `1 MiB`; full
  stdout/stderr/output are written as artifacts.

## 5. Environment Variable Conventions

When the host invokes the JavaScript runtime, it merges environment variables
from sandbox env and overrides/adds:

```text
GOPATH=/usr/local/go
PATH=/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
SANDBOX_ID=<sandbox_id>
WORKSPACE=/workspace
STATE_ROOT=/data/state
RUNTIME_ROOT=/data/runtime
VERSION=<version>
```

The environment used to start Jupyter during sandbox creation also contains:

```text
JUPYTER_TOKEN=<token>
```

The JavaScript runtime additionally supports this Codex variable:

```text
CODEX_BIN=<custom codex executable>
```

When unset, it looks for `/usr/bin/codex`, `/usr/local/bin/codex`, and then
`codex` in `PATH`.

When `agent-compose-runtime exec` starts a user command, it also injects:

```text
WORKSPACE=/workspace
STATE_ROOT=/data/state
RUNTIME_ROOT=/data/runtime
```

Artifact dir comes only from the command request or CLI arguments and is no
longer injected as a global environment variable. Child processes inherit the
runtime process's native `HOME`.

## 6. Standard Input/Output Protocol

### 6.1 stdin

stdin is not used. Prompts must be specified through `--message-file`.

### 6.2 Stdio Streams And Protocol Markers

Runtime drivers transport output as `ExecChunk{Text, Stream}`. Both the driver
layer and host domain model use stdout/stderr stream enums, and empty or
unspecified streams are normalized to stdout. `Stream` only identifies the
original stdio channel. It does not mean that a chunk is internal, hidden,
machine-readable, or user-visible.

The only protocol payload markers are:

- `__AGENT_RESULT__`
- `__COMMAND_RESULT__`

The host decides whether bytes are protocol payload by searching for those
markers, never by checking stdout/stderr. Driver implementations (`docker`,
`boxlite`, and `microsandbox`) do not parse or filter these markers.

The v2 Connect API exposes the same channel concept with `StdioStream`:

```proto
enum StdioStream {
  STDIO_STREAM_UNSPECIFIED = 0;
  STDIO_STREAM_STDOUT = 1;
  STDIO_STREAM_STDERR = 2;
}
```

`RunAgentStreamResponse`, `ExecStreamResponse`, and `TranscriptEvent` carry a
`stream` field. `STDIO_STREAM_UNSPECIFIED` is treated as stdout by CLI and host
consumers. The v1 API keeps its historical `is_stderr` fields for compatibility;
the conversion to that boolean happens only at v1/session compatibility
boundaries.

The protocol deliberately does not add `chunk_type`, `payload_kind`, typed
payload events, new CLI flags, JSON schema changes, or stdin forwarding for this
boundary.

### 6.3 stderr: Human-Readable Transcript

The JavaScript runtime writes human-readable agent execution output to stderr.
The host `ExecStream` forwards stderr chunks as streaming output for
`SendAgentMessageStream`, and finally persists them to cell `stderr` / `output`.

### 6.4 stdout: Structured Result

After the `prompt` subcommand completes successfully, stdout contains one
structured result line:

```text
__AGENT_RESULT__{"provider":"codex","threadId":"...","stopReason":"completed","finalText":"...","transcript":"...","stderr":""}
```

Fixed prefix:

```text
__AGENT_RESULT__
```

JSON fields:

| Field | Type | Description |
| --- | --- | --- |
| `provider` | string | Normalized provider |
| `threadId` | string | agent-compose thread id; adapters map provider-native resume ids into this field |
| `stopReason` | string | Stop reason, usually `completed` |
| `finalText` | string | Final response text |
| `transcript` | string | Aggregated human-readable transcript |
| `stderr` | string | Reserved field; currently empty for most providers |

The host parser searches backward from the last stdout line for the payload. If
stdout does not contain it, the parser also searches merged output. The parser is
compatible with both formats:

```text
__AGENT_RESULT__{...}
{...}
```

The runtime should always emit the prefixed format to avoid confusion with
ordinary stdout.

After the `exec` subcommand completes, stdout contains one command result line:

```text
__COMMAND_RESULT__{"stdout":"...","stderr":"...","output":"...","exitCode":0,"success":true,"stdoutTruncated":false,"stderrTruncated":false,"outputTruncated":false,"artifacts":{"stdout":"/data/state/cells/<cell_id>/stdout.txt","stderr":"/data/state/cells/<cell_id>/stderr.txt","output":"/data/state/cells/<cell_id>/output.txt","request":"/data/state/cells/<cell_id>/command-request.json","result":"/data/state/cells/<cell_id>/command-result.json"}}
```

Fixed prefix:

```text
__COMMAND_RESULT__
```

Command result JSON fields:

| Field | Type | Description |
| --- | --- | --- |
| `stdout` | string | Truncated stdout |
| `stderr` | string | Truncated stderr |
| `output` | string | Truncated merged stdout/stderr output |
| `exitCode` | number | Child process exit code |
| `success` | boolean | `exitCode == 0` |
| `stdoutTruncated` | boolean | Whether returned stdout is truncated |
| `stderrTruncated` | boolean | Whether returned stderr is truncated |
| `outputTruncated` | boolean | Whether returned output is truncated |
| `artifacts` | object | Guest-side artifact paths |

The `exec` subcommand should emit a command result payload even when the user
command exit code is non-zero. Only invalid request, spawn, timeout, artifact,
or other infrastructure errors are handled by the runtime top-level error path,
which exits non-zero and does not guarantee a payload.

## 7. Host Parsing And Persistence

Host parsing flow:

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

After parsing succeeds, the host strips `__AGENT_RESULT__...` from `Stdout` and
`Output` so the protocol payload does not appear in final cell artifacts.

Streaming transcript paths also use host-side marker filters. Agent streams use
`FilterAgentStreamChunk`; command, exec, run, and loader command streams use
`FilterCommandStreamChunk`. These helpers strip `__AGENT_RESULT__...` and
`__COMMAND_RESULT__...` protocol payloads before writing human transcript,
run logs, notebook cell output, or CLI text output.

Loader command host parsing flow:

```text
LoaderHost.Command
  -> ensure loader sandbox
  -> Executor.ExecuteLoaderCommand
  -> Store.AddCell(running SHELL)
  -> write command-request.json
  -> runtime.ExecStream(agent-compose-runtime exec)
  -> parseCommandExecResult
  -> preserve guest command-result.json; mirror stdout/stderr/output artifacts
  -> Store.AddCell(completed SHELL)
  -> loader.command.completed / loader.command.failed
```

After parsing succeeds, the guest runtime has already written
`command-result.json` in the shared cell directory. The host does not rewrite
that file; it only backfills `stdout.txt`, `stderr.txt`, and `output.txt` when
missing. The host uses stdout/stderr/output from the command result payload to
update the cell, rather than saving the protocol payload as cell output.
Artifact paths returned to the loader script are host-side paths.

Multiple command/shell calls in the same loader run reuse the loader sandbox for
that run. After the run ends, the host stops command sandboxes used by that run
and records `loader.sandbox.stopped`. `scheduler.agent` sandbox stop behavior
still follows the agent path.

## 8. Resume State Convention

The JavaScript runtime is responsible for saving provider-level resume indexes:

```text
/data/state/agents/providers/<provider>.json
```

Content:

```json
{
  "provider": "codex",
  "threadId": "<provider-thread-id>",
  "updatedAt": "2026-01-01T00:00:00.000Z"
}
```

Codex and Claude read this file on the next call and resume:

- Codex: `codex.resumeThread(threadId, ...)`
- Claude: `resume: threadId`

Gemini currently does not write provider state.

After agent execution completes, the host also generates a cell-level manifest:

```text
/data/state/cells/<cell_id>/agent-thread.json
```

The host writes this file to record:

- provider
- provider thread state file path
- provider thread id
- provider-native log paths, such as Codex
  `/data/home/.codex/sessions/.../*.jsonl`

### 8.1 Resume Limits After Failure Or Cancellation

`/data/state/agents/providers/<provider>.json` is currently written only after
the JavaScript runtime reaches normal completion.

If host context is cancelled, the agent times out, the sandbox is stopped, or
the provider runner throws, this can happen:

- provider-native logs already exist, such as
  `/data/home/.codex/sessions/.../*.jsonl`
- `/data/state/agents/providers/codex.json` has not yet been generated
- the host can record discovered native log paths in `agent-thread.json`, but
  cannot obtain a definite `threadId`

This means automatic resume after cancellation/failure depends on whether
provider state has already been written successfully.

## 9. Runtime Resource Directory

`/data/runtime` is currently reserved for runtime resources and extension
capabilities. The mount manifest maps it to the host sandbox runtime directory,
so both host and guest can read and write it:

```text
host:  <sandbox>/runtime
guest: /data/runtime
```

### 9.1 MPI Resource Directory

`/data/runtime/mpi/` passes MPI resource files. Here MPI means Model Program
Interface, used to expose runtime-accessible model resources to agents.

Before starting Codex or Claude, the JavaScript runtime attempts to read:

```text
/data/runtime/mpi/
  catalog.md
  resources/
    <resource-name>.md
```

Behavior:

- Only `/data/runtime/mpi/catalog.md` is automatically read and injected.
- If `catalog.md` does not exist, it is silently skipped.
- If `catalog.md` exists but is unreadable or not a regular file, the JavaScript
  runtime writes a warning to stderr but does not interrupt the agent.
- Injected context includes catalog content and tells the agent that detailed
  resource files live under `/data/runtime/mpi/resources/`.
- `resources/` is a flat directory and is not preloaded. The agent reads
  detailed resources on demand only when the catalog references them.
- Codex and Claude `additionalDirectories` include `/data/runtime`, allowing the
  agent to read detailed documents under `resources/`.

Current boundary:

- The JavaScript runtime only reads and injects existing
  `/data/runtime/mpi/catalog.md`.
- Resource generation, synchronization, versioning, permissions, refresh, and
  invalidation are not implemented inside the runtime.
- There is no additional enforced mapping layer between MPI Markdown resource
  entries and backend APIs.

## 10. Provider Adapter Behavior

### 10.1 Codex

The JavaScript runtime uses `@openai/codex-sdk`.

Thread options:

```text
workingDirectory=/workspace
additionalDirectories=[/data/state, /root, /data/runtime]
skipGitRepoCheck=true
sandboxMode=danger-full-access
approvalPolicy=never
networkAccessEnabled=true
```

If `/data/state/agents/system-prompts/system-prompt.txt` and/or
`/data/runtime/mpi/catalog.md` exist and are readable, the JavaScript runtime
composes Agent Identity + MPI into `systemContext` and injects it through Codex
`config.developer_instructions`.

Codex events are converted into a human-readable transcript, including agent
messages, reasoning, command execution, file changes, MCP calls, web search, and
todo lists.

### 10.2 Claude

The JavaScript runtime uses `@anthropic-ai/claude-agent-sdk`.

Key options:

```text
cwd=/workspace
additionalDirectories=[/data/state, /root, /data/runtime]
includePartialMessages=true
forwardSubagentText=true
permissionMode=bypassPermissions
allowDangerouslySkipPermissions=true
resume=<stored thread id>
```

If `/data/state/agents/system-prompts/system-prompt.txt` and/or
`/data/runtime/mpi/catalog.md` exist and are readable, the JavaScript runtime
composes Agent Identity + MPI into `systemContext` and injects it through
`systemPrompt: { type: "preset", preset: "claude_code", append: <systemContext> }`.

### 10.3 Gemini

The JavaScript runtime invokes Gemini as a subprocess:

```sh
gemini -p <systemContext + user prompt> --output-format stream-json --approval-mode yolo
```

When `systemContext` is non-empty, it is prepended to the user prompt separated
by a blank line. The current Gemini runner reads stream-json and generates a
transcript, but does not write `/data/state/agents/providers/gemini.json`.

### 10.4 OpenCode

The JavaScript runtime invokes OpenCode as a subprocess:

```sh
opencode run <prompt> --format json --dir /workspace --dangerously-skip-permissions
```

When a model is provided by the host, the runner appends `--model <model>`.
When a stored provider thread exists, the runner appends
`--session <stored thread id>`. The `--session` flag is OpenCode's provider-native
resume flag. The runner sets
`OPENCODE_DISABLE_AUTOUPDATE=true` unless the environment already defines it.

OpenCode raw JSON events are converted into a human-readable transcript. The
runner writes `/data/state/agents/providers/opencode.json` after a successful
run with a non-empty provider thread id.

## 11. Error Semantics

JavaScript runtime top-level error handling:

```text
stderr: error stack/message
exit:   1
```

Host-side behavior:

- If `ExecStream` returns an error, the host saves a failed cell with
  `Success=false`.
- If exit code is non-zero, the host still attempts to parse a protocol payload;
  when no payload exists, it treats the run as failed.
- If no structured payload is found, it reports
  `decode agent result ... no result payload found`.
- If stdout is empty, it reports `agent <provider> returned empty stdout`.

Failed cells write:

```text
/data/state/cells/<cell_id>/source.txt
/data/state/cells/<cell_id>/stdout.txt
/data/state/cells/<cell_id>/stderr.txt
/data/state/cells/<cell_id>/output.txt
/data/state/cells/<cell_id>/exitcode.txt
/data/state/cells/<cell_id>/agent-thread.json
```

and write an `agent.assistant.failed` event.

Loader command error semantics:

- When command/shell exit code is non-zero, `scheduler.exec` /
  `scheduler.shell` does not throw. It returns `success=false` and records an
  error-level `loader.command.completed`.
- Runtime driver exec failure, missing parseable command payload from
  `agent-compose-runtime exec`, timeout/context cancellation, or artifact write
  failure makes `scheduler.exec` / `scheduler.shell` throw and records
  `loader.command.failed`.
- Command cells use the `SHELL` type. No new proto cell enum is introduced.

## 12. Guest Runtime SDK

`@chaitin-ai/agent-compose-runtime-sdk` is the SDK for ordinary Node.js scripts
inside the guest. It lives in `runtime/agent-compose-runtime-sdk` and is packed
into a tarball during guest image build:

```text
/opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

Workspace scripts can install it offline:

```bash
npm install --offline /opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

The SDK is a normal npm dependency. The runtime runner does not implicitly
install dependencies or modify the workspace dependency tree. When workspace
scripts need the SDK, the workspace should install it through npm registry,
`.npmrc`, or the offline tarball in the guest image.

CommonJS and ESM are both supported:

```js
const { runtime } = require("@chaitin-ai/agent-compose-runtime-sdk");
```

```js
import { runtime } from "@chaitin-ai/agent-compose-runtime-sdk";
```

`runtime` is the main object for Node.js scripts. The SDK default export and
named `runtime` export point to the same object. Functions such as `exec`,
`shell`, `agent`, and `llm` may also be imported individually, but product
documentation and examples should prefer `runtime.*`.

The SDK currently uses only Node standard library APIs, environment variables,
file system, child processes, built-in `fetch`, and declared npm dependencies.
`runtime.exec`, `runtime.shell`, and `runtime.agent` do not call back into the Go
host directly. The host still sees only the outer command cell's
stdout/stderr/output and artifacts. `runtime.llm` calls the agent-compose
`LLMService.Generate` Connect JSON endpoint.

The current runtime CLI has only two host-dependent subcommands: `prompt` and
`exec`. There is no `workflow` subcommand, `__WORKFLOW_RESULT__` stdout
protocol, dedicated bridge token from scheduler to Node workflow, or context
object that lets a Node workflow directly operate on loader state, events, or
artifacts. Complex Node.js logic should be run through
`agent-compose-runtime exec`, `scheduler.exec` / `scheduler.shell`, or ordinary
workspace scripts, and composed with already implemented SDK APIs.

### 12.1 SDK API

`runtime.exec(command, args?, options?)` runs a command with
`child_process.spawn(command, args, { shell: false })`.

`runtime.shell(script, options?)` runs shell with `bash -lc <script>`.

Common options:

| Field | Description |
| --- | --- |
| `cwd` | Defaults to `runtime.paths.workspace` |
| `env` | Per-child environment overrides |
| `timeoutMs` | Terminates the child process after timeout |
| `maxOutputBytes` | Return limit for each stream; default `1 MiB` |
| `rejectOnFailure` | Throw `CommandError` for non-zero exit code |
| `streamOutput` | Whether to forward child stdout/stderr to current process; default true |

Return:

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

`runtime.agent(prompt, options?)` writes a temporary message file and invokes the
existing `agent-compose-runtime prompt` inside the guest. It reuses Codex,
Claude, and Gemini provider adapters, MPI injection, and provider state, but
does not call back to the host to create a separate agent cell.

`runtime.agent` supports `outputSchema`. It accepts either a Zod schema or a
plain JSON Schema object. Zod schemas are converted to JSON Schema and written to
`--output-schema-file`, and the returned `result.json` is validated again with
the same Zod schema. When `outputSchema` is set, `finalText` must be a JSON
string, which the SDK parses into `result.json`; when unset, `result.json` is
`null`.

`runtime.llm(prompt, options?)` calls `LLMService.Generate`. The daemon selects
the HTTP protocol with `LLM_API_PROTOCOL` (`responses` by default, or
`chat_completions` for OpenAI-compatible Chat Completions backends):

| Field | Description |
| --- | --- |
| `model` | Optional model name; server config is used when omitted |
| `baseUrl` | agent-compose service URL. Defaults in order to `BASE_URL`, `HTTP_URL`, then `http://127.0.0.1:7410` |
| `timeoutMs` | Request timeout in milliseconds |
| `outputSchema` | Zod schema or plain JSON Schema object |

Return:

```ts
type RuntimeLLMResult<T = unknown> = {
  text: string;
  model: string;
  responseId: string;
  finishReason: string;
  json: T | null;
};
```

With `outputSchema`, the SDK sends JSON Schema to `LLMService.Generate` as
`output_schema`. When schema is set, `text` must be a JSON string; the SDK
parses it into `json` and validates Zod schemas again locally. With
`LLM_API_PROTOCOL=responses`, the daemon enforces strict JSON Schema via the
Responses API. With `chat_completions`, it uses prompt guidance and
`response_format: json_object` instead.

`runtime.env` provides:

```ts
runtime.env.get(name)
runtime.env.require(name)
runtime.env.all()
```

`runtime.paths` derives current guest paths from environment variables:

| Field | Environment variable | Default |
| --- | --- | --- |
| `workspace` | `WORKSPACE` | `/workspace` |
| `stateRoot` | `STATE_ROOT` | `/data/state` |
| `runtimeRoot` | `RUNTIME_ROOT` | `/data/runtime` |
| `home` | `HOME` | `/root` |

`runtime.log(message, payload?)` writes one JSON line to stdout:

```json
{"type":"agent-compose.runtime.log","message":"...","payload":{},"createdAt":"..."}
```

`runtime.report.writeMarkdown(name, content, options?)` writes Markdown to a
selected directory, artifact directory, or workspace and returns the written
path.

## 13. Compatibility Requirements

Changes to the JavaScript runtime or host invocation should preserve:

- `agent-compose-runtime prompt` subcommand availability.
- `agent-compose-runtime exec` subcommand outputting command result JSON with
  the `__COMMAND_RESULT__` prefix.
- Existing semantics for `--provider`, `--message-file`, `--state-root`,
  `--workspace`, and `--home`.
- Agent identity discovery via `<state-root>/agents/system-prompts/system-prompt.txt`.
- On success, stdout must contain parseable agent result JSON. The
  `__AGENT_RESULT__` prefix is recommended.
- Human-readable process output should continue to use stderr to avoid
  contaminating the stdout protocol channel.
- Provider state file path remains
  `/data/state/agents/providers/<provider>.json`.
- The host can continue collecting provider-native session records from
  `/data/home` through declared home-entry symlinks on directory-only runtimes.
