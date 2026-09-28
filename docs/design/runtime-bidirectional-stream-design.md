# Runtime Bidirectional Interaction Stream Design

The Docker command and agent attach paths are implemented. Microsandbox
supports native command attach but not agent attach; BoxLite interaction
remains unsupported by the current runtime API.

## Background

The current execution boundary from the agent-compose daemon to the runtime is centered on `ExecStream`:

```go
ExecStream(context.Context, *Sandbox, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error)
```

This interface can express "one-shot start parameters plus a runtime-to-daemon output stream", but it cannot express ongoing interaction such as stdin, TTY, terminal resize, signal/cancel, multi-turn agent input, or structured runtime events. As a result, `exec`, `run --command`, scheduler commands, cells, and agent prompts currently rely on file protocols such as `command-request.json`, prompt files, script files, stdout markers, and result files to carry control semantics.

This design upgrades the lower execution boundary to an interactive runtime stream without breaking existing command behavior or artifact layout. Docker provides the original native attach implementation. Microsandbox command attach is supported through its streaming exec API; BoxLite remains behind the explicit capability contract until its runtime API exposes the required input controls.

## Goals

1. Keep existing behavior unchanged for regular `exec`, `run --command`, `run --prompt`, schedulers, and cells.
2. Add a lower-level bidirectional stream that supports `stdin`, `stdin EOF`, `TTY`, `resize`, `signal/cancel`, `stdout/stderr`, `result`, and `agent event`.
3. Prioritize the Docker `exec/run -it` path.
4. Define separate semantics for `--command -it` and `--prompt -it`:
   - `--command -it` is process-level TTY attach, aligned with `docker exec -it`.
   - `--prompt -it` is agent-session-level multi-turn interaction, not raw process stdin/stdout.
5. Continue using mounted directories and files for artifacts. Do not introduce inline artifacts.
6. Keep the external API stable and expressive: unary, server-stream, and bidirectional stream APIs all remain. Internally, implementation should gradually converge on the lower bidirectional stream.
7. Keep `logs` and `logs --follow` on the same external interface, while upgrading the internal implementation to a "file snapshot plus real-time fanout" observer stream.

## Non-Goals

1. Do not emulate interactive controls in a driver that lacks native support. Docker, Microsandbox, and BoxLite now expose the stdin, TTY, resize, and signal controls required by command attach.
2. Do not merge workspace files, artifact bodies, or the LLM facade HTTP/SSE protocol into the runtime stream.
3. Do not implement `run --prompt -it` by making users call `agent-compose exec <sandbox> -it codex` or `run --command "codex" -it`.
4. Do not remove compatibility artifacts such as `command-request.json`, `command-result.json`, `stdout.txt`, `stderr.txt`, `output.txt`, or `transcript.txt`.
5. Do not deprecate the regular `StreamExec` / `StreamAgentRun` server-stream APIs. They are server-stream projections of the attach engine.

## Overall Model

The new model has three layers:

```text
CLI local stdin/stdout/tty
  <-> daemon external bidirectional RPC
  <-> daemon RuntimeInteraction contract
  <-> driver/native attach or runtime wrapper stream
  <-> guest process or agent SDK loop
```

The external API keeps three usage shapes:

```text
Exec / RunAgent
  -> build attach start
  -> execute internal bidirectional stream
  -> aggregate output/result
  -> return unary response

StreamExec / StreamAgentRun
  -> build attach start
  -> execute internal bidirectional stream
  -> close or do not use stdin
  -> project attach output/result into server-stream responses

AttachExec / AttachAgentRun
  -> expose bidirectional stream directly
  -> support stdin/resize/human_message and other continuous input
```

The implementation should gradually converge:

```text
external API handlers
  -> daemon AttachEngine
  -> RuntimeInteraction
  -> Docker, Microsandbox, or BoxLite native attach
```

To reduce risk in the first phase, only `-it` paths need to use the `AttachExec` / `AttachAgentRun` kernel. Existing unary and server-stream paths may keep their current implementation. In later migrations, handlers should call the same internal Go `AttachEngine` instead of making RPC calls to their own `AttachExec` / `AttachAgentRun` endpoints. This avoids protocol recursion and complicated connection lifecycles.

