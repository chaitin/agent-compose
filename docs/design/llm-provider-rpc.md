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
applies explicit fields only; omitted name, base_url, protocol, api_key, and
enabled preserve stored values. Absent api_key preserves the current key
atomically, present nonempty rotates it, and present empty is invalid. An absent
enabled field means true on create and is preserved on update. An empty name
defaults to the ID on create and is preserved on update. Anthropic Messages
providers send anthropic-version: 2023-06-01 by default. Per-model overrides,
custom headers, and default-model management remain outside this change.
Protocol selects the existing OpenAI Bearer or Anthropic x-api-key upstream
authentication and refreshes those headers when protocol is updated.

CLI `agent-compose llm provider` exposes ls, create, inspect, update, and rm.
Create requires --base-url, --protocol, and --api-key. Update sends only flags
that were set. Environment bootstrap remains last fallback; API CRUD takes
effect on the next target resolution.

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
   reserved bootstrap connection (`default`/`anthropic`) wins; with none, the only
   configured connection of the requested family is used. A session-env
   connection never acts as a daemon default. Competing connections are reported
   as an ambiguity instead of being resolved by accident.

Model bindings are optional metadata rather than an authorization boundary, so a
provider created through this RPC is usable with a bare Agent model name and no
models.json entry. The facade agents (pi, opencode, dsh) treat the
`<connection>/<model>` prefix as optional for the same reason; codex and claude
already accepted unqualified model names. Prefixed values keep their established
meaning: a configured connection id, a family alias, or an env-backed custom
endpoint.

The next target resolution reads current provider settings, so address/key
updates require no restart. Existing in-flight requests use their resolved
configuration; agent-side model/protocol setup may require restarting a run after
protocol changes. API CRUD does not change global or catalog defaults. Unknown
slash prefixes reaching the runtime LLM facade retain the existing literal-model
interpretation; clients should verify provider existence when constructing a new
reference after deletion.

## Validation

Domain tests cover ID/protocol/URL/key validation and input ownership, and the
resolution stages above: configured-connection defaulting, reserved-default
preference, ambiguity rejection, disabled-connection exclusion, binding
precedence, and unqualified model names for pi, opencode and dsh. SQLite
integration tests cover literal routing, bare-model routing with an RPC-created
provider, key preservation/rotation, protocol mapping, disabled providers,
restart/catalog coexistence, collisions, cancellation, concurrent create and
token invalidation across deletion/recreation. Connect integration tests exercise
generated clients over HTTP, response redaction, pagination and error codes. A
local service E2E exercises CreateProvider, Generate, URL/key rotation and
disabled-provider rejection against an HTTP upstream stub. Existing Generate and
runtime tests remain applicable.
