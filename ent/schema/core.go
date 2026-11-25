package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// GlobalEnv mirrors global_env. The physical primary key is `name`, mapped to
// the ent ID.
type GlobalEnv struct{ ent.Schema }

func (GlobalEnv) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "global_env"}}
}

func (GlobalEnv) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("name"),
		field.String("value"),
		field.Int("secret").Optional(),
		field.Int64("updated_at").Optional(),
	}
}

// WorkspaceConfig mirrors workspace_config. config_json is stored as text and
// decoded by the owning store.
type WorkspaceConfig struct{ ent.Schema }

func (WorkspaceConfig) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "workspace_config"}}
}

func (WorkspaceConfig) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("name"),
		field.String("type"),
		field.String("config_json").Optional(),
		field.String("comment").Optional(),
		field.Int64("created_at").Optional(),
		field.Int64("updated_at").Optional(),
	}
}

// CapabilityGateway mirrors capability_gateway, a single-row table.
type CapabilityGateway struct{ ent.Schema }

func (CapabilityGateway) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "capability_gateway"}}
}

func (CapabilityGateway) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id"),
		field.String("addr").Optional(),
		field.String("token").Optional(),
		field.Int64("updated_at").Optional(),
	}
}
