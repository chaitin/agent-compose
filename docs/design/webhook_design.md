# agent-compose Webhook / Event Ingress Current State

Chinese version: [../zh-CN/design/webhook_design.md](../zh-CN/design/webhook_design.md)

This document describes the external event ingress and topic event dispatch
model currently implemented in code, and records the target design still to be
completed. Relevant implementation lives mainly in:

- HTTP handler: `pkg/events/webhooks/http.go`
- Topic event model: `pkg/model/`
- SQLite store: `pkg/storage/configstore/topic_event_store.go`
- Dispatcher: `pkg/events/dispatcher.go`
- Loader bus: `pkg/bus/`
- Loader JS API: `pkg/loaders/engine.go`
- Loader run host: `pkg/loaders/run_host.go` with daemon adapters in `pkg/agentcompose/adapters/loader_host.go`

## Overall Flow

Current implementation:

```text
HTTP webhook ingress
  -> event
  -> EventDispatcher
  -> LoaderBus
  -> loader scheduler.on(...)
  -> optional scheduler.event.publish(...)
  -> event
```

Webhook handler and loader `scheduler.event.publish(...)` both write only to
`event`, initially with `pending` status. Background `EventDispatcher` scans
pending events by sequence and publishes them to the in-process `LoaderBus`.
Current code lets `LoaderManager` ack the event when either no loader matches,
or when the matching loader run record has been created, and marks the event as
`published_to_bus`.

`published_to_bus` only means current in-process delivery has been acknowledged.
It does not mean a matching loader exists, and it does not mean the loader
business logic has completed.

## Topic Policy

Topics may contain only:

```text
[a-zA-Z0-9._-]+
```

Maximum length is 128. Empty values, whitespace, and `/` are not allowed.

Current prefix boundaries:

| Publisher | Allowed topic |
| --- | --- |
| External webhook ingress | `webhook.*` |
| Loader `scheduler.event.publish` | `runtime.*`, `workflow.*`, `external.*` |
| Go internal lifecycle events | `agent-compose.*` |

`scheduler.on(...)` reuses loader topic matching and supports exact match and
prefix wildcard:

```js
scheduler.on("webhook.github.push", function(event) {});
scheduler.on("webhook.github.*", function(event) {});
```

## HTTP API

### Publish Event

Current implementation:

```http
POST /api/webhooks/:topic
```

Example:

```bash
curl -X POST http://127.0.0.1:7410/api/webhooks/webhook.github.push \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: github-delivery-xxx' \
  -H 'X-Correlation-ID: github:push:xxx' \
  -d '{"ref":"refs/heads/main","repository":"agent-compose"}'
```

Successful response:

```json
{
  "accepted": true,
  "topic": "webhook.github.push",
  "event_id": "evt_xxx",
  "sequence": 123,
  "correlation_id": "github:push:xxx"
}
```

The endpoint still returns `202 Accepted` when no loader subscribes to that
topic.

After source configuration is completed, the same publish endpoint should still
be used. `webhook_source` participates in matching through topic prefix,
provider header, token, or signature. `source_id` does not need to be placed in
the URL.

### Query Events

```http
GET /api/events/:event_id
GET /api/events?correlation_id=some_system:object:123
GET /api/events?topic=runtime.some_adapter.requested&after_sequence=123&limit=100
```

Query constraints:

- `event_id` query returns one event.
- `correlation_id` query returns the event stream for the same business flow,
  sorted by `sequence` ascending.
- `topic + after_sequence` can be used by external adapters to poll derived
  events.
- `limit` defaults to 100 and is capped at 500.
- List queries must contain at least `correlation_id` or `topic`.

Topic polling is not message-queue semantics. Adapters must store their own last
seen sequence and implement idempotency by `event_id` or business id.

## Authentication

Configuration:

```text
WEBHOOK_BODY_LIMIT_BYTES=1048576
```

Current implementation:

- `/api/webhooks/*` bypasses UI session auth. The handler validates token
  according to webhook source.
