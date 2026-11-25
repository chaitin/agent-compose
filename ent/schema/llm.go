package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// LLMProvider mirrors llm_provider. headers_json is text decoded by the owning
// store.
type LLMProvider struct{ ent.Schema }

func (LLMProvider) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "llm_provider"}}
}

func (LLMProvider) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("name"),
		field.String("provider_type").Optional(),
		field.String("default_wire_api").Optional(),
		field.String("base_url"),
		field.String("api_key").Optional(),
		field.String("auth_header").Optional(),
		field.String("auth_scheme").Optional(),
		field.String("headers_json").Optional(),
		field.Int("use_generic_responses_text_parts").Optional(),
		field.Int("weight").Optional(),
		field.Int("enabled").Optional(),
		field.String("scope").Optional(),
		field.Int64("created_at"),
		field.Int64("updated_at"),
		field.String("auth").Optional(),
	}
}

// LLMModel mirrors llm_model.
type LLMModel struct{ ent.Schema }

func (LLMModel) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "llm_model"}}
}

func (LLMModel) Fields() []ent.Field {
	return []ent.Field{
		field.String("id"),
		field.String("name"),
		field.String("description").Optional(),
		field.Int("default_model").Optional(),
		field.Int("enabled").Optional(),
		field.String("scope").Optional(),
		field.Int64("created_at"),
		field.Int64("updated_at"),
	}
}

// LLMProviderModel mirrors llm_provider_model. The physical primary key is
// (provider_id, model_id); provider_id is the ent ID, so
// UpdateOneID/DeleteOneID must never be used on this entity. Filter by the
// model_id field and, when needed, IDEQ(provider_id).
type LLMProviderModel struct{ ent.Schema }

func (LLMProviderModel) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "llm_provider_model"}}
}

func (LLMProviderModel) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("provider_id"),
		field.String("model_id"),
		field.String("wire_api").Optional(),
		field.Int("weight").Optional(),
		field.String("base_url").Optional(),
		field.String("headers_json").Optional(),
		field.Int("max_output_tokens").Optional(),
		field.Int("context_window").Optional(),
		field.String("display_name").Optional(),
	}
}

// LLMFacadeToken mirrors llm_facade_token. The physical primary key is
// token_hash, mapped to the ent ID.
type LLMFacadeToken struct{ ent.Schema }

func (LLMFacadeToken) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "llm_facade_token"}}
}

func (LLMFacadeToken) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("token_hash"),
		field.String("sandbox_id"),
		field.String("token_fingerprint"),
		field.String("model").Optional(),
		field.String("provider_id").Optional(),
		field.String("wire_api").Optional(),
		field.String("source").Optional(),
		field.String("run_id").Optional(),
		field.Int64("issued_at"),
		field.Int64("expires_at"),
		field.Int64("revoked_at").Optional(),
		field.String("guest_model").Optional(),
	}
}

// LLMCatalogDefault mirrors llm_catalog_default, a single-row table whose
// primary key column is `singleton`.
type LLMCatalogDefault struct{ ent.Schema }

func (LLMCatalogDefault) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "llm_catalog_default"}}
}

func (LLMCatalogDefault) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").StorageKey("singleton"),
		field.String("provider_id"),
		field.String("model_id"),
		field.Int64("updated_at"),
	}
}
