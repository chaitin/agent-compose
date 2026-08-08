# agent-compose Webhook / Event Ingress Current State

This document describes the external event ingress and topic event dispatch
model currently implemented in code, and records the target design still to be
completed. Sections explicitly labeled as current describe implemented behavior;
target delivery and provider verification sections remain proposals. Relevant
implementation lives mainly in:

- HTTP handler: `pkg/events/webhooks/http.go`
- Topic event model: `pkg/model/`
- SQLite store: `pkg/storage/configstore/topic_event_store.go`
- Dispatcher: `pkg/events/dispatcher.go`
- Scheduler bus: `pkg/schedulers/bus.go`
- Scheduler JS API: `pkg/schedulers/engine.go`
- Scheduler run host: `pkg/schedulers/run_host.go` with daemon adapters in `pkg/agentcompose/adapters/scheduler_host.go`

## Overall Flow

Current implementation:

```text
HTTP webhook ingress
  -> event
  -> EventDispatcher
  -> SchedulerBus
  -> scheduler.on(...)
  -> optional scheduler.event.publish(...)
  -> event
```

Webhook handler and scheduler `scheduler.event.publish(...)` both write only to
`event`, initially with `pending` status. Background `EventDispatcher` scans
pending events by sequence and publishes them to the in-process scheduler bus.
Current code lets the scheduler controller ack the event when either no scheduler
matches, or when the matching scheduler run record has been created, and marks the event as
`published_to_bus`.

`published_to_bus` only means current in-process delivery has been acknowledged.
It does not mean a matching scheduler exists, and it does not mean the scheduler
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
| Scheduler `scheduler.event.publish` | `runtime.*`, `workflow.*`, `external.*` |
| Go internal lifecycle events | `agent-compose.*` |

`scheduler.on(...)` reuses scheduler topic matching and supports exact match and
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

The endpoint still returns `202 Accepted` when no scheduler subscribes to that
topic.

After source configuration is completed, the same publish endpoint should still
be used. `webhook_source` participates in matching through topic prefix,
provider header, token, or signature. `source_id` does not need to be placed in
the URL.

### Query Events

```http
GET /api/events/:event_id
GET /api/events?correlation_id=some_system:object:123&offset=0&limit=100
GET /api/events?topic=runtime.some_adapter.requested&after_sequence=123&limit=100
GET /api/events?source=webhook&view=summary&offset=0&limit=100
GET /api/events/topics?source=webhook&offset=0&limit=100
GET /api/events/:event_id/trace
```

These queries remain on the existing HTTP event compatibility surface; they do
not add a protobuf or Connect service.

Query constraints:

- `event_id` query returns one event.
- `source` is an exact event-source filter and is sufficient on its own. The
  management UI uses `source=webhook` to browse webhook ingress without
  exposing unrelated scheduler or system events in that workflow.
- `view=summary` uses a lightweight SQLite projection and omits the raw
  payload, idempotency key, and other fields that are not needed by collection
  views. The default response remains the full compatibility representation.
- Normal list queries explicitly use `offset` and `limit`, return `total`, and sort by
  `sequence` descending so the newest events are returned first.
- `topic + after_sequence` can be used by external adapters to poll derived
  events in ascending sequence order. This compatibility mode returns
  `next_after_sequence` and cannot be combined with `offset`. For compatibility,
  omitting both parameters also uses ascending sequence order and returns
  `next_after_sequence`.
- `limit` defaults to 100 and is capped at 500.
- List queries must contain at least `source`, `correlation_id`, or `topic`.
- `/api/events/topics` requires `source` and returns distinct observed topics,
  event counts, and latest event timestamps for that source.
- `/api/events/:event_id/trace` follows up to 1,000 causal graph nodes,
  including the requested root event, and
  returns their combined scheduler deliveries, runs, scheduler events, and
  sandbox links. Descendant event records and payloads are not returned; a
  truncated causal graph is reported with `descendants_truncated=true`.

Topic polling is not message-queue semantics. Adapters must store their own last
seen sequence and implement idempotency by `event_id` or business id.

## Authentication

Configuration:

```text
WEBHOOK_BODY_LIMIT_BYTES=1048576
```

Current implementation:

- `/api/webhooks/*` bypasses UI session auth. The handler applies the
  authentication configured by the matching webhook source; an explicit
  unsigned source does not authenticate the caller.
- Each webhook source binds `topic_prefix` and authentication settings; the
  request URL topic must match an enabled source.
- Token-authenticated callers should send `Authorization: Bearer
  <source-token>` or `X-WEBHOOK-TOKEN`.
- Source token comparison uses hash + constant-time compare.
- `/api/events` and `/api/events/*` use normal API/auth path and no longer
  depend on webhook token.

GitHub sources configured with `signature_type=github_sha256` verify
`X-Hub-Signature-256` over the exact raw body when a signature secret is
configured, and accept GitHub's optional unsigned delivery when it is empty. A
configured source token remains required in unsigned mode. Both modes route
from `X-GitHub-Event`; generic token-authenticated sources, including legacy
sources with an empty signature type, retain the URL-derived topic.

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