- Each webhook source binds `topic_prefix` and token hash; request topic must
  match an enabled source.
- External callers should send `Authorization: Bearer <source-token>` or
  `X-WEBHOOK-TOKEN`.
- Source token comparison uses hash + constant-time compare.
- `/api/events` and `/api/events/*` use normal API/auth path and no longer
  depend on webhook token.

Current model is per-source token. Provider signature verification is needed
before exposing this to the public internet.

Target behavior:

- Webhook handler must match enabled source configuration by `:topic`.
- Token auth uses `Authorization: Bearer <source-token>` or
  `X-WEBHOOK-TOKEN: <source-token>`.
- Provider signature auth is enabled by source configuration, for example GitHub
  `X-Hub-Signature-256` or GitLab token.
- Legacy project-prefixed environment variables, table names, or headers should
  not be kept as target naming.

### Webhook Source Configuration Enhancement

Explicit webhook source configuration is needed to constrain ingress,
authentication, and UI display. Suggested new `webhook_source` table and
management API:

| Field | Description |
| --- | --- |
| `id` | Source id, for example `github-main` |
| `name` | UI display name |
| `enabled` | Whether receiving is allowed |
| `provider` | `github`, `gitlab`, `generic`, etc. |
| `topic_prefix` | Allowed topic prefix, for example `webhook.github.` |
| `token_hash` | Source-level bearer token hash, replacing plaintext storage |
| `signature_type` | `none`, `github_sha256`, `gitlab_token` |
| `signature_secret` | Provider signature secret, encrypted or managed by secret mechanism |
| `body_limit_bytes` | Source-level body limit; defaults to global limit |
| `created_at` / `updated_at` | Metadata |

The handler first finds sources where `enabled=true` and `topic_prefix` matches
`:topic`, then validates token or signature and body limit. If multiple sources
match the same topic, the request must pass exactly one source's authentication.
Otherwise return `401 Unauthorized` or `409 Conflict`, avoiding one global token
owning all topic write permissions.

## Event Envelope

Webhook body accepts only JSON objects. `Content-Type` may be `application/json`
or a JSON media type with parameters. Arrays, strings, numbers, booleans, and
`null` return `400 Bad Request`.

Webhook payload written to `event.payload_json` uses camelCase:

```json
{
  "eventId": "evt_xxx",
  "sequence": 123,
  "source": "webhook",
  "provider": "github",
  "intent": "notification",
  "method": "POST",
  "path": "/api/webhooks/webhook.github.push",
  "topic": "webhook.github.push",
  "correlationId": "github:push:xxx",
  "idempotencyKey": "github-delivery-xxx",
  "deliveryId": "provider-delivery-id",
  "remoteAddr": "127.0.0.1:12345",
  "headers": {
    "content-type": "application/json",
    "user-agent": "GitHub-Hookshot/..."
  },
  "query": {},
  "body": {
    "ref": "refs/heads/main"
  }
}
```

`LoaderTopicEvent` shape received by loader callback:

```json
{
  "topic": "webhook.github.push",
  "createdAt": "2026-05-28T10:00:00Z",
  "payload": {
    "eventId": "evt_xxx",
    "sequence": 123,
    "source": "webhook",
    "provider": "github",
    "intent": "notification",
    "correlationId": "github:push:xxx",
    "body": {
      "ref": "refs/heads/main"
    }
  }
}
```

`correlation_id` source priority:

1. `X-Correlation-ID`
2. Top-level JSON body `correlation_id`
3. Top-level JSON body `correlationId`
4. New event's own `event_id`

`provider` for `webhook.<provider>.*` uses the second segment. Loader-derived
events read `provider` from the top-level payload field.

Headers keep only an allowlist:

- `content-type`
- `user-agent`
- `x-request-id`
- `x-correlation-id`
- `x-github-event`
- `x-github-delivery`
- `x-gitlab-event`
- `x-hub-signature-256`