`StreamExec` / `StreamAgentRun` should not be marked legacy or deprecated. Their long-term role is a read-only server-stream projection for SDK/UI callers that do not need stdin.

## Driver Capability Matrix

Drivers without the required controls must report an explicit unsupported capability and must not silently fall back to the regular `StreamExec` RPC. Docker, Microsandbox, and BoxLite now satisfy the command-interaction contract through their native streaming exec APIs.

| driver | stdin | stdout/stderr | TTY | resize | current strategy |
|---|---:|---:|---:|---:|---|
| Docker | supported | existing basis | supported | supported | full implementation |
| Microsandbox | supported | streaming exec events | supported | supported | native streaming exec |
| BoxLite | supported | stdout/stderr callbacks | supported | supported | native C FFI execution handle |

Suggested driver extension interface:

```go
type RuntimeInteractionCapabilities struct {
    NativeExec    bool
    WrapperStream bool
    Stdin         bool
    StdinEOF      bool
    TTY           bool
    Resize        bool
    Signal        bool
    Artifacts     bool
    AgentTurns    bool
}

type RuntimeInteractor interface {
    InteractionCapabilities() RuntimeInteractionCapabilities
    OpenInteraction(ctx context.Context, session *Session, vmState VMState, spec RuntimeStartSpec) (RuntimeInteraction, error)
}
```

`BoxRuntime` can keep its existing interface unchanged at first. The daemon can detect whether a runtime implements `RuntimeInteractor` via type assertion:

```go
if interactor, ok := runtime.(RuntimeInteractor); ok {
    // use new bidirectional capability
}
```

If the runtime does not implement the interface or does not satisfy the required capability, the daemon returns a structured error.

## RuntimeInteraction Contract

The lower-level contract represents execution facts and is not tied to a specific API entrypoint.

```go
type RuntimeOperationKind string

const (
    RuntimeOperationCommand RuntimeOperationKind = "command"
    RuntimeOperationAgent   RuntimeOperationKind = "agent"
)

type RuntimeStartSpec struct {
    OperationID string
    Kind        RuntimeOperationKind
    Origin      string
    Command     *RuntimeCommandSpec
    Agent       *RuntimeAgentSpec
    Cwd         string
    Env         map[string]string
    AttachStdin bool
    TTY         bool
    Rows        uint32
    Cols        uint32
    TimeoutMs   int64
    ArtifactDir string
}

type RuntimeInteraction interface {
    Send(RuntimeInputFrame) error
    CloseSend() error
    Recv() (RuntimeOutputFrame, error)
    Wait() (RuntimeResult, error)
}
```

Input frames:

| frame | purpose |
|---|---|
| `stdin` | process stdin bytes |
| `stdin_eof` | close process stdin |
| `resize` | TTY rows/cols |
| `signal` | SIGINT/SIGTERM/KILL or driver equivalent |
| `human_message` | next user input for interactive agent prompt mode |
| `cancel` | cancel operation |

Output frames:

| frame | purpose |
|---|---|
| `started` | runtime operation has started |
| `stdout` | process stdout or merged TTY bytes |
| `stderr` | non-TTY process stderr |
| `agent_event` | structured agent message/tool/progress events |
| `agent_turn_completed` | one agent turn has completed; CLI may show the next input prompt |
| `result` | final operation result |
| `error` | protocol, driver, or runtime error |

## Docker Native Attach

`--command -it` uses Docker native exec attach.

Docker exec create must set:

```go
containerapi.ExecOptions{
    AttachStdin:  spec.AttachStdin,
    OpenStdin:    spec.AttachStdin,
    AttachStdout: true,
    AttachStderr: !spec.TTY,
    Tty:          spec.TTY,
    Cmd:          append([]string{spec.Command}, spec.Args...),
    Env:          dockerEnvList(spec.Env),
    WorkingDir:   spec.Cwd,
}
```

Data handling rules:

1. Non-TTY: split stdout/stderr with Docker stdcopy.
2. TTY: stdout/stderr are merged; project terminal bytes as stdout.
3. stdin: write CLI bytes to the hijacked connection.
4. stdin EOF: close the write direction or close attach stdin.
5. resize: call Docker exec resize API.
6. context cancel/client disconnect: close the attach connection and best-effort signal/terminate the remote exec.

