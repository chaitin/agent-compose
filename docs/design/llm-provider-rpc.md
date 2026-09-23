# LLM provider management RPC

## Problem and scope

The daemon already persists upstream providers, but models.json is startup-only
and no public RPC manages providers. Add live CRUD to
agentcompose.v2.LLMService for upstream model endpoints and credentials.
Coding-agent providers such as codex and pi are unrelated. The daemon env,
models.json and this RPC are three sources for one set of connection data; they
merge into a single catalog while configuration loads, and the runtime resolves
against that catalog alone.

## Contract

CreateProvider, GetProvider, ListProviders, UpdateProvider and DeleteProvider use
an immutable caller-chosen ID. IDs are 1–128 ASCII letters, digits, dots,
underscores or hyphens, beginning with a letter or digit. The environment-owned
IDs default and anthropic are reserved, because the daemon projects its own
configuration under them at startup.

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
presentation. RPC writes preserve an explicit auth choice even when it matches
the current protocol's default. An update that omits auth preserves that choice
across protocol changes; an explicit unspecified auth clears it. Only connections
without an explicit choice follow the protocol's default. This keeps a gateway's
authentication stable when switching between Chat Completions and Responses.

Legacy migration and environment bootstrap have only effective headers, not an
explicit auth field. They infer an override only when the header differs from
the protocol's default; a matching header gives no evidence of operator intent.
This inference is confined to those compatibility boundaries and is not used for
new RPC writes. Existing empty auth values keep following the protocol; operators
can pin them with an explicit update. Unknown presentations are rejected. The
environment-bootstrap path already chooses the same two presentations through
ANTHROPIC_AUTH_TOKEN (Bearer) and ANTHROPIC_API_KEY or LLM_API_KEY (x-api-key).

CLI `agent-compose llm provider` exposes ls, create, inspect, update, and rm.
Create requires --base-url, --protocol, and --api-key. Update sends only flags
that were set. `--auth x-api-key|bearer` overrides the protocol default on either
command. Both write into the same store; API CRUD takes effect on the next target
resolution.

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
because its fixed IDs are unavailable to API creation.

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
the existing scope rules. The daemon env, `models.json`, and this RPC are three
sources for one set of connection data; they merge into a single `Catalog`
snapshot while configuration loads. Resolution reads only that snapshot, so no
part of it consults the environment again.

`PrepareAgentLLM` is the one entry point for an agent's LLM configuration:

1. **Model.** The agent's declared `model`, else the catalog's default model,
   else `ErrNoModel`. A model id is opaque: it is never split on `/` and never
   matched against a connection to infer anything.
2. **Connection.** The runtime facade re-resolves the connection its token already
   names; that is a lookup of a decision already made, not a second choice. For
   every other caller, in order: among the connections that serve the model, the
   one speaking the caller's most preferred protocol; the connection that owns
   the default model; the only configured connection; otherwise every configured
   connection, ranked the same way, because a model id is opaque and a connection
   that never declared the model may still serve it. Preference exists so an
   agent is served by a passthrough whenever a connection can do that; connections
   that speak the model over the same protocol are interchangeable, and one of
   them is chosen at random instead of failing the run. With no connection at all
   the answer is `ErrNoConnection`, and the agent keeps its own authentication.
3. **Protocol.** The dialect table gives each agent the protocols it speaks
   natively, in affinity order, and one canonical protocol. Because step 2 ranks
   candidates by that order, an upstream protocol the agent speaks is passed
   through; anything else is converted to the agent's canonical protocol.
   If no conversion exists the run fails while preparing, not on the first
   request.

A provider created through this RPC is usable with a bare agent model name and
no `models.json` entry: model bindings are optional metadata, not an
authorization boundary. Family plays no part in any of the above. There is no
reserved `default` or `anthropic` connection that wins by name, and no search
that widens to another family when the first has no connection.

Connection selection is daemon configuration, not agent configuration: an agent
declares only its `model`, and an operator resolves a tie by making one
connection the owner of the default model or by binding the model to exactly one
connection. The retired `<connection>/<model>` form is not interpreted, and
splitting it would corrupt a legitimate model id that contains a slash; when such
a value is served by no connection while its prefix names a connection that
serves the remainder, the daemon reports `ErrLegacyQualifiedModel` naming the
model to write and the connection to configure instead.

**Protocol coverage.** Every (agent, upstream protocol) combination is served;
the agent's protocol never restricts which connection it may use.

| Agent | Passes through | Converted to |
| --- | --- | --- |
| codex | `responses` | `chat_completions`, `anthropic_messages` → `responses` |
| claude | `anthropic_messages` | `responses`, `chat_completions` → `anthropic_messages` |
| opencode | `chat_completions`, `anthropic_messages` | `responses` → `chat_completions` |
| pi | all three | — |
| dsh | all three | — |

Same-family conversion (`chat_completions` ↔ `responses`) re-encodes through the
shared adapters. Cross-family conversion uses the protocol bridge, which covers
all four cells. An agent whose dialect declares a canonical protocol the upstream
cannot reach fails with `unsupported llm protocol bridge`.

Claude SDK results must also honor `is_error`: the SDK can return a `success`
subtype with `is_error=true` for an upstream HTTP error. Such a result fails the
run and emits a fatal error event instead of publishing an API error as a
successful answer.

The facade publishes the model it resolved as `AGENT_COMPOSE_RESOLVED_MODEL`, in
the namespace the guest addresses models by, and the runner uses that value
instead of the model the agent declared. A declaration is a request that
resolution may rewrite: the catalog default may supply a model the agent
omitted, and pi and opencode address models through the provider key written
into their config. The runner passes the published reference through untouched,
so a resolved model id that itself contains slashes reaches the upstream intact.
An agent CLI told the declaration instead would address a model the facade token
is not bound to. A model whose literal text already begins with the guest
provider still keeps both components: provider `agent-compose` and model
`agent-compose/example` produce `agent-compose/agent-compose/example`.

The published value is the only model channel that may add a prefix, and it
never removes one. Compatibility with an older daemon runs one way: a new pi or
dsh runtime still converts the legacy runtime argument when
`AGENT_COMPOSE_RESOLVED_MODEL` is absent, which is what makes a rollback safe.
The daemon does not reciprocate — it sends the model verbatim — so an older dsh
guest driven by a newer daemon truncates a slashed model id at the first slash.
Update the guest image before or together with the daemon. See
`docs/pages/guest-image-abi.md`, which states the same constraint.

Ambiguous defaulting is reported instead of silently falling back to an agent's
own credentials. When multiple managed connections exist and no connection can
be selected, the run fails naming the candidates; make one of them the owner of
the default model or bind the model to exactly one connection.

The next target resolution reads current provider settings, so address/key
updates require no restart. Existing in-flight requests use their resolved
configuration; agent-side model/protocol setup may require restarting a run after
protocol changes. API CRUD does not change global or catalog defaults. A model id
reaching the runtime LLM facade is forwarded as written; clients should verify
provider existence when constructing a new reference after deletion.

## Validation

Domain tests cover ID/protocol/URL/key/auth validation and input ownership, and
the resolution rules above: model selection with and without a catalog default,
connection precedence from the catalog default model down to the only
connection, ambiguity rejection naming its candidates, the no-connection case,
disabled-connection exclusion, binding precedence, and the diagnostic for the
retired `<connection>/<model>` form. SQLite
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
