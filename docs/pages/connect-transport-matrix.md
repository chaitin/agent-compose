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

Attachments are resumable only while the daemon process remains alive. After a daemon restart, persisted run events and logs remain readable, but runtime input/output handles cannot be reconstructed and attach fails closed. After a page refresh or channel switch, clients should read `ListRunEvents`/`FollowRunLogs` before subscribing to live attachment output.
