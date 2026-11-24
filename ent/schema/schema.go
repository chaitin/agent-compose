// Package schema is the single source of truth for the Go-side database model
// of agent-compose.
//
// The physical schema is owned by the released SQL migrations under
// pkg/storage/sqlite/migrations and pkg/storage/postgres/migrations. These ent
// schemas mirror that physical schema one-to-one: every table, column, type,
// default, and primary key here must match the migrated databases, because the
// runtime never runs ent's automatic migration and existing data roots must
// keep working without an upgrade step.
//
// pkg/storage/database/ent_schema_test.go enforces the mirror: it compares every
// ent entity and field with the SQLite schema produced by the released
// migrations. A table or column change therefore needs both an ent schema
// change and a paired SQL migration; see ent/README.md and AGENTS.md.
//
// Do not declare ent edges. Foreign keys are enforced by the SQL schema, and
// several relationships use composite keys that ent cannot model as edges; the
// owning column stays an explicit field so queries read like the physical
// schema.
//
// Composite primary keys: ent requires exactly one ID field per entity, so the
// entity whose physical table has a composite primary key maps that key's first
// column to the ent ID field (see each Annotations comment). The generated
// UpdateOneID/DeleteOneID methods only constrain that first column and would
// affect every row that shares it. Use predicates on the remaining key columns
// instead, never the *ID methods, for those entities.
package schema
