# agent-compose chat SDK for Go

A client for holding a conversation with an agent-compose Agent.

```go
client, err := chat.New(chat.Config{
    BaseURL: "http://127.0.0.1:7410",
    Token:   chat.StaticToken(os.Getenv("AGENT_COMPOSE_AUTH_TOKEN")),
})
if err != nil {
    return err
}
agent := client.Agent("review-project", "reviewer")

conversation := agent.Start()
defer func() { _ = conversation.Close(ctx) }()

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

## Finding an agent

`Agent` takes a project ID and an agent name. `Projects` is how a product
learns them, so a chooser does not need them configured out of band:

```go
projects, err := client.Projects(ctx)
for _, project := range projects {
    for _, agent := range project.Agents {
        if agent.Available {
            fmt.Println(project.ID, agent.Name, agent.DisplayName)
        }
    }
}
```

An agent that is disabled, or whose configuration did not validate, is listed
with `Available` false and a `Unavailable` reason rather than hidden: someone
looking for it should see that it exists.

## The model

A `Conversation` is a durable thread of turns with one Agent. `Send`
contributes a message and returns a `Reply` that streams while the agent works.
Persist `conversation.ID()`; `agent.Open(ctx, id)` resumes the thread later,
from another process or another replica.

Nothing in this package exposes the daemon's Run, Sandbox, or attach-frame
vocabulary. The SDK owns the three things the daemon deliberately does not
model: the conversation's identity, the ordering of its turns, and the
continuity of the environment they run in.

## Finding conversations again

`Open` needs an ID, which a product normally already has against its own thread
record. Two questions come up when it does not, and they cost very different
things.

**Is this conversation this user's?** `Lookup` answers in one call, because the
run list's label filter answers it without reading a single label back:

```go
conversation := agent.Start(chat.WithLabels(map[string]string{"user": "alice"}))
...
info, ok, err := client.Lookup(ctx, id, map[string]string{"user": "alice"})
```

`ok` is false when no run carries both labels — the conversation does not exist,
or is not hers, and the caller cannot tell which. That is the right answer for
an authorization check, and it means a product does not have to trust its own
record of who owns what.

**Which conversations does this user have?** `Conversations` answers it, but a
run summary deliberately carries no labels — they belong to a run's detail — so
discovering which conversation each run belongs to costs one read per run:

```go
found, err := client.Conversations(ctx, chat.Search{
    Labels: map[string]string{"user": "alice"},
    Limit:  500, // a budget over the whole walk; omit it to read every match
})
if err != nil && !errors.Is(err, chat.ErrIncomplete) {
    return err
}
```

An unbounded search walks every matching run, one detail read each, so on a
long-lived daemon it costs one request per run. `Search.Limit` is how you refuse
to pay that, and a search that runs out of budget returns what it found
alongside `ErrIncomplete` — a subset is never passed off as the whole list.

Keep your own index of the IDs you created and call `Open` directly; use this to
rebuild that index, not to draw a list on every page load. A conversation that
has been rebuilt occupies several runs and is folded into one entry described by
the most recent, with `Live` reporting whether that environment is still up.

What comes back either way is only what the server holds. A title, an unread
marker, or anything else a product invents about a conversation stays the
product's to keep: run labels are fixed when a run starts, so they cannot carry
a name that has to be changeable.

## Continuity

Turns share one environment, and that environment is what carries the agent's
context forward. A run ending stops that environment, but stopping is not
losing it: the sandbox keeps the workspace and the directories a provider
stores its session in, so every new run asks the daemon to resume the sandbox
the conversation last had rather than starting from nothing. `Continuity()` reports `Restarted`
only once that resume actually fails to happen, not merely because a new run
was needed:

```go
if conversation.Continuity() == chat.Restarted {
    ui.Notice("this conversation's context was reset")
}
```

Losing the network is not losing the environment. A dropped stream fails the
turn in flight but keeps the conversation `Continuous`, and the next `Send`
reattaches to the same session.

## Ending a session

`Close(ctx)` releases everything the handle is holding: its stream, and the run
behind it, whose environment the daemon then stops. The conversation itself
survives — its durable history and identity are untouched, `Lookup` and
`Conversations` keep finding it, and `Agent.Open` resumes it later.

Closing has to end the run. A conversation attaches with the detach disconnect
policy, because a turn must survive the browser tab that started it going
away; the cost is that a dropped stream leaves the run — and the sandbox it
holds — alive, and nothing on the daemon side expires it. A `Close` that only
dropped the stream would leave one running environment per conversation ever
opened. `defer conversation.Close(ctx)` is the right default precisely because
it does not.

Resuming after a close asks the daemon to reuse the conversation's last
sandbox. `Continuity` reports `Continuous` when that succeeds and `Restarted`
when the sandbox is gone and a replacement is built. Nothing here makes a
conversation's history or identity irrecoverable.

`Client.EndSession(ctx, id)` does the same thing addressed by ID, for a
conversation no handle of yours is attached to — after a restart, say, when
your own sessions are gone but the daemon's runs are not. It finds the run
through the label every conversation's runs carry. Ending a session that has
already ended is not an error.

A handle that never attached holds no run, so closing it is free, and in
particular it cannot end a run some other handle is holding — which matters
when two handles race to attach to the same conversation and the loser is
discarded.

## Reading history

`History(ctx)` fetches every message the conversation has ever had, which is
fine for a short thread but costs one request per page of every run the
conversation has occupied. `HistoryPage` fetches events in bounded pages and
walks backward from the most recent message; it still discovers the
conversation's run list first, so a conversation with many historical runs is
not free to open:

```go
page, err := conversation.HistoryPage(ctx, chat.HistoryOptions{Limit: 30})
// page.Messages is the 30 most recent, oldest first.
older, err := conversation.HistoryPage(ctx, chat.HistoryOptions{Limit: 30, Before: page.Cursor})
// page.Cursor is "" once there is nothing older.
```

Use `HistoryPage` for a chat UI that loads recent messages first and fetches
further back only when the reader scrolls up; `History` remains the
convenience for a caller that genuinely wants the whole thread at once.

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
- **A `StepEndEvent` with `Scope == StepEndScopeRun` closes the turn/run, not
  an individual step.** Consumers pairing step boundaries must ignore it.

An optional field a provider does not report stays nil rather than becoming a
zero value, so `Step == nil` and `Step` pointing at `0` stay distinguishable.
Event kinds this build does not know are exposed as `RawEvent`, preserving the
daemon's name, display text, and payload so provider extensions are not lost.

Events are retained on the `Reply`: `Wait` and `Events` can be used together,
in either order, and a second `Events` pass replays from the start.

## Requirements

Go 1.24 or newer. The SDK depends on `connectrpc.com/connect` and on
`github.com/chaitin/agent-compose/proto`, the wire contract as its own module —
so taking this SDK does not mean taking the daemon's dependencies. Requests are
binary Protobuf over Connect; the daemon accepts JSON too, and
`connect.WithProtoJSON()` on a hand-built client is how you would ask for it,
usually only to read a capture.

Conversations need **HTTP/2**, which the Connect protocol requires for
bidirectional streams. The default HTTP client handles both h2c (plaintext, as
the daemon serves it) and TLS. An HTTP/1-only proxy between the client and the
daemon will break conversations, and the SDK says so rather than failing
obscurely.

## Errors

Classify with `errors.Is` against `ErrInvalidArgument`, `ErrNotFound`,
`ErrPermission`, `ErrUnavailable`, `ErrConflict`, `ErrBusy`, `ErrClosed` and
`ErrIncomplete`. `*Error` carries the daemon's own `Code`, `Status` and
`Message` for logging.

`ErrIncomplete` is the one that comes back with a usable result: `Conversations`
returns it when `Search.Limit` ran out before the matching runs did, so the list
is real but partial.

`ErrConflict` is the daemon refusing to hand over something it gives to one
holder at a time, a run's input being the one this SDK meets: reattaching just
after losing a stream can arrive before the previous attachment has let go.
Retrying shortly is usually right.

A `Reply` that fails because the stream broke leaves the turn's **outcome
unknown**: the run was asked to survive its viewer, so the agent may well have
finished. Resending the same text is how to recover, and resending it
immediately does not record the message twice — it travels under the identity
the lost turn already had. What it does not yet prevent is the agent working
through that message a second time; see Known gaps.

## Concurrency

A `Client` and an `Agent` are safe for concurrent use. A `Conversation` is not,
and allows one `Reply` in flight at a time; a second `Send` returns `ErrBusy`.

## Known gaps

- **`Reply.Interrupt` ends the conversation's session, not just the turn.** The
  daemon's cancel cancels the whole interactive execution, so the next `Send`
  needs a new run — which asks to resume the same sandbox, same as any other
  run boundary. A turn-scoped cancel needs daemon support.
- **Provider event vocabularies differ.** The SDK has typed variants for the
  provider-neutral schema and treats the legacy generic `output` event as a
  text delta. Other provider-defined Attach events are returned as `RawEvent`;
  the daemon does not rewrite them for a particular provider.
- **A detached run never expires on its own.** The daemon keeps a run alive
  when its client disconnects and has no idle timeout for one, so a process
  that exits without closing its conversations leaves a run — and a sandbox —
  behind for each of them. Close them on the way out; recovering the ones a
  crash left behind means finding them through their labels. `Close` covers the
  run whose start frame had not arrived yet by looking it up under the
  conversation's label, but a `Close` fast enough to beat the daemon's own
  creation of that run finds nothing to stop — closing that sliver means the
  daemon not keeping a run whose client left before the handshake finished.
- **Resending a lost turn is deduplicated in history, not in execution.** The
  message carries a client frame ID the daemon keys its persisted identity on,
  so the resend is recorded once. The daemon still hands the message to the
  agent again, so the turn can run twice; suppressing that needs the daemon to
  stop forwarding a human message it has already recorded. The exception is a
  run's **opening** turn, whose message is recorded under an identity derived
  from the run rather than from a frame ID: resending that one does duplicate.
  A caller that cares reads `History` before resending.
- **`History` and `HistoryPage` return only user and assistant text.** Tool
  calls, reasoning, usage, and every other event kind are turn-scoped and not
  durable messages; replaying a turn's full activity trace after a reload
  needs the live stream, not history.

## Examples

A terminal client:

```bash
export AGENT_COMPOSE_AUTH_TOKEN=optional-token
go run ./examples/chat -project my-project -agent my-agent
go run ./examples/chat -project my-project -agent my-agent -conversation conv_1a2b3c
```

A browser chat UI lives in [`chatui`](../../chatui), which is also the answer
to "why not talk to the daemon from the browser directly": a browser cannot,
because Connect needs HTTP/2 bidirectional streaming for a multi-turn session
and a `fetch()` with a streaming request body is half duplex.
