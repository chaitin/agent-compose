package sqlite

import (
	"context"
	"testing"
)

func TestProjectApplyTrustedHeadersMigrationLeavesExistingProjectsWithoutIdentity(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	seedManagedV4Fixture(t, db)
	if err := applyMigrationSet(ctx, db, chain[:18]); err != nil {
		t.Fatalf("apply project apply trusted headers migration: %v", err)
	}
	var got string
	if err := db.QueryRowContext(ctx, `SELECT apply_trusted_headers_json FROM project WHERE id = 'project-1'`).Scan(&got); err != nil {
		t.Fatalf("read project apply trusted headers: %v", err)
	}
	if got != "[]" {
		t.Fatalf("existing project apply trusted headers = %q, want []", got)
	}
}
