# chatui

A browser chat UI for agent-compose agents, built on the
[Go chat SDK](../sdk/go).

```bash
go run . -daemon http://127.0.0.1:7410
# http://127.0.0.1:7500
```

With no accounts yet, the first start creates `admin` and prints a generated
password once. `AGENT_COMPOSE_TOKEN` is sent to the daemon as a bearer token
when set.

```bash
go run . -add-user alice        # prompts for a password, no echo
go run . -set-password alice
```

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

- Signs in, and shows each person only their own conversations
- Lists past conversations by recency, with rename and delete
- Picks a project and agent, and holds a multi-turn conversation with one
- Shows what the agent is doing as it works: a collapsible trace of steps, tool
  calls and their output, reasoning, plans, retries, and token usage
- Survives a restart of this server: conversations are resumed by ID, and the
  agent keeps the context it built up
- Says so when a conversation's environment was rebuilt, rather than silently
  starting over with no context
- Reads back history, including turns that ran while nobody was watching

## The activity trace

A turn renders as two things: what the agent **did**, and what it **said**. The
trace above each answer holds the first, folded away once the turn finishes
unless the reader opened it themselves.

Work is grouped by the step number each event carries — never by what falls
between a `step_start` and a `step_end`. Providers interleave, so an event's own
step number is the only thing that says where it belongs. Events from a provider
that does not number its steps land in one shared panel rather than being
assigned a step they never claimed.

Token usage is shown per scope and **never summed across scopes**: one provider
reports a step's tokens, another repeats a running total for the whole turn, and
adding those together produces a number that means nothing. Step-scoped records
cover disjoint work and do add up; turn- and run-scoped records replace the
previous total.

A field a provider did not report is omitted rather than sent as a zero, so the
page can tell "did not happen" from "this provider never reports it".

## Where the chat list lives

On the daemon. Every run this server starts carries three labels:

| | |
|---|---|
| `chat.conversation` | the conversation's identity — what `Agent.Open` resumes |
| `chat.user` | who it belongs to |
| `chat.app` | that this app started it |

so one `ListRuns` filtered by `chat.user` **is** the chat list. The SDK's
`Client.Conversations` folds a conversation's runs together — a rebuilt one
occupies several — and reports when each was last active and whether its
environment is still alive.

This server's state file holds only the two things labels cannot: **titles**,
because a run's labels are fixed when it starts and a rename has to work without
starting one; and **conversations that have not run yet**, which have no run
behind them until their first message. Delete the file and the list comes back
from the daemon, ownership included — only the titles are gone.

## Accounts

Accounts live in the state file (`-state`, by default
`~/.agent-compose/chatui/state.json`). Passwords are stored as
PBKDF2-HMAC-SHA256 verifiers, never in the clear. Sessions are cookie-based and
live in memory, so restarting the server signs everyone out.

Each conversation belongs to one account, and ownership is enforced from the
`chat.user` label rather than from this server's own record — so it holds even
for a conversation this process has never seen. Asking for someone else's is
answered as *not found* rather than *forbidden*: that another user has a
conversation is itself none of the asker's business.

This is a sign-in, not an identity system: there are no roles, no sharing, and
no password reset beyond `-set-password`. Put it behind TLS before exposing it
past localhost — the session cookie is marked `Secure` only when the request
arrives over HTTPS.

## HTTP API

Discrete actions stay plain HTTP; only the conversation itself needs a socket.
Everything except `POST /api/login` and the page requires a session.

| | |
|---|---|
| `POST /api/login` | `{user, password}` → session cookie |
| `POST /api/logout` | drop the session |
| `GET /api/me` | who is signed in, or `null` |
| `GET /api/agents` | projects and their agents |
| `GET /api/conversations` | this user's chat list, most recent first |
| `POST /api/conversations` | `{projectId, agentName}` → a new conversation |
| `GET /api/conversations/{id}` | one conversation and its transcript |
| `PATCH /api/conversations/{id}` | `{title}` |
| `DELETE /api/conversations/{id}` | end the conversation and release its environment |
| `GET /api/conversations/{id}/socket` | the conversation, as a WebSocket |

On the socket, the browser sends `{type:"message", text}` and `{type:"stop"}`,
and receives `turn_started` (with the prompt, so a viewer that did not send it
sees the question), `event` (one SDK event), `turn_done`, `conversation` (the
updated sidebar row), `stopped`, and `error`.

A handshake is accepted only from the page this server serves; `-allow-origin`
adds others. A WebSocket handshake is not subject to the same-origin policy,
so a permissive check would let any site a viewer visits drive their
conversations.

Attached conversations live in this process's memory, and one nobody has watched
for fifteen minutes is closed — not ended. The conversation survives on the
daemon and is reopened by ID on the next visit, with its context intact.

## Limits

- **Stop ends the conversation's session, not just the turn.** The daemon
  cancels the whole interactive execution, so the next message rebuilds the
  environment; the UI reports that rather than hiding it.
- **The activity trace needs [#666](https://github.com/chaitin/agent-compose/pull/666).**
  Until it merges, only the codex provider emits structured agent events; the
  others complete their turns in one piece, and the trace stays hidden.
- **Titles are local.** Everything else about a conversation comes from the
  daemon; a title cannot, because labels are fixed when a run starts.
- **The list is paged by run, not by conversation.** `ListRuns` returns runs, and
  a conversation that has been rebuilt occupies several, so a page of 200 runs
  yields somewhere between 1 and 200 conversations.
