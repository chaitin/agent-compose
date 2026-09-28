# Project definition boundaries

## Goal

Keep one implementation of project parsing, normalization, and static
validation so agent-compose and upstream applications cannot drift. This is a
package-boundary change, not a new repository or a rewrite of project
runtime management.

## Target layout

`pkg/projectdef` is the supported reusable API surface. Today it is a façade
over `pkg/compose`: it re-exports the definition types as aliases and forwards
`Parse`, `ParseFile`, `Normalize`, `NormalizeFile`, `Validate`, and
`ParseCanonicalJSON`. `pkg/compose` still owns the project schema, YAML/JSON
loading, deterministic normalization, canonical JSON/hash, static validation,
and definition-only references.

`internal/projects` owns agent-compose application behavior: records,
revisions, stores, controller workflows, scheduler reconciliation, sandbox and
volume lifecycle, secrets, and runtime capability checks.

The existing `pkg/compose` package is to be split along this boundary during
migration, at which point `pkg/projectdef` becomes the owner rather than a
façade. It may retain file-format compatibility helpers, but it must not remain
a second implementation of definition semantics.

## Dependency rule

`pkg/projectdef` must not import `internal/*`, persistence, runtime drivers,
sandbox/image backends, schedulers, or transport packages. Runtime validation
is layered by `internal/projects` after static validation returns.

## Migration invariants

For every existing project fixture, migration must preserve normalized output,
canonical JSON, spec hash, validation paths/messages, and runtime behavior.
Compatibility wrappers are temporary migration mechanics only; the final
layout has no duplicate project-definition implementation in
`internal/projects`.

## Public API principles

Export only definition-level types and functions. Treat field names, default
values, validation issue paths, canonical serialization, and hash behavior as
versioned compatibility contracts. Store records and controller dependency
interfaces are not public definition API.

## Rollout

1. Inventory and classify compose/projects code.
2. Move definition types and pure logic into `pkg/projectdef`.
3. Make compose loading and internal project code call that package.
4. Move runtime project implementation under `internal/projects` and update
   imports in one controlled change.
5. Run compatibility, package, lint, build, and full test gates.
