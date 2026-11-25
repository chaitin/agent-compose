package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// SchedulerTrigger mirrors scheduler_trigger. The physical primary key is
// (scheduler_id, trigger_id); scheduler_id is the ent ID, so
// UpdateOneID/DeleteOneID must never be used. Constrain trigger_id as well.
type SchedulerTrigger struct{ ent.Schema }

func (SchedulerTrigger) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "scheduler_trigger"}}
}

func (SchedulerTrigger) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("scheduler_id"),
		field.String("trigger_id"),
		field.String("kind"),
		field.String("topic").Optional(),
		field.Int64("interval_ms").Optional(),
		field.Int("enabled").Optional(),
		field.Int("auto_id").Optional(),
		field.String("spec_json").Optional(),
		field.Int64("next_fire_at").Optional(),
		field.Int64("last_fired_at").Optional(),
	}
}

// SchedulerRun mirrors scheduler_run. The physical primary key is run_id, mapped
// to the ent ID.
type SchedulerRun struct{ ent.Schema }

func (SchedulerRun) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "scheduler_run"}}
}

func (SchedulerRun) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("run_id"),
		field.String("scheduler_id"),
		field.String("trigger_id").Optional(),
		field.String("trigger_kind").Optional(),
		field.String("trigger_source").Optional(),
		field.String("status").Optional(),
		field.Int64("started_at"),
		field.Int64("completed_at").Optional(),
		field.Int64("duration_ms").Optional(),
		field.String("error").Optional(),
		field.String("result_json").Optional(),
		field.String("payload_json").Optional(),
		field.String("source_script_sha256").Optional(),
		field.String("artifacts_dir").Optional(),
	}
}

// SchedulerEvent mirrors scheduler_event. The physical primary key is
// (scheduler_id, event_id); scheduler_id is the ent ID, so
// UpdateOneID/DeleteOneID must never be used. Constrain event_id as well.
type SchedulerEvent struct{ ent.Schema }

func (SchedulerEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "scheduler_event"}}
}

func (SchedulerEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("scheduler_id"),
		field.String("event_id"),
		field.String("scheduler_run_id").Optional(),
		field.String("trigger_id").Optional(),
		field.String("type"),
		field.String("level").Optional(),
		field.String("message").Optional(),
		field.String("payload_json").Optional(),
		field.String("linked_sandbox_id").Optional(),
		field.String("linked_cell_id").Optional(),
		field.String("linked_agent_thread_id").Optional(),
		field.Int64("created_at"),
	}
}

// SchedulerState mirrors scheduler_state. The physical primary key is
// (scheduler_id, key); scheduler_id is the ent ID, so UpdateOneID/DeleteOneID
// must never be used. Constrain the key field as well.
type SchedulerState struct{ ent.Schema }

func (SchedulerState) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "scheduler_state"}}
}

func (SchedulerState) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("scheduler_id"),
		field.String("key"),
		field.String("value_json"),
		field.Int64("updated_at"),
	}
}

// SchedulerSandboxBinding mirrors scheduler_sandbox_binding. The physical primary
// key is (scheduler_id, trigger_id); scheduler_id is the ent ID, so
// UpdateOneID/DeleteOneID must never be used. Constrain trigger_id as well.
type SchedulerSandboxBinding struct{ ent.Schema }

func (SchedulerSandboxBinding) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "scheduler_sandbox_binding"}}
}

func (SchedulerSandboxBinding) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("scheduler_id"),
		field.String("trigger_id").Optional(),
		field.String("sandbox_id"),
		field.String("sandbox_config_hash").Optional(),
		field.String("scheduler_config_hash").Optional(),
		field.Int64("created_at"),
		field.Int64("updated_at"),
	}
}
