package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// Volume mirrors volumes. labels_json and options_json are text columns decoded
// by the owning store; the physical unique constraint on `name` is enforced by
// the SQL schema.
type Volume struct{ ent.Schema }

func (Volume) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "volumes"}}
}

func (Volume) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("name"),
		field.String("driver").Optional(),
		field.String("path").Optional(),
		field.String("labels_json").Optional(),
		field.String("options_json").Optional(),
		field.String("project_id").Optional(),
		field.Int64("created_at").Optional(),
		field.Int64("updated_at").Optional(),
	}
}

// ProjectVolume mirrors project_volumes. The physical primary key is
// (project_id, volume_key); project_id is the ent ID, so
// UpdateOneID/DeleteOneID must never be used. Filter by the volume_key field
// and, when needed, IDEQ(project_id).
type ProjectVolume struct{ ent.Schema }

func (ProjectVolume) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "project_volumes"}}
}

func (ProjectVolume) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("project_id"),
		field.String("volume_key"),
		field.String("volume_id"),
		field.Int("external").Optional(),
		field.Int64("created_at").Optional(),
		field.Int64("updated_at").Optional(),
	}
}
