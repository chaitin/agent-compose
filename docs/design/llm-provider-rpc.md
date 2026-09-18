# LLM provider management RPC

## Problem and scope

The daemon already persists upstream providers and routes literal provider/model
references, but models.json is startup-only and no public RPC manages providers.
Add live CRUD to agentcompose.v2.LLMService for upstream model endpoints and
credentials. Coding-agent providers such as codex and pi are unrelated.

## Contract

CreateProvider, GetProvider, ListProviders, UpdateProvider and DeleteProvider use
an immutable caller-chosen ID. IDs are 1–128 ASCII letters, digits, dots,
underscores or hyphens, beginning with a letter or digit. The environment-owned
IDs default and anthropic are reserved; session-env IDs cannot match this syntax.

Create requires an absolute HTTP(S) base URL, a supported protocol, and a nonempty
literal API key. Responses contain api_key_set, never the credential. Update
applies explicit fields only; omitted name, base_url, protocol, api_key, auth,
and enabled preserve stored values. Absent api_key preserves the current key
atomically, present nonempty rotates it, and present empty is invalid. An absent
enabled field means true on create and is preserved on update. An empty name
defaults to the ID on create and is preserved on update. Anthropic Messages
providers send anthropic-version: 2023-06-01 by default. Per-model overrides,
custom headers, and default-model management remain outside this change.

Protocol selects the default upstream credential presentation: Bearer for the
OpenAI protocols and x-api-key for Anthropic Messages. A gateway can serve the
Anthropic Messages wire protocol while authenticating with a bearer token, so a
connection may override that presentation with auth = bearer or auth = x-api-key;
the override changes only the header, not the protocol or endpoint. Responses
report the stored override rather than the effective header, so a Get response
can be sent back through Update without freezing the protocol convention into an
override; a connection with no stored override carries the unspecified
presentation. Every write path records an override only when the presentation
differs from the protocol in effect: the migration and the environment bootstrap
infer that difference from a stored header, and the RPC create and update paths
measure the presentation the request names against the same protocol. Naming the
convention therefore stores nothing, so a later protocol change refreshes the
header instead of pinning it, while a presentation that does differ is stored and
carried forward even when a later update changes the protocol. Because the spec's
auth field is optional, an absent
auth preserves the stored override even when the same update changes protocol,
while an explicit unspecified auth clears the override and returns the connection
to the protocol convention. An unknown presentation is rejected rather
than falling back to the protocol default, because silently keeping x-api-key
would resurface as an unexplained upstream 401. The environment-bootstrap path
already chooses the same two presentations through ANTHROPIC_AUTH_TOKEN (Bearer)
and ANTHROPIC_API_KEY or LLM_API_KEY (x-api-key).

CLI `agent-compose llm provider` exposes ls, create, inspect, update, and rm.
Create requires --base-url, --protocol, and --api-key. Update sends only flags
that were set. `--auth x-api-key|bearer` overrides the protocol default on either
command. Environment bootstrap remains last fallback; API CRUD takes effect on
the next target resolution.

List includes disabled API-owned providers in ID order, using the existing
offset/limit pagination convention. Get/Update/Delete reject non-API ownership.
Duplicate IDs return AlreadyExists; missing IDs return NotFound; ownership
conflicts return FailedPrecondition; invalid configuration returns InvalidArgument.
There is no upsert, rename or ownership-transfer operation. Concurrent creates
have one winner; updates use atomic SQL replacement (last completed write wins),
and omitted keys are preserved in SQL rather than through a read/write race.

## Ownership and persistence

Use scope=api in the existing llm_provider table. No schema migration is needed.
Catalog synchronization affects only catalog scope, and existing collision checks
reject models.json entries that collide with API-owned IDs. Restart therefore
preserves API-owned configuration. Environment bootstrap cannot overwrite it
because its fixed IDs and session prefix are unavailable to API creation.

API keys follow the existing provider storage contract: application-level
plaintext in data.db, never returned by the management RPCs. The existing daemon
API authentication boundary applies to these methods; this change does not add
a separate provider authorization system.