Sensitive headers are filtered, for example `authorization`, `cookie`,
`set-cookie`, and `x-webhook-token`.

Currently, provider signature-related headers are stored only for audit and
future extension input. Provider signature verification is not performed yet.

## Event Log

The event table is `event`, initialized by `ConfigStore.initSchema`. Go type is
`TopicEventRecord`.

Core fields:

| Field | Description |
| --- | --- |
| `sequence` | Globally increasing cursor, SQLite autoincrement |
| `id` | Event id, using `evt_<uuid>` |
| `topic` | Event topic |
| `source` | `webhook`, `loader`, `system` |
| `provider` | Webhook provider or loader payload provider |
| `intent` | Metadata such as `notification`, `command` |
| `correlation_id` | Business flow id |
| `idempotency_key` | Idempotency key |
| `delivery_id` | Provider delivery id |
| `payload_hash` | Raw payload hash, excluding sequence |
| `payload_json` | Standard event payload |
| `dispatch_status` | Currently `pending` or `published_to_bus`; target delivery states below |
| `parent_event_id` | Upstream event for derived events |
| `publisher_type` | `webhook`, `loader`, `system` |
| `publisher_id` | Loader id and similar ids |
| `publisher_run_id` | Loader run id |
| `created_at` | Unix milli |
| `dispatched_at` | Unix milli |

Target field to add:

| Field | Description |
| --- | --- |
| `replay_of_event_id` | Source event id for manual replay; empty for non-replay events |

Indexes:

- `correlation_id, sequence`
- `topic, sequence`
- `dispatch_status, sequence`
- unique index on `topic, idempotency_key`, ignoring empty idempotency keys

Idempotency rules:

- Prefer `Idempotency-Key`.
- Without `Idempotency-Key`, use provider delivery id, such as
  `X-GitHub-Delivery`.
- Without an available idempotency key, platform-level deduplication is not
  performed.
- Same `topic + idempotency_key` with identical `payload_hash` returns the
  existing event and `202 Accepted`.
- Same `topic + idempotency_key` with different `payload_hash` returns
  `409 Conflict`.

## Dispatcher Semantics

`EventDispatcher` is an in-process background goroutine:

1. Scan `pending` events by `sequence` ascending.
2. Decode `payload_json` into map.
3. Call `LoaderBus.Publish(LoaderTopicEvent)`.
4. After `LoaderManager` consumes the bus event, if no loader matches or the
   matching loader run has been created, it calls event ack.
5. After ack succeeds, mark `published_to_bus` and `dispatched_at`.
6. If bus is full or publish fails, keep `pending` for the next retry.

There is currently no cross-process claim, lease, ack, consumer group, or
durable delivery. Atomic claim and lease mechanisms are needed before
multi-replica deployment.

Known unreliable windows:

- After an event is written to bus, if the process exits before the loader event
  loop consumes it, the event may need pending retry; if it was already acked,
  it will not be replayed automatically.
- After an event creates a loader run and is acked, if the process exits before
  loader business action completes, Event log does not know loader-side result.
- If an event is already published to bus but the process exits before updating
  `published_to_bus`, restart may publish it again.

Loader callbacks, external adapters, and business callbacks should all be
idempotent by `eventId`, `correlationId`, or business id.

### Dispatch State Completion

Event delivery state and loader business state need to be separated.
`dispatch_status` should not mean "business completed".

Suggested first phase: extend current event table `dispatch_status` to delivery
states:

| State | Meaning |
| --- | --- |
| `pending` | Written and waiting for dispatcher scan |
| `publishing_to_bus` | Claimed by current dispatcher and being published to bus |
| `published_to_bus` | Current in-process bus delivery acknowledged |
| `no_subscriber` | No matching loader; event needs no business handling |
| `retrying` | This publish or ack attempt failed; waiting for retry |
| `dead_letter` | Retry exhausted or payload cannot be decoded; needs manual handling |

Delivery state completion needs these fields so multiple processes or retries
are not judged only by memory state:

