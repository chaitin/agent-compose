package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// Project mirrors project. removed_at is a soft-delete marker; 0 means active.
type Project struct{ ent.Schema }

func (Project) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "project"}}
}

func (Project) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("name"),
		field.String("short_id").Optional(),
		field.String("source_path").Optional(),
		field.String("source_json").Optional(),
		field.Int64("current_revision").Optional(),
		field.String("spec_hash").Optional(),
		field.Int64("created_at").Optional(),
		field.Int64("updated_at").Optional(),
		field.Int64("removed_at").Optional(),
	}
}

// ProjectRevision mirrors project_revision. The physical primary key is
// (project_id, revision); project_id is the ent ID, so
// UpdateOneID/DeleteOneID must never be used. Always constrain the revision
// field as well.
type ProjectRevision struct{ ent.Schema }

func (ProjectRevision) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "project_revision"}}
}

func (ProjectRevision) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("project_id"),
		field.Int64("revision"),
		field.String("spec_hash"),
		field.String("spec_json"),
		field.Int64("created_at").Optional(),
	}
}

// ProjectAgent mirrors project_agent. The physical unique constraint on
// (project_id, agent_name) is enforced by the SQL schema.
type ProjectAgent struct{ ent.Schema }

func (ProjectAgent) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "project_agent"}}
}

func (ProjectAgent) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("name").Optional(),
		field.String("short_id").Optional(),
		field.String("project_id"),
		field.String("agent_name"),
		field.Int64("revision").Optional(),
		field.String("provider").Optional(),
		field.String("model").Optional(),
		field.String("image").Optional(),
		field.String("driver").Optional(),
		field.Int("scheduler_enabled").Optional(),
		field.String("spec_json").Optional(),
		field.Int64("created_at"),
		field.Int64("updated_at"),
	}
}

// ProjectScheduler mirrors project_scheduler. The physical unique constraint on
// (project_id, scheduler_id) is enforced by the SQL schema.
type ProjectScheduler struct{ ent.Schema }

func (ProjectScheduler) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "project_scheduler"}}
}

func (ProjectScheduler) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("short_id").Optional(),
		field.String("project_id"),
		field.String("scheduler_id"),
		field.String("agent_name"),
		field.Int64("revision").Optional(),
		field.Int("enabled").Optional(),
		field.Int("trigger_count").Optional(),
		field.String("spec_json").Optional(),
		field.String("last_error").Optional(),
		field.Int64("created_at"),
		field.Int64("updated_at"),
	}
}
