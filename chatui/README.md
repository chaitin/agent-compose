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
bidirectional stream. The browser keeps a duplex connection of its own, in the
shape it can actually open:

```
browser ──WebSocket──> chatui ──Connect bidi (h2c)──> daemon
```

Duplex the whole way, because a conversation is duplex the whole way. The
browser still never encodes a protocol frame, never threads a sandbox ID
between turns, and never tracks session lifetime — those belong to the SDK.

Keeping the browser connected, rather than streaming one turn per request,
is what makes the rest work:

- **A turn nobody here started still shows up.** Once a conversation outlives
  the page that opened it, the turn in flight is often one this browser did not
  send — from a phone, or from before a refresh. Viewers render from the socket,
  not from what they just typed.
- **Several viewers share one conversation.** A laptop and a phone on the same
  conversation both watch the same turn live.
- **Leaving is a signal.** A closed socket is how this server learns nobody is
  watching, which is what lets it release the conversation.

There is no fan-out machinery behind this: every viewer ranges over the same
`chat.Reply`, which retains its events and replays them for each caller, so a
viewer joining mid-turn sees that turn from its beginning.

## What it does

- Picks a project and agent, and holds a multi-turn conversation with one
- Streams text, tool calls and results, token usage, and retries as they happen
- Survives a restart of this server: conversations are resumed by ID, and the
  agent keeps the context it built up
- Says so when a conversation's environment was rebuilt, rather than silently
  starting over with no context
- Reads back history, including turns that ran while nobody was watching

## HTTP API

Discrete actions stay plain HTTP; only the conversation itself needs a socket.

| | |
|---|---|
| `GET /api/agents` | projects and their agents |
| `POST /api/conversations` | `{projectId, agentName, resume?}` → `{id, continuity}` |
| `GET /api/conversations/{id}/socket` | the conversation, as a WebSocket |
| `GET /api/conversations/{id}/history` | every message, oldest first |
| `DELETE /api/conversations/{id}` | end the conversation and release its environment |

On the socket, the browser sends `{type:"message", text}` and `{type:"stop"}`,
and receives `turn_started` (with the prompt, so a viewer that did not send it
sees the question), `event` (one SDK event), `turn_done`, `stopped`, and
`error`.

A handshake is accepted only from the page this server serves; `-allow-origin`
adds others. A WebSocket handshake is not subject to the same-origin policy,
so a permissive check would let any site a viewer visits drive their
conversations.

Conversations live in this process's memory only, and one nobody has watched
for fifteen minutes is closed — not ended. The conversation survives on the
daemon, and `resume` reopens it with its context intact. A real product would
store each conversation ID against its own thread record and reopen it on
demand.

## Limits

- **Stop ends the conversation's session, not just the turn.** The daemon
  cancels the whole interactive execution, so the next message rebuilds the
  environment; the UI reports that rather than hiding it.
- **Streaming text needs [#666](https://github.com/chaitin/agent-compose/pull/666).**
  Until it merges, only the codex provider emits structured agent events; the
  others complete their turns without deltas.
