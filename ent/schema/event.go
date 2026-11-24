package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// Event mirrors event. The physical primary key is the monotonic sequence
// column, mapped to the ent ID; the public event identifier is the unique
// event_id column (named id on the table).
type Event struct{ ent.Schema }

func (Event) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "event"}}
}

func (Event) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").StorageKey("sequence"),
		field.String("event_id").StorageKey("id"),
		field.String("topic"),
		field.String("source"),
		field.String("provider").Optional(),
		field.String("intent").Optional(),
		field.String("correlation_id"),
		field.String("idempotency_key").Optional(),
		field.String("delivery_id").Optional(),
		field.String("payload_hash"),
		field.String("payload_json"),
		field.String("dispatch_status"),
		field.String("parent_event_id").Optional(),
		field.String("publisher_type").Optional(),
		field.String("publisher_id").Optional(),
		field.String("publisher_run_id").Optional(),
		field.String("replay_of_event_id").Optional(),
		field.String("claim_id").Optional(),
		field.Int64("claim_until").Optional(),
		field.Int("attempt_count").Optional(),
		field.Int64("next_attempt_at").Optional(),
		field.String("last_error").Optional(),
		field.Int64("dead_letter_at").Optional(),
		field.Int64("created_at"),
		field.Int64("dispatched_at").Optional(),
		field.String("publisher_project_id").Optional(),
	}
}

// EventDelivery mirrors event_delivery. The physical primary key is
// (event_id, scheduler_id, trigger_id); event_id is the ent ID, so
// UpdateOneID/DeleteOneID must never be used. Constrain scheduler_id and
// trigger_id as well.
type EventDelivery struct{ ent.Schema }

func (EventDelivery) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "event_delivery"}}
}

func (EventDelivery) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("event_id"),
		field.String("scheduler_id"),
		field.String("trigger_id"),
		field.String("scheduler_run_id").Optional(),
		field.String("status"),
		field.String("error").Optional(),
		field.Int64("created_at"),
		field.Int64("updated_at"),
	}
}

// EventSandboxLink mirrors event_sandbox_link. The physical primary key is
// (event_id, sandbox_id, relation, scheduler_run_id); event_id is the ent ID,
// so UpdateOneID/DeleteOneID must never be used.
type EventSandboxLink struct{ ent.Schema }

func (EventSandboxLink) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "event_sandbox_link"}}
}

func (EventSandboxLink) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("event_id"),
		field.String("sandbox_id"),
		field.String("relation"),
		field.String("scheduler_id").Optional(),
		field.String("scheduler_run_id").Optional(),
		field.String("trigger_id").Optional(),
		field.String("scheduler_event_id").Optional(),
		field.Int64("created_at"),
	}
}

// WebhookSource mirrors webhook_source. The secret columns are stored as text
// and never leave the storage boundary.
type WebhookSource struct{ ent.Schema }

func (WebhookSource) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "webhook_source"}}
}

func (WebhookSource) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("name"),
		field.Int("enabled").Optional(),
		field.String("provider").Optional(),
		field.String("topic_prefix"),
		field.String("token_hash").Optional(),
		field.String("token_header").Optional(),
		field.String("signature_type").Optional(),
		field.String("signature_secret").Optional(),
		field.Int64("body_limit_bytes").Optional(),
		field.Int64("created_at"),
		field.Int64("updated_at"),
	}
}