TTY transcript must be marked `tty=true`; it must not pretend stderr can be split reliably.

## External Semantics for `--command -it`

Commands:

```bash
agent-compose exec <sandbox> -it bash
agent-compose run <agent> --command bash -it
```

Semantics:

1. `-i/--interactive` means attach local stdin.
2. `-t/--tty` means allocate TTY and requires `-i`.
3. `--json` is mutually exclusive with `-i/-t`.
4. Without `-i`, keep the regular server-stream behavior.
5. If the driver does not support the capability, return an explicit unsupported error.

`run --command -it` still creates a `ProjectRun`, maintains run status, writes the run transcript, and transitions to succeeded/failed/canceled when execution completes.

## External Semantics for `--prompt -it`

`run --prompt -it` is not process-level TTY. It is agent-session-level multi-turn interaction:

```text
CLI sends first human message
runtime agent SDK streams assistant/tool/progress events
runtime sends agent_turn_completed
CLI prints local input prompt
CLI sends next human message
...
CLI sends /exit or EOF
runtime sends final result
```

This path must integrate with the existing prompt runner. It must not require the user to explicitly execute the Codex CLI.

The first phase should prioritize the Codex provider because the current `CodexRunner` already has useful foundations:

1. It uses `@openai/codex-sdk`.
2. `runStreamed` produces an event stream.
3. Provider session id is already persisted.
4. The next run can `resumeThread`.

The runtime side needs to evolve from the one-shot method:

```ts
runPrompt(promptText): Promise<AgentResult>
```

into an interactive session loop:

```ts
interface AgentInteractiveRunner {
  start(): Promise<AgentSessionInfo>
  runTurn(prompt: string): AsyncIterable<AgentEvent>
  close(): Promise<AgentResult>
}
```

The Codex implementation can reuse the same thread and call `thread.runStreamed(prompt)` for each user input. After each turn completes, the runtime sends `agent_turn_completed`; only then should the daemon/CLI display the next input prompt.

Provider capability is explicit. Codex was the first implementation of the interactive turn loop; the supported set now covers every provider whose runner persists a resume thread:

| provider | prompt interactive status |
|---|---|
| Codex | supported |
| Claude | supported |
| OpenCode | supported |
| Pi | supported |
| dsh | supported |

A provider whose runner cannot resume its session between turns is deliberately excluded, because each turn spawns a fresh run and would otherwise lose the previous turn's context. If a provider does not support an agent turn loop, return an explicit error.

### `--prompt -it` and Output Schema

Output schema semantics are ambiguous in multi-turn interaction. Recommended first-phase behavior:

1. Disallow output schema with `--prompt -it`; or
2. Only allow an explicit exit command to trigger the final structured result.

To reduce complexity, the recommended first phase is to disallow output schema.

## External Connect API

Existing unary and server-stream APIs cannot carry client stdin/resize/human message. A bidirectional RPC is therefore required. Adding this RPC does not mean deprecating the existing APIs; the external interface should remain stable with clear semantic layers.

```proto
service ExecService {
  rpc Exec(ExecRequest) returns (ExecResponse);
  rpc StreamExec(ExecRequest) returns (stream StreamExecResponse);
  rpc AttachExec(stream AttachExecRequest) returns (stream AttachExecResponse);
}

service RunService {
  rpc RunAgent(RunAgentRequest) returns (RunAgentResponse);
  rpc StartAgentRun(StartAgentRunRequest) returns (StartAgentRunResponse);
  rpc StreamAgentRun(RunAgentRequest) returns (stream StreamAgentRunResponse);
  rpc AttachAgentRun(stream AttachAgentRunRequest) returns (stream AttachAgentRunResponse);
}
```

Interface roles:

| API shape | external semantics | internal target implementation |
|---|---|---|
| `Exec` / `RunAgent` | one request, one response | attach engine + output/result aggregation |
| `StreamExec` / `StreamAgentRun` | one request, continuous output, no continuous stdin | attach engine + server-stream projection |
| `AttachExec` / `AttachAgentRun` | continuous bidirectional interaction | direct attach engine projection |

