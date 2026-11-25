# Connect transport support matrix

This matrix describes the transports required by the daemon's Connect control-plane RPCs. “Unary” means request/response RPCs, “server-stream” means the daemon sends multiple responses, and “bidi” means `AttachAgentRun`/`AttachExec` style interactive calls where both sides may send messages.

| Transport | Unary | Server-stream | Bidi attach | Deployment notes |
| --- | --- | --- | --- | --- |
| Unix socket | Supported | Supported | Supported | The CLI uses the local socket when configured; no HTTP proxy is involved. |
| HTTP/1.1 | Supported | Supported with streaming response support | Not supported | Proxies must preserve chunked responses and flush each event. Interactive attach requires HTTP/2. |
| h2c ( cleartext HTTP/2 ) | Supported | Supported | Supported | Use when a trusted network or an outer tunnel provides transport protection. |
| TLS h2 ( HTTPS ) | Supported | Supported | Supported | Recommended for direct TCP exposure; terminate TLS only at a proxy that preserves HTTP/2 end to end for bidi. |

## Proxy requirements

- Disable response buffering for server streams and flush data as it arrives (for example, use the proxy's streaming/no-buffer mode).
- Set idle/read timeouts longer than the expected stream or attach session. A timeout of zero in the CLI means “wait for completion”, not “disable a proxy timeout”.
- Preserve the Connect protocol headers and HTTP/2 stream semantics. HTTP/1-only upstreams cannot carry bidi attach.

When a requested combination is unavailable, the daemon returns a Connect `CodeUnimplemented` or transport error identifying the operation. This is distinct from a successful unary call through an HTTP/1 proxy: a proxy can support status/list operations while still preventing attach.

## Resumable agent attachments

`AttachAgentRun` keeps its existing create-and-attach behavior when `AttachAgentRunStart.run_id` is empty. Set `disconnect_policy` to `DETACH` to keep that interactive run alive after the client disconnects; reconnect with another `AttachAgentRun` stream whose start frame contains the returned `run_id`. Only one attachment may send input at a time.

`StartAgentRun(interactive=true)` starts the same detached prompt session without keeping the submission request open. Ordinary `StartAgentRun` calls remain non-interactive.

A detached prompt run using the guest supervisor can be recovered after a daemon restart while its sandbox and retained execution remain available; clients can attach again once the daemon has restored the run. Its journal cursor, partial protocol frames and turn state are recovered with the committed log, including a result received before the process exit. Other attachments require a live daemon process; if their runtime handles cannot be reconstructed, attach fails closed. Persisted history remains readable. After a page refresh or channel switch, clients should read `ListRunMessages` (the conversation) and `FollowRunLogs` (the run log; pass `structured` for entries, resuming with `start_seq`) before subscribing to live attachment output.

`RunSummary.log_incomplete` reports log persistence failures or gaps in the guest journal. These do not change the execution result; output streaming and conversation messages continue. New run logs replace duplicate agent cell output and trace events; existing notebook history remains readable.

In structured run logs, tool IDs are scoped to a turn: identify a call and its result by `(turn, id)`. Patch calls include `changes` with each affected file’s path and change kind (`add`, `delete`, or `update`); the text view includes the same details. Older entries may omit these details.

## Staged run input files

`UploadRunInput` is a client-stream RPC: the client sends many frames and the daemon answers after the stream ends, except when the project already holds the same content, in which case it answers right after `info`. It works over every transport in the matrix, including HTTP/1.1, because the generated client's `CloseAndReceive` closes the send side before reading the response. A client that keeps sending chunks after a dedup response may see `Send` fail once the daemon has answered; it should stop sending and read the response. Proxies must accept a request body as large as the upload and must not buffer it beyond their limits.

Use it for files too large to send inline in `AgentInputFile.bytes`:

1. Send `info` first with `project_id`, `size_bytes`, and the lowercase hex `sha256`, then the content in `chunk` frames of at most `EngineInputLimits.max_upload_chunk_bytes` bytes. The response carries an opaque `upload_id` and `expires_at`. If the project already holds the same content, the daemon answers right after `info` with `deduplicated` set; the client should stop sending chunks and read the response.
2. Reference the file from a run with `AgentInputFile{filename, upload{upload_id}}` in `RunAgentRequest.input` or `AttachHumanMessage.input`. The daemon copies it into the sandbox workspace under `.agent-compose/uploads/<run>/turn-<n>/` and names its path in the prompt, exactly like inline bytes.

Limits come from `EngineInputLimits`: `max_upload_file_bytes` per file and `max_upload_turn_bytes` per turn, counted separately from the inline limits. Zero means the daemon cannot accept the upload source; K8s reports zero. An upload expires after `AGENT_INPUT_UPLOAD_TTL` (1 hour by default, twice the default 30-minute sandbox start budget, which includes the image pull); uploading the same content again or referencing it from a run extends it. If sandbox preparation outlives the TTL, the run fails before the agent starts, and the client must upload again and retry with a new `client_request_id`.

The upload source needs a driver that shares the sandbox workspace with the daemon (Docker and the microVM drivers do; K8s does not). `EngineInputLimits` describes the daemon's default driver, so a deployment that mixes drivers can accept a reference for a run whose driver then cannot materialize it; that run fails explicitly before the agent starts.

Errors report `effect = NOT_APPLIED` when nothing was stored: `INVALID_ARGUMENT` for a malformed stream, a size or digest mismatch, or a chunk over `max_upload_chunk_bytes`; `RESOURCE_EXHAUSTED` for a frame that exceeds the daemon's per-message limit, when the staging area or the project share is full, or when the filesystem is below its free-space floor at admission; `NOT_FOUND` for an unknown project. A run whose opening turn references an unknown, expired, or other-project upload fails with `NOT_FOUND` and `NOT_APPLIED` before the run is created, so the client can upload again and retry with the same `client_request_id`. A continuation turn with such a reference is invalid input and cancels the interactive run, like any other invalid `AttachHumanMessage.input`.
