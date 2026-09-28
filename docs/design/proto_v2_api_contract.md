# Proto v2 selector and mutation contract

This document freezes the selector, field-presence, and collection-update
semantics of the v2 API. Clients must preserve protobuf presence and oneof
cases when translating CLI flags or Web forms into requests; they must not
infer a priority between selector fields or omit explicit zero values from
`Set` requests.

## Selector audit

Single-resource requests use one of these forms:

| Selector | Contract |
| --- | --- |
| `ProjectRef` | Exactly one non-empty `project_id`, `name`, or `source_path`. IDs are stable, names are daemon-unique project identities, and source paths are exact mutable lookup keys. |
| `ExecRequest.target` | Exactly one `sandbox_id`, `run_id`, or `selector`. Empty selected scalar cases are invalid. |
| `ExecSandboxSelector.project` | Exactly one non-empty `project_id` or `project_name`; `agent_name` is an optional narrowing filter, not an alternative project selector. |
| Hierarchical project resources | Scheduler requests combine one `ProjectRef` with required `agent_name` and, where applicable, `trigger_id` or `run_id`. These fields identify nested resources and are not mutually exclusive alternatives. |
| Direct resource IDs | Run, sandbox, image, volume, cache, workspace-preset, and session operations use one required resource ID or name. Query/list fields are filters and may be combined. |

Handlers reject a missing or empty selected case with `InvalidArgument`. A
oneof makes simultaneous alternatives unrepresentable in generated clients;
malformed wire input follows normal protobuf last-one-wins decoding. A lookup
with no match is `NotFound`, while a selector that matches multiple resources
is `InvalidArgument`.

## Driver model

`DriverSpec` is a single-config model. Exactly one of `boxlite`, `docker`,
`microsandbox`, or `k8s` is selected in `config`, and the required `name`
field must match that case. `name` remains in the wire and JSON representation
for compatibility with existing project output; the server always emits both.
There is no persistence of inactive driver configurations. A missing config,
missing name, or mismatch is invalid. This matches compose normalization,
which has always required exactly one runtime configuration, and prevents
silent ignored configuration.

## Mutation semantics

| Request | Field semantics |
| --- | --- |
| `ApplyProject` | `spec` is required and declaratively replaces the complete project. Repeated agents, variables, volumes, workspaces, MCP servers, OctoBus servers, and every nested repeated/map field are replaced. Empty clears; omitted entries are deleted. `source` is optional normalization metadata, `submitted_spec_hash` optionally verifies the normalized submitted spec (it is not a stored-revision concurrency guard), and `dry_run` explicitly chooses plan versus apply. |
| `PatchProject` | Updates an existing project with a complete desired `spec`; it is not JSON Patch, FieldMask, or a mutation DSL. `project` and `expected_current_spec_hash` are required, and a stale hash fails with `ABORTED`. The project name and persisted source cannot change. A `********` marker preserves an existing secret only at the same stable location; new, moved, and non-secret marker use is rejected. A real value replaces the secret, and omitted collection entries are deleted. `dry_run` performs the same checks without persistence. |
| `SetSchedulerEnabled` | `project` and `agent_name` are required. `enabled` is an explicit value; `false` disables and is never a no-op. |
| `SetSchedulerTriggerEnabled` | `project`, `agent_name`, and `trigger_id` are required. `enabled` is an explicit value; `false` disables and is never a no-op. |
| `UpdateGlobalEnv` | `env` is a complete replacement keyed by name. Empty clears all entries and omitted names are deleted. For an entry marked secret, absent `value` retains the stored secret with that name; present empty clears it. `secret` is an explicit replacement value. Duplicate names normalize with the last occurrence winning. |
| `UpdateCapabilityGatewayConfig` | Field-level patch. Absent `addr`/`token` is no-op; present empty explicitly clears that field. |
| `UpdateWorkspacePreset` | `preset_id` identifies the resource. All other fields are complete replacement values. Empty `comment` clears it; empty name/type is rejected; empty `config_json` is normalized to the provider default rather than treated as no-op. |

Create requests provide complete initial values. Delete, remove, prune, stop,
and other action requests are commands rather than patches: their ordinary
bool/scalar zero values are explicit documented command modes or optional
filters. List and stream repeated fields are filters; empty means no filter,
not mutation.

The protobuf definitions are the shared CLI/Web boundary. Both clients must
construct the same oneof case and preserve optional-field presence described
above. JSON object omission means absent/no-op only for proto `optional`
fields; an explicitly supplied empty string means clear.