| Field | Description |
| --- | --- |
| `claim_id` | Current claim token; empty means unclaimed |
| `claim_until` | Claim expiry time, Unix milli |
| `attempt_count` | Dispatcher delivery attempt count |
| `next_attempt_at` | Next retry time, Unix milli |
| `last_error` | Last delivery error |
| `dead_letter_at` | Dead letter time, Unix milli |

Dispatcher scan condition should become:
`dispatch_status IN ('pending', 'retrying') AND next_attempt_at <= now`, with
atomic claim through a single conditional update. After claim expiry, other
processes may claim again.

Add `event_delivery` table to represent one event's processing result for
multiple loader triggers, avoiding loss of multi-subscriber information in a
single event row:

| Field | Description |
| --- | --- |
| `event_id` | Source event |
| `loader_id` | Matched loader |
| `trigger_id` | Matched event trigger |
| `run_id` | Created loader run |
| `status` | `matched`, `run_started`, `run_succeeded`, `run_failed`, `skipped` |
| `error` | Failure reason |
| `created_at` / `updated_at` | Metadata |

Suggested schema:

```sql
CREATE TABLE event_delivery (
  event_id TEXT NOT NULL,
  loader_id TEXT NOT NULL,
  trigger_id TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(event_id, loader_id, trigger_id)
);

CREATE INDEX idx_event_delivery_run ON event_delivery(run_id);
CREATE INDEX idx_event_delivery_status ON event_delivery(status, updated_at);
```

Delivery is written as `matched` when loader trigger matches, updated to
`run_started` after run creation, then updated to `run_succeeded` /
`run_failed` / `skipped` after run completion.

`published_to_bus` remains a delivery-layer state, but UI and APIs should show
delivery/run state so users do not mistake webhook delivery for successful
business processing.

### Observability And Operations

Suggested HTTP APIs or Connect APIs for UI and troubleshooting:

```http
GET /api/events/:event_id/trace
GET /api/events/:event_id/sandboxes
POST /api/events/:event_id/replay
GET /api/webhook-sources
GET /api/webhook-sources/:source_id/stats
```

`trace` returns event, parent/child events, delivery, loader run, loader event,
and related sandbox. `sandboxes` returns only sandboxes created or operated by
the flow triggered by the event, suitable for external systems to look up
sandboxes by `event_id`. The implemented
`GET /api/events/:event_id/sessions` route is a compatibility alias for the
same sandbox query.

`replay` should not rewrite the original event or reuse the original event id.
It should create a new event, inherit original payload, and write
`replay_of_event_id`, a new `event_id`, new `sequence`, and optional replay
reason. This avoids idempotency-key conflict with original provider delivery and
allows trace to distinguish original delivery from manual replay.

Metrics to expose:

- Pending event count and maximum wait time.
- Dispatcher last success time, failure count, bus-full count.
- Receive/reject/2xx/4xx/5xx counts grouped by topic/source/provider.
- Signature verification failures, idempotency conflicts, body-too-large.
- Latency from event to run_started and run_completed.
- Dead letter count and latest errors.

UI should add a "Webhook Events" view under automation/runs: filter by
source/topic/status and display event id, correlation id, delivery id, matched
loader, run status, related sandbox, and replay entry.

## Event To Sandbox Query

The system needs the ability to find sandboxes by `event_id`. Some existing paths
can be reused:

- Webhook event payload contains `eventId` / `correlationId`.
- When event triggers a loader run, run `payload_json` stores the triggering
  event envelope.
- When loader creates or operates sandboxes through the v1-compatible session
  RPC bridge, it writes loader events with `linked_sandbox_id`.
- Loader-derived events write `parent_event_id` and `publisher_run_id`.
- Sandbox itself has `trigger_source=script:<loader_id>`, but lacks direct
  event/run relation.

Suggested query semantics:

```http
GET /api/events/:event_id/sandboxes
```

Response:

```json
{
  "event_id": "evt_xxx",
  "correlation_id": "github:push:xxx",
  "sandboxes": [
    {
      "sandbox_id": "sandbox_xxx",
      "relation": "created_by_loader_run",
      "loader_id": "loader-1",
      "run_id": "run-1",
      "trigger_id": "on-webhook",
      "loader_event_id": "loader_event_id",
      "created_at": "2026-05-28T10:00:00Z"
    }
  ]
}
```

Existing tables can help manual troubleshooting, but are not suitable as the
main implementation path for a formal API:

- `loader_run.payload_json` contains the triggering event envelope, but has no
  event id index.
- `loader_event.linked_sandbox_id` can find sandboxes, but the related run must
  be known first.
- `correlation_id` may cover multiple derived events and runs; it is only a
  trace helper and cannot be used alone as an exact relation.
- Therefore `GET /api/events/:event_id/sandboxes` should not rely on full-table
  JSON scan as the main implementation.

Formal implementation needs explicit relation tables. Event-to-run relation is
stored in `event_delivery`; event-to-sandbox relation is stored in
`event_sandbox_link`:

```sql
CREATE TABLE event_sandbox_link (
  event_id TEXT NOT NULL,
  sandbox_id TEXT NOT NULL,
  relation TEXT NOT NULL,
  loader_id TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  trigger_id TEXT NOT NULL DEFAULT '',
  loader_event_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY(event_id, sandbox_id, relation, run_id)
);

CREATE INDEX idx_event_sandbox_link_sandbox ON event_sandbox_link(sandbox_id, created_at);
CREATE INDEX idx_event_sandbox_link_run ON event_sandbox_link(run_id);
```

Write timing:

- When loader event trigger matches and creates a run, write
  `event_delivery(event_id, loader_id, trigger_id, run_id, status=run_started)`.
- When `loaderRunHost.CallSessionRPC` sees `linked_sandbox_id`, also write
  `event_id -> sandbox_id`.
- When loader derives events, write `parent_event_id`. Query
  `GET /api/events/:event_id/sandboxes` should expand descendant events by
  `parent_event_id`, then aggregate `event_sandbox_link` for those events.
- If business wants the original webhook event to find sandboxes created by
  derived events, query layer expands descendants; descendants do not need to
  write duplicate ancestor links.
- Manual runs without `event_id` do not write this table.

## Loader API

Loader runtime provides:

```js
scheduler.event.publish(topic, payload)
```

Semantics:

- `topic` must satisfy topic policy.
- `payload` must be a JSON object.
- Only `runtime.*`, `workflow.*`, and `external.*` are allowed.
- Write Event log with `source=loader`, `publisher_type=loader`.
- Inherit `correlationId` and `parent_event_id` from the current triggering
  event.
- For manual runs with no current triggering event, if payload lacks
  `correlationId`, use the new event's own `eventId`.
- Do not call `LoaderBus` directly; `EventDispatcher` performs unified dispatch.
- JS call returns `{ eventId, sequence, topic, correlationId }`.
- Unavailable during validation and returns
  `scheduler.event.publish is unavailable during validation`.

Go internal `agent-compose.*` lifecycle events still use direct
`LoaderBus.Publish` path and do not enter `event`. Therefore not every
`agent-compose.*` event can be queried through `/api/events`.

## Error Responses

| Condition | Response |
| --- | --- |
| No matching webhook source | `404 Not Found` |
| Missing or invalid token | `401 Unauthorized` |
| Empty, invalid, or disallowed topic prefix | `400 Bad Request` |
| `Content-Type` is not JSON | `415 Unsupported Media Type` |
| Body is not a valid JSON object | `400 Bad Request` |
| Body exceeds size limit | `413 Payload Too Large` |
| Duplicate idempotency key with different payload hash | `409 Conflict` |
| Event log write failure | `500 Internal Server Error` |
| `GET /api/events/:event_id` event not found | `404 Not Found` |
| Invalid query params or missing query boundary | `400 Bad Request` |
