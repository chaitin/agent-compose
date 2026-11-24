package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// Sandbox mirrors the sandboxes projection table. The *_search columns hold the
// normalized forms used by the listing queries; they are maintained by the
// owning store.
type Sandbox struct{ ent.Schema }

func (Sandbox) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "sandboxes"}}
}

func (Sandbox) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("short_id").Optional(),
		field.String("title").Optional(),
		field.String("trigger_source").Optional(),
		field.String("driver").Optional(),
		field.String("vm_status").Optional(),
		field.String("project_id").Optional(),
		field.String("workspace_path").Optional(),
		field.String("workspace_id").Optional(),
		field.String("nested_workspace_id").Optional(),
		field.String("workspace_name").Optional(),
		field.String("workspace_type").Optional(),
		field.Int64("created_at").Optional(),
		field.Int64("updated_at").Optional(),
		field.String("sandbox_type").Optional(),
		field.String("title_search").Optional(),
		field.String("trigger_source_search").Optional(),
		field.String("driver_search").Optional(),
		field.String("vm_status_search").Optional(),
		field.String("project_id_search").Optional(),
		field.String("workspace_path_search").Optional(),
		field.String("workspace_id_search").Optional(),
		field.String("nested_workspace_id_search").Optional(),
		field.String("workspace_name_search").Optional(),
		field.String("workspace_type_search").Optional(),
	}
}

// SandboxProjectionMeta mirrors sandbox_projection_meta, a single-row table
// recording the projection schema version.
type SandboxProjectionMeta struct{ ent.Schema }

func (SandboxProjectionMeta) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "sandbox_projection_meta"}}
}

func (SandboxProjectionMeta) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id"),
		field.Int("version"),
	}
}