Generic webhook bodies accept only JSON objects. `Content-Type` may be
`application/json` or a JSON media type with parameters. Native GitHub sources
also accept `application/x-www-form-urlencoded`; the handler verifies any
configured signature against the raw form body, then decodes the single
non-empty `payload` field as the JSON object. Arrays, strings, numbers, booleans,
and `null` return `400 Bad Request`.

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

`SchedulerTopicEvent` shape received by a scheduler callback:

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

A token-protected generic webhook source may set
`X-Agent-Compose-Parent-Event-ID` when it is durably forwarding an existing
event into another webhook queue. Unsigned or signature-only sources cannot set
the header. The referenced event must exist. The child inherits the parent's
correlation id when the request does not provide one; an explicitly different
correlation id is rejected. The stored child record and payload retain the
parent id so the original event's trace can include descendant Scheduler runs
and sandboxes.

`provider` for `webhook.<provider>.*` uses the second segment. Scheduler-derived
events read `provider` from the top-level payload field.

Headers keep only an allowlist:

- `content-type`
- `user-agent`
- `x-request-id`
- `x-correlation-id`
- `x-agent-compose-parent-event-id`
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
| `source` | `webhook`, `scheduler`, `system` |
| `provider` | Webhook provider or scheduler payload provider |
| `intent` | Metadata such as `notification`, `command` |
| `correlation_id` | Business flow id |
| `idempotency_key` | Idempotency key |
| `delivery_id` | Provider delivery id |
| `payload_hash` | Raw payload hash, excluding sequence |
| `payload_json` | Standard event payload |
| `dispatch_status` | Currently `pending` or `published_to_bus`; target delivery states below |
| `parent_event_id` | Upstream event for derived events |
| `publisher_type` | `webhook`, `scheduler`, `system` |
| `publisher_id` | Scheduler id and similar ids |
| `publisher_run_id` | Scheduler run id |
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
- Same `topic + idempotency_key` with an identical canonical JSON request body,
  parent Event ID, and effective correlation ID returns the existing event and
  `202 Accepted`. An omitted correlation matches only the default root delivery
  whose correlation is its own Event ID; for a parented delivery it resolves to
  the parent's correlation before comparison. Other request headers and query
  values are not part of this comparison.
- Same `topic + idempotency_key` with a different canonical JSON request body,
  parent Event ID, or effective correlation ID returns `409 Conflict`. The
  response keeps the backward-compatible code
  `idempotency_payload_mismatch` and includes the existing event metadata so an
  authorized publisher can recover the accepted delivery without creating a
  second event.
- A configured custom token header is removed before event metadata is derived
  or persisted. Headers that already define lineage or delivery identity
  (`Idempotency-Key`, `X-Correlation-ID`, `X-Request-ID`,
  `X-GitHub-Delivery`, `X-Gitlab-Event-UUID`, and
  `X-Agent-Compose-Parent-Event-ID`) are reserved and rejected as token headers.
  Runtime stripping also protects sources saved by an older release.

## Dispatcher Semantics

`EventDispatcher` is an in-process background goroutine:

1. Scan `pending` events by `sequence` ascending.
2. Decode `payload_json` into map.
3. Call `SchedulerBus.Publish(SchedulerTopicEvent)`.
4. After the scheduler controller consumes the bus event, if no scheduler
   matches or the matching scheduler run has been created, it calls event ack.
5. After ack succeeds, mark `published_to_bus` and `dispatched_at`.
6. If bus is full or publish fails, keep `pending` for the next retry.

There is currently no cross-process claim, lease, ack, consumer group, or
durable delivery. Atomic claim and lease mechanisms are needed before
multi-replica deployment.

Known unreliable windows:

- After an event is written to bus, if the process exits before the scheduler event
  loop consumes it, the event may need pending retry; if it was already acked,
  it will not be replayed automatically.
- After an event creates a scheduler run and is acked, if the process exits before
  scheduler business action completes, Event log does not know scheduler-side result.
- If an event is already published to bus but the process exits before updating
  `published_to_bus`, restart may publish it again.

Scheduler callbacks, external adapters, and business callbacks should all be
idempotent by `eventId`, `correlationId`, or business id.

### Dispatch State Completion

Event delivery state and scheduler business state need to be separated.
`dispatch_status` should not mean "business completed".

Suggested first phase: extend current event table `dispatch_status` to delivery
states:

| State | Meaning |
| --- | --- |
| `pending` | Written and waiting for dispatcher scan |
| `publishing_to_bus` | Claimed by current dispatcher and being published to bus |
| `published_to_bus` | Current in-process bus delivery acknowledged |
| `no_subscriber` | No matching scheduler; event needs no business handling |
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
multiple scheduler triggers, avoiding loss of multi-subscriber information in a
single event row:

