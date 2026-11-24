package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// CredentialHandle mirrors credential_handle. owners_json is text decoded by the
// owning store.
type CredentialHandle struct{ ent.Schema }

func (CredentialHandle) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "credential_handle"}}
}

func (CredentialHandle) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("handle_id"),
		field.String("kind"),
		field.String("token_hash"),
		field.String("token_fingerprint"),
		field.String("env_name").Optional(),
		field.String("sandbox_id").Optional(),
		field.String("run_id").Optional(),
		field.String("endpoint").Optional(),
		field.String("owners_json").Optional(),
		field.Int64("issued_at"),
		field.Int64("expires_at"),
		field.Int64("revoked_at").Optional(),
	}
}