Delete runs a transaction removing provider-bound facade tokens, model bindings
and the provider. Shared model identities are not removed. An old facade token
cannot become valid when an ID is reused. References in project definitions are
not rewritten, and in-flight upstream requests are not canceled. A disabled
provider remains stored and can be re-enabled; disabling does not revoke tokens.

## Runtime behavior

No resolver fork is introduced. API providers are configured connections under
the existing scope rules, and target resolution is one staged pipeline:

1. An explicit provider reference (the `<connection>/<model>` form) selects that
   connection and passes the literal model to the upstream. Literal models do not
   require model-table registration.
2. A registered model with a provider binding selects its bound connection.
3. Otherwise the daemon's default connection serves the literal model. The
   reserved bootstrap connection (`default`/`anthropic`) wins, including when it
   survives only as a persisted env-default row; with none, the only configured
   connection of the requested family is used. When the requested family has no
   connection at all, the same choice runs over the other families, because the
   runtime bridge translates across protocols: an OpenAI-compatible connection
   can serve claude, exactly as the OpenAI bootstrap environment always could. A
   session-env connection never acts as a daemon default. Competing connections
   are reported as an ambiguity instead of being resolved by accident. A bare
   model does not imply a family, so the reserved connection is chosen without a
   family comparison; qualify the model as `<connection>/<model>` to select a
   different connection explicitly.

Model bindings are optional metadata rather than an authorization boundary, so a
provider created through this RPC is usable with a bare Agent model name and no
models.json entry. The facade agents (pi, opencode, dsh) treat the
`<connection>/<model>` prefix as optional for the same reason; codex and claude
already accepted unqualified model names. Prefixed values keep their established
meaning: a configured connection id, a family alias, or an env-backed custom
endpoint.

A connection's protocol decides which agents it can serve, because the runtime
facade bridges only some protocol pairs. An OpenAI `responses` connection serves
every facade agent. An OpenAI `chat_completions` connection serves codex, pi,
opencode, and dsh but not claude: no bridge converts an Anthropic Messages
request into OpenAI Chat, so the run fails with `unsupported llm protocol bridge
from "anthropic_messages" to "openai_chat"`. Give claude a `responses` or an
`anthropic_messages` connection.

The facade publishes the model it resolved as `AGENT_COMPOSE_RESOLVED_MODEL`,
already rewritten into the namespace the guest addresses models by, and the
daemon tells the guest runner that value instead of the model the agent
declared. A declaration is a request that resolution may rewrite: a
`<connection>/<model>` prefix is stripped, a catalog or bootstrap default
supplies a model the agent omitted, and pi and opencode address models through
the provider key written into their config. The runner passes the published
reference through untouched — no guest runtime strips or re-adds a prefix — so a
resolved model id that itself contains slashes reaches the upstream intact. An
agent CLI told the declaration instead addresses a model the facade token is not
bound to.

The next target resolution reads current provider settings, so address/key
updates require no restart. Existing in-flight requests use their resolved
configuration; agent-side model/protocol setup may require restarting a run after
protocol changes. API CRUD does not change global or catalog defaults. Unknown
slash prefixes reaching the runtime LLM facade retain the existing literal-model
interpretation; clients should verify provider existence when constructing a new
reference after deletion.

## Validation

Domain tests cover ID/protocol/URL/key/auth validation and input ownership, and
the resolution stages above: configured-connection defaulting, reserved-default
preference, ambiguity rejection, disabled-connection exclusion, binding
precedence, unqualified model names for pi, opencode and dsh, and rejection of a
reference with an empty `<connection>/<model>` side. SQLite
integration tests cover literal routing, bare-model routing with an RPC-created
provider, key preservation/rotation, protocol mapping, the protocol-default and
explicit credential presentations, presentation-only updates, disabled providers,
restart/catalog coexistence, collisions, cancellation, concurrent create and
token invalidation across deletion/recreation. Connect integration tests exercise
generated clients over HTTP, response redaction, pagination, error codes, and the
auth override round trip. A
local service E2E exercises CreateProvider, Generate, URL/key rotation and
disabled-provider rejection against an HTTP upstream stub. Existing Generate and
runtime tests remain applicable.
