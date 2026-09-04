# chatui

A browser chat UI for agent-compose agents, built on the
[Go chat SDK](../sdk/go).

```bash
go run . -daemon http://127.0.0.1:7411
# http://127.0.0.1:7500
```

`AGENT_COMPOSE_TOKEN` is sent as a bearer token when set.

## Why a server sits in the middle

A browser cannot hold a conversation with the daemon directly. Connect needs
HTTP/2 bidirectional streaming for a multi-turn session, and a browser
`fetch()` with a streaming request body is **half duplex** — no response bytes
arrive while the request body is still open, which is exactly the state a
multi-turn session lives in.

So the conversation stays on the Go side, where the SDK handles the
bidirectional stream, and the browser gets the one-way stream it can consume:

```
browser ──POST + server-sent events──> chatui ──Connect bidi (h2c)──> daemon
```

The browser therefore never encodes a protocol frame, never threads a sandbox
ID between turns, and never tracks session lifetime. Those belong to the SDK.

## What it does

- Picks a project and agent, and holds a multi-turn conversation with one
- Streams text, tool calls and results, token usage, and retries as they happen
- Survives a restart of this server: conversations are resumed by ID, and the
  agent keeps the context it built up
- Says so when a conversation's environment was rebuilt, rather than silently
  starting over with no context
- Reads back history, including turns that ran while nobody was watching

## HTTP API

| | |
|---|---|
| `GET /api/agents` | projects and their agents |
| `POST /api/conversations` | `{projectId, agentName, resume?}` → `{id, continuity}` |
| `POST /api/conversations/{id}/messages` | `{text}` → `text/event-stream` |
| `POST /api/conversations/{id}/stop` | interrupt the turn in flight |
| `GET /api/conversations/{id}/history` | every message, oldest first |
| `DELETE /api/conversations/{id}` | end the conversation and release its environment |

Conversations live in this process's memory only. A real product would store
each conversation ID against its own thread record and reopen it on demand,
which is what `resume` is for.

## Limits

- **Stop ends the conversation's session, not just the turn.** The daemon
  cancels the whole interactive execution, so the next message rebuilds the
  environment; the UI reports that rather than hiding it.
- **Streaming text needs [#666](https://github.com/chaitin/agent-compose/pull/666).**
  Until it merges, only the codex provider emits structured agent events; the
  others complete their turns without deltas.
