package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// ProjectRun mirrors project_run. The physical primary key is run_id, mapped to
// the ent ID. scheduler_run_id is the only nullable column in the schema: an
// empty value is stored as NULL.
type ProjectRun struct{ ent.Schema }

func (ProjectRun) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "project_run"}}
}

func (ProjectRun) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("run_id"),
		field.String("project_id"),
		field.String("project_name").Optional(),
		field.Int64("project_revision").Optional(),
		field.String("agent_name").Optional(),
		field.String("agent_id"),
		field.String("source").Optional(),
		field.String("scheduler_id").Optional(),
		field.String("scheduler_run_id").Optional().Nillable(),
		field.String("trigger_id").Optional(),
		field.String("status").Optional(),
		field.String("sandbox_id").Optional(),
		field.Int("exit_code").Optional(),
		field.String("error").Optional(),
		field.String("error_stack").Optional(),
		field.String("prompt").Optional(),
		field.String("output").Optional(),
		field.String("result_json").Optional(),
		field.String("logs_path").Optional(),
		field.String("artifacts_dir").Optional(),
		field.String("cleanup_error").Optional(),
		field.String("cleanup_policy").Optional(),
		field.Int("sandbox_created").Optional(),
		field.String("driver").Optional(),
		field.String("image_ref").Optional(),
		field.Int64("started_at").Optional(),
		field.Int64("completed_at").Optional(),
		field.Int64("duration_ms").Optional(),
		field.Int64("created_at"),
		field.Int64("updated_at"),
		field.String("execution_id").Optional(),
		field.Int("detach").Optional(),
		field.Int64("deadline").Optional(),
		field.String("detach_kind").Optional(),
		field.String("log_format").Optional(),
		field.String("summary_json").Optional(),
		field.String("agent_cell_id").Optional(),
		field.Int("log_incomplete").Optional(),
		field.String("execution_checkpoint_json").Optional(),
	}
}

// ProjectRunEvent mirrors project_run_event. sequence is the per-run ordering
// column, named seq on the physical table.
type ProjectRunEvent struct{ ent.Schema }

func (ProjectRunEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "project_run_event"}}
}

func (ProjectRunEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("run_id"),
		field.Int64("sequence").StorageKey("seq"),
		field.String("kind"),
		field.String("text").Optional(),
		field.String("agent").Optional(),
		field.String("name").Optional(),
		field.String("payload_json").Optional(),
		field.Int("success").Optional(),
		field.Int("exit_code").Optional(),
		field.String("stop_reason").Optional(),
		field.Int64("created_at"),
		field.Int64("log_seq").Optional(),
	}
}

// ProjectRunCompletion mirrors project_run_completion. The physical primary key
// is run_id, mapped to the ent ID. transition_json is opaque to the store.
type ProjectRunCompletion struct{ ent.Schema }

func (ProjectRunCompletion) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "project_run_completion"}}
}

func (ProjectRunCompletion) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("run_id"),
		field.String("target_status"),
		field.String("transition_json"),
		field.String("cleanup_action"),
		field.Int("attempt").Optional(),
		field.String("last_error").Optional(),
		field.Int64("next_attempt_at").Optional(),
		field.Int64("created_at"),
		field.Int64("updated_at"),
	}
}

// ProjectRunLabel mirrors project_run_label. The physical table has no primary
// key; it has a unique constraint on (run_id, key). run_id is the ent ID, so
// UpdateOneID/DeleteOneID must never be used. Constrain the key field as well.
type ProjectRunLabel struct{ ent.Schema }

func (ProjectRunLabel) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "project_run_label"}}
}

func (ProjectRunLabel) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("run_id"),
		field.String("key"),
		field.String("value"),
	}
}