`AttachExec` and `AttachAgentRun` can share similar frames:

```proto
message RuntimeAttachFrame {
  string operation_id = 1;
  uint64 seq = 2;
  oneof frame {
    RuntimeAttachStart start = 10;
    RuntimeAttachStdin stdin = 11;
    RuntimeAttachStdinEOF stdin_eof = 12;
    RuntimeAttachResize resize = 13;
    RuntimeAttachSignal signal = 14;
    RuntimeAttachHumanMessage human_message = 15;
    RuntimeAttachStdout stdout = 20;
    RuntimeAttachStderr stderr = 21;
    RuntimeAttachAgentEvent agent_event = 22;
    RuntimeAttachAgentTurnCompleted agent_turn_completed = 23;
    RuntimeAttachResult result = 24;
    RuntimeAttachError error = 25;
  }
}
```

`--command -it` uses `stdin/stdout/stderr/resize/signal/result`.

`--prompt -it` uses `human_message/agent_event/agent_turn_completed/result`.

Implementation requirements:

1. Handlers only perform protocol adaptation, permission/parameter validation, and error mapping.
2. Execution semantics belong in an internal `AttachEngine`; handlers must not call each other over RPC.
3. Server-stream projection does not support continuous stdin. It may close send immediately after start or never open stdin.
4. Unary projection must enforce an output aggregation limit. Full large output should be available through transcript/log artifacts.
5. The newly added `AttachExec` / `AttachAgentRun` endpoints are new external capabilities. Existing APIs must not be changed in a breaking way.

## CLI TTY Behavior

The CLI needs `-t/--tty` while preserving `-i/--interactive`.

Local TTY flow:

1. Validate that `-t` is only used with `-i`.
2. When `-t` is set, verify stdin/stdout are terminals.
3. Enter raw mode.
4. Get the initial terminal size and send the start frame.
5. Goroutine A: local stdin bytes -> request stdin frame.
6. Goroutine B: SIGWINCH -> resize frame.
7. Goroutine C: daemon response stdout/stderr -> local stdout/stderr.
8. Restore terminal mode on exit, error, or cancel.

`run --prompt -it` is not a raw terminal. After an agent turn completes, it should show a simple input prompt, for example:

```text
agent>
```

The user enters one line, which is sent as the next human message. `/exit` or EOF ends the session.

## File Artifacts and Double Write

Artifacts continue to use files and mounted directories. Do not introduce inline artifacts.

Execution output must be double-written:

```text
runtime output
  -> current attach/server stream response
  -> transcript/log file
```

For `logs --follow`, a third daemon-internal real-time fanout is needed:

```text
runtime output
  -> current attach/server stream response
  -> transcript/log file
  -> daemon RunLogHub subscribers
```

The file is the source of truth. The in-memory `RunLogHub` is only a real-time acceleration channel. Publish failure must not fail the run; file write failure should follow the existing execution error behavior.

The command wrapper is already a natural fit for double write. Child process output should be abstracted as a sink:

```text
child stdout chunk
  -> emit RuntimeStdout frame
  -> write stdout.txt
  -> write output.txt
  -> append stdout/output capture
```

stderr follows the same rule.

Files that must continue to be generated:

| file | compatibility requirement |
|---|---|
| `command-request.json` | keep; content may be mirrored from the new start spec |
| `command-result.json` | keep; content comes from the RuntimeResult mirror |
| `stdout.txt` | keep |
| `stderr.txt` | keep |
| `output.txt` | keep |
| `transcript.txt` | keep |
| prompt/system/schema files | keep for prompt paths |
| agent session state | continue writing provider session id |

Prompt interactive may add per-turn artifacts, but must not remove old files:

```text
state/agents/providers/codex.json
state/agents/transcript.txt
state/agents/turns/<turn-id>/prompt.txt
state/agents/turns/<turn-id>/result.json
```

## Logs Observer Stream

`logs` and `logs --follow` continue to reuse `FollowRunLogs` externally:

```proto
rpc FollowRunLogs(FollowRunLogsRequest) returns (stream RunLogChunk);
```

The internal implementation has two paths:

