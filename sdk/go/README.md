# agent-compose chat SDK for Go

A client for holding a conversation with an agent-compose Agent.

```go
client, err := chat.New(chat.Config{
    BaseURL: "http://127.0.0.1:7410",
    Token:   chat.StaticToken(os.Getenv("AGENT_COMPOSE_TOKEN")),
})
if err != nil {
    return err
}
agent := client.Agent("review-project", "reviewer")

conversation := agent.Start()
defer conversation.Close()

reply, err := conversation.Send(ctx, "check this PR")
if err != nil {
    return err
}
for event, err := range reply.Events(ctx) {
    if err != nil {
        return err
    }
    if delta, ok := event.(*chat.TextDeltaEvent); ok {
        fmt.Print(delta.Text)
    }
}
```

Or, when only the answer matters:

```go
message, err := conversation.Ask(ctx, "check this PR")
```

## The model

A `Conversation` is a durable thread of turns with one Agent. `Send`
contributes a message and returns a `Reply` that streams while the agent works.
Persist `conversation.ID()`; `agent.Open(ctx, id)` resumes the thread later,
from another process or another replica.

Nothing in this package exposes the daemon's Run, Sandbox, or attach-frame
vocabulary. The SDK owns the three things the daemon deliberately does not
model: the conversation's identity, the ordering of its turns, and the
continuity of the environment they run in.

## Continuity

Turns share one environment, and that environment is what carries the agent's
context forward. When it is gone and the conversation had to be rebuilt,
`Continuity()` reports `Restarted` instead of silently starting over:

```go
if conversation.Continuity() == chat.Restarted {
    ui.Notice("this conversation's context was reset")
}
```

Losing the network is not losing the environment. A dropped stream fails the
turn in flight but keeps the conversation `Continuous`, and the next `Send`
reattaches to the same session.

## Close, not Delete

`Close()` releases this client's stream and leaves the conversation intact on
the server, so `defer conversation.Close()` is the safe default. Work already
in progress keeps running; a user who closes the browser tab mid-answer finds
the finished answer in `History()` on return. Only `Delete(ctx)` ends a
conversation and releases its environment.

## Events

`Reply.Events` yields the provider-neutral agent event model, so the same code
handles every provider:

```go
switch typed := event.(type) {
case *chat.TextDeltaEvent:   ui.Append(typed.Text)
case *chat.ToolCallEvent:    ui.ToolCard(typed.ID, typed.Name, typed.ToolKind)
case *chat.ToolResultEvent:  ui.ToolDone(typed.ID, typed.OK, typed.Output)
case *chat.UsageEvent:       meter.Add(typed.Scope, typed.InputTokens, typed.OutputTokens)
}
```

Two rules the event model carries, both measured against real provider runs:

- **`UsageEvent.Scope` differs between providers.** Some report per step, some
  per turn, some per run. Never sum records of differing scope.
- **Group tool events by `Step`, never by the `step_start`/`step_end`
  interval.** Some providers close a step before its tool events arrive.

An optional field a provider does not report stays nil rather than becoming a
zero value, so `Step == nil` and `Step` pointing at `0` stay distinguishable.
An event kind this build does not know is dropped rather than failing the turn.

Events are retained on the `Reply`: `Wait` and `Events` can be used together,
in either order, and a second `Events` pass replays from the start.

## Requirements

Conversations need **HTTP/2**, which the Connect protocol requires for
bidirectional streams. The default HTTP client handles both h2c (plaintext, as
the daemon serves it) and TLS. An HTTP/1-only proxy between the client and the
daemon will break conversations, and the SDK says so rather than failing
obscurely.

## Errors

Classify with `errors.Is` against `ErrInvalidArgument`, `ErrNotFound`,
`ErrPermission`, `ErrUnavailable`, `ErrBusy` and `ErrClosed`. `*Error` carries
the daemon's own `Code`, `Status` and `Message` for logging.

## Concurrency

A `Client` and an `Agent` are safe for concurrent use. A `Conversation` is not,
and allows one `Reply` in flight at a time; a second `Send` returns `ErrBusy`.

## Known gaps

- **`Reply.Interrupt` ends the conversation's session, not just the turn.** The
  daemon's cancel cancels the whole interactive execution, so the next `Send`
  rebuilds the environment and reports `Restarted`. A turn-scoped cancel needs
  daemon support.
- **Streaming text requires the provider-neutral agent events from
  [#666](https://github.com/chaitin/agent-compose/pull/666), which is not
  merged yet.** Until it lands, only the codex provider emits structured
  events; the others complete their turns without deltas. The interface does
  not change when it merges.
- **Listing a user's conversations is not offered.** The daemon has no
  conversation table; products store their own thread list, which they need
  anyway for titles, ownership, and ordering.

## Examples

A terminal client:

```bash
export AGENT_COMPOSE_TOKEN=optional-token
go run ./examples/chat -project my-project -agent my-agent
go run ./examples/chat -project my-project -agent my-agent -conversation conv_1a2b3c
```

A browser chat UI lives in [`chatui`](../../chatui), which is also the answer
to "why not talk to the daemon from the browser directly": a browser cannot,
because Connect needs HTTP/2 bidirectional streaming for a multi-turn session
and a `fetch()` with a streaming request body is half duplex.
