package sqlite

import (
	"context"
	"testing"
)

// The auth column records an explicit credential presentation. The migration
// backfills it only when the stored presentation differed from the protocol
// default, so a row that merely followed the convention keeps refreshing when
// its protocol later changes.
func TestLLMProviderAuthMigrationBackfillsOnlyExplicitPresentation(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	if err := MigrateThrough(ctx, db, 14); err != nil {
		t.Fatalf("migrate through version 14: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_provider(
		id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json,
		use_generic_responses_text_parts, weight, enabled, scope, created_at, updated_at)
		VALUES
		('convention', 'c', 'anthropic', 'anthropic_messages', 'https://convention.test', 'k', 'x-api-key', '', '{}', 0, 10, 1, 'api', 1, 1),
		('override', 'o', 'anthropic', 'anthropic_messages', 'https://override.test', 'k', 'Authorization', 'Bearer', '{}', 0, 10, 1, 'api', 1, 1),
		('reverse', 'r', 'openai', 'responses', 'https://reverse.test', 'k', 'x-api-key', '', '{}', 0, 10, 1, 'api', 1, 1),
		('plain', 'p', 'openai', 'responses', 'https://plain.test', 'k', 'Authorization', 'Bearer', '{}', 0, 10, 1, 'api', 1, 1)`); err != nil {
		t.Fatalf("seed pre-auth providers: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate to latest: %v", err)
	}

	want := map[string]string{
		"convention": "",          // x-api-key is the anthropic_messages default
		"override":   "bearer",    // bearer on anthropic_messages is an override
		"reverse":    "x-api-key", // x-api-key on responses is an override
		"plain":      "",          // bearer is the responses default
	}
	rows, err := db.QueryContext(ctx, `SELECT id, auth FROM llm_provider ORDER BY id`)
	if err != nil {
		t.Fatalf("query provider auth: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var id, auth string
		if err := rows.Scan(&id, &auth); err != nil {
			t.Fatalf("scan provider auth: %v", err)
		}
		got[id] = auth
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate provider auth: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("provider auth = %#v, want %#v", got, want)
	}
	for id, expected := range want {
		if got[id] != expected {
			t.Fatalf("provider %q auth = %q, want %q", id, got[id], expected)
		}
	}
}