| Field | Description |
| --- | --- |
| `event_id` | Source event |
| `scheduler_id` | Matched scheduler |
| `trigger_id` | Matched event trigger |
| `scheduler_run_id` | Created scheduler run |
| `status` | `matched`, `run_started`, `run_succeeded`, `run_failed`, `skipped` |
| `error` | Failure reason |
| `created_at` / `updated_at` | Metadata |

Suggested schema:

```sql
CREATE TABLE event_delivery (
  event_id TEXT NOT NULL,
  scheduler_id TEXT NOT NULL,
  trigger_id TEXT NOT NULL,
  scheduler_run_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(event_id, scheduler_id, trigger_id)
);

CREATE INDEX idx_event_delivery_scheduler_run ON event_delivery(scheduler_run_id);
CREATE INDEX idx_event_delivery_status ON event_delivery(status, updated_at);
```

Delivery is written as `matched` when a scheduler trigger matches, updated to
`run_started` after run creation, then updated to `run_succeeded` /
`run_failed` / `skipped` after run completion.

`published_to_bus` remains a delivery-layer state, but UI and APIs should show
delivery/run state so users do not mistake webhook delivery for successful
business processing.

### Observability And Operations

HTTP APIs for UI and troubleshooting:

```http
GET /api/events/:event_id/trace
GET /api/events/:event_id/sandboxes
POST /api/events/:event_id/replay
GET /api/webhook-sources
GET /api/webhook-sources/:source_id/stats
```

The implemented `trace` endpoint returns a lightweight root event summary and
aggregates delivery, scheduler run, scheduler event, and related sandbox data
from the root and its descendants. It does not return descendant event records
or event payloads. `sandboxes` returns only sandboxes created or operated by the
flow triggered by the event, suitable for external systems to look up
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
scheduler, run status, related sandbox, and replay entry.

## Event To Sandbox Query

The system needs the ability to find sandboxes by `event_id`. Some existing paths
can be reused:

- Webhook event payload contains `eventId` / `correlationId`.
- When event triggers a scheduler run, run `payload_json` stores the triggering
  event envelope.
- When a scheduler creates or operates sandboxes through the compatibility
  RPC bridge, it writes scheduler events with `linked_sandbox_id`.
- Scheduler-derived events write `parent_event_id` and `publisher_run_id`.
- Sandbox itself has `trigger_source=script:<scheduler_id>`, but lacks direct
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
      "relation": "created_by_scheduler_run",
      "scheduler_id": "scheduler-1",
      "scheduler_run_id": "run-1",
      "trigger_id": "on-webhook",
      "scheduler_event_id": "scheduler_event_id",
      "created_at": "2026-05-28T10:00:00Z"
    }
  ]
}
```

Existing tables can help manual troubleshooting, but are not suitable as the
main implementation path for a formal API:

- `scheduler_run.payload_json` contains the triggering event envelope, but has no
  event id index.
- `scheduler_event.linked_sandbox_id` can find sandboxes, but the related run must
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
  scheduler_id TEXT NOT NULL DEFAULT '',
  scheduler_run_id TEXT NOT NULL DEFAULT '',
  trigger_id TEXT NOT NULL DEFAULT '',
  scheduler_event_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY(event_id, sandbox_id, relation, scheduler_run_id)
);

CREATE INDEX idx_event_sandbox_link_sandbox ON event_sandbox_link(sandbox_id, created_at);
CREATE INDEX idx_event_sandbox_link_scheduler_run ON event_sandbox_link(scheduler_run_id);
```

Write timing:

- When a scheduler event trigger matches and creates a run, write
  `event_delivery(event_id, scheduler_id, trigger_id, scheduler_run_id, status=run_started)`.
- When the scheduler run host sees `linked_sandbox_id`, also write
  `event_id -> sandbox_id`.
- When a scheduler derives events, write `parent_event_id`. Query
  `GET /api/events/:event_id/sandboxes` should expand descendant events by
  `parent_event_id`, then aggregate `event_sandbox_link` for those events.
- If business wants the original webhook event to find sandboxes created by
  derived events, query layer expands descendants; descendants do not need to
  write duplicate ancestor links.
- Manual runs without `event_id` do not write this table.

## Scheduler API

Scheduler runtime provides:

```js
scheduler.event.publish(topic, payload)
```

Semantics:

- `topic` must satisfy topic policy.
- `payload` must be a JSON object.
- Only `runtime.*`, `workflow.*`, and `external.*` are allowed.
- Write Event log with `source=scheduler`, `publisher_type=scheduler`.
- Inherit `correlationId` and `parent_event_id` from the current triggering
  event.
- For manual runs with no current triggering event, if payload lacks
  `correlationId`, use the new event's own `eventId`.
- Do not call the scheduler bus directly; `EventDispatcher` performs unified dispatch.
- JS call returns `{ eventId, sequence, topic, correlationId }`.
- Unavailable during validation and returns
  `scheduler.event.publish is unavailable during validation`.

Go internal `agent-compose.*` lifecycle events still use direct
`schedulers.Bus.Publish` path and do not enter `event`. Therefore not every
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