1. `logs`: read snapshot/tail from the file pointed to by `LogsPath`, then send a final chunk.
2. `logs --follow`: read snapshot/tail from the file first, then subscribe to the daemon-internal `RunLogHub` for subsequent real-time chunks.

Suggested internal abstractions:

```go
type RunLogPublisher interface {
    PublishRunLog(runID string, chunk RunLogChunk)
}

type RunLogSubscriber interface {
    SubscribeRunLog(runID string) (RunLogSubscription, func())
}
```

`RunLogChunk` should include at least:

1. `RunID`
2. `Data`
3. `Offset`
4. `Stream`
5. `CreatedAt`

The append function should return the offset after the write:

```go
offset, err := appendProjectRunLogChunk(logsPath, chunk)
if err == nil {
    logHub.Publish(run.RunID, RunLogChunk{Data: chunk.Text, Offset: offset})
}
```

Recommended order for `FollowRunLogs(follow=true)`:

1. Subscribe to `RunLogHub` first to avoid missing logs written during the subscription window.
2. Read backlog from the file according to `tail_lines` / `start_offset`, and record the latest offset.
3. Consume hub messages and only send messages where `msg.Offset > currentOffset` to avoid duplicates.
4. Periodically check run status. After terminal state, read the file one last time and then send a final chunk.

This design allows multiple `logs --follow` clients to observe the same run, supports late joiners, and supports resume-by-offset after disconnect. It does not consume the runtime attach stream directly because attach stream is an execution session, while logs is a multi-consumer observer stream with different lifecycle and backpressure semantics.

## State Machines

### Command Attach

```text
Created
  -> Started
  -> Running
  -> StdinClosed
  -> Completed
  -> Closed
```

On error or disconnect:

```text
Running
  -> Canceling
  -> Failed/Canceled
  -> Closed
```

### Prompt Interactive

```text
Created
  -> AgentSessionStarted
  -> WaitingInput
  -> RunningTurn
  -> TurnCompleted
  -> WaitingInput
  -> Closing
  -> Completed
```

Rules:

1. Do not accept a new human message while in `RunningTurn`, unless queued input is explicitly supported later.
2. Only after `TurnCompleted` should the daemon/CLI show the next input prompt.
3. EOF or `/exit` enters Closing.
4. Client disconnect cancels the current interactive foreground session by default.

## Compatibility and Deprecation Strategy

1. Keep regular `Exec`, `StreamExec`, `RunAgent`, `StartAgentRun`, and `StreamAgentRun` with unchanged external semantics.
2. Do not mark `StreamExec` / `StreamAgentRun` as legacy/deprecated. They are stable server-stream projections.
3. Only consider comments or `deprecated = true` for fields/APIs that are no longer used internally and are genuinely not recommended externally.
4. Do not destructively remove file artifacts.
5. The new stream result is the source of truth for the online path; old result files are mirrors and crash recovery artifacts.

## Technical Challenges

1. Docker hijack connection half-close, cancel, stdin EOF, and wait-exit ordering.
2. TTY raw mode restore, SIGWINCH resize, and Ctrl-C behavior.
3. Transcript representation after TTY merges stdout/stderr.
4. Connect bidirectional stream backpressure and runtime cleanup after either direction disconnects.
5. Accurate agent turn boundary definition in prompt interactive mode.
6. Provider SDK multi-turn capability differences, especially for Claude/OpenCode.
7. Consistency between the new stream and old artifact double-write.
8. `RunLogHub` and file offset deduplication, compensation, and backpressure.
9. Memory limits and truncation semantics for large-output unary projection.

## Recommended First-Phase Scope

1. Define the internal `RuntimeInteraction` contract and capability matrix.
2. Implement Docker native command attach.
3. Add `AttachExec` and wire `agent-compose exec <sandbox> -it ...`.
4. Add command mode for `AttachAgentRun` and wire `run --command -it`.
5. Implement prompt interactive turn loop for the Codex provider and wire `run --prompt -it`.
6. Upgrade `logs --follow` from file polling to "file snapshot plus RunLogHub real-time fanout".
7. Return explicit unsupported errors for BoxLite and non-Codex providers.

The follow-on Microsandbox implementation uses native streaming exec for command attach while preserving the same `RuntimeInteraction` and external RPC contracts.
