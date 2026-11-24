package postgres

import (
	"context"
	"testing"
	"time"
)

func TestPostgresModelContextWindowUpgradeIntegration(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	schema, _ := newTestSchema(t, ctx, dsn)
	db, err := openDB(withSearchPath(dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	chain, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(ctx, db, chain[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_provider_model(provider_id, model_id, max_output_tokens)
		VALUES(?, ?, ?)`, "gateway", "model", 2048); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var window, output int
	if err := db.QueryRowContext(ctx, `SELECT context_window, max_output_tokens FROM llm_provider_model
		WHERE provider_id = ? AND model_id = ?`, "gateway", "model").Scan(&window, &output); err != nil {
		t.Fatal(err)
	}
	if window != 0 || output != 2048 {
		t.Fatalf("upgraded model window = %d, output = %d, want 0 and 2048", window, output)
	}
}
