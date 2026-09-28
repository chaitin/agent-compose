# Protobuf v2 field layout freeze

The `agentcompose.v2` contract uses a compact field layout as its freeze
baseline. A reviewed terminology correction subsequently renamed the prune
response fields `loader_events` and `loader_id` to `scheduler_events` and
`scheduler_id` without changing their field numbers. The descriptor containing
that correction is the enforced compatibility baseline. CLI, Web clients, and
the daemon must therefore be generated from and deployed with the same
revision; rolling or mixed-version deployment across this change is
unsupported.

## Decision

We chose pre-freeze renumbering instead of restoring historical reservations.
The v2 API has not yet promised cross-version wire compatibility, so retaining
deleted pre-freeze tags would make accidental historical layout permanent. The
committed proto descriptor after this change is the compatibility baseline.

The removed fields and their former tags were confirmed from git history:

| Type | Old tag | Removed field |
| --- | ---: | --- |
| `CacheDomain` | 4 | `CACHE_DOMAIN_SANDBOX_EPHEMERAL_STATE` |
| `ProjectScheduler` | 4 | `managed_loader_id` |
| `ProjectSpec` | 3 | `workspace` |
| `ProjectSpec` | 5 | `network` |
| `RunAgentRequest` | 5 | `session_id` |
| `ListRunsRequest` | 3 | `session_id` |
| `RunSummary` | 11 | `session_id` |
| `TranscriptEvent` | 3 | `is_stderr` |
| `ListImagesResponse` | 3 | `has_more` |
| `ListImagesResponse` | 4 | `next_offset` |
| `PruneCachesRequest` | 2 | `include_referenced` |
| `CacheItem` | 10 | `session_id` |
| `CacheItem` | 11 | `sandbox_id` |

`AttachAgentRunRequest`, `AttachAgentRunResponse`, `AttachExecRequest`, and
`AttachExecResponse` intentionally keep frame variants in the low-number range
and envelope metadata at tags 15 and 16. The request messages define
`client_frame_id = 15`; the response messages define `server_frame_id = 15` and
`created_at = 16`. This separation leaves tags available
for future frame variants and is asserted by the field-layout contract test.

## Persistence and upgrade impact

Project revisions are persisted as canonical JSON owned by `pkg/compose`, not
as binary protobuf payloads. Sandbox metadata, run history, scheduler records,
cache metadata, and audit data are likewise persisted through their domain or
storage JSON/database representations. The only binary protobuf marshaling in
these paths is transient boundary traffic or integration code; no persisted
`agentcompose.v2` binary payload or fixture requires migration.

Consequently there is no data migration. The upgrade requirement is operational:
stop the old daemon and update the daemon, CLI, and generated Web client
together. Old and new clients or daemons must not be mixed across this change.
This repository commits the shared Go types used by the CLI and daemon, but no
generated Web protocol client; the Web client must be regenerated from this
same proto revision in its owning repository before deployment.

After this baseline, a removed field number and name must be reserved rather
than compacted or reused. `TestV2FieldNumbersHaveNoUnexplainedGaps` rejects new
unexplained message or enum gaps, while `buf breaking` enforces compatibility
against the repository baseline.
